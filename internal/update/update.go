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
	cfg *config.Config

	mu       sync.Mutex // guards the fields below and s.cfg.Update
	ctx      context.Context
	running  bool
	progress *Progress
	reboot   func(context.Context) error
}

func NewService(cfg *config.Config) *Service {
	if cfg == nil {
		cfg = config.Defaults()
	}
	return &Service{cfg: cfg, reboot: sysd.Reboot}
}

// Routes registers /update/* (see CONTRACTS.md).
func (s *Service) Routes(srv *api.Server) {
	srv.Handle(http.MethodGet, "/update", api.Authed, s.handleGet)
	srv.Handle(http.MethodPost, "/update/check", api.Authed, s.handleCheck)
	srv.Handle(http.MethodPost, "/update/stage", api.Authed, s.handleStage)
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
	if err := s.stageNow(ctx, Options{}); err != nil && !IsBenign(err) && !errors.Is(err, ErrBusy) {
		log.Printf("update: %v", err)
	}
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
	l, err := lockFile(updateLockPath(), false)
	if err != nil {
		return errors.Is(err, errLocked)
	}
	l.Unlock()
	return false
}

// config returns a snapshot of the configuration.
func (s *Service) config() *config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *s.cfg
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

// reserve claims the one update slot this process runs at a time.
func (s *Service) reserve() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	s.progress = &Progress{Phase: "check"}
	return true
}

// StartStage stages in the background; progress goes out as
// update.progress events. It fails at once if an update is running.
func (s *Service) StartStage(opts Options) error {
	if !s.reserve() {
		return ErrBusy
	}
	go s.doStage(s.lifetime(), opts)
	return nil
}

func (s *Service) stageNow(ctx context.Context, opts Options) error {
	if !s.reserve() {
		return ErrBusy
	}
	return s.doStage(ctx, opts)
}

func (s *Service) doStage(ctx context.Context, opts Options) error {
	defer func() {
		s.mu.Lock()
		s.running, s.progress = false, nil
		s.mu.Unlock()
	}()
	opts.Progress = func(p Progress) {
		s.mu.Lock()
		s.progress = &p
		s.mu.Unlock()
		events.Publish("update.progress", p)
	}
	res, err := Stage(ctx, s.config(), opts)
	if err != nil && !IsBenign(err) {
		p := Progress{Phase: "error", Error: err.Error()}
		if res != nil {
			p.Version = res.Manifest.Version
		}
		events.Publish("update.progress", p)
		log.Printf("update: %v", err)
	}
	return err
}

// updateView is GET /update: the update-state plus configuration and slots.
type updateView struct {
	State
	Config     config.UpdateConfig `json:"config"`
	BootedSlot string              `json:"booted_slot"`
	OtherSlot  *SlotStatus         `json:"other_slot"`
	Busy       bool                `json:"busy"`
	Progress   *Progress           `json:"progress"`
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	st, err := LoadState()
	if err != nil {
		log.Printf("update state: %v", err)
	}
	st.Booted = bootedImage().Version
	v := updateView{State: *st, Config: s.effectiveConfig(), BootedSlot: config.BootedSlot()}
	if v.BootedSlot != "" && boot.EnsureESP(config.ESP) == nil {
		v.OtherSlot, _ = slotStatus(config.OtherSlot(v.BootedSlot), v.BootedSlot)
	}
	v.Busy, _ = s.Busy()
	s.mu.Lock()
	if s.progress != nil {
		p := *s.progress
		v.Progress = &p
	}
	s.mu.Unlock()
	api.WriteJSON(w, http.StatusOK, v)
}

// effectiveConfig is config.update with the defaults filled in.
func (s *Service) effectiveConfig() config.UpdateConfig {
	s.mu.Lock()
	c := s.cfg.Update
	s.mu.Unlock()
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
	api.OK(w)
	go func() {
		time.Sleep(rebootDelay)
		if err := s.reboot(context.Background()); err != nil {
			log.Printf("update: reboot: %v", err)
		}
	}()
}

func (s *Service) handleRollback(w http.ResponseWriter, r *http.Request) {
	if _, err := Rollback(false); err != nil {
		api.Error(w, http.StatusConflict, "%v", err)
		return
	}
	modifyState(func(*State) error { return nil }) // tell the UI
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
	s.mu.Lock()
	oldChannel := s.cfg.Update.Channel
	if req.Channel != nil {
		ch := *req.Channel
		if ch == "" {
			ch = config.Defaults().Update.Channel // back to the image's channel
		}
		s.cfg.Update.Channel = ch
	}
	if req.Auto != nil {
		s.cfg.Update.Auto = *req.Auto
	}
	err := s.cfg.Save()
	changed := s.cfg.Update.Channel != oldChannel
	s.mu.Unlock()
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
