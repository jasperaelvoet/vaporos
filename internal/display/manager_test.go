package display

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

func TestChooseMode(t *testing.T) {
	avail := append(slices.Clone(edid.Catalogue), edid.Mode{W: 4096, H: 2160, Refresh: 60}, edid.Mode{W: 640, H: 480, Refresh: 60})
	for _, c := range []struct {
		want  edid.Mode
		got   string
		exact bool
	}{
		{md(2560, 1600, 60), "2560x1600@60", true},    // MacBook
		{md(2796, 1290, 120), "2796x1290@120", true},  // iPhone Pro Max
		{md(1280, 800, 90), "1280x800@90", true},      // Steam Deck OLED
		{md(3840, 2160, 120), "3840x2160@60", false},  // too fast for the link: same size, lower rate
		{md(2560, 1440, 165), "2560x1440@120", false}, // highest rate not above the client's
		{md(1920, 1080, 30), "1920x1080@60", false},   // nothing that slow: lowest above
		{md(2622, 1206, 120), "2556x1179@120", false}, // iPhone 16 Pro: same shape, smaller
		{md(1366, 768, 60), "1280x720@60", false},     // 16:9-ish laptop
		{md(2880, 1800, 120), "2560x1600@120", false}, // 16:10 retina
		{md(2340, 1080, 90), "1920x1080@60", false},   // odd phone shape: 1080p fallback
		{md(5120, 2160, 60), "3440x1440@60", false},   // 5K2K: the ultrawide shape
		{md(4096, 2160, 60), "1920x1080@60", false},   // blocklisted even though offered
		{md(1024, 768, 30), "1920x1080@60", false},    // 4:3: never the 640x480 fail-safe; 1080p, lowest rate
		{md(640, 480, 60), "640x480@60", true},        // unless asked for exactly
		{md(3440, 1440, 144), "3440x1440@100", false}, // ultrawide
		{md(1920, 1200, 144), "1920x1200@120", false},
	} {
		got, exact := chooseMode(c.want, avail)
		if got.String() != c.got || exact != c.exact {
			t.Errorf("chooseMode(%s) = %s exact=%v; want %s exact=%v", c.want, got, exact, c.got, c.exact)
		}
	}
	// With only an odd list, fall back to its first (preferred) mode.
	if got, _ := chooseMode(md(800, 600, 60), []edid.Mode{md(1024, 768, 75)}); got.String() != "1024x768@75" {
		t.Errorf("fallback = %s", got)
	}
	// An unknown list means the catalogue.
	if got, exact := chooseMode(md(2732, 2048, 60), nil); !exact || got.String() != "2732x2048@60" {
		t.Errorf("catalogue fallback = %s %v", got, exact)
	}
}

func TestClientMode(t *testing.T) {
	if m := clientMode(session.Request{}); m != (md(1920, 1080, 60)) {
		t.Errorf("defaults = %s", m)
	}
	if m := clientMode(session.Request{Width: 2560, Height: 1440, FPS: 144}); m != (md(2560, 1440, 144)) {
		t.Errorf("mode = %s", m)
	}
}

func TestPolicyNoMonitorRunsGamescope(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	if !h.isActive(GamescopeUnit, true) || h.isActive(WelcomeUnit, false) {
		t.Fatalf("units after start: %v", h.callLog())
	}
	env, err := os.ReadFile(GamescopeEnvPath())
	if err != nil || string(env) != "# Written by vosd; read by vos-gamescope.service.\nVOS_OUTPUT=DP-1\nVOS_GS_EXTRA=\n" {
		t.Errorf("gamescope.env = %q, %v", env, err)
	}
	if m.info().State != StateGaming {
		t.Errorf("state = %s", m.info().State)
	}
	// Composition is forced on the fresh gamescope (asynchronously).
	waitFor(t, func() bool { return slices.Contains(h.callLog(), "gamescopectl composite_force 1") })

	// A monitor appears while nothing streams or plays: welcome screen.
	h.setMonitor(true)
	m.onScan(ctx)
	if h.isActive(GamescopeUnit, true) || !h.isActive(WelcomeUnit, false) {
		t.Fatalf("after hotplug: %v", h.callLog())
	}
	// Unplugged again: back to gamescope.
	h.setMonitor(false)
	m.onScan(ctx)
	if !h.isActive(GamescopeUnit, true) || h.isActive(WelcomeUnit, false) {
		t.Fatalf("after unplug: %v", h.callLog())
	}
}

