// Package power is the idle-shutdown policy (port of the reference
// gaming-idle-shutdown), keep-awake, and Wake-on-LAN status.
//
// The machine is meant to be woken by Wake-on-LAN when someone wants to
// play and to switch itself off again when nobody does. "Nobody does" is
// the reference machine's definition, check for check: no keep-awake, no
// Moonlight stream, no game process, no Steam download, no disk activity
// in the gamescope session, plus (new here) no one using the web UI. After
// idle_minutes of that, vosd powers off.
package power

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// BusyFunc reports whether something keeps the machine awake, and why.
type BusyFunc func() (bool, string)

const (
	// interval is how often the policy looks, as in the reference.
	interval = 15 * time.Second
	// downloadWindow: a file in a download directory newer than this
	// means Steam is downloading.
	downloadWindow = 30 * time.Second
	// ioBusyBytes of gamescope-session disk I/O per interval counts as
	// activity (the reference's conservative 1 MiB).
	ioBusyBytes = 1 << 20
	// webActivityWindow keeps the machine up after the last web request,
	// so it does not switch off under someone changing its settings.
	webActivityWindow = 5 * time.Minute
	// maxKeepAwake caps one keep-awake request.
	maxKeepAwake = 7 * 24 * time.Hour
)

type Service struct {
	cfg  *config.Config
	busy []BusyFunc

	mu          sync.Mutex
	keepUntil   time.Time // in-memory keep-awake deadline (POST /power/keep-awake)
	lastTouch   time.Time // last web UI activity
	idleSince   time.Time // start of the current idle period
	state       string    // last tick: "" before the first, "idle", or the busy reason
	prevIO      uint64
	haveIO      bool
	poweringOff bool // poweroff requested; never ask twice

	// Seams for tests. The defaults read the real system.
	now          func() time.Time
	poweroff     func(context.Context) error
	publish      func(topic string, data any)
	gameRunning  func() bool
	downloading  func(now time.Time) bool
	ioBytes      func() (uint64, bool)
	keepFileSeen func() bool
	ethtool      func(ctx context.Context, iface string) (string, error)
	sysNet       string
}

func NewService(cfg *config.Config, busy ...BusyFunc) *Service {
	return &Service{
		cfg:      cfg,
		busy:     busy,
		now:      time.Now,
		poweroff: sysd.Poweroff,
		publish:  events.Publish,
		gameRunning: func() bool {
			return gameRunning("/proc", config.GamerUID)
		},
		downloading: func(now time.Time) bool {
			libs := steam.Libraries(steam.Root(config.GamerHome))
			return steamDownloading(libs, now.Add(-downloadWindow))
		},
		ioBytes: func() (uint64, bool) {
			return ioBytes(gamescopeIOStat("/sys/fs/cgroup", config.GamerUID))
		},
		keepFileSeen: func() bool { return fileExists(keepAwakePath()) },
		ethtool:      runEthtool,
		sysNet:       "/sys/class/net",
	}
}

func runEthtool(ctx context.Context, iface string) (string, error) {
	return sysd.Run(ctx, "ethtool", iface)
}

// keepAwakePath is a file whose mere existence keeps the machine awake
// (`touch /run/vos/keep-awake` over SSH), like the reference's
// /run/gaming-keep-awake. It lives in /run, so a reboot clears it.
func keepAwakePath() string { return filepath.Join(config.RunDir, "keep-awake") }

// Routes registers /power*.
func (s *Service) Routes(srv *api.Server) {
	srv.Handle("GET", "/power", api.Authed, s.handleGet)
	srv.Handle("PUT", "/power", api.Authed, s.handlePut)
	srv.Handle("POST", "/power/keep-awake", api.Authed, s.handleKeepAwake)
}

// Run polls every 15 s and powers off after idle_minutes of idleness.
// The live ISO never powers itself off: someone is installing.
func (s *Service) Run(ctx context.Context) {
	if config.IsLive() {
		return
	}
	s.start()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// start begins the first idle period and takes the I/O baseline, as the
// reference does before its loop.
func (s *Service) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idleSince = s.now()
	s.prevIO, s.haveIO = s.ioBytes()
}

