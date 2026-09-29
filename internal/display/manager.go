package display

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/drm"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

// Units vosd drives (docs/CONTRACTS.md "Units").
const (
	WelcomeUnit   = "vos-welcome.service"   // system unit: `vos welcome`
	GamescopeUnit = "vos-gamescope.service" // user unit of the gaming user
)

// Display states, as GET /display reports them.
const (
	StateNone      = "none"      // nothing drives a screen
	StateWelcome   = "welcome"   // `vos welcome` owns the GPU
	StateGaming    = "gaming"    // gamescope + Steam own the GPU
	StateStreaming = "streaming" // gaming, with a Moonlight client attached
)

// sessionInfo describes the Moonlight session in progress.
type sessionInfo struct {
	Client string
	App    string
	Mode   string // chosen "WxH@R"
	HDR    bool
	Since  time.Time
	// freeSince is when Sunshine was first seen running no app during
	// this session (zero while it runs one); see dropStaleSession.
	freeSince time.Time
}

// staleSessionAfter is how long Sunshine must run no app before a session
// whose undo never came is ended.
const staleSessionAfter = 30 * time.Second

// Manager is vosd's display policy (docs/CONTRACTS.md "Display policy"):
//   - no supported GPU: never gamescope; the welcome screen whenever a
//     connector is connected;
//   - supported GPU, no monitor: gamescope and Steam run permanently on the
//     virtual connector;
//   - supported GPU and a monitor: the welcome screen while idle; a session
//     swaps it for gamescope, and 60 s after the session (with no game or
//     download running) the welcome screen comes back.
//
// Sessions additionally switch gamescope to the client's resolution.
type Manager struct {
	cfg  *config.Config
	h    host
	hub  *events.Hub
	live bool

	// Timings; tests shrink them.
	returnDelay  time.Duration // idle time before gamescope yields to the welcome screen
	settle       time.Duration // a new mode must hold this long
	modeTimeout  time.Duration // give up waiting for a mode after this
	recheckAfter time.Duration // re-ask gamescope for its display key after this
	poll         time.Duration // DRM polling interval while waiting for a mode
	scanEvery    time.Duration // connector re-scan
	refreshEvery time.Duration // hostname/IP refresh for the welcome screen
	verifyEvery  time.Duration // re-check that units match the state
	startBackoff time.Duration // don't retry a failed unit start sooner
	composeWait  time.Duration // how long gamescopectl gets to come up
	// beginBudget bounds a whole Begin. Sunshine runs `vos session begin`
	// as a prep-cmd and Moonlight's launch waits for it, so Begin answers
	// well inside the hook's 90 s ceiling (session.Timeout) whatever
	// systemd or gamescope do. The mode wait gets at most modeTimeout of it.
	beginBudget time.Duration
	// composeReserve is kept back from the budget for forcing composition
	// after the mode wait.
	composeReserve time.Duration
	// compositeEvery is how often the composite watchdog looks.
	compositeEvery time.Duration
	now            func() time.Time

	// op serialises everything that starts or stops units: sessions,
	// reconciliation and settings. Begin holds it while it waits for the
	// mode, so the policy loop only ever TryLocks it, and Begin itself
	// gives up waiting for it when its budget runs out.
	op opLock
	// gsHDR is the HDR flag the running gamescope was started with
	// (guarded by op: only Begin and apply start gamescope).
	gsHDR bool
	// kick wakes the Run loop to reconcile and refresh welcome.json.
	kick chan struct{}
	// startOnce starts what lives as long as vosd, not as long as one Run
	// (the daemon restarts Run after a panic): the session socket, the
	// composite watchdog and the hotplug watcher, whose channel is hot.
	startOnce sync.Once
	hot       <-chan struct{}

	mu           sync.Mutex // guards everything below
	code         string
	gpu          GPUInfo
	conns        []drm.SysConnector
	connSig      string
	state        string
	session      *sessionInfo
	holdUntil    time.Time
	rebootNeeded bool
	overlay      statusOverlay
	lastWelcome  *welcome.State
	lastStart    map[string]time.Time
	lastBusy     string
	composite    compositeWatch
}

// opLock is a mutex whose Lock can give up when a context ends.
type opLock chan struct{}

func newOpLock() opLock { return make(opLock, 1) }

