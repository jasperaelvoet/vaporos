package display

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/drm"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// fakeHost stands in for systemd, sysfs/DRM and gamescope. Its gamescope
// behaves like the real one where it matters: when started, restarted or
// nudged it looks its display up in modes.cfg by "<Make> <Model>" and
// scans that mode out; its composite_force convar follows both
// `gamescopectl composite_force` and the GAMESCOPE_COMPOSITE_FORCE root
// property, and without it the game scans out on direct planes.
type fakeHost struct {
	mu         sync.Mutex
	active     map[string]bool   // "unit" or "unit@user"
	restarting map[string]bool   // waiting out RestartSec: "activating", not active
	states     map[string]string // UnitState's answer, overriding the two above
	calls      []string
	gpu        GPUInfo
	conns      []drm.SysConnector
	modes      []edid.Mode // what the virtual connector offers (nil: unknown)
	scan       edid.Mode
	scanOK     bool
	scanErr    error
	gsKey      string // display key the fake gamescope uses
	gsReport   string // key gamescopectl reports ("" = same as gsKey)
	gsHDR      bool   // HDR flag the fake gamescope runs with
	ignoreMC   bool   // gamescope ignores modes.cfg (to test timeouts)
	// ApplyEDID: applyErr makes it fail, applyNoop makes the kernel keep
	// its EDID, gsStale makes gamescope miss the modes it adds.
	applyErr  error
	applyNoop bool
	gsStale   bool
	gsMissing map[edid.Mode]bool // modes gamescope does not know of
	// composite is gamescope's composite_force convar; props are the X root
	// window properties of its Xwayland (both reset when gamescope starts).
	composite bool
	props     map[string]string
	direct    int           // planes scanned out without composition (0: 1)
	xErr      error         // xprop fails (no X server)
	ctlDelay  time.Duration // gamescopectl takes this long
	busy      string
	game      bool   // a Steam game runs (GameRunning)
	sunApp    string // Sunshine's serverinfo: "busy", "free" or "" (no answer)
	ips       []string
	startErr  error
	hotplug   chan struct{}
	// hotplugCalls counts Hotplug; panicIPs makes that many LocalIPs
	// calls panic (to restart Run as the daemon does).
	hotplugCalls int
	panicIPs     int
	// Steam in the fake gamescope: steamPID holds steam.pipe (0: none),
	// and a new one comes with every gamescope start. steamExits makes
	// it obey `steam -shutdown`, after which systemd starts gamescope
	// again; shutdownErr makes the command fail. gsJob and gsMain are the
	// unit's last start job and main process start (UnitStarted), set
	// from clock at every start.
	steamPID        int
	steamExits      bool
	shutdownErr     error
	shutdownGate    chan struct{} // ShutdownSteam waits for it to close
	shutdownStarted chan struct{} // closed when ShutdownSteam starts waiting
	gsJob, gsMain   time.Time
	gsStarts        int
	clock           func() time.Time
}

func unitKey(unit string, user bool) string {
	if user {
		return unit + "@user"
	}
	return unit
}

func (f *fakeHost) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeHost) UnitActive(ctx context.Context, unit string, user bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[unitKey(unit, user)]
}

// UnitState: what states says, else active, activating (restarting) or
// inactive.
func (f *fakeHost) UnitState(ctx context.Context, unit string, user bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := unitKey(unit, user)
	switch {
	case f.states[k] != "":
		return f.states[k]
	case f.active[k]:
		return "active"
	case f.restarting[k]:
		return "activating"
	}
	return "inactive"
}

// UnitStopped: the fake's units are either active or stopped, except one
// marked restarting (waiting out RestartSec: neither).
func (f *fakeHost) UnitStopped(ctx context.Context, unit string, user bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := unitKey(unit, user)
	return !f.active[k] && !f.restarting[k]
}

func (f *fakeHost) StartUnit(ctx context.Context, unit string, user bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("start %s", unit)
	if f.startErr != nil {
		return f.startErr
	}
	f.active[unitKey(unit, user)] = true
	if unit == GamescopeUnit {
		f.gamescopeStartedLocked()
	}
	return nil
}

func (f *fakeHost) StopUnit(ctx context.Context, unit string, user bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("stop %s", unit)
	f.active[unitKey(unit, user)] = false
	delete(f.restarting, unitKey(unit, user)) // a stop cancels a pending restart
	if unit == GamescopeUnit {
		f.scanOK = false
		f.composite, f.props = false, nil
	}
	return nil
}

