// Package update implements `vos update|rollback|status|health|sign|keygen`
// and vosd's update service: signed manifests from OCI (ghcr), HTTP or a
// directory, streamed into the idle A/B slot, boot counting, health, and
// the update-state bookkeeping. See docs/CONTRACTS.md "Update format".
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Automatic staging schedule (config.update.auto == "stage"): the first
// check a while after boot, so it does not compete with starting up, then
// every 6 h. The jitter keeps a fleet from hitting the registry at once.
var (
	firstCheckDelay = 5 * time.Minute
	checkInterval   = 6 * time.Hour
	checkJitter     = 30 * time.Minute
	rebootDelay     = time.Second // lets the HTTP response go out first
)

type Service struct {
	cfg *config.Config // shared: read through Snapshot, changed through Mutate

	mu       sync.Mutex // guards the fields below
	ctx      context.Context
	running  bool
	stopping bool // Run is returning: no new background stages
	progress *Progress
	cancel   context.CancelCauseFunc // ends the running stage's context
	stages   sync.WaitGroup          // stages StartStage runs
	reboot   func(context.Context) error
	// slotsChanged runs whenever what the other slot boots may have
	// changed (SetSlotsChanged).
	slotsChanged func()
}

var (
	// ErrCancelled is the cause of a stage that Cancel stopped.
	ErrCancelled       = errors.New("the update was cancelled")
	errNothingToCancel = errors.New("no update is running")
	errShellUpdate     = errors.New("the update running now was started with vos update from a shell; stop it there")
	errTooLate         = errors.New("VaporOS is already installing the update; it takes a few seconds")
)

func NewService(cfg *config.Config) *Service {
	if cfg == nil {
		cfg = config.Defaults()
	}
	return &Service{cfg: cfg, reboot: sysd.Reboot}
}

// SetSlotsChanged sets f, which must return at once, to run whenever the
// image the other slot boots may have changed: after a stage records the
// idle slot's extensions (Write order 1) and again when it ends, after a
// rollback and before an activation's restart. The extensions decide
// from it whether Steam may start games through `vos ext launch`, which
// an image built before extensions lacks.
func (s *Service) SetSlotsChanged(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slotsChanged = f
}

func (s *Service) notifySlots() {
	s.mu.Lock()
	f := s.slotsChanged
	s.mu.Unlock()
	if f != nil {
		f()
	}
}

// Routes registers /update/* (see CONTRACTS.md).
func (s *Service) Routes(srv *api.Server) {
	srv.Handle(http.MethodGet, "/update", api.Authed, s.handleGet)
	srv.Handle(http.MethodPost, "/update/check", api.Authed, s.handleCheck)
	srv.Handle(http.MethodPost, "/update/stage", api.Authed, s.handleStage)
	srv.Handle(http.MethodPost, "/update/cancel", api.Authed, s.handleCancel)
	srv.Handle(http.MethodPost, "/update/activate", api.Authed, s.handleActivate)
	srv.Handle(http.MethodPost, "/update/rollback", api.Authed, s.handleRollback)
	srv.Handle(http.MethodPut, "/update/settings", api.Authed, s.handleSettings)
}

// Run does post-boot bookkeeping (failed[] detection) and, when
// config.update.auto == "stage", periodic check+stage (6 h, jittered).
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	if config.IsLive() {
		return
	}
	defer s.waitStages(ctx)
	if _, err := Reconcile(); err != nil {
		log.Printf("update: %v", err)
	}
	timer := time.NewTimer(firstCheckDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.autoStage(ctx)
		timer.Reset(checkInterval + jitter())
	}
}

// waitStages lets a stage started from the web UI finish before vosd exits.
// Its context is the daemon's, so a download or write stops at once; what
// may still run is the install tail (ESP files, the entry, then
// update-state), and stopping between the entry and its record would hide a
// failing version from failed[]. The daemon bounds the wait. After a panic
// (ctx still live) Run is restarted instead, and must not wait.
func (s *Service) waitStages(ctx context.Context) {
	if ctx.Err() == nil {
		return
	}
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	s.stages.Wait()
}