// Touch marks user activity (e.g. web UI requests) as busy for a while.
func (s *Service) Touch() {
	s.mu.Lock()
	s.lastTouch = s.now()
	s.mu.Unlock()
}

func (s *Service) settings() config.PowerConfig {
	var pc config.PowerConfig
	s.cfg.View(func(c *config.Config) { pc = c.Power })
	return pc
}

// ioDelta returns the gamescope session's I/O since the previous call. A
// counter that went backwards (the unit restarted) counts as no I/O.
func (s *Service) ioDelta() uint64 {
	cur, ok := s.ioBytes()
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, had := s.prevIO, s.haveIO
	s.prevIO, s.haveIO = cur, ok
	if !ok || !had || cur < prev {
		return 0
	}
	return cur - prev
}

// quickReason runs the cheap checks: keep-awake and the other services.
func (s *Service) quickReason(now time.Time) string {
	s.mu.Lock()
	keep := now.Before(s.keepUntil)
	s.mu.Unlock()
	if keep {
		return "keep-awake"
	}
	if s.keepFileSeen() {
		return "manual keep-awake"
	}
	for _, f := range s.busy {
		if f == nil {
			continue
		}
		if b, why := f(); b {
			if why == "" {
				why = "busy"
			}
			return why
		}
	}
	return ""
}

func (s *Service) webActive(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.lastTouch.IsZero() && now.Sub(s.lastTouch) < webActivityWindow
}

// busyReason is one evaluation of every check, in the reference's order
// of precedence, with web activity last. The I/O delta is taken on every
// call, busy or not, so each interval is measured on its own.
func (s *Service) busyReason(now time.Time) string {
	delta := s.ioDelta()
	if r := s.quickReason(now); r != "" {
		return r
	}
	switch {
	case s.gameRunning():
		return "Steam game"
	case s.downloading(now):
		return "Steam download/update"
	case delta >= ioBusyBytes:
		return "Steam disk activity"
	case s.webActive(now):
		return "web UI in use"
	}
	return ""
}

// idleEvent is the power.idle event payload.
type idleEvent struct {
	IdleSeconds int  `json:"idle_seconds"`
	ShutdownIn  *int `json:"shutdown_in"` // seconds; null when idle shutdown is off or busy
}

// tick is one pass of the policy loop.
func (s *Service) tick(ctx context.Context) {
	now := s.now()
	reason := s.busyReason(now)
	pc := s.settings()
	limit := time.Duration(pc.IdleMinutes) * time.Minute

	s.mu.Lock()
	prevState := s.state
	if reason != "" {
		s.state = reason
		s.idleSince = now
		s.mu.Unlock()
		if prevState != reason {
			log.Printf("power: busy: %s", reason)
		}
		if prevState == "idle" || prevState == "" {
			s.publish("power.idle", idleEvent{})
		}
		return
	}
	s.state = "idle"
	idle := now.Sub(s.idleSince)
	already := s.poweringOff
	s.mu.Unlock()

	if prevState != "idle" {
		log.Printf("power: idle timer started")
	}
	if int(idle/time.Minute) > int((idle-interval)/time.Minute) {
		log.Printf("power: idle for %d/%d seconds", int(idle.Seconds()), int(limit.Seconds()))
	}
	ev := idleEvent{IdleSeconds: int(idle.Seconds())}
	if pc.IdleShutdown && limit > 0 {
		left := max(int((limit - idle).Seconds()), 0)
		ev.ShutdownIn = &left
	}
	s.publish("power.idle", ev)

	if !pc.IdleShutdown || limit <= 0 || idle < limit || already {
		return
	}
	log.Printf("power: idle for %d minutes; powering off", pc.IdleMinutes)
	s.publish("system.message", map[string]string{
		"level": "info",
		"text":  fmt.Sprintf("Nothing has happened for %d minutes, so VaporOS is switching off. Wake it with Wake-on-LAN (Moonlight does this for you).", pc.IdleMinutes),
	})
	s.mu.Lock()
	s.poweringOff = true
	s.mu.Unlock()
	if err := s.poweroff(ctx); err != nil {
		log.Printf("power: poweroff: %v", err)
		s.publish("system.message", map[string]string{"level": "error", "text": "Automatic power-off failed: " + err.Error()})
		s.mu.Lock()
		s.poweringOff = false
		s.idleSince = now // try again after another full idle period
		s.mu.Unlock()
	}
}

