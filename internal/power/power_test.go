package power

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		log.SetOutput(io.Discard)
	}
	os.Exit(m.Run())
}

// isolate points the config paths this package uses into temp dirs.
func isolate(t *testing.T) {
	t.Helper()
	oldState, oldRun, oldImage, oldCmdline := config.StateDir, config.RunDir, config.ImageInfoPath, config.ProcCmdline
	t.Cleanup(func() {
		config.StateDir, config.RunDir, config.ImageInfoPath, config.ProcCmdline = oldState, oldRun, oldImage, oldCmdline
	})
	config.StateDir = t.TempDir()
	config.RunDir = t.TempDir()
	config.ImageInfoPath = filepath.Join(t.TempDir(), "image.json")
	config.ProcCmdline = filepath.Join(t.TempDir(), "cmdline")
}

// rig is a Service wired to a fake clock and fake probes.
type rig struct {
	*Service
	clock     time.Time
	game      bool
	download  bool
	io        uint64
	ioOK      bool
	keepFile  bool
	busyNow   bool
	poweroffs int
	powerErr  error
	events    []string
	lastIdle  idleEvent
}

func newRig(t *testing.T, minutes int, enabled bool) *rig {
	t.Helper()
	isolate(t)
	cfg := config.Defaults()
	cfg.Power = config.PowerConfig{IdleShutdown: enabled, IdleMinutes: minutes}
	r := &rig{clock: time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC), ioOK: true}
	s := NewService(cfg, func() (bool, string) { return r.busyNow, "Moonlight stream" }, nil)
	s.now = func() time.Time { return r.clock }
	s.poweroff = func(context.Context) error { r.poweroffs++; return r.powerErr }
	s.publish = func(topic string, data any) {
		r.events = append(r.events, topic)
		if ev, ok := data.(idleEvent); ok {
			r.lastIdle = ev
		}
	}
	s.gameRunning = func() bool { return r.game }
	s.downloading = func(time.Time) bool { return r.download }
	s.ioBytes = func() (uint64, bool) { return r.io, r.ioOK }
	s.keepFileSeen = func() bool { return r.keepFile }
	s.ethtool = func(context.Context, string) (string, error) { return "", errors.New("no ethtool") }
	s.sysNet = t.TempDir()
	r.Service = s
	s.start()
	return r
}

// step advances the clock one interval and runs the policy once.
func (r *rig) step() {
	r.clock = r.clock.Add(interval)
	r.tick(context.Background())
}

func (r *rig) steps(n int) {
	for range n {
		r.step()
	}
}

func TestIdlePowersOffAfterLimit(t *testing.T) {
	r := newRig(t, 1, true)
	r.steps(3) // 45 s idle
	if r.poweroffs != 0 {
		t.Fatal("powered off early")
	}
	if r.lastIdle.IdleSeconds != 45 || r.lastIdle.ShutdownIn == nil || *r.lastIdle.ShutdownIn != 15 {
		t.Errorf("idle event = %+v", r.lastIdle)
	}
	r.step() // 60 s
	if r.poweroffs != 1 {
		t.Fatalf("poweroffs = %d after the limit", r.poweroffs)
	}
	r.steps(3)
	if r.poweroffs != 1 {
		t.Errorf("poweroff requested %d times", r.poweroffs)
	}
	if !strings.Contains(strings.Join(r.events, ","), "system.message") {
		t.Errorf("no system.message before poweroff: %v", r.events)
	}
}

func TestEveryBusyReasonResetsTheTimer(t *testing.T) {
	type setter func(r *rig, on bool)
	cases := map[string]setter{
		"Moonlight stream":      func(r *rig, on bool) { r.busyNow = on },
		"manual keep-awake":     func(r *rig, on bool) { r.keepFile = on },
		"Steam game":            func(r *rig, on bool) { r.game = on },
		"Steam download/update": func(r *rig, on bool) { r.download = on },
	}
	for want, set := range cases {
		t.Run(want, func(t *testing.T) {
			r := newRig(t, 1, true)
			r.steps(3)
			set(r, true)
			r.step()
			if r.state != want {
				t.Errorf("state = %q, want %q", r.state, want)
			}
			if r.lastIdle.ShutdownIn != nil || r.lastIdle.IdleSeconds != 0 {
				t.Errorf("busy event = %+v", r.lastIdle)
			}
			set(r, false)
			r.steps(3)
			if r.poweroffs != 0 {
				t.Fatal("powered off although the timer was reset")
			}
			r.step()
			if r.poweroffs != 1 {
				t.Errorf("no poweroff a full minute after %s ended", want)
			}
		})
	}
}