func TestPolicyNoGPU(t *testing.T) {
	m, h, _, _ := newTestManager(t, true)
	h.gpu = GPUInfo{Vendor: "virtual", Name: "Virtio GPU", Driver: "virtio_gpu", Card: "/dev/dri/card0", cardName: "card1"}
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	if h.isActive(GamescopeUnit, true) || !h.isActive(WelcomeUnit, false) {
		t.Fatalf("no-GPU with monitor: %v", h.callLog())
	}
	// A session changes nothing but is acknowledged.
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 90})
	if !resp.OK || h.isActive(GamescopeUnit, true) || !h.isActive(WelcomeUnit, false) {
		t.Fatalf("Begin without GPU: %+v %v", resp, h.callLog())
	}
	if ok, why := m.Streaming(); !ok || !strings.Contains(why, "Deck") {
		t.Errorf("Streaming = %v %q", ok, why)
	}
	m.End(ctx)
	// Monitor gone: nothing to show.
	h.setMonitor(false)
	m.onScan(ctx)
	if h.isActive(WelcomeUnit, false) || h.isActive(GamescopeUnit, true) {
		t.Fatalf("no GPU, no monitor: %v", h.callLog())
	}
	if d := m.info(); d.Profile != "none" || d.State != StateNone || len(d.Modes) != 0 {
		t.Errorf("info = %+v", d)
	}
}

func TestPolicyLiveNeverGames(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	m.live = true
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	if h.isActive(GamescopeUnit, true) {
		t.Fatal("gamescope started on the live ISO")
	}
	h.setMonitor(true)
	m.onScan(ctx)
	if !h.isActive(WelcomeUnit, false) {
		t.Fatal("installer welcome screen not shown")
	}
}

