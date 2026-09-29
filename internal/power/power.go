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
	"net"
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
	// webReason is the busy reason for web UI activity, the last one checked.
	webReason = "web UI in use"
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
	ifaceAddrs   func(iface string) ([]net.Addr, error)
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
		ifaceAddrs:   ifaceAddrs,
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

// keepReason is keep-awake, set in the web UI or by the manual file.
func (s *Service) keepReason(now time.Time) string {
	s.mu.Lock()
	keep := now.Before(s.keepUntil)
	s.mu.Unlock()
	if keep {
		return "keep-awake"
	}
	if s.keepFileSeen() {
		return "manual keep-awake"
	}
	return ""
}

// quickReason runs the cheap checks: keep-awake and the other services.
func (s *Service) quickReason(now time.Time) string {
	if r := s.keepReason(now); r != "" {
		return r
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

func (s *Service) webActive(now time.Time) bool { return !s.webUntil(now).IsZero() }

// webUntil is when the last web UI activity stops keeping the machine
// awake; the zero time when it no longer does.
func (s *Service) webUntil(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastTouch.IsZero() {
		return time.Time{}
	}
	until := s.lastTouch.Add(webActivityWindow)
	if !now.Before(until) {
		return time.Time{}
	}
	return until
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
		return webReason
	}
	return ""
}

// idleEvent is the power.idle event payload.
type idleEvent struct {
	IdleSeconds int       `json:"idle_seconds"`
	ShutdownIn  *int      `json:"shutdown_in"` // seconds; null when idle shutdown is off or busy
	Busy        *busyInfo `json:"busy"`        // null while idle
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
			s.publish("power.idle", idleEvent{Busy: newBusy(reason)})
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

// busyInfo is the "busy" object of GET /power and power.idle. Web is set
// when web UI activity is the only reason, so the UI can tell its own
// requests apart from something real.
type busyInfo struct {
	Reason string `json:"reason"`
	Web    bool   `json:"web,omitempty"`
}

func newBusy(reason string) *busyInfo {
	if reason == "" {
		return nil
	}
	return &busyInfo{Reason: reason, Web: reason == webReason}
}

// Summary is GET /power without wol.
type Summary struct {
	IdleShutdown   bool       `json:"idle_shutdown"`
	IdleMinutes    int        `json:"idle_minutes"`
	KeepAwakeUntil *time.Time `json:"keep_awake_until,omitempty"`
	Busy           *busyInfo  `json:"busy"`
	WebUntil       *time.Time `json:"web_until,omitempty"`
	IdleSeconds    int        `json:"idle_seconds"`
	ShutdownIn     *int       `json:"shutdown_in"`
}

type powerState struct {
	Summary
	WoL []WoLIface `json:"wol"`
}

// snapshot builds the GET /power answer, with the cheap checks run now.
func (s *Service) snapshot(ctx context.Context) powerState {
	wol := s.wolStatus(ctx)
	now := s.now()
	return powerState{s.summary(now, s.quickReason(now)), wol}
}

// Summary is GET /power without wol, for GET /status. It never waits on
// the other services or ethtool: only keep-awake is checked now, the
// other reasons come from the last policy pass, at most 15 s old.
func (s *Service) Summary() Summary {
	now := s.now()
	return s.summary(now, s.keepReason(now))
}

// summary is the busy reason checked just now (fresh), else what the last
// policy pass saw (the process, download and I/O checks are too slow to
// repeat per request), else web activity, which is cheap and so always
// read fresh; with the settings and the idle timer.
func (s *Service) summary(now time.Time, fresh string) Summary {
	pc := s.settings()
	st := Summary{IdleShutdown: pc.IdleShutdown, IdleMinutes: pc.IdleMinutes}
	reason := fresh
	s.mu.Lock()
	if !s.keepUntil.IsZero() && now.Before(s.keepUntil) {
		t := s.keepUntil.UTC().Truncate(time.Second)
		st.KeepAwakeUntil = &t
	}
	last, idleSince := s.state, s.idleSince
	s.mu.Unlock()
	if reason == "" && last != "idle" && last != "" && last != webReason {
		reason = last
	}
	if until := s.webUntil(now); !until.IsZero() {
		t := until.UTC().Truncate(time.Second)
		st.WebUntil = &t
		if reason == "" {
			reason = webReason
		}
	}
	if reason != "" {
		st.Busy = newBusy(reason)
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