func TestDiskActivity(t *testing.T) {
	r := newRig(t, 1, true)
	r.io += 2 << 20
	r.step()
	if r.state != "Steam disk activity" {
		t.Fatalf("state = %q after 2 MiB", r.state)
	}
	r.io += 512 << 10
	r.step()
	if r.state != "idle" {
		t.Errorf("state = %q after 0.5 MiB", r.state)
	}
	r.io = 10 // counter reset: the unit restarted
	r.step()
	if r.state != "idle" {
		t.Errorf("state = %q after a counter reset", r.state)
	}
	r.ioOK = false // gamescope not running
	r.step()
	r.ioOK = true
	r.io = 50 << 20 // first reading after it comes back is a baseline
	r.step()
	if r.state != "idle" {
		t.Errorf("state = %q on a fresh baseline", r.state)
	}
}

func TestKeepAwakeAndWebActivity(t *testing.T) {
	r := newRig(t, 1, true)
	w := httptest.NewRecorder()
	r.handleKeepAwake(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"minutes":2}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("keep-awake status %d", w.Code)
	}
	r.steps(7) // +1:45, the last tick before the 2-minute deadline
	if r.poweroffs != 0 || r.state != "keep-awake" {
		t.Fatalf("state %q, poweroffs %d during keep-awake", r.state, r.poweroffs)
	}
	r.steps(3) // +2:30: idle since the last keep-awake tick
	if r.poweroffs != 0 || r.state != "idle" {
		t.Fatalf("state %q, poweroffs %d right after keep-awake ended", r.state, r.poweroffs)
	}
	r.step()
	if r.poweroffs != 1 {
		t.Errorf("poweroffs = %d a minute after keep-awake", r.poweroffs)
	}

	r = newRig(t, 1, true)
	r.Touch()
	r.steps(19) // 4:45 < webActivityWindow
	if r.state != "web UI in use" || r.poweroffs != 0 {
		t.Fatalf("state %q during web activity", r.state)
	}
	r.steps(5)
	if r.poweroffs != 1 {
		t.Errorf("poweroffs = %d a minute after the web UI went quiet", r.poweroffs)
	}
}

func TestIdleShutdownDisabled(t *testing.T) {
	r := newRig(t, 1, false)
	r.steps(20)
	if r.poweroffs != 0 {
		t.Fatal("powered off with idle_shutdown off")
	}
	if r.lastIdle.IdleSeconds != 300 || r.lastIdle.ShutdownIn != nil {
		t.Errorf("idle event = %+v", r.lastIdle)
	}
}

func TestPoweroffFailureRetriesLater(t *testing.T) {
	r := newRig(t, 1, true)
	r.powerErr = errors.New("access denied")
	r.steps(4)
	if r.poweroffs != 1 {
		t.Fatalf("poweroffs = %d", r.poweroffs)
	}
	r.steps(3)
	if r.poweroffs != 1 {
		t.Fatal("retried before another idle period")
	}
	r.step()
	if r.poweroffs != 2 {
		t.Errorf("poweroffs = %d, want a retry after another minute", r.poweroffs)
	}
}

func TestRunDoesNothingLive(t *testing.T) {
	r := newRig(t, 1, true)
	os.WriteFile(config.ProcCmdline, []byte("quiet vos.mode=live vos.label=VOS_LIVE\n"), 0o644)
	done := make(chan struct{})
	go func() { r.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run kept running in live mode")
	}
}

func TestGameRunning(t *testing.T) {
	proc := t.TempDir()
	write := func(pid, env string) {
		os.MkdirAll(filepath.Join(proc, pid), 0o755)
		os.WriteFile(filepath.Join(proc, pid, "environ"), []byte(env), 0o644)
	}
	write("100", "HOME=/var/home/vapor\x00SteamAppId=0\x00")
	write("101", "SteamAppId=12x\x00")
	write("self", "SteamAppId=5\x00") // not a pid
	uid := os.Getuid()
	if gameRunning(proc, uid) {
		t.Fatal("no game should be running")
	}
	write("202", "A=1\x00SteamAppId=1091500\x00B=2")
	if !gameRunning(proc, uid) {
		t.Fatal("game not found")
	}
	if gameRunning(proc, uid+1) {
		t.Error("another user's game counted")
	}
	if gameRunning(filepath.Join(proc, "missing"), uid) {
		t.Error("missing /proc reported a game")
	}
}