// busyInfo is the "busy" object of GET /power.
type busyInfo struct {
	Reason string `json:"reason"`
}

type powerState struct {
	IdleShutdown   bool       `json:"idle_shutdown"`
	IdleMinutes    int        `json:"idle_minutes"`
	KeepAwakeUntil *time.Time `json:"keep_awake_until,omitempty"`
	WoL            []WoLIface `json:"wol"`
	Busy           *busyInfo  `json:"busy"`
	IdleSeconds    int        `json:"idle_seconds"`
	ShutdownIn     *int       `json:"shutdown_in"`
}

// snapshot builds the GET /power answer. The busy reason is the cheap
// checks now, else what the last policy pass saw (the process, download
// and I/O checks are too slow to repeat per request), else web activity.
func (s *Service) snapshot(ctx context.Context) powerState {
	now := s.now()
	pc := s.settings()
	st := powerState{IdleShutdown: pc.IdleShutdown, IdleMinutes: pc.IdleMinutes, WoL: s.wolStatus(ctx)}

	reason := s.quickReason(now)
	s.mu.Lock()
	if !s.keepUntil.IsZero() && now.Before(s.keepUntil) {
		t := s.keepUntil.UTC().Truncate(time.Second)
		st.KeepAwakeUntil = &t
	}
	last, idleSince := s.state, s.idleSince
	s.mu.Unlock()
	if reason == "" && last != "idle" && last != "" {
		reason = last
	}
	if reason == "" && s.webActive(now) {
		reason = "web UI in use"
	}
	if reason != "" {
		st.Busy = &busyInfo{Reason: reason}
		return st
	}
	if !idleSince.IsZero() {
		idle := now.Sub(idleSince)
		st.IdleSeconds = int(idle.Seconds())
		if limit := time.Duration(pc.IdleMinutes) * time.Minute; pc.IdleShutdown && limit > 0 {
			left := max(int((limit - idle).Seconds()), 0)
			st.ShutdownIn = &left
		}
	}
	return st
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, s.snapshot(r.Context()))
}

// handlePut changes idle_shutdown and/or idle_minutes.
func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IdleShutdown *bool `json:"idle_shutdown"`
		IdleMinutes  *int  `json:"idle_minutes"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.IdleMinutes != nil && (*req.IdleMinutes < 1 || *req.IdleMinutes > 24*60) {
		api.Error(w, http.StatusBadRequest, "idle_minutes must be between 1 and 1440")
		return
	}
	var prev config.PowerConfig
	err := s.cfg.Mutate(func(c *config.Config) {
		prev = c.Power
		if req.IdleShutdown != nil {
			c.Power.IdleShutdown = *req.IdleShutdown
		}
		if req.IdleMinutes != nil {
			c.Power.IdleMinutes = *req.IdleMinutes
		}
	})
	if err != nil {
		// config.json keeps the old settings; so does memory (saving may
		// fail again, which changes nothing).
		s.cfg.Mutate(func(c *config.Config) { c.Power = prev })
		api.Error(w, http.StatusInternalServerError, "cannot save configuration: %v", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s.snapshot(r.Context()))
}

// handleKeepAwake sets (minutes > 0) or clears (0) keep-awake. Clearing
// also removes the manual keep-awake file: "stay on" off means off.
func (s *Service) handleKeepAwake(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Minutes *int `json:"minutes"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.Minutes == nil || *req.Minutes < 0 || time.Duration(*req.Minutes)*time.Minute > maxKeepAwake {
		api.Error(w, http.StatusBadRequest, "minutes must be between 0 and %d", int(maxKeepAwake/time.Minute))
		return
	}
	now := s.now()
	s.mu.Lock()
	if *req.Minutes == 0 {
		s.keepUntil = time.Time{}
	} else {
		s.keepUntil = now.Add(time.Duration(*req.Minutes) * time.Minute)
		s.idleSince = now
	}
	s.mu.Unlock()
	if *req.Minutes == 0 {
		if err := os.Remove(keepAwakePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			api.Error(w, http.StatusInternalServerError, "cannot clear %s: %v", keepAwakePath(), err)
			return
		}
	}
	api.OK(w)
}