// LockCtx takes the lock, or returns ctx's error once ctx ends first.
func (l opLock) LockCtx(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryLock takes the lock if it is free.
func (l opLock) TryLock() bool {
	select {
	case l <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l opLock) Unlock() { <-l }

// NewManager creates the display manager for vosd.
func NewManager(cfg *config.Config) *Manager {
	return newManager(cfg, newRealHost(), events.Default)
}

func newManager(cfg *config.Config, h host, hub *events.Hub) *Manager {
	if cfg == nil {
		cfg = config.Defaults()
	}
	// Probing sysfs is cheap; doing it now lets GET /display and /welcome
	// answer correctly even before Run starts.
	m := &Manager{
		gpu:          h.GPU(),
		cfg:          cfg,
		h:            h,
		hub:          hub,
		live:         config.IsLive(),
		returnDelay:  60 * time.Second,
		settle:       500 * time.Millisecond,
		modeTimeout:  60 * time.Second,
		recheckAfter: 5 * time.Second,
		poll:         100 * time.Millisecond,
		scanEvery:    2 * time.Second,
		refreshEvery: 5 * time.Second,
		verifyEvery:  15 * time.Second,
		startBackoff: 10 * time.Second,
		composeWait:  20 * time.Second,
		// The spike: gamescope lights the CRTC ~1 s after its start, and
		// Sunshine is ready ~3 s after. 75 s leaves the full 60 s mode wait
		// plus time for units and composition, and still answers the hook
		// (and Moonlight) well before its 90 s ceiling.
		beginBudget:    75 * time.Second,
		composeReserve: 5 * time.Second,
		compositeEvery: 5 * time.Second,
		now:            time.Now,
		op:             newOpLock(),
		state:          StateNone,
		lastStart:      map[string]time.Time{},
		kick:           make(chan struct{}, 1),
	}
	m.rescan()
	return m
}

// SetSetupCode sets the code the welcome screen shows ("" for none).
func (m *Manager) SetSetupCode(code string) {
	m.mu.Lock()
	m.code = code
	m.mu.Unlock()
	m.poke()
}

// Streaming reports whether a Moonlight session is active.
func (m *Manager) Streaming() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return false, ""
	}
	return true, "streaming to " + m.session.Client
}

// poke asks the Run loop to reconcile and refresh the welcome screen.
func (m *Manager) poke() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Run applies the display policy until ctx ends: hotplug, welcome vs
// gamescope, welcome.json, and the session socket. It leaves units as they
// are on exit, so restarting vosd never interrupts a stream.
//
// The daemon calls Run again (with the same ctx) after a panic. The
// session socket, the composite watchdog and the hotplug watcher are
// started by the first Run only, so a restart neither runs a second copy
// of them nor cuts short a session request in flight.
func (m *Manager) Run(ctx context.Context) {
	m.init(ctx)
	// After init: should init panic, the next Run starts these.
	m.startOnce.Do(func() { m.start(ctx) })
	hot := m.hot
	evs, unsubscribe := m.hub.Subscribe()
	defer unsubscribe()

	scan := time.NewTicker(m.scanEvery)
	defer scan.Stop()
	refresh := time.NewTicker(m.refreshEvery)
	defer refresh.Stop()
	verify := time.NewTicker(m.verifyEvery)
	defer verify.Stop()

	m.reconcile(ctx, true)
	m.refreshWelcome()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-hot:
			if !ok {
				hot = nil
				continue
			}
			m.onScan(ctx)
		case <-scan.C:
			m.onScan(ctx)
		case <-refresh.C:
			m.refreshWelcome()
		case <-verify.C:
			m.dropStaleSession(ctx)
			m.reconcile(ctx, true)
		case <-m.kick:
			m.reconcile(ctx, false)
			m.refreshWelcome()
		case ev, ok := <-evs:
			if !ok {
				evs = nil
				continue
			}
			m.mu.Lock()
			changed := m.overlay.apply(ev, m.now())
			m.mu.Unlock()
			if changed {
				m.refreshWelcome()
			}
		}
	}
}

// start runs once per vosd, from the first Run that gets past init.
func (m *Manager) start(ctx context.Context) {
	// Adopt the HDR flag of a gamescope that outlived the previous vosd.
	if m.op.LockCtx(ctx) == nil {
		m.gsHDR = readGamescopeEnv().HDR
		m.op.Unlock()
	}
	sock := config.SessionSock()
	go func() {
		if err := session.Serve(ctx, sock, m); err != nil {
			log.Printf("display: session socket: %v", err)
		}
	}()
	go m.watchComposite(ctx)
	m.hot = m.h.Hotplug(ctx)
}