func TestSteamDownloading(t *testing.T) {
	now := time.Now()
	lib := t.TempDir()
	old := filepath.Join(lib, "steamapps", "downloading", "1091500", "old.chunk")
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte("x"), 0o644)
	os.Chtimes(old, now.Add(-time.Hour), now.Add(-time.Hour))
	libs := []string{filepath.Join(lib, "missing"), lib}
	if steamDownloading(libs, now.Add(-downloadWindow)) {
		t.Fatal("an hour-old chunk counted as downloading")
	}
	fresh := filepath.Join(lib, "steamapps", "temp", "1091500", "new.chunk")
	os.MkdirAll(filepath.Dir(fresh), 0o755)
	os.WriteFile(fresh, []byte("x"), 0o644)
	if !steamDownloading(libs, now.Add(-downloadWindow)) {
		t.Error("fresh chunk in temp/ not seen")
	}
}

func TestIOBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "io.stat")
	os.WriteFile(p, []byte("259:0 rbytes=1048576 wbytes=4096 rios=10 wios=1 dbytes=0 dios=0\n8:16 rbytes=10 wbytes=5 rios=1 wios=1 dbytes=99 dios=1\n"), 0o644)
	if n, ok := ioBytes(p); !ok || n != 1048576+4096+10+5 {
		t.Errorf("ioBytes = %d, %v", n, ok)
	}
	if _, ok := ioBytes(p + ".missing"); ok {
		t.Error("missing io.stat reported ok")
	}
	want := "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice/vos-gamescope.service/io.stat"
	if got := gamescopeIOStat("/sys/fs/cgroup", 1000); got != want {
		t.Errorf("gamescopeIOStat = %q", got)
	}
}

func TestParseEthtoolWoL(t *testing.T) {
	out, err := os.ReadFile("testdata/ethtool-enp4s0.txt")
	if err != nil {
		t.Fatal(err)
	}
	sup, cur := parseEthtoolWoL(string(out))
	if sup != "pumbg" || cur != "g" {
		t.Errorf("parse = %q, %q", sup, cur)
	}
	if sup, cur := parseEthtoolWoL("Settings for eth0:\n\tLink detected: yes\n"); sup != "" || cur != "" {
		t.Errorf("no WoL lines parsed as %q, %q", sup, cur)
	}
}

func TestWoLStatus(t *testing.T) {
	r := newRig(t, 15, true)
	mk := func(name, typ, addr string, extra ...string) {
		d := filepath.Join(r.sysNet, name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "type"), []byte(typ+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "address"), []byte(addr+"\n"), 0o644)
		for _, e := range extra {
			os.MkdirAll(filepath.Join(d, e), 0o755)
		}
	}
	mk("enp4s0", "1", "a8:a1:59:00:00:01", "device")
	mk("enp5s0", "1", "a8:a1:59:00:00:02", "device")
	mk("wlan0", "1", "a8:a1:59:00:00:03", "device", "wireless")
	mk("lo", "772", "00:00:00:00:00:00")
	mk("veth1234", "1", "a8:a1:59:00:00:04")
	fixture, _ := os.ReadFile("testdata/ethtool-enp4s0.txt")
	r.ethtool = func(_ context.Context, iface string) (string, error) {
		if iface == "enp4s0" {
			return string(fixture), nil
		}
		return "Supports Wake-on: d\nWake-on: d\n", errors.New("exit status 75")
	}
	got := r.wolStatus(context.Background())
	want := []WoLIface{
		{Iface: "enp4s0", MAC: "a8:a1:59:00:00:01", Enabled: true, Supported: true},
		{Iface: "enp5s0", MAC: "a8:a1:59:00:00:02"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("wolStatus = %+v", got)
	}

	// Capable: some wired NIC supports magic-packet wake, armed or not.
	if !wolCapable(context.Background(), r.sysNet, r.ethtool) {
		t.Error("enp4s0 supports g, but the machine is not capable")
	}
	armedless := func(_ context.Context, iface string) (string, error) {
		if iface == "enp4s0" {
			return "Supports Wake-on: pumbg\nWake-on: d\n", nil
		}
		return "", errors.New("no ethtool")
	}
	if !wolCapable(context.Background(), r.sysNet, armedless) {
		t.Error("a NIC that supports g but is not armed yet must count")
	}
	none := func(context.Context, string) (string, error) { return "Supports Wake-on: pumb\nWake-on: d\n", nil }
	if wolCapable(context.Background(), r.sysNet, none) {
		t.Error("capable without any NIC supporting g")
	}
	// Only wired NICs count: Wi-Fi cannot wake a switched-off PC.
	wifiOnly := t.TempDir()
	os.MkdirAll(filepath.Join(wifiOnly, "wlan0", "device"), 0o755)
	os.MkdirAll(filepath.Join(wifiOnly, "wlan0", "wireless"), 0o755)
	os.WriteFile(filepath.Join(wifiOnly, "wlan0", "type"), []byte("1\n"), 0o644)
	if wolCapable(context.Background(), wifiOnly, func(context.Context, string) (string, error) { return "Supports Wake-on: g\n", nil }) {
		t.Error("Wi-Fi counted")
	}
}