func jitter() time.Duration {
	if checkJitter <= 0 {
		return 0
	}
	return rand.N(checkJitter)
}

// autoStage checks the channel and stages anything newer.
func (s *Service) autoStage(ctx context.Context) {
	cfg := s.config()
	if cfg.Update.Auto != "stage" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	res, err := Check(cctx, cfg, Options{})
	cancel()
	if err != nil {
		log.Printf("update check: %v", err)
		return
	}
	if res.Available == nil {
		return
	}
	if err := s.stageNow(ctx, Options{}); err != nil && !IsBenign(err) && !errors.Is(err, ErrBusy) && !errors.Is(err, ErrCancelled) {
		log.Printf("update: %v", err)
	}
}

// ApplyMachineCmdline sets the machine kernel arguments
// (/var/lib/vos/cmdline) and rewrites both slots' boot entries to carry
// them; they take effect on the next boot. It holds the update lock, so it
// never interleaves with a stage writing an entry, and waits for one until
// ctx ends. The boot disk (vos.disk=) stays unless cmdline names one.
func ApplyMachineCmdline(ctx context.Context, cmdline string) error {
	if config.IsLive() {
		return ErrLive
	}
	cmdline = strings.TrimSpace(cmdline)
	if strings.ContainsFunc(cmdline, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return errors.New("machine cmdline contains control characters")
	}
	lock, err := takeUpdateLock(ctx, 0)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	esp := config.ESP
	if err := boot.EnsureESP(esp); err != nil {
		return err
	}
	old := boot.MachineCmdline()
	if boot.DiskArgOf(cmdline) == "" {
		cmdline = boot.WithDiskArg(cmdline, boot.DiskArgOf(old))
	}
	// The entries first and the file last: until the file changes, a retry
	// computes the same swap, and dropping the new arguments as well as the
	// old ones lets it finish a rewrite that stopped half way.
	drop := old + " " + cmdline
	if err := boot.RewriteOptions(esp, func(e boot.Entry) string {
		return boot.SwapMachineArgs(e.Options, drop, cmdline)
	}); err != nil {
		return fmt.Errorf("boot entries: %w", err)
	}
	return boot.SetMachineCmdline("", cmdline)
}

// Busy reports whether an update is downloading/writing (keeps power awake).
// It also sees a `vos update` running in another process, through its lock.
func (s *Service) Busy() (bool, string) {
	s.mu.Lock()
	running, p := s.running, s.progress
	s.mu.Unlock()
	if running {
		if p != nil && p.Phase != "check" && p.Version != "" {
			return true, fmt.Sprintf("installing update %s (%d%%)", p.Version, p.Percent)
		}
		return true, "checking for updates"
	}
	if updateLocked() {
		return true, "installing an update"
	}
	return false, ""
}

// updateLocked reports whether some process holds the update lock.
func updateLocked() bool {
	l, err := takeUpdateLock(context.Background(), probePatience)
	if err != nil {
		return errors.Is(err, ErrBusy)
	}
	l.Unlock()
	return false
}

// config returns a snapshot of the configuration.
func (s *Service) config() *config.Config {
	c := s.cfg.Snapshot()
	return &c
}

// lifetime is the daemon's context, for stages that outlive a request.
func (s *Service) lifetime() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// claim marks a stage as running (s.mu held) and returns its context, which
// Cancel ends.
func (s *Service) claim(parent context.Context) context.Context {
	ctx, cancel := context.WithCancelCause(parent)
	s.running, s.progress, s.cancel = true, &Progress{Phase: "check"}, cancel
	return ctx
}

// reserve claims the one update slot this process runs at a time.
func (s *Service) reserve(parent context.Context) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil, false
	}
	return s.claim(parent), true
}

