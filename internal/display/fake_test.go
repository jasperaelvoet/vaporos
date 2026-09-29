package display

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// scans that mode out.
type fakeHost struct {
	mu       sync.Mutex
	active   map[string]bool // "unit" or "unit@user"
	calls    []string
	gpu      GPUInfo
	conns    []drm.SysConnector
	modes    []edid.Mode // what the virtual connector offers (nil: unknown)
	scan     edid.Mode
	scanOK   bool
	scanErr  error
	gsKey    string // display key the fake gamescope uses
	gsReport string // key gamescopectl reports ("" = same as gsKey)
	gsHDR    bool   // HDR flag the fake gamescope runs with
	ignoreMC bool   // gamescope ignores modes.cfg (to test timeouts)
	busy     string
	ips      []string
	startErr error
	hotplug  chan struct{}
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
	if unit == GamescopeUnit {
		f.scanOK = false
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

// gamescopeStartedLocked reads the env file and modes.cfg like gamescope.
func (f *fakeHost) gamescopeStartedLocked() {
	if b, err := os.ReadFile(GamescopeEnvPath()); err == nil {
		f.gsHDR = parseGamescopeEnv(b).HDR
	}
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

func (f *fakeHost) ConnectorModes(card, name string) []edid.Mode { return f.modes }

func (f *fakeHost) Scanout(card, name string) (edid.Mode, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scan, f.scanOK, f.scanErr
}

func (f *fakeHost) Gamescopectl(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("gamescopectl %s", strings.Join(args, " "))
	if !f.active[unitKey(GamescopeUnit, true)] {
		return "", errors.New("Failed to open GAMESCOPE_WAYLAND_DISPLAY.")
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

func (f *fakeHost) Xprop(ctx context.Context, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("xprop %s", strings.Join(args, " "))
	return nil
}

func (f *fakeHost) Busy(ctx context.Context) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy != "", f.busy
}

func (f *fakeHost) LocalIPs() []string { return f.ips }

func (f *fakeHost) Hotplug(ctx context.Context) <-chan struct{} { return f.hotplug }

func (f *fakeHost) OwnByGamer(path string) error { return nil }

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
		config.ProcCmdline, config.ImageInfoPath, UserRuntimeDir, PNPIDsPath, X11SocketDir}
	t.Cleanup(func() {
		config.StateDir, config.RunDir, config.GamerHome = save.state, save.run, save.home
		config.ImageEDIDPath, config.HostnamePath, config.ProcCmdline = save.img, save.host, save.proc
		config.ImageInfoPath, UserRuntimeDir, PNPIDsPath, X11SocketDir = save.imgInfo, save.rt, save.pnp, save.x11
		resetPNPCache()
	})
	config.StateDir = filepath.Join(dir, "var/lib/vos")
	config.RunDir = filepath.Join(dir, "run/vos")
	config.GamerHome = filepath.Join(dir, "home/vapor")
	config.ImageEDIDPath = filepath.Join(dir, "usr/lib/firmware/edid/vaporos.bin")
	config.HostnamePath = filepath.Join(dir, "etc/hostname")
	config.ProcCmdline = filepath.Join(dir, "proc/cmdline")
	config.ImageInfoPath = filepath.Join(dir, "usr/lib/vos/image.json")
	UserRuntimeDir = filepath.Join(dir, "run/user/1000")
	PNPIDsPath = filepath.Join(dir, "pnp.ids")
	X11SocketDir = filepath.Join(dir, "tmp/.X11-unix")
	resetPNPCache()

	mustWrite(t, config.HostnamePath, "vapor\n")
	mustWrite(t, config.ProcCmdline, "quiet vos.slot=a video=DP-1:e drm.edid_firmware=DP-1:edid/vaporos.bin\n")
	mustWrite(t, PNPIDsPath, "DEL\tDell Inc.\nVPR\tBest Buy\n")
	if err := os.MkdirAll(UserRuntimeDir, 0o755); err != nil {
		t.Fatal(err)
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
		active:  map[string]bool{},
		gpu:     GPUInfo{Vendor: "amd", Name: "Navi 48", Driver: "amdgpu", Card: "/dev/dri/card1", Supported: true, cardName: "card1"},
		gsKey:   "Best Buy VaporOS",
		ips:     []string{"192.168.1.50"},
		hotplug: nil,
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
	m.settle = 0
	m.poll = time.Millisecond
	m.recheckAfter = 0
	m.composeWait = 0
	return m, h, clk, hub
}