func TestHandlers(t *testing.T) {
	r := newRig(t, 15, true)
	get := func() map[string]any {
		w := httptest.NewRecorder()
		r.handleGet(w, httptest.NewRequest("GET", "/api/v1/power", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET status %d", w.Code)
		}
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	m := get()
	if m["idle_shutdown"] != true || m["idle_minutes"] != float64(15) || m["busy"] != nil || m["wol"] == nil {
		t.Errorf("GET = %v", m)
	}
	if _, ok := m["keep_awake_until"]; ok {
		t.Error("keep_awake_until present without keep-awake")
	}

	put := func(body string) int {
		w := httptest.NewRecorder()
		r.handlePut(w, httptest.NewRequest("PUT", "/api/v1/power", strings.NewReader(body)))
		return w.Code
	}
	if c := put(`{"idle_minutes":30}`); c != http.StatusOK {
		t.Fatalf("PUT status %d", c)
	}
	var saved config.Config
	if err := config.ReadJSON(config.ConfigPath(), &saved); err != nil || saved.Power != (config.PowerConfig{IdleShutdown: true, IdleMinutes: 30}) {
		t.Errorf("saved power = %+v, %v", saved.Power, err)
	}
	if c := put(`{"idle_shutdown":false}`); c != http.StatusOK || r.cfg.Power.IdleShutdown || r.cfg.Power.IdleMinutes != 30 {
		t.Errorf("partial PUT: %d %+v", c, r.cfg.Power)
	}
	for _, bad := range []string{`{"idle_minutes":0}`, `{"idle_minutes":1441}`, `{`} {
		if c := put(bad); c != http.StatusBadRequest {
			t.Errorf("PUT %s = %d", bad, c)
		}
	}

	keep := func(body string) int {
		w := httptest.NewRecorder()
		r.handleKeepAwake(w, httptest.NewRequest("POST", "/api/v1/power/keep-awake", strings.NewReader(body)))
		return w.Code
	}
	if c := keep(`{"minutes":90}`); c != http.StatusOK {
		t.Fatalf("keep-awake status %d", c)
	}
	m = get()
	if m["keep_awake_until"] != "2026-09-29T21:30:00Z" || m["busy"].(map[string]any)["reason"] != "keep-awake" {
		t.Errorf("GET during keep-awake = %v", m)
	}
	os.WriteFile(keepAwakePath(), nil, 0o644)
	if c := keep(`{"minutes":0}`); c != http.StatusOK {
		t.Fatalf("clear status %d", c)
	}
	if _, err := os.Stat(keepAwakePath()); !os.IsNotExist(err) {
		t.Error("clearing keep-awake left the manual file")
	}
	if m = get(); m["keep_awake_until"] != nil {
		t.Errorf("keep-awake not cleared: %v", m)
	}
	for _, bad := range []string{`{}`, `{"minutes":-1}`, `{"minutes":20000}`} {
		if c := keep(bad); c != http.StatusBadRequest {
			t.Errorf("keep-awake %s = %d", bad, c)
		}
	}
}