func TestSessionWithMonitor(t *testing.T) {
	m, h, clk, hub := newTestManager(t, true)
	ctx := context.Background()
	evs, cancel := hub.Subscribe()
	defer cancel()
	m.init(ctx)
	m.reconcile(ctx, false)
	if !h.isActive(WelcomeUnit, false) || h.isActive(GamescopeUnit, true) {
		t.Fatalf("idle with monitor: %v", h.callLog())
	}

	h.resetCalls()
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "MacBook", App: "Steam", Width: 2560, Height: 1600, FPS: 60, HDR: false})
	if !resp.OK || resp.Mode != "2560x1600@60" || resp.HDR {
		t.Fatalf("Begin = %+v", resp)
	}
	calls := h.callLog()
	iStop, iStart := slices.Index(calls, "stop "+WelcomeUnit), slices.Index(calls, "start "+GamescopeUnit)
	if iStop < 0 || iStart < 0 || iStop > iStart {
		t.Errorf("welcome must stop before gamescope starts: %v", calls)
	}
	if !slices.Contains(calls, "gamescopectl composite_force 1") {
		t.Errorf("composition not forced: %v", calls)
	}
	cfg, _ := os.ReadFile(ModesCfgPath())
	if !strings.Contains(string(cfg), "Best Buy VaporOS:2560x1600@60\n") {
		t.Errorf("modes.cfg = %q", cfg)
	}
	if got := drain(evs, "session.begin"); got == nil || !strings.Contains(string(got.Data), `"mode":"2560x1600@60"`) {
		t.Errorf("session.begin event = %+v", got)
	}
	if d := m.info(); d.State != StateStreaming || d.Current == nil || *d.Current != "2560x1600@60" {
		t.Errorf("info during stream = %+v", d)
	}
	st := m.welcomeState()
	if st.Status != "Streaming to MacBook" {
		t.Errorf("welcome status = %q", st.Status)
	}

	// Second client, same HDR: no restart, just a nudge to the new mode.
	h.resetCalls()
	resp = m.Begin(ctx, session.Request{Op: "begin", Client: "iPhone", Width: 2796, Height: 1290, FPS: 120})
	if !resp.OK || resp.Mode != "2796x1290@120" {
		t.Fatalf("second Begin = %+v", resp)
	}
	calls = h.callLog()
	if !slices.Contains(calls, "gamescopectl backend_set_dirty") || slices.Contains(calls, "restart "+GamescopeUnit) || slices.Contains(calls, "start "+GamescopeUnit) {
		t.Errorf("mode switch should nudge, not restart: %v", calls)
	}

	// HDR client: gamescope restarts with --hdr-enabled.
	h.resetCalls()
	resp = m.Begin(ctx, session.Request{Op: "begin", Client: "TV", Width: 3840, Height: 2160, FPS: 60, HDR: true})
	if !resp.OK || !resp.HDR || resp.Mode != "3840x2160@60" {
		t.Fatalf("HDR Begin = %+v", resp)
	}
	if !slices.Contains(h.callLog(), "restart "+GamescopeUnit) || !h.gsHDR {
		t.Errorf("HDR switch did not restart gamescope with HDR: %v", h.callLog())
	}
	env, _ := os.ReadFile(GamescopeEnvPath())
	if !strings.Contains(string(env), "VOS_GS_EXTRA=--hdr-enabled") {
		t.Errorf("env = %q", env)
	}

	// Session ends: gamescope stays for the grace period, then the welcome
	// screen returns unless a game or download keeps it.
	m.End(ctx)
	if drain(evs, "session.end") == nil {
		t.Error("no session.end event")
	}
	m.reconcile(ctx, false)
	if !h.isActive(GamescopeUnit, true) {
		t.Fatal("gamescope stopped before the grace period")
	}
	clk.advance(61 * time.Second)
	h.busy = "a Steam game is running"
	m.reconcile(ctx, false)
	if !h.isActive(GamescopeUnit, true) || h.isActive(WelcomeUnit, false) {
		t.Fatal("gamescope stopped while a game runs")
	}
	h.busy = ""
	clk.advance(30 * time.Second)
	m.reconcile(ctx, false)
	if !h.isActive(GamescopeUnit, true) {
		t.Fatal("busy should re-arm the full grace period")
	}
	clk.advance(31 * time.Second)
	m.reconcile(ctx, false)
	if h.isActive(GamescopeUnit, true) || !h.isActive(WelcomeUnit, false) {
		t.Fatalf("welcome did not return: %v", h.callLog())
	}

	var clients Clients
	if err := config.ReadJSON(config.ClientsPath(), &clients); err != nil || len(clients) != 3 || clients["TV"].FPS != 60 || !clients["TV"].HDR {
		t.Errorf("clients.json = %+v, %v", clients, err)
	}
}