// init learns the hardware and adopts whatever is already running.
func (m *Manager) init(ctx context.Context) {
	gpu := m.h.GPU()
	m.mu.Lock()
	m.gpu = gpu
	m.mu.Unlock()
	m.resolveVirtual()
	virtual := m.virtual()
	if gpu.Card != "" && virtual != "" {
		// Open the DRM observer now, before we start any display client.
		m.h.Scanout(gpu.Card, virtual)
	}
	m.rescan()

	state := StateNone
	switch {
	case m.h.UnitActive(ctx, GamescopeUnit, true):
		state = StateGaming
	case m.h.UnitActive(ctx, WelcomeUnit, false):
		state = StateWelcome
	}
	m.mu.Lock()
	m.state = state
	if state == StateGaming {
		// Perhaps a stream survived a vosd restart: give it the idle grace.
		m.holdUntil = m.now().Add(m.returnDelay)
	}
	m.mu.Unlock()
	log.Printf("display: gpu %q (%s, supported=%v), virtual connector %q, state %s",
		gpu.Name, gpu.Driver, gpu.Supported, virtual, state)
}

// displayConfig returns a copy of config.json's display settings. The
// shared config has its own lock; m.mu may be held when calling this, but
// no config callback ever takes m.mu.
func (m *Manager) displayConfig() config.DisplayConfig {
	var d config.DisplayConfig
	m.cfg.View(func(c *config.Config) {
		d = c.Display
		d.ExtraModes = slices.Clone(c.Display.ExtraModes)
	})
	return d
}

// virtual returns the configured virtual connector.
func (m *Manager) virtual() string {
	var v string
	m.cfg.View(func(c *config.Config) { v = c.Display.VirtualConnector })
	return v
}