// StartStage stages in the background; progress goes out as
// update.progress events. It fails at once if an update is running.
func (s *Service) StartStage(opts Options) error {
	parent := s.lifetime()
	s.mu.Lock()
	if s.running || s.stopping {
		s.mu.Unlock()
		return ErrBusy
	}
	ctx := s.claim(parent)
	s.stages.Add(1) // under mu, so never after waitStages began to Wait
	s.mu.Unlock()
	go func() {
		defer s.stages.Done()
		s.doStage(ctx, opts)
	}()
	return nil
}

func (s *Service) stageNow(ctx context.Context, opts Options) error {
	ctx, ok := s.reserve(ctx)
	if !ok {
		return ErrBusy
	}
	return s.doStage(ctx, opts)
}

// Cancel stops the stage this process runs, unless it is already writing
// the ESP. The outcome arrives as update.progress: "cancelled", or "done"
// when the stage got past its last look at ctx first.
func (s *Service) Cancel() error {
	if err := s.cancelStage(); !errors.Is(err, errNothingToCancel) {
		return err
	}
	if updateLocked() {
		return errShellUpdate
	}
	return errNothingToCancel
}

func (s *Service) cancelStage() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.running:
		return errNothingToCancel
	case s.progress.Phase == "install" || s.progress.Phase == "done":
		return errTooLate
	}
	s.cancel(ErrCancelled)
	return nil
}

func (s *Service) doStage(ctx context.Context, opts Options) error {
	defer func() {
		s.mu.Lock()
		cancel := s.cancel
		s.running, s.progress, s.cancel = false, nil, nil
		s.mu.Unlock()
		if cancel != nil {
			cancel(nil)
		}
	}()
	opts.Progress = func(p Progress) {
		s.mu.Lock()
		s.progress = &p
		s.mu.Unlock()
		events.Publish("update.progress", p)
	}
	opts.SlotsChanged = s.notifySlots
	res, err := Stage(ctx, s.config(), opts)
	if err == nil {
		return nil
	}
	var p Progress
	switch {
	case errors.Is(context.Cause(ctx), ErrCancelled):
		s.mu.Lock()
		p = Progress{Phase: "cancelled", Percent: s.progress.Percent}
		s.mu.Unlock()
		log.Print("update: cancelled")
		err = ErrCancelled
	case IsBenign(err):
		// A stage that finds nothing to do still ends: otherwise its "check"
		// stays the replayed update.progress, and a new page shows it running.
		p = Progress{Phase: "idle"}
	default:
		p = Progress{Phase: "error", Error: err.Error()}
		log.Printf("update: %v", err)
	}
	if res != nil {
		p.Version = res.Manifest.Version
	}
	events.Publish("update.progress", p)
	return err
}

// View is GET /update: the update-state plus configuration and slots.
type View struct {
	State
	Config     config.UpdateConfig `json:"config"`
	BootedSlot string              `json:"booted_slot"`
	OtherSlot  *SlotStatus         `json:"other_slot"`
	NextBoot   *NextBoot           `json:"next_boot"`
	Busy       bool                `json:"busy"`
	Progress   *Progress           `json:"progress"`
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, s.View())
}

// View builds GET /update, which GET /status shares: it reads the state
// file and the ESP and probes the update lock for probePatience, nothing
// slower.
func (s *Service) View() View {
	st, err := LoadState()
	if err != nil {
		log.Printf("update state: %v", err)
	}
	st.Booted = bootedImage().Version
	v := View{State: *st, Config: s.effectiveConfig(), BootedSlot: config.BootedSlot()}
	if v.BootedSlot != "" && boot.EnsureESP(config.ESP) == nil {
		v.OtherSlot, _ = slotStatus(config.OtherSlot(v.BootedSlot), v.BootedSlot)
		v.NextBoot, _ = nextBoot(v.BootedSlot)
	}
	v.Busy, _ = s.Busy()
	s.mu.Lock()
	if s.progress != nil {
		p := *s.progress
		v.Progress = &p
	}
	s.mu.Unlock()
	return v
}