func TestSessionLearnsMode(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	if m.rebootNeededNow() {
		t.Fatal("reboot needed before anything changed")
	}
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Pixel", Width: 2400, Height: 1080, FPS: 120})
	// 20:9 has no match yet: 1080p60 now, the exact mode after a reboot.
	if !resp.OK || resp.Mode != "1920x1080@60" || !strings.Contains(resp.Message, "learned") {
		t.Fatalf("Begin = %+v", resp)
	}
	b, err := os.ReadFile(config.LearnedEDIDPath())
	if err != nil {
		t.Fatal(err)
	}
	info, err := edid.Decode(b)
	if err != nil || !info.Has(md(2400, 1080, 120)) {
		t.Fatalf("learned EDID lacks 2400x1080@120: %v", err)
	}
	if !m.rebootNeededNow() {
		t.Error("reboot_needed not set after learning a mode")
	}
	if d := m.info(); !slices.Contains(d.Learned, "2400x1080@120") || !d.RebootNeeded {
		t.Errorf("info = %+v", d)
	}
	// Once the kernel runs with that EDID, nothing is pending.
	mustWrite(t, h.conns[0].Dir+"/edid", string(b))
	m.rebootNeeded = false
	if m.rebootNeededNow() {
		t.Error("reboot still needed with the learned EDID active")
	}
	// An impossible mode is recorded for the client but never learned.
	m.Begin(ctx, session.Request{Op: "begin", Client: "8K", Width: 7680, Height: 4320, FPS: 60})
	b2, _ := os.ReadFile(config.LearnedEDIDPath())
	if string(b2) != string(b) {
		t.Error("an impossible mode changed the EDID")
	}
}

func TestSessionKeyRecheck(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	// gamescope names the display differently than pnp.ids suggests (an
	// older hwdata, say): the first modes.cfg write misses, the recheck
	// asks gamescopectl and writes the right key.
	h.gsKey = "VPR VaporOS"
	h.gsReport = "VPR VaporOS"
	h.mu.Lock()
	h.active[unitKey(GamescopeUnit, true)] = false
	h.mu.Unlock()
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 90})
	if !resp.OK || resp.Mode != "1280x800@90" {
		t.Fatalf("Begin = %+v %v", resp, h.callLog())
	}
	cfg, _ := os.ReadFile(ModesCfgPath())
	if !strings.Contains(string(cfg), "VPR VaporOS:1280x800@90") {
		t.Errorf("modes.cfg = %q", cfg)
	}
}

func TestSessionModeTimeout(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	m.now = time.Now
	m.modeTimeout = 30 * time.Millisecond
	h.ignoreMC = true
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 60})
	if resp.OK || !strings.Contains(resp.Message, "instead of 1280x800@60") {
		t.Errorf("Begin = %+v", resp)
	}
	// Composition is still forced and the session is still tracked.
	if ok, _ := m.Streaming(); !ok {
		t.Error("session dropped after a timeout")
	}
}

func TestSessionGamescopeFailsToStart(t *testing.T) {
	m, h, _, _ := newTestManager(t, true)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	h.startErr = errors.New("unit not found")
	resp := m.Begin(ctx, session.Request{Op: "begin", Width: 1920, Height: 1080, FPS: 60})
	if resp.OK || !strings.Contains(resp.Message, "unit not found") {
		t.Errorf("Begin = %+v", resp)
	}
}

func TestSessionUnobservableDRM(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	h.scanErr = errors.New("permission denied")
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	resp := m.Begin(ctx, session.Request{Op: "begin", Width: 1920, Height: 1080, FPS: 120})
	if !resp.OK || !strings.Contains(resp.Message, "not verified") {
		t.Errorf("Begin = %+v", resp)
	}
}

func TestAdoptRunningGamescope(t *testing.T) {
	m, h, clk, _ := newTestManager(t, true)
	h.setActive(GamescopeUnit, true, true)
	ctx := context.Background()
	m.init(ctx)
	// vosd restarted mid-session: gamescope keeps running for the grace period.
	m.reconcile(ctx, false)
	if !h.isActive(GamescopeUnit, true) || h.isActive(WelcomeUnit, false) {
		t.Fatal("restart tore down gamescope")
	}
	clk.advance(2 * time.Minute)
	m.reconcile(ctx, false)
	if h.isActive(GamescopeUnit, true) || !h.isActive(WelcomeUnit, false) {
		t.Fatal("never returned to the welcome screen")
	}
}