// resolveVirtual fills config.display.virtual_connector on an installed
// system: from the kernel's `video=<C>:e` if present, else by choosing a
// free DP/HDMI connector (which then needs the machine cmdline and a reboot).
func (m *Manager) resolveVirtual() {
	if m.live {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.virtual() != "" {
		return
	}
	if c := forcedConnector(config.KernelArgs()); c != "" {
		if err := m.cfg.Mutate(func(cfg *config.Config) {
			if cfg.Display.VirtualConnector == "" {
				cfg.Display.VirtualConnector = c
			}
		}); err != nil {
			log.Printf("display: saving config: %v", err)
		}
		return
	}
	if !m.gpu.Supported {
		return
	}
	c := chooseVirtual(m.h.Connectors(m.gpu.cardName), "")
	if c == "" {
		log.Printf("display: no free DP or HDMI connector for the virtual display")
		return
	}
	if err := m.setVirtualLocked(c, nil); err != nil {
		log.Printf("display: setting up virtual connector %s: %v", c, err)
	}
}

// setVirtualLocked switches the virtual connector: config (together with
// whatever also changes in it), machine kernel cmdline and both slots'
// boot entries. It takes effect after a reboot.
func (m *Manager) setVirtualLocked(c string, also func(*config.Config)) error {
	old := readMachineCmdline()
	mc := MachineCmdlineFor(c)
	if err := m.cfg.Mutate(func(cfg *config.Config) {
		cfg.Display.VirtualConnector = c
		if also != nil {
			also(cfg)
		}
	}); err != nil {
		return err
	}
	m.rebootNeeded = true
	if err := boot.SetMachineCmdline("", mc); err != nil {
		return fmt.Errorf("machine cmdline: %w", err)
	}
	if err := boot.RewriteOptions(config.ESP, func(e boot.Entry) string {
		return replaceMachineArgs(e.Options, old, mc)
	}); err != nil {
		return fmt.Errorf("boot entries: %w", err)
	}
	log.Printf("display: virtual connector is now %s (reboot needed)", c)
	return nil
}

// readMachineCmdline reads /var/lib/vos/cmdline ("" if missing).
func readMachineCmdline() string {
	b, err := os.ReadFile(config.MachineCmdlinePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// rescan re-reads the connectors from sysfs and reports a change.
func (m *Manager) rescan() bool {
	m.mu.Lock()
	card := m.gpu.cardName
	m.mu.Unlock()
	conns := m.h.Connectors(card)
	var sb strings.Builder
	for _, c := range conns {
		fmt.Fprintf(&sb, "%s/%s=%s;", c.Card, c.Name, c.Status)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := sb.String() != m.connSig
	m.conns, m.connSig = conns, sb.String()
	return changed
}

func (m *Manager) onScan(ctx context.Context) {
	gpuChanged := m.reprobeGPU()
	if m.rescan() || gpuChanged {
		m.hub.Publish("display.changed", struct{}{})
		log.Printf("display: connectors changed")
	}
	m.reconcile(ctx, false)
	if gpuChanged {
		m.refreshWelcome()
	}
}

// reprobeGPU looks for the GPU again while none is supported, and reports
// whether what it found changed. amdgpu is not in the initramfs: udev
// loads it after switch_root, and its probe (firmware, display core) can
// finish seconds after vosd first looked. onScan runs on every DRM uevent,
// the card's arrival included, and every scanEvery.
func (m *Manager) reprobeGPU() bool {
	m.mu.Lock()
	cur := m.gpu
	m.mu.Unlock()
	if cur.Supported {
		return false
	}
	g := m.h.GPU()
	if g == cur {
		return false
	}
	m.mu.Lock()
	m.gpu = g
	m.mu.Unlock()
	log.Printf("display: gpu is now %q (%s, supported=%v)", g.Name, g.Driver, g.Supported)
	if g.Supported {
		m.resolveVirtual()
		if v := m.virtual(); g.Card != "" && v != "" {
			m.h.Scanout(g.Card, v) // open the DRM observer, as init does
		}
	}
	return true
}

// physicalConnectedLocked reports whether a monitor is attached: any
// connected connector other than the virtual one.
func (m *Manager) physicalConnectedLocked() bool {
	virtual := m.virtual()
	for _, c := range m.conns {
		if c.Connected() && c.Name != virtual && c.Type != "Writeback" {
			return true
		}
	}
	return false
}

// canGameLocked reports whether gamescope may run at all here: a
// supported GPU whose virtual connector the kernel really forces on. A
// connector that is configured but not connected waits for the reboot that
// applies its kernel arguments; gamescope would pick the wrong output.
func (m *Manager) canGameLocked() bool {
	return !m.live && m.gpu.Supported && m.virtualPresentLocked()
}

func (m *Manager) virtualPresentLocked() bool {
	v := m.virtual()
	for _, c := range m.conns {
		if c.Name == v && v != "" {
			return c.Connected()
		}
	}
	return false
}

// desiredLocked is the policy.
func (m *Manager) desiredLocked(now time.Time) string {
	physical := m.physicalConnectedLocked()
	if !m.canGameLocked() {
		if physical {
			return StateWelcome
		}
		return StateNone
	}
	if m.session != nil || !physical {
		return StateGaming
	}
	if m.state == StateGaming && now.Before(m.holdUntil) {
		return StateGaming
	}
	return StateWelcome
}

// reconcile moves the units to the desired state. verify also re-checks
// units when the state already matches (a crashed unit gets restarted).
func (m *Manager) reconcile(ctx context.Context, verify bool) {
	if !m.op.TryLock() {
		return // a session is switching modes; try again on the next tick
	}
	defer m.op.Unlock()
	now := m.now()
	m.mu.Lock()
	want, cur := m.desiredLocked(now), m.state
	m.mu.Unlock()
	if want == cur && !verify {
		return
	}
	if cur == StateGaming && want == StateWelcome {
		if busy, why := m.h.Busy(ctx); busy {
			m.mu.Lock()
			m.holdUntil = now.Add(m.returnDelay)
			logIt := why != m.lastBusy
			m.lastBusy = why
			m.mu.Unlock()
			if logIt {
				log.Printf("display: keeping gamescope: %s", why)
			}
			return
		}
	}
	m.mu.Lock()
	m.lastBusy = ""
	m.mu.Unlock()
	m.apply(ctx, want)
}

// apply makes the units match state. Callers hold m.op.
func (m *Manager) apply(ctx context.Context, state string) {
	switch state {
	case StateWelcome:
		m.ensureStopped(ctx, GamescopeUnit, true)
		m.ensureStarted(ctx, WelcomeUnit, false)
	case StateGaming:
		m.ensureStopped(ctx, WelcomeUnit, false)
		if err := m.writeGamescopeEnv(m.virtual(), m.gsHDR); err != nil {
			log.Printf("display: %v", err)
		}
		if m.ensureStarted(ctx, GamescopeUnit, true) {
			// A fresh gamescope scans out directly; composite everything into
			// one plane so Sunshine's KMS capture always finds the picture.
			go func() {
				if err := m.forceComposite(ctx); err != nil {
					log.Printf("display: forcing composition: %v", err)
				}
			}()
		}
	default:
		m.ensureStopped(ctx, GamescopeUnit, true)
		m.ensureStopped(ctx, WelcomeUnit, false)
	}
	m.mu.Lock()
	changed := m.state != state
	m.state = state
	m.mu.Unlock()
	if changed {
		log.Printf("display: state %s", state)
		m.hub.Publish("display.changed", struct{}{})
	}
}

// ensureStarted starts a unit unless it is active, rate-limiting retries.
// It reports whether it started the unit just now.
func (m *Manager) ensureStarted(ctx context.Context, unit string, user bool) bool {
	if m.h.UnitActive(ctx, unit, user) {
		return false
	}
	m.mu.Lock()
	last := m.lastStart[unit]
	now := m.now()
	if !last.IsZero() && now.Sub(last) < m.startBackoff {
		m.mu.Unlock()
		return false
	}
	m.lastStart[unit] = now
	m.mu.Unlock()
	if err := m.h.StartUnit(ctx, unit, user); err != nil {
		log.Printf("display: start %s: %v", unit, err)
		return false
	}
	m.mu.Lock()
	delete(m.lastStart, unit)
	m.mu.Unlock()
	return true
}

// ensureStopped stops a unit unless it is really down. A unit that is
// merely not active may be waiting out RestartSec (gamescope after Steam
// exited, say) and would come back to fight the other unit for DRM
// master; a stop cancels that pending restart.
func (m *Manager) ensureStopped(ctx context.Context, unit string, user bool) {
	if m.h.UnitStopped(ctx, unit, user) {
		return
	}
	if err := m.h.StopUnit(ctx, unit, user); err != nil {
		log.Printf("display: stop %s: %v", unit, err)
	}
}

// welcomeState computes what the welcome screen shows now.
func (m *Manager) welcomeState() welcome.State {
	ips := m.h.LocalIPs()
	version := config.BinaryVersion
	if ii, err := config.LoadImageInfo(); err == nil && ii.Version != "" {
		version = ii.Version
	}
	var https bool
	m.cfg.View(func(c *config.Config) { https = c.Web.HTTPS })
	m.mu.Lock()
	defer m.mu.Unlock()
	in := welcomeInputs{
		live:           m.live,
		hostname:       config.Hostname(),
		ips:            ips,
		code:           m.code,
		version:        version,
		https:          https,
		port:           config.HTTPPort,
		gpuSupported:   m.gpu.Supported,
		gpuName:        m.gpu.Name,
		virtualPending: m.gpu.Supported && !m.live && !m.virtualPresentLocked(),
		session:        m.session,
		overlay:        m.overlay,
		now:            m.now(),
	}
	if m.gpu.Vendor == "" || m.gpu.Vendor == "virtual" {
		in.gpuName = ""
	}
	return buildWelcome(in)
}

// refreshWelcome rewrites /run/vos/welcome.json when its content changes.
func (m *Manager) refreshWelcome() {
	st := m.welcomeState()
	m.mu.Lock()
	same := m.lastWelcome != nil && *m.lastWelcome == st
	m.mu.Unlock()
	if same {
		return
	}
	// Root-only: it holds the setup code, the one secret guarding an
	// unclaimed machine, and `vos welcome` (its only reader) runs as root.
	if err := config.WriteJSONAtomic(config.WelcomeStatePath(), st, 0o600); err != nil {
		log.Printf("display: welcome.json: %v", err)
		return
	}
	m.mu.Lock()
	m.lastWelcome = &st
	m.mu.Unlock()
}

// dropStaleSession ends a session whose undo (`vos session end`) never
// came: Sunshine crashed or was killed mid-stream and came back without
// an app, so nothing will ever send it. Until then the session would keep
// gamescope on a monitor machine and keep the machine from idling off.
// Only a clear "no app" from Sunshine for staleSessionAfter counts: an
// unreachable Sunshine proves nothing, and while a Begin runs Sunshine
// reports no app on purpose (it runs prep-cmds before the app).
func (m *Manager) dropStaleSession(ctx context.Context) {
	// Held across the probe (which gives Sunshine 2 s), so no Begin runs
	// while Sunshine's answer is taken.
	if !m.op.TryLock() {
		return // a Begin or End is running; look again next time
	}
	defer m.op.Unlock()
	m.mu.Lock()
	s := m.session
	m.mu.Unlock()
	if s == nil {
		return
	}
	busy, ok := m.h.SunshineApp(ctx)
	now := m.now()
	m.mu.Lock()
	if m.session != s { // a Begin or End that gave up waiting for op
		m.mu.Unlock()
		return
	}
	if !ok || busy {
		s.freeSince = time.Time{}
		m.mu.Unlock()
		return
	}
	if s.freeSince.IsZero() {
		s.freeSince = now
	}
	if now.Sub(s.freeSince) < staleSessionAfter {
		m.mu.Unlock()
		return
	}
	m.session = nil
	m.holdUntil = now.Add(m.returnDelay)
	m.mu.Unlock()
	log.Printf("display: Sunshine has run no app for %s; ending %s's session (its undo never came)", staleSessionAfter, s.Client)
	m.hub.Publish("session.end", struct{}{})
	m.poke()
}