// effectiveConfig is config.update with the defaults filled in.
func (s *Service) effectiveConfig() config.UpdateConfig {
	c := s.cfg.Snapshot().Update
	if c.Source == "" {
		c.Source = config.DefaultUpdateSrc
	}
	if c.Channel == "" {
		c.Channel = "main"
	}
	if c.Auto == "" {
		c.Auto = "stage"
	}
	return c
}

func (s *Service) handleCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := Check(ctx, s.config(), Options{})
	if err != nil {
		api.Error(w, http.StatusBadGateway, "%v", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]*Available{"available": res.Available})
}

func (s *Service) handleStage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := api.ReadJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Version != "" && !manifest.ValidVersion(req.Version) {
		api.Error(w, http.StatusBadRequest, "invalid version %q", req.Version)
		return
	}
	// Picking a version explicitly may pick an older one.
	opts := Options{Version: req.Version, AllowDowngrade: req.Version != ""}
	if updateLocked() {
		api.Error(w, http.StatusConflict, "%v", ErrBusy)
		return
	}
	if err := s.StartStage(opts); err != nil {
		api.Error(w, http.StatusConflict, "%v", err)
		return
	}
	api.OK(w)
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Cancel(); err != nil {
		api.Error(w, http.StatusConflict, "%v", err)
		return
	}
	api.OK(w)
}

func (s *Service) handleActivate(w http.ResponseWriter, r *http.Request) {
	if busy, _ := s.Busy(); busy {
		api.Error(w, http.StatusConflict, "an update is still being installed")
		return
	}
	st, _ := LoadState()
	if st.Staged == nil {
		api.Error(w, http.StatusConflict, "no update is staged")
		return
	}
	if err := boot.EnsureESP(config.ESP); err != nil {
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	e, err := boot.EntryForSlot(config.ESP, st.Staged.Slot)
	if err != nil || e == nil || e.Version != st.Staged.Version || !e.Bootable() {
		api.Error(w, http.StatusConflict, "the staged update %s is no longer bootable", st.Staged.Version)
		return
	}
	s.notifySlots()
	api.OK(w)
	go func() {
		time.Sleep(rebootDelay)
		if err := s.reboot(context.Background()); err != nil {
			log.Printf("update: reboot: %v", err)
		}
	}()
}

func (s *Service) handleRollback(w http.ResponseWriter, r *http.Request) {
	// Rollback records the hold in update-state, which tells the UI.
	if _, err := Rollback(false); err != nil {
		api.Error(w, http.StatusConflict, "%v", err)
		return
	}
	s.notifySlots()
	api.OK(w)
}

func (s *Service) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel *string `json:"channel"`
		Auto    *string `json:"auto"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Channel != nil && *req.Channel != "" && !ValidChannel(*req.Channel) {
		api.Error(w, http.StatusBadRequest, "invalid channel %q", *req.Channel)
		return
	}
	if req.Auto != nil && *req.Auto != "stage" && *req.Auto != "off" {
		api.Error(w, http.StatusBadRequest, `auto must be "stage" or "off"`)
		return
	}
	var channel string
	if req.Channel != nil {
		channel = *req.Channel
		if channel == "" {
			channel = config.Defaults().Update.Channel // back to the image's channel
		}
	}
	changed := false
	err := s.cfg.Mutate(func(c *config.Config) {
		if req.Channel != nil {
			changed = c.Update.Channel != channel
			c.Update.Channel = channel
		}
		if req.Auto != nil {
			c.Update.Auto = *req.Auto
		}
	})
	if err != nil {
		api.Error(w, http.StatusInternalServerError, "saving settings: %v", err)
		return
	}
	if changed {
		// What the old channel offered says nothing about the new one.
		modifyState(func(st *State) error { st.Available = nil; return nil })
	}
	api.OK(w)
}