func TestVerifyRestartsCrashedUnit(t *testing.T) {
	m, h, clk, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	h.setActive(GamescopeUnit, true, false) // crashed and systemd gave up
	m.reconcile(ctx, false)
	if h.isActive(GamescopeUnit, true) {
		t.Fatal("non-verify reconcile should not touch a matching state")
	}
	m.reconcile(ctx, true)
	if !h.isActive(GamescopeUnit, true) {
		t.Fatal("verify did not restart gamescope")
	}
	// Failed starts back off.
	h.setActive(GamescopeUnit, true, false)
	h.startErr = errors.New("boom")
	m.reconcile(ctx, true)
	n := len(h.callLog())
	m.reconcile(ctx, true)
	if len(h.callLog()) != n {
		t.Error("retried a failed start inside the backoff")
	}
	clk.advance(11 * time.Second)
	m.reconcile(ctx, true)
	if len(h.callLog()) == n {
		t.Error("never retried after the backoff")
	}
}

func TestResolveVirtualFromCmdline(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	m.cfg.Display.VirtualConnector = ""
	m.init(context.Background())
	if m.virtual() != "DP-1" {
		t.Errorf("virtual = %q", m.virtual())
	}
	saved, err := config.Load()
	if err != nil || saved.Display.VirtualConnector != "DP-1" {
		t.Errorf("config not saved: %+v %v", saved.Display, err)
	}
}

func TestWelcomeFileAndEvents(t *testing.T) {
	m, _, clk, _ := newTestManager(t, true)
	m.SetSetupCode("ABCD-EFGH")
	m.refreshWelcome()
	var st welcome.State
	if err := config.ReadJSON(config.WelcomeStatePath(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Code != "ABCD-EFGH" || st.QR != "http://192.168.1.50/setup?code=ABCD-EFGH" || st.URL != "http://vapor.local" || st.Mode != "os" {
		t.Errorf("welcome.json = %+v", st)
	}
	fi, _ := os.Stat(config.WelcomeStatePath())
	mt := fi.ModTime()
	time.Sleep(10 * time.Millisecond)
	m.refreshWelcome() // unchanged: not rewritten
	if fi2, _ := os.Stat(config.WelcomeStatePath()); !fi2.ModTime().Equal(mt) {
		t.Error("unchanged welcome.json rewritten")
	}

	ev := func(topic string, v any) events.Event {
		b, _ := json.Marshal(v)
		return events.Event{Topic: topic, Data: b}
	}
	m.overlay.apply(ev("pairing.pending", map[string]string{"name": "Jasper's iPhone"}), clk.now())
	if st := m.welcomeState(); st.Status != "Jasper's iPhone wants to pair" {
		t.Errorf("pairing status = %q", st.Status)
	}
	clk.advance(3 * time.Minute)
	if st := m.welcomeState(); strings.Contains(st.Status, "pair") {
		t.Errorf("stale pairing notice: %q", st.Status)
	}
}

// drain returns the first queued event with topic, or nil.
func drain(ch <-chan events.Event, topic string) *events.Event {
	for {
		select {
		case ev := <-ch:
			if ev.Topic == topic {
				return &ev
			}
		default:
			return nil
		}
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

// md is a short edid.Mode literal for tables.
func md(w, h, r int) edid.Mode { return edid.Mode{W: w, H: h, Refresh: r} }

func TestVirtualPendingReboot(t *testing.T) {
	m, h, _, _ := newTestManager(t, true)
	// Configured, but the running kernel does not force DP-1 on yet.
	h.conns[0].Status = "disconnected"
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	if h.isActive(GamescopeUnit, true) || !h.isActive(WelcomeUnit, false) {
		t.Fatalf("units: %v", h.callLog())
	}
	if st := m.welcomeState(); st.Status != "Restart to finish setup" {
		t.Errorf("welcome = %+v", st)
	}
	resp := m.Begin(ctx, session.Request{Op: "begin", Width: 1920, Height: 1080, FPS: 60})
	if !resp.OK || h.isActive(GamescopeUnit, true) {
		t.Errorf("Begin started gamescope without its output: %+v", resp)
	}
}