func (f *fakeHost) RestartUnit(ctx context.Context, unit string, user bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("restart %s", unit)
	f.active[unitKey(unit, user)] = true
	if unit == GamescopeUnit {
		f.gamescopeStartedLocked()
	}
	return nil
}

// gamescopeStartedLocked reads the env file and modes.cfg like gamescope;
// the convar starts off and the fresh Xwayland has no properties.
func (f *fakeHost) gamescopeStartedLocked() {
	if b, err := os.ReadFile(GamescopeEnvPath()); err == nil {
		f.gsHDR = parseGamescopeEnv(b).HDR
	}
	f.gsStarts++
	var now time.Time
	if f.clock != nil {
		now = f.clock()
	}
	f.gsJob = now.Add(time.Duration(f.gsStarts) * time.Microsecond)
	f.gsMain = f.gsJob.Add(time.Second)
	if f.steamPID != 0 {
		f.steamPID += 100
	}
	f.composite, f.props = false, nil
	f.applySavedModeLocked()
}

func (f *fakeHost) applySavedModeLocked() {
	if f.ignoreMC {
		f.scan, f.scanOK = edid.Preferred, true
		return
	}
	b, _ := os.ReadFile(ModesCfgPath())
	for _, line := range strings.Split(string(b), "\n") {
		key, mode, ok := strings.Cut(line, ":")
		if ok && key == f.gsKey {
			if m, err := edid.ParseMode(mode); err == nil {
				if f.gsMissing[m] {
					return // gamescope keeps what it shows
				}
				f.scan, f.scanOK = m, true
				return
			}
		}
	}
	f.scan, f.scanOK = edid.Preferred, true
}

func (f *fakeHost) GPU() GPUInfo { return f.gpu }

func (f *fakeHost) Connectors(card string) []drm.SysConnector {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]drm.SysConnector(nil), f.conns...)
}

func (f *fakeHost) ConnectorModes(card, name string) []edid.Mode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.modes
}

func (f *fakeHost) Scanout(card, name string) (edid.Mode, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scan, f.scanOK, f.scanErr
}

// Planes is what the virtual connector's CRTC scans out: nothing without
// gamescope, one plane while it composites, f.direct planes otherwise.
func (f *fakeHost) Planes(card, name string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.scanErr != nil:
		return 0, f.scanErr
	case !f.scanOK:
		return 0, nil
	case f.composite:
		return 1, nil
	}
	return max(f.direct, 1), nil
}

// ApplyEDID is the kernel taking a new EDID: the connector's sysfs edid
// and, when known, its mode list follow it.
func (f *fakeHost) ApplyEDID(card, name string, b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("apply-edid %s %s", card, name)
	if f.applyErr != nil {
		return f.applyErr
	}
	if f.applyNoop {
		return nil
	}
	info, err := edid.Decode(b)
	if err != nil {
		return err
	}
	var before []edid.Mode
	for _, c := range f.conns {
		if c.Name == name && c.Dir != "" {
			if old, err := edid.Decode(c.EDID()); err == nil {
				before = old.Modes()
			}
			if err := os.WriteFile(filepath.Join(c.Dir, "edid"), b, 0o644); err != nil {
				return err
			}
		}
	}
	if f.gsStale {
		if f.gsMissing == nil {
			f.gsMissing = map[edid.Mode]bool{}
		}
		for _, md := range info.Modes() {
			if !slices.Contains(before, md) {
				f.gsMissing[md] = true
			}
		}
	}
	if f.modes != nil {
		f.modes = info.Modes()
	}
	return nil
}

func (f *fakeHost) Gamescopectl(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	delay := f.ctlDelay
	f.mu.Unlock()
	if delay > 0 && !sleepCtx(ctx, delay) {
		return "", ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("gamescopectl %s", strings.Join(args, " "))
	if !f.active[unitKey(GamescopeUnit, true)] {
		return "", errors.New("Failed to open GAMESCOPE_WAYLAND_DISPLAY.")
	}
	if len(args) == 2 && args[0] == "composite_force" {
		f.composite = args[1] != "0"
		return "", nil // gamescopectl never echoes a convar
	}
	if len(args) == 0 {
		key := f.gsReport
		if key == "" {
			key = f.gsKey
		}
		mk := strings.TrimSuffix(key, " VaporOS")
		return fmt.Sprintf("gamescope version 3.16.31\ngamescope_control info:\n  - Connector Name: DP-1\n  - Display Make: %s\n  - Display Model: VaporOS\n  - Display Flags: 0x0\n", mk), nil
	}
	if args[0] == "backend_set_dirty" {
		f.applySavedModeLocked()
	}
	return "", nil
}

// Xprop understands `-root -f NAME FMT -set NAME VALUE` and `-root NAME`.
// Setting GAMESCOPE_COMPOSITE_FORCE sets the convar, as gamescope's
// PropertyNotify handler does.
func (f *fakeHost) Xprop(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("xprop %s", strings.Join(args, " "))
	if f.xErr != nil {
		return "", f.xErr
	}
	if !f.active[unitKey(GamescopeUnit, true)] {
		return "", errors.New("xprop:  unable to open display ':0'")
	}
	switch {
	case len(args) == 7 && args[0] == "-root" && args[4] == "-set":
		f.setPropLocked(args[5], args[6])
		return "", nil
	case len(args) == 2 && args[0] == "-root":
		if v, ok := f.props[args[1]]; ok {
			return fmt.Sprintf("%s(CARDINAL) = %s", args[1], v), nil
		}
		return args[1] + ":  not found.", nil
	}
	return "", fmt.Errorf("fake xprop: unexpected %v", args)
}

func (f *fakeHost) setPropLocked(name, value string) {
	if f.props == nil {
		f.props = map[string]string{}
	}
	f.props[name] = value
	if name == compositeForceProp {
		f.composite = value != "0"
	}
}

// steamWrites is Steam writing the composite property on its own.
func (f *fakeHost) steamWrites(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setPropLocked(compositeForceProp, value)
}

func (f *fakeHost) compositeState() (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.composite, f.props[compositeForceProp]
}

func (f *fakeHost) Busy(ctx context.Context) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy != "", f.busy
}

func (f *fakeHost) GameRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.game
}

func (f *fakeHost) UnitStarted(ctx context.Context, unit string, user bool) (time.Time, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if unit != GamescopeUnit || !user {
		return time.Time{}, time.Time{}
	}
	return f.gsJob, f.gsMain
}

func (f *fakeHost) SteamPID() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.steamPID
}

// ShutdownSteam: a Steam that obeys exits, gamescope with it, and systemd
// starts both again (Restart=always). With shutdownGate the command first
// waits for the gate to close (or ctx to end, its error then).
func (f *fakeHost) ShutdownSteam(ctx context.Context, pid int) error {
	f.mu.Lock()
	f.record("steam -shutdown %d", pid)
	gate, started := f.shutdownGate, f.shutdownStarted
	f.mu.Unlock()
	if gate != nil {
		if started != nil {
			close(started)
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shutdownErr != nil {
		return f.shutdownErr
	}
	if f.steamExits && f.active[unitKey(GamescopeUnit, true)] {
		f.gamescopeStartedLocked()
	}
	return nil
}

func (f *fakeHost) SunshineApp(ctx context.Context) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sunApp == "busy", f.sunApp != ""
}

func (f *fakeHost) LocalIPs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicIPs > 0 {
		f.panicIPs--
		panic("fake: LocalIPs")
	}
	return f.ips
}

func (f *fakeHost) Hotplug(ctx context.Context) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hotplugCalls++
	return f.hotplug
}

func (f *fakeHost) GamerIDs() (int, int) { return -1, -1 }

func (f *fakeHost) setActive(unit string, user, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[unitKey(unit, user)] = on
}

func (f *fakeHost) isActive(unit string, user bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[unitKey(unit, user)]
}

func (f *fakeHost) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeHost) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeHost) setMonitor(connected bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.conns {
		if f.conns[i].Name == "HDMI-A-1" {
			f.conns[i].Status = map[bool]string{true: "connected", false: "disconnected"}[connected]
		}
	}
}

// clock is a manual clock for the policy's timers.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testEnv points every path the display package touches into a temp dir.
type testEnv struct {
	dir     string
	edidDir string
}

func setupPaths(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	save := struct {
		state, run, home, img, host, proc, imgInfo, rt, pnp, x11 string
	}{config.StateDir, config.RunDir, config.GamerHome, config.ImageEDIDPath, config.HostnamePath,
		config.ProcCmdline, config.ImageInfoPath, config.GamerRuntimeDir, PNPIDsPath, X11SocketDir}
	t.Cleanup(func() {
		config.StateDir, config.RunDir, config.GamerHome = save.state, save.run, save.home
		config.ImageEDIDPath, config.HostnamePath, config.ProcCmdline = save.img, save.host, save.proc
		config.ImageInfoPath, config.GamerRuntimeDir, PNPIDsPath, X11SocketDir = save.imgInfo, save.rt, save.pnp, save.x11
		resetPNPCache()
	})
	config.StateDir = filepath.Join(dir, "var/lib/vos")
	config.RunDir = filepath.Join(dir, "run/vos")
	config.GamerHome = filepath.Join(dir, "home/vapor")
	config.ImageEDIDPath = filepath.Join(dir, "usr/lib/firmware/edid/vaporos.bin")
	config.HostnamePath = filepath.Join(dir, "etc/hostname")
	config.ProcCmdline = filepath.Join(dir, "proc/cmdline")
	config.ImageInfoPath = filepath.Join(dir, "usr/lib/vos/image.json")
	config.GamerRuntimeDir = filepath.Join(dir, "run/user/1000")
	PNPIDsPath = filepath.Join(dir, "pnp.ids")
	X11SocketDir = filepath.Join(dir, "tmp/.X11-unix")
	resetPNPCache()

	mustWrite(t, config.HostnamePath, "vapor\n")
	mustWrite(t, config.ProcCmdline, "quiet vos.slot=a video=DP-1:e drm.edid_firmware=DP-1:edid/vaporos.bin\n")
	// Lines from hwdata's pnp.ids: VPR is taken (Best Buy), VOS is not listed.
	mustWrite(t, PNPIDsPath, "DEL\tDell Inc.\nVPR\tBest Buy\n")
	// Both exist on a real system (tmpfiles, logind); vosd never makes them.
	for _, d := range []string{config.GamerRuntimeDir, config.GamerHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	res, err := edid.Generate(nil)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, config.ImageEDIDPath, string(res.EDID))
	env := &testEnv{dir: dir, edidDir: filepath.Join(dir, "sys/card1-DP-1")}
	mustWrite(t, filepath.Join(env.edidDir, "edid"), string(res.EDID))
	return env
}

func resetPNPCache() {
	pnpOnce = sync.Once{}
	pnpDB = nil
	keyMu.Lock()
	keyCache = map[[32]byte]string{}
	keyMu.Unlock()
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestManager builds a Manager on an AMD machine with a forced virtual
// DP-1 and an HDMI port (monitor plugged in or not).
func newTestManager(t *testing.T, monitor bool) (*Manager, *fakeHost, *clock, *events.Hub) {
	t.Helper()
	env := setupPaths(t)
	h := &fakeHost{
		active:     map[string]bool{},
		restarting: map[string]bool{},
		gpu:        GPUInfo{Vendor: "amd", Name: "Navi 48", Driver: "amdgpu", Card: "/dev/dri/card1", Supported: true, cardName: "card1"},
		gsKey:      "VOS VaporOS", // gamescope's Make falls back to the raw PNP id
		ips:        []string{"192.168.1.50"},
		direct:     2, // the spike: a primary plus a scaled overlay
		hotplug:    nil,
	}
	h.conns = []drm.SysConnector{
		{Card: "card1", Name: "DP-1", Type: "DP", Status: "connected", Dir: env.edidDir},
		{Card: "card1", Name: "DP-2", Type: "DP", Status: "disconnected"},
		{Card: "card1", Name: "HDMI-A-1", Type: "HDMI-A", Status: "disconnected"},
		{Card: "card1", Name: "Writeback-1", Type: "Writeback", Status: "unknown"},
	}
	h.setMonitor(monitor)
	cfg := config.Defaults()
	cfg.Display.VirtualConnector = "DP-1"
	hub := events.NewHub()
	m := newManager(cfg, h, hub)
	clk := &clock{t: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	m.now = clk.now
	h.mu.Lock()
	h.clock = clk.now
	h.mu.Unlock()
	m.steamWait, m.steamPoll, m.steamGateWait = 20*time.Millisecond, time.Millisecond, 0
	m.settle = 0
	m.poll = time.Millisecond
	m.recheckAfter = 0
	m.composeWait = 0
	return m, h, clk, hub
}
