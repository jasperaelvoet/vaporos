package display

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/steamui"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

var (
	phoneAddr = netip.MustParseAddr("192.168.1.40")
	phoneMAC  = "aa:bb:cc:dd:ee:01"
	otherAddr = netip.MustParseAddr("192.168.1.41")
	otherMAC  = "aa:bb:cc:dd:ee:02"

	iPhone  = session.Request{Op: "begin", Client: "iPhone", Width: 2796, Height: 1290, FPS: 120}
	iPhoneM = md(2796, 1290, 120)
)

// newScaleTest is a headless box with gamescope up and Steam in it: its
// debugger answers, gamescope gives games X display :1, and the phone is
// the one client in Moonlight's /launch.
func newScaleTest(t *testing.T) (*Manager, *fakeHost, *clock, *events.Hub) {
	t.Helper()
	m, h, clk, hub := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	waitFor(t, func() bool { return compositeOK(h) })
	m.scaleStep, m.scaleReadback, m.scaleResend = time.Millisecond, 40*time.Millisecond, 10*time.Millisecond
	h.mu.Lock()
	h.steamPID = 4242
	h.steamEnv = map[string]string{"DISPLAY": ":0", "STEAM_GAME_DISPLAY_0": ":1"}
	h.peers = []netip.Addr{phoneAddr}
	h.macs = map[netip.Addr]string{phoneAddr: phoneMAC, otherAddr: otherMAC}
	h.mu.Unlock()
	h.ui.set(func(f *fakeSteamUI) { f.down = nil })
	return m, h, clk, hub
}

// wantGen is the generation the scaler holds now (0 for nothing).
func wantGen(m *Manager) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.want == nil {
		return 0
	}
	return m.want.gen
}

// storedScreen reads screen id back from screens.json.
func storedScreen(t *testing.T, id string) (Screen, bool) {
	t.Helper()
	b, err := os.ReadFile(config.ScreensPath())
	if err != nil {
		return Screen{}, false
	}
	var s Screens
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("screens.json: %v", err)
	}
	sc, ok := s.ByID[id]
	return sc, ok
}

func beginEvent(t *testing.T, evs <-chan events.Event) Session {
	t.Helper()
	ev := drain(evs, "session.begin")
	if ev == nil {
		t.Fatal("no session.begin")
	}
	var s Session
	if err := json.Unmarshal(ev.Data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func dpiProp(h *fakeHost, display string) string {
	v, _ := h.xstrProp(display, resourceManager)
	return v
}

// TestScaleAppliesAfterBegin: Begin works out the phone's screen without
// talking to Steam; the scaler then sets Steam's scale and the games'
// Xft.dpi, records them on the screen, and leaves Steam alone once it
// holds them.
func TestScaleAppliesAfterBegin(t *testing.T) {
	m, h, clk, hub := newScaleTest(t)
	ctx := context.Background()
	evs, cancel := hub.Subscribe()
	defer cancel()

	if resp := m.Begin(ctx, iPhone); !resp.OK || resp.Mode != "2796x1290@120" {
		t.Fatalf("Begin = %+v", resp)
	}
	if calls := h.ui.snapshot().calls; len(calls) != 0 {
		t.Errorf("Begin talked to Steam: %v", calls)
	}
	id := ScreenID("iPhone")
	want := ScreenRef{ID: id, Name: "iPhone", Kind: KindPhone, KindFrom: FromName, UIScale: 2.7, GameDPI: 168}
	if s := beginEvent(t, evs); s.Screen == nil || *s.Screen != want {
		t.Errorf("session.begin screen = %+v, want %+v", s.Screen, want)
	}
	if s := m.CurrentSession(); s == nil || s.Screen == nil || *s.Screen != want {
		t.Errorf("CurrentSession = %+v", s)
	}
	select {
	case <-m.scaleKick:
	default:
		t.Error("Begin did not wake the scaler")
	}

	m.scaleRound(ctx)
	ui := h.ui.snapshot()
	if ui.auto || ui.current != 2.7 || ui.gen != wantGen(m) || ui.gen == 0 {
		t.Errorf("Steam = auto %v, %.2f, gen %d (want %d)", ui.auto, ui.current, ui.gen, wantGen(m))
	}
	if v := dpiProp(h, ":1"); v != "Xft.dpi:\t168\n" {
		t.Errorf("games' RESOURCE_MANAGER = %q", v)
	}
	if _, ok := h.xstrProp(":0", resourceManager); ok {
		t.Error("Xft.dpi set on Steam's own display")
	}
	sc, ok := storedScreen(t, id)
	if !ok || sc.UIScale != 2.7 || sc.GameDPI != 168 || sc.Guess != KindPhone || sc.GuessFrom != FromName ||
		sc.MAC != phoneMAC || sc.IP != phoneAddr.String() || sc.Kind != "" || sc.Size != 1 || len(sc.Modes) != 1 || sc.Modes[0] != "2796x1290@120" {
		t.Errorf("stored screen = %+v (%v)", sc, ok)
	}
	if drain(evs, "display.changed") == nil {
		t.Error("no display.changed for what was applied")
	}
	d := m.Info()
	if !d.UIScaling || d.SteamUI != steamUIOK || len(d.Screens) != 1 {
		t.Fatalf("GET /display = scaling %v, steam_ui %q, screens %+v", d.UIScaling, d.SteamUI, d.Screens)
	}
	v := d.Screens[0]
	if v.ID != id || v.Kind != KindPhone || v.KindFrom != FromName || v.Guess != KindPhone || v.Size != 1 || v.SteamAuto ||
		v.Mode != "2796x1290@120" || v.UIScale != 2.7 || v.GameDPI != 168 || !v.Savable || !v.LastSeen.Equal(clk.now()) {
		t.Errorf("screens[0] = %+v", v)
	}

	// Steam holds it: nothing more to set.
	h.ui.resetCalls()
	h.resetCalls()
	for range 3 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if n := h.ui.count("set") + h.ui.count("auto"); n != 0 {
		t.Errorf("set Steam again while it held the scale: %v", h.ui.snapshot().calls)
	}
	for _, c := range h.callLog() {
		if strings.Contains(c, "-set "+resourceManager) {
			t.Errorf("rewrote Xft.dpi that was right: %q", c)
		}
	}

	// The end: the scaler holds nothing, and Steam keeps what it has.
	m.End(ctx)
	m.scaleRound(ctx)
	if d := m.Info(); d.SteamUI != steamUIIdle {
		t.Errorf("steam_ui after the session = %q", d.SteamUI)
	}
	if ui := h.ui.snapshot(); ui.current != 2.7 || h.ui.count("set") != 0 {
		t.Errorf("Steam after the session: %.2f %v", ui.current, ui.calls)
	}
}

// TestScaleReadsBackAndSetsOnce: a set Steam did not take is sent once
// more after scaleResend, and Steam's clamp is what vosd holds.
func TestScaleReadsBackAndSetsOnce(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	h.ui.set(func(f *fakeSteamUI) { f.ignore, f.max = 1, 2.5 })
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	if n := h.ui.count("set"); n != 2 {
		t.Errorf("sets = %d: %v", n, h.ui.snapshot().calls)
	}
	if ui := h.ui.snapshot(); ui.current != 2.5 {
		t.Errorf("Steam = %.2f", ui.current)
	}
	if sc, _ := storedScreen(t, ScreenID("iPhone")); sc.UIScale != 2.5 {
		t.Errorf("ui_scale = %v, want Steam's clamp", sc.UIScale)
	}
	// Steam's clamp is not the user's doing.
	h.ui.resetCalls()
	for range 6 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if sc, _ := storedScreen(t, ScreenID("iPhone")); h.ui.count("set") != 0 || sc.Kind != "" || sc.Size != 1 {
		t.Errorf("after the clamp: %v, screen %+v", h.ui.snapshot().calls, sc)
	}
	// The bounds were still those of the last mode; once Steam's catch up,
	// the value goes in whole.
	h.ui.set(func(f *fakeSteamUI) { f.max = 4.5 })
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.current != 2.7 || h.ui.count("set") != 1 {
		t.Errorf("after Steam's bounds moved: %.2f %v", ui.current, ui.calls)
	}
}

// TestScaleDropsStaleResults: what the scaler learns for a generation
// that ended meanwhile (the session ended, another device began) is
// dropped, never applied.
func TestScaleDropsStaleResults(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	h.ui.set(func(f *fakeSteamUI) {
		f.onRead = func(n int) {
			if n == 1 {
				m.End(ctx)
			}
		}
	})
	m.scaleRound(ctx)
	if n := h.ui.count("set"); n != 0 {
		t.Errorf("set Steam for a session that ended: %v", h.ui.snapshot().calls)
	}
	if v := dpiProp(h, ":1"); v != "" {
		t.Errorf("Xft.dpi for a session that ended: %q", v)
	}

	// Another device begins while vosd reads Steam back after a set.
	m.Begin(ctx, iPhone)
	deck := session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 90}
	h.ui.set(func(f *fakeSteamUI) {
		base := f.reads
		f.onRead = func(n int) {
			if n == base+2 {
				m.Begin(ctx, deck)
			}
		}
	})
	m.scaleRound(ctx)
	m.scaleMu.Lock()
	settled := m.sc.held.settled
	m.scaleMu.Unlock()
	if settled {
		t.Error("settled on the iPhone's scale after the Deck began")
	}
	h.ui.set(func(f *fakeSteamUI) { f.onRead = nil })
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.current != 1.5 || ui.gen != wantGen(m) {
		t.Errorf("Steam = %.2f gen %d, want the Deck's 1.50 at gen %d", ui.current, ui.gen, wantGen(m))
	}
	if v := dpiProp(h, ":1"); v != "Xft.dpi:\t96\n" {
		t.Errorf("Xft.dpi = %q", v)
	}
}

// TestScaleAdoptsTheUsersChange: a value the user sets with Steam's own
// slider becomes the screen's size, once it holds still, and the screen's
// next session gets it back.
func TestScaleAdoptsTheUsersChange(t *testing.T) {
	m, h, clk, hub := newScaleTest(t)
	ctx := context.Background()
	id := ScreenID("iPhone")
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	evs, cancel := hub.Subscribe()
	defer cancel()
	// On the way to Steam's slider the main menu is laid out; it follows
	// the slider, and within the first minute after the set, while vosd
	// looks at the views every round.
	h.ui.set(func(f *fakeSteamUI) {
		f.views = []steamui.View{{ID: "m", Title: "MainMenu_uid2", DPR: 2.7, Height: 800}}
	})
	h.ui.resetCalls()

	// A drag: no two reads agree, so nothing is adopted yet.
	clk.advance(11 * time.Second)
	for _, v := range []float64{1.8, 1.9, 2.03} {
		h.ui.userSets(false, v)
		m.scaleRound(ctx)
		clk.advance(5 * time.Second)
	}
	if sc, _ := storedScreen(t, id); sc.Kind != "" || sc.Size != 1 {
		t.Fatalf("adopted mid-drag: %+v", sc)
	}
	m.scaleRound(ctx) // two reads 5 s apart agree
	size := AdoptSize(2.03, KindPhone, iPhoneM, Panel{2796, 1290})
	sc, _ := storedScreen(t, id)
	if sc.Kind != KindPhone || sc.Size != size || sc.SteamAuto || sc.UIScale != 2.03 {
		t.Errorf("stored = %+v, want a phone at size %v", sc, size)
	}
	if h.ui.count("set") != 0 {
		t.Errorf("set Steam while the user moved it: %v", h.ui.snapshot().calls)
	}
	if drain(evs, "display.changed") == nil {
		t.Error("no display.changed for the adopted size")
	}
	if v := m.Info().Screens[0]; v.KindFrom != FromYou || v.Kind != KindPhone || v.Size != size || v.UIScale != 2.03 {
		t.Errorf("screens[0] = %+v", v)
	}

	// The user's value stays, though vosd's own rounding of that size
	// would give 2.05.
	if s := ScaleFor(KindPhone, iPhoneM, size, Panel{2796, 1290}); s != 2.05 {
		t.Fatalf("premise: the size gives %.2f", s)
	}
	for range 4 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if ui := h.ui.snapshot(); h.ui.count("set") != 0 || ui.current != 2.03 {
		t.Errorf("after adopting: Steam %.2f, %v", ui.current, ui.calls)
	}

	// Its next session.
	m.End(ctx)
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.current != 2.05 {
		t.Errorf("next session: Steam %.2f", ui.current)
	}
	if v := m.Info().Screens[0]; v.KindFrom != FromYou || v.Size != size {
		t.Errorf("next session's screen = %+v", v)
	}
}

// TestScaleAdoptionFalsePositives: what Steam does by itself (its clamp, a
// restart, its stored value right after vosd's set, another display, a
// mode gamescope switched to) is never adopted; vosd sets its value again.
func TestScaleAdoptionFalsePositives(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	id := ScreenID("iPhone")
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	notAdopted := func(what string) {
		t.Helper()
		if sc, _ := storedScreen(t, id); sc.Kind != "" || sc.Size != 1 || sc.SteamAuto {
			t.Errorf("%s: adopted %+v", what, sc)
		}
	}
	setAgain := func(what string, want float64) {
		t.Helper()
		if ui := h.ui.snapshot(); h.ui.count("set") == 0 || ui.current != want {
			t.Errorf("%s: Steam %.2f, %v", what, ui.current, ui.calls)
		}
		notAdopted(what)
		h.ui.resetCalls()
	}

	// Steam's stored value catches up moments after vosd's set.
	h.ui.resetCalls()
	clk.advance(3 * time.Second)
	h.ui.userSets(false, 1.8)
	m.scaleRound(ctx)
	setAgain("right after the set", 2.7)

	// Steam restarted: VaporOS's marker is gone, Steam is back on its own.
	clk.advance(30 * time.Second)
	h.ui.restart(true, 0)
	h.mu.Lock()
	h.steamPID += 100
	h.mu.Unlock()
	m.scaleRound(ctx)
	setAgain("a Steam restart", 2.7)

	// Another display name.
	clk.advance(30 * time.Second)
	h.ui.set(func(f *fakeSteamUI) { f.name = `External: Other 32"|||Windowed` })
	h.ui.userSets(false, 1.8)
	m.scaleRound(ctx)
	setAgain("another display", 2.7)

	// gamescope switched to 1080p by itself: the phone's scale for that.
	clk.advance(30 * time.Second)
	h.mu.Lock()
	h.scan = md(1920, 1080, 60)
	h.mu.Unlock()
	h.ui.userSets(false, 1.3)
	m.scaleRound(ctx)
	setAgain("another mode", 2.25)
	for range 4 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	notAdopted("later")
}

// TestScaleInferenceOnlySession: a device that cannot be told apart is
// sized by inference, nothing is stored for it, and a change in Steam
// lasts for its session only.
func TestScaleInferenceOnlySession(t *testing.T) {
	m, h, clk, hub := newScaleTest(t)
	ctx := context.Background()
	h.mu.Lock()
	h.peers = []netip.Addr{phoneAddr, otherAddr} // another device polls Sunshine all along
	h.mu.Unlock()
	evs, cancel := hub.Subscribe()
	defer cancel()
	m.Begin(ctx, session.Request{Op: "begin", Client: "roth", Width: 2796, Height: 1290, FPS: 120})
	s := beginEvent(t, evs)
	if s.Screen == nil || s.Screen.ID != "" || s.Screen.Kind != KindPhone || s.Screen.KindFrom != FromResolution || s.Screen.UIScale != 2.7 {
		t.Errorf("screen = %+v", s.Screen)
	}
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.current != 2.7 {
		t.Errorf("Steam = %.2f", ui.current)
	}
	d := m.Info()
	if len(d.Screens) != 1 || d.Screens[0].ID != "" || d.Screens[0].Savable || d.Screens[0].UIScale != 2.7 {
		t.Errorf("screens = %+v", d.Screens)
	}
	clk.advance(11 * time.Second)
	h.ui.userSets(false, 2.0)
	h.ui.resetCalls()
	for range 3 {
		m.scaleRound(ctx)
		clk.advance(5 * time.Second)
	}
	if h.ui.count("set") != 0 || h.ui.snapshot().current != 2.0 {
		t.Errorf("fought the user: %v", h.ui.snapshot().calls)
	}
	b, _ := os.ReadFile(config.ScreensPath())
	var stored Screens
	json.Unmarshal(b, &stored)
	if len(stored.ByID) != 0 {
		t.Errorf("stored a screen for a device that cannot be told apart: %s", b)
	}
	if v := m.Info().Screens[0]; v.UIScale != 2.0 || v.Size != 1 {
		t.Errorf("live screen = %+v", v)
	}
}

// TestScaleSteamAuto: Steam's own automatic scale, chosen in Steam or in
// the control center, is turned on at every begin of that screen; auto
// being on is then no change of the user's, but turning it off and moving
// the slider is.
func TestScaleSteamAuto(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	id := ScreenID("iPhone")
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	h.ui.set(func(f *fakeSteamUI) {
		f.views = []steamui.View{{ID: "m", Title: "MainMenu_uid2", DPR: 2.7, Height: 800}}
	})

	// The user turns Steam's automatic scale on in Steam, with the main
	// menu laid out, which follows.
	clk.advance(11 * time.Second)
	h.ui.resetCalls()
	h.ui.userSets(true, 0)
	m.scaleRound(ctx)
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if sc, _ := storedScreen(t, id); !sc.SteamAuto || sc.Size != 1 || sc.UIScale != 0 {
		t.Errorf("auto not adopted: %+v", sc)
	}
	if n := h.ui.count("set"); n != 0 {
		t.Errorf("set Steam while the user turned its automatic scale on: %v", h.ui.snapshot().calls)
	}
	h.ui.resetCalls()
	for range 4 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if n := h.ui.count("set") + h.ui.count("auto"); n != 0 {
		t.Errorf("acted on Steam's auto: %v", h.ui.snapshot().calls)
	}
	// 1.55/1.2 floored to a quarter: 1.25.
	if v := dpiProp(h, ":1"); v != "Xft.dpi:\t120\n" {
		t.Errorf("Xft.dpi on Steam's auto = %q", v)
	}

	// Off again, and a value: adopted as a size.
	h.ui.userSets(false, 1.8)
	m.scaleRound(ctx)
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if sc, _ := storedScreen(t, id); sc.SteamAuto || sc.Kind != KindPhone || sc.Size != AdoptSize(1.8, KindPhone, iPhoneM, Panel{2796, 1290}) {
		t.Errorf("value not adopted: %+v", sc)
	}

	// Steam's own size, picked in the control center: on at once.
	w, out := callID(t, m.handleScreenPut, http.MethodPut, id, `{"steam_auto":true}`)
	if w.Code != 200 || out["steam_auto"] != true || out["kind_from"] != "you" {
		t.Fatalf("PUT = %d %v", w.Code, out)
	}
	select {
	case <-m.scaleKick:
	default:
		t.Error("PUT did not wake the scaler")
	}
	h.ui.resetCalls()
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); !ui.auto || h.ui.count("auto ") != 1 || ui.gen != wantGen(m) {
		t.Errorf("Steam = auto %v, %v", ui.auto, ui.calls)
	}

	// Every begin of that screen turns it on, whatever the last device left.
	m.End(ctx)
	h.ui.userSets(false, 1.1)
	m.Begin(ctx, iPhone)
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.UIScale != 0 || s.Screen.GameDPI != 120 {
		t.Errorf("session.begin screen = %+v", s.Screen)
	}
	h.ui.resetCalls()
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); !ui.auto || h.ui.count("auto ") != 1 {
		t.Errorf("begin of a steam_auto screen: auto %v, %v", ui.auto, ui.calls)
	}
}

// TestScaleHandback: turning scaling off gives Steam its automatic scale
// back and takes Xft.dpi away; while Steam is down that waits, in
// screens.json, across a restart of vosd.
func TestScaleHandback(t *testing.T) {
	m, h, _, hub := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	h.ui.set(func(f *fakeSteamUI) { f.down = steamui.ErrNoDebugger })

	if w, _ := call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":false}`); w.Code != 200 {
		t.Fatalf("PUT settings = %d", w.Code)
	}
	if saved, _ := config.Load(); saved.Display.UIScaling {
		t.Error("ui_scaling not saved")
	}
	m.scaleRound(ctx)
	if !m.handbackPending() || m.Info().SteamUI != steamUIOff {
		t.Fatalf("pending %v, steam_ui %q", m.handbackPending(), m.Info().SteamUI)
	}
	// vosd starts again: the flag is in screens.json.
	m.screensMu.Lock()
	m.screens = nil
	m.screensMu.Unlock()
	if !m.handbackPending() {
		t.Fatal("handback_pending lost")
	}

	h.ui.set(func(f *fakeSteamUI) { f.down = nil })
	h.ui.resetCalls()
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); !ui.auto || h.ui.count("auto ") != 1 {
		t.Errorf("Steam = auto %v %v", ui.auto, ui.calls)
	}
	if _, ok := h.xstrProp(":1", resourceManager); ok {
		t.Error("Xft.dpi still on the games' display")
	}
	if s, _ := LoadScreens(config.ScreensPath()); s.HandbackPending {
		t.Error("handback_pending not cleared")
	}

	// Sessions while off: no screen in session.begin, Steam untouched.
	m.End(ctx)
	evs, cancel := hub.Subscribe()
	defer cancel()
	h.ui.resetCalls()
	m.Begin(ctx, iPhone)
	if s := beginEvent(t, evs); s.Screen != nil {
		t.Errorf("screen while scaling is off: %+v", s.Screen)
	}
	if s := m.CurrentSession(); s.Screen != nil {
		t.Errorf("stream's screen while scaling is off: %+v", s.Screen)
	}
	m.scaleRound(ctx)
	if calls := h.ui.snapshot().calls; len(calls) != 0 {
		t.Errorf("talked to Steam while scaling is off: %v", calls)
	}
	// On again: the running session is sized again.
	if w, _ := call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":true}`); w.Code != 200 {
		t.Fatalf("PUT settings = %d", w.Code)
	}
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.auto || ui.current != 2.7 || m.handbackPending() {
		t.Errorf("on again: Steam auto %v %.2f, pending %v", ui.auto, ui.current, m.handbackPending())
	}
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.UIScale != 2.7 {
		t.Errorf("stream's screen once on again: %+v", s.Screen)
	}
	call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":false}`)
	if s := m.CurrentSession(); s.Screen != nil {
		t.Errorf("stream's screen once off again: %+v", s.Screen)
	}
}

// TestScaleResumeHoldsNothing: after a resume (maybe another device's)
// the scaler neither holds Steam's scale nor adopts a change, until the
// next begin.
func TestScaleResumeHoldsNothing(t *testing.T) {
	m, h, clk, hub := newScaleTest(t)
	ctx := context.Background()
	id := ScreenID("iPhone")
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	evs, cancel := hub.Subscribe()
	defer cancel()
	m.NoteResume()
	if drain(evs, "display.changed") == nil {
		t.Error("no display.changed for the resume")
	}
	h.ui.resetCalls()
	h.ui.userSets(false, 1.5)
	h.setXstrProp(":1", resourceManager, "Xft.dpi:\t96\n")
	for range 4 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if n := h.ui.count("set") + h.ui.count("auto"); n != 0 {
		t.Errorf("held Steam's scale after a resume: %v", h.ui.snapshot().calls)
	}
	if sc, _ := storedScreen(t, id); sc.Size != 1 || sc.Kind != "" {
		t.Errorf("adopted after a resume: %+v", sc)
	}
	if v := dpiProp(h, ":1"); v != "Xft.dpi:\t96\n" {
		t.Errorf("Xft.dpi after a resume = %q", v)
	}
	// GET /display says so; a size the user gives the screen meanwhile is
	// stored for its next begin, and Steam is left alone.
	if d := m.Info(); d.SteamUI != steamUIResumed {
		t.Errorf("steam_ui after a resume = %q", d.SteamUI)
	}
	if w, out := callID(t, m.handleScreenPut, http.MethodPut, id, `{"size":0.7}`); w.Code != 200 || out["size"] != 0.7 {
		t.Fatalf("PUT = %d %v", w.Code, out)
	}
	m.scaleRound(ctx)
	if n := h.ui.count("set") + h.ui.count("auto"); n != 0 || h.ui.snapshot().current != 1.5 {
		t.Errorf("applied a size while resumed: %v", h.ui.snapshot().calls)
	}
	if sc, _ := storedScreen(t, id); sc.Size != 0.7 {
		t.Errorf("size not stored while resumed: %+v", sc)
	}
	if d := m.Info(); d.SteamUI != steamUIResumed {
		t.Errorf("steam_ui after a PUT while resumed = %q", d.SteamUI)
	}
	call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":false}`)
	if d := m.Info(); d.SteamUI != steamUIOff {
		t.Errorf("steam_ui with scaling off = %q", d.SteamUI)
	}
	call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":true}`)
	m.scaleRound(ctx)
	h.ui.resetCalls()

	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	want := ScaleFor(KindPhone, iPhoneM, 0.7, Panel{2796, 1290})
	if ui := h.ui.snapshot(); ui.current != want || dpiProp(h, ":1") != fmt.Sprintf("Xft.dpi:\t%d\n", GameDPI(want, iPhoneM)) {
		t.Errorf("next begin: Steam %.2f (want %.2f), %q", ui.current, want, dpiProp(h, ":1"))
	}
	if d := m.Info(); d.SteamUI != steamUIOK {
		t.Errorf("steam_ui at the next begin = %q", d.SteamUI)
	}
}

// TestScaleResendsToLateViews: a view Steam laid out late, at the old
// scale, gets the same value once more; for a minute after the set every
// round, then every 30 s.
func TestScaleResendsToLateViews(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	gen := wantGen(m)
	h.ui.set(func(f *fakeSteamUI) {
		f.views = []steamui.View{
			{ID: "q", Title: "QuickAccess_uid2", DPR: 1.55, Height: 450},
			{ID: "m", Title: "MainMenu_uid2", DPR: 1.0, Height: 1}, // never shown yet
		}
	})
	h.ui.resetCalls()
	m.scaleRound(ctx)
	if calls := h.ui.snapshot().calls; h.ui.count("set") != 1 || !strings.Contains(strings.Join(calls, ","), "set "+strconv.FormatUint(gen, 10)+" 2.70") {
		t.Errorf("late view: %v", calls)
	}
	h.ui.resetCalls()
	m.scaleRound(ctx)
	if h.ui.count("set") != 0 || h.ui.count("views") != 1 {
		t.Errorf("views right: %v", h.ui.snapshot().calls)
	}
	views := func(d time.Duration) int {
		h.ui.resetCalls()
		clk.advance(d)
		m.scaleRound(ctx)
		return h.ui.count("views")
	}
	if views(61*time.Second) != 1 || views(10*time.Second) != 0 || views(21*time.Second) != 1 {
		t.Error("late views not looked at every 30 s after the first minute")
	}
}

// TestScaleViewsNeverUndoTheUser: the views are only set again while
// Steam shows vosd's value, as a last read right before the set confirms;
// that set is no reason to take a change in Steam for Steam's own doing;
// and a view that does not follow gets it once.
func TestScaleViewsNeverUndoTheUser(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	id := ScreenID("iPhone")
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)

	// The user moves Steam's slider between the round's read and its look
	// at the views, which follow the slider.
	clk.advance(11 * time.Second)
	moved := false
	h.ui.set(func(f *fakeSteamUI) {
		f.views = []steamui.View{{ID: "m", Title: "MainMenu_uid2", DPR: 2.7, Height: 800}}
		f.onViews = func() {
			if !moved {
				moved = true
				h.ui.userSets(false, 2.0)
			}
		}
	})
	h.ui.resetCalls()
	m.scaleRound(ctx)
	if !moved || h.ui.count("set") != 0 || h.ui.snapshot().current != 2.0 {
		t.Fatalf("set Steam over the user's change: %v", h.ui.snapshot().calls)
	}
	for range 2 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if sc, _ := storedScreen(t, id); sc.Kind != KindPhone || sc.Size != AdoptSize(2.0, KindPhone, iPhoneM, Panel{2796, 1290}) || h.ui.count("set") != 0 {
		t.Errorf("not adopted: %+v, %v", sc, h.ui.snapshot().calls)
	}

	// A view laid out late is set again; a change of the user's moments
	// later is still the user's.
	m.End(ctx)
	callID(t, m.handleScreenDelete, http.MethodDelete, id, "")
	h.ui.set(func(f *fakeSteamUI) { f.views, f.onViews = nil, nil })
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	clk.advance(20 * time.Second)
	h.ui.set(func(f *fakeSteamUI) {
		f.views = []steamui.View{{ID: "q", Title: "QuickAccess_uid2", DPR: 1.55, Height: 450}}
	})
	h.ui.resetCalls()
	m.scaleRound(ctx)
	if h.ui.count("set") != 1 || h.ui.snapshot().current != 2.7 {
		t.Fatalf("late view not set again: %v", h.ui.snapshot().calls)
	}
	h.ui.resetCalls()
	clk.advance(5 * time.Second)
	h.ui.userSets(false, 2.2)
	m.scaleRound(ctx)
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if sc, _ := storedScreen(t, id); sc.Size != AdoptSize(2.2, KindPhone, iPhoneM, Panel{2796, 1290}) || h.ui.count("set") != 0 {
		t.Errorf("a change after a view's set: %+v, %v", sc, h.ui.snapshot().calls)
	}

	// A view that never follows (a page that took the title) gets the
	// value once in a generation.
	m.End(ctx)
	h.ui.set(func(f *fakeSteamUI) {
		f.views = []steamui.View{{ID: "x", Title: "MainMenu_x", DPR: 1.0, Height: 600}}
		f.stuck = map[string]bool{"x": true}
	})
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	h.ui.resetCalls()
	for range 12 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if n := h.ui.count("set"); n != 1 {
		t.Errorf("sets for a view that never follows: %d, %v", n, h.ui.snapshot().calls)
	}
}

// TestScaleReadBackBlipIsNoLostDebugger: only a round's first read counts
// towards Steam's debugger being gone; a blip while vosd reads a set back
// is no reason to restart Steam.
func TestScaleReadBackBlipIsNoLostDebugger(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	m.scaleReadback, m.scaleResend = 5*time.Second, 5*time.Second
	h.ui.set(func(f *fakeSteamUI) { f.notReady = true })
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx) // Steam's process is up, not ready yet
	clk.advance(4 * time.Minute)
	h.ui.set(func(f *fakeSteamUI) {
		f.notReady = false
		base := f.reads
		f.onRead = func(n int) {
			// The debugger drops right after the set, for 7 reads.
			switch n {
			case base + 2:
				h.ui.set(func(f *fakeSteamUI) { f.down = steamui.ErrNoDebugger })
			case base + 9:
				h.ui.set(func(f *fakeSteamUI) { f.down = nil })
			}
		}
	})
	m.scaleRound(ctx)
	h.ui.set(func(f *fakeSteamUI) { f.onRead = nil })
	m.mu.Lock()
	req := m.steamReq
	m.mu.Unlock()
	if req != nil {
		t.Errorf("asked for a Steam restart after a blip: %+v", req)
	}
	m.scaleMu.Lock()
	settled := m.sc.held.settled
	m.scaleMu.Unlock()
	if ui := h.ui.snapshot(); !settled || ui.current != 2.7 {
		t.Errorf("Steam %.2f, settled %v: %v", ui.current, settled, ui.calls)
	}
}

// TestScaleReadBackIsBoundedInRealTime: reading a set back lasts
// scaleReadback of real time, however slow Steam's answers are.
func TestScaleReadBackIsBoundedInRealTime(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	m.scaleStep, m.scaleReadback, m.scaleResend = time.Millisecond, 60*time.Millisecond, 10*time.Millisecond
	m.Begin(ctx, iPhone)
	h.ui.set(func(f *fakeSteamUI) { f.ignore, f.readDelay = 1000, 30*time.Millisecond })
	start := time.Now()
	m.scaleRound(ctx)
	// 60 reads of 30 ms, were the steps all that counted.
	if d := time.Since(start); d > time.Second {
		t.Errorf("the read-back took %s", d)
	}
	if n := h.ui.count("set"); n != 2 {
		t.Errorf("sets = %d: %v", n, h.ui.snapshot().calls)
	}
}

// TestScaleIgnoresAForeignMarker: a marker in Steam no generation of
// vosd's can reach (not VaporOS's) blocks the sets while it is there, and
// nothing after: once Steam lost it, vosd sets its value and hands Steam
// its automatic scale back.
func TestScaleIgnoresAForeignMarker(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	h.ui.set(func(f *fakeSteamUI) { f.gen = jsMaxGen })
	for range 3 {
		m.scaleRound(ctx)
	}
	if g := wantGen(m); g >= genCeiling {
		t.Fatalf("generation %d followed a marker that is not VaporOS's", g)
	}
	if d := m.Info(); d.SteamUI != steamUINoDebugger {
		t.Errorf("steam_ui with a foreign marker = %q", d.SteamUI)
	}
	h.ui.restart(true, 0)
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.current != 2.7 || ui.gen != wantGen(m) {
		t.Errorf("once the marker is gone: Steam %.2f gen %d, want gen %d: %v", ui.current, ui.gen, wantGen(m), ui.calls)
	}
	if d := m.Info(); d.SteamUI != steamUIOK {
		t.Errorf("steam_ui once the marker is gone = %q", d.SteamUI)
	}

	// The handback waits for it the same way.
	h.ui.set(func(f *fakeSteamUI) { f.gen = jsMaxGen })
	call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":false}`)
	m.scaleRound(ctx)
	if !m.handbackPending() || h.ui.snapshot().auto {
		t.Fatalf("handed back over a foreign marker: pending %v", m.handbackPending())
	}
	h.ui.restart(false, 2.7)
	m.scaleRound(ctx)
	if m.handbackPending() || !h.ui.snapshot().auto {
		t.Errorf("handback once the marker is gone: pending %v, %v", m.handbackPending(), h.ui.snapshot().calls)
	}
}

// TestBeginKeepsAScreenChangedMeanwhile: a PUT or DELETE of the screen
// while Begin still switches the display applies to the session it
// begins.
func TestBeginKeepsAScreenChangedMeanwhile(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	id := ScreenID("iPhone")
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	m.End(ctx)

	// begin runs Begin with gamescope holding it up while edit runs.
	begin := func(edit func()) {
		t.Helper()
		gate := make(chan struct{})
		h.mu.Lock()
		h.ctlGate = gate
		h.mu.Unlock()
		done := make(chan struct{})
		go func() { m.Begin(ctx, iPhone); close(done) }()
		waitFor(t, func() bool { s, _ := m.streamingScreen(id); return s != nil })
		select {
		case <-done:
			t.Fatal("Begin did not wait for gamescope")
		default:
		}
		edit()
		h.mu.Lock()
		h.ctlGate = nil
		h.mu.Unlock()
		close(gate)
		<-done
	}
	live := func() (float64, ScreenView) {
		t.Helper()
		d := m.Info()
		if len(d.Screens) == 0 {
			t.Fatal("no screens")
		}
		s := m.CurrentSession()
		if s == nil || s.Screen == nil {
			t.Fatal("no screen on the session")
		}
		return s.Screen.UIScale, d.Screens[0]
	}

	begin(func() {
		if w, _ := callID(t, m.handleScreenPut, http.MethodPut, id, `{"size":1.2}`); w.Code != 200 {
			t.Fatalf("PUT = %d", w.Code)
		}
	})
	want := ScaleFor(KindPhone, iPhoneM, 1.2, Panel{2796, 1290})
	if s, v := live(); s != want || v.Size != 1.2 {
		t.Errorf("after a PUT during Begin: session %.2f (want %.2f), screen %+v", s, want, v)
	}
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.current != want {
		t.Errorf("Steam %.2f, want %.2f", ui.current, want)
	}
	m.End(ctx)

	begin(func() {
		if w, _ := callID(t, m.handleScreenDelete, http.MethodDelete, id, ""); w.Code != 200 {
			t.Fatalf("DELETE = %d", w.Code)
		}
	})
	if s, v := live(); s != 2.7 || v.Size != 1 {
		t.Errorf("after a DELETE during Begin: session %.2f, screen %+v", s, v)
	}
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.current != 2.7 {
		t.Errorf("Steam %.2f after the DELETE", ui.current)
	}
}

// TestScaleGameDPIRules: Xft.dpi goes on the games' display only, never
// on Steam's, and never over someone else's resources.
func TestScaleGameDPIRules(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	ours := "Xft.dpi:\t168\n"
	for _, c := range []struct {
		name, steam, game, before, after string
	}{
		{"unset", ":0", ":1", "", ours},
		{"VaporOS's, another value", ":0", ":1", "Xft.dpi:\t96\n", ours},
		{"VaporOS's", ":0", ":1", ours, ours},
		{"someone else's", ":0", ":1", "Xft.dpi:\t96\nXcursor.size:\t24\n", "Xft.dpi:\t96\nXcursor.size:\t24\n"},
		{"odd bytes", ":0", ":1", "*custom:\t\"x\\y\"\x01\n", "*custom:\t\"x\\y\"\x01\n"},
		{"Steam's own display", ":1", ":1", "", ""},
		{"Steam's own display, a screen", "unix:1.0", ":1", "", ""},
		{"not a display", ":0", "1", "", ""},
		{"too long", ":0", ":1234", "", ""},
		{"no DISPLAY for Steam", "", ":1", "", ""},
	} {
		h.mu.Lock()
		h.steamPID++
		h.steamEnv = map[string]string{"STEAM_GAME_DISPLAY_0": c.game}
		if c.steam != "" {
			h.steamEnv["DISPLAY"] = c.steam
		}
		h.xstr = nil
		h.mu.Unlock()
		if c.before != "" {
			h.setXstrProp(c.game, resourceManager, c.before)
		}
		h.resetCalls()
		m.scaleRound(ctx)
		got, _ := h.xstrProp(c.game, resourceManager)
		if got != c.after {
			t.Errorf("%s: RESOURCE_MANAGER %q, want %q", c.name, got, c.after)
		}
		if c.after == "" {
			for _, call := range h.callLog() {
				if strings.HasPrefix(call, "xprop@") {
					t.Errorf("%s: %q", c.name, call)
				}
			}
		}
	}
}

// TestScaleAsksForSteamRestart: when Steam has been up for 3 minutes and
// its debugger failed 6 reads in a row, vosd asks for a Steam restart,
// once per Steam process.
func TestScaleAsksForSteamRestart(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	stranger := &steamui.ListenerError{Port: steamui.DevtoolsPort, Owner: steamui.Owner{PID: 777, UID: 1000, Comm: "python3"}}
	gone := func(err error) { h.ui.set(func(f *fakeSteamUI) { f.down = err }) }
	asked := func() *steamRequest {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.steamReq
	}
	rounds := func(n int) {
		for range n {
			m.scaleRound(ctx)
		}
	}
	gone(stranger)
	rounds(5)
	gone(nil)
	rounds(1) // an answer: the count starts over
	gone(stranger)
	clk.advance(3*time.Minute + time.Second)
	rounds(5)
	if asked() != nil {
		t.Fatal("asked after 5 failed reads")
	}
	if d := m.Info(); d.SteamUI != steamUINoDebugger {
		t.Errorf("steam_ui = %q", d.SteamUI)
	}
	rounds(1)
	if r := asked(); r == nil || r.reason != "Steam's debugger is gone" || r.user {
		t.Fatalf("request = %+v", r)
	}
	m.mu.Lock()
	m.steamReq = nil
	m.mu.Unlock()
	rounds(10)
	if asked() != nil {
		t.Error("asked twice for the same Steam")
	}
	// Steam's own "not ready" is an answer.
	h.mu.Lock()
	h.steamPID += 100
	h.mu.Unlock()
	gone(nil)
	h.ui.set(func(f *fakeSteamUI) { f.notReady = true })
	rounds(1)
	clk.advance(4 * time.Minute)
	rounds(8)
	if asked() != nil {
		t.Error("asked while Steam was starting")
	}
	if d := m.Info(); d.SteamUI != steamUIStarting {
		t.Errorf("steam_ui = %q", d.SteamUI)
	}
	h.ui.set(func(f *fakeSteamUI) { f.notReady = false })
	gone(errors.New("connection refused"))
	clk.advance(3*time.Minute + time.Second)
	rounds(6)
	if asked() == nil {
		t.Error("a new Steam process was not asked for")
	}
}

// TestScaleReresolvesAnAmbiguousClient: when Begin could not tell the
// client among several devices, Sunshine's RTSP connection tells it
// within the first minute, and the scaler applies once more.
func TestScaleReresolvesAnAmbiguousClient(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	h.mu.Lock()
	h.peers = []netip.Addr{phoneAddr, otherAddr}
	h.mu.Unlock()
	roth := session.Request{Op: "begin", Client: "roth", Width: 2796, Height: 1290, FPS: 120}
	m.Begin(ctx, roth)
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.ID != "" {
		t.Fatalf("screen = %+v", s.Screen)
	}
	gen := wantGen(m)
	h.mu.Lock()
	h.rtsp = []netip.AddrPort{netip.AddrPortFrom(otherAddr, 50001)}
	h.mu.Unlock()
	m.scaleRound(ctx)
	id := ScreenID("roth@" + otherMAC)
	if wantGen(m) == gen {
		t.Error("no new generation after telling the client")
	}
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.ID != id {
		t.Errorf("screen = %+v, want %s", s.Screen, id)
	}
	m.scaleRound(ctx)
	if sc, ok := storedScreen(t, id); !ok || sc.UIScale != 2.7 || sc.MAC != otherMAC {
		t.Errorf("stored = %+v %v", sc, ok)
	}
	if d := m.Info(); len(d.Screens) != 1 || d.Screens[0].ID != id || !d.Screens[0].Savable {
		t.Errorf("screens = %+v", d.Screens)
	}

	// Only within the first minute.
	m.End(ctx)
	h.mu.Lock()
	h.rtsp = nil
	h.mu.Unlock()
	m.Begin(ctx, roth)
	clk.advance(61 * time.Second)
	h.mu.Lock()
	h.rtsp = []netip.AddrPort{netip.AddrPortFrom(otherAddr, 50003)}
	h.mu.Unlock()
	m.scaleRound(ctx)
	if s := m.CurrentSession(); s.Screen.ID != "" {
		t.Errorf("re-resolved after the first minute: %+v", s.Screen)
	}
}

// TestScaleReresolvesOnlyByNewConnections: RTSP connections that were
// there before the stream (another device's, in TIME_WAIT after its own
// stream a moment ago, or held open), a device that was not in /launch and
// a resumed stream tell nothing; the client's own new connections do.
func TestScaleReresolvesOnlyByNewConnections(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	thirdAddr := netip.MustParseAddr("192.168.1.42")
	leftover := netip.AddrPortFrom(otherAddr, 50001)
	rtsp := func(c ...netip.AddrPort) {
		h.mu.Lock()
		h.rtsp = c
		h.mu.Unlock()
	}
	screenID := func() string {
		if s := m.CurrentSession(); s != nil && s.Screen != nil {
			return s.Screen.ID
		}
		return "?"
	}
	h.mu.Lock()
	h.peers = []netip.Addr{phoneAddr, otherAddr} // the other device stays in Moonlight's app list
	h.mu.Unlock()
	roth := session.Request{Op: "begin", Client: "roth", Width: 2796, Height: 1290, FPS: 120}

	// The other device streamed and quit moments ago: its setup is in
	// TIME_WAIT when the phone begins, and is all there is when the scaler
	// first looks, before the phone's stream sets up.
	rtsp(leftover)
	m.Begin(ctx, roth)
	m.scaleRound(ctx)
	if id := screenID(); id != "" {
		t.Fatalf("taken for the device whose connections were there before: %q", id)
	}
	// A device that was not in /launch.
	rtsp(leftover, netip.AddrPortFrom(thirdAddr, 40000))
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if id := screenID(); id != "" {
		t.Fatalf("taken for a device that was not in /launch: %q", id)
	}
	// The phone's stream sets up.
	rtsp(leftover, netip.AddrPortFrom(thirdAddr, 40000), netip.AddrPortFrom(phoneAddr, 60001))
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if id, want := screenID(), ScreenID("roth@"+phoneMAC); id != want {
		t.Errorf("screen %q, want the phone's %q", id, want)
	}
	if _, ok := storedScreen(t, ScreenID("roth@"+otherMAC)); ok {
		t.Error("the other device's screen was written")
	}

	// A device that restarts its own stream: its old connections are
	// still there, its new ones count.
	m.End(ctx)
	rtsp(netip.AddrPortFrom(phoneAddr, 60001))
	m.Begin(ctx, roth)
	m.scaleRound(ctx)
	rtsp(netip.AddrPortFrom(phoneAddr, 60001), netip.AddrPortFrom(phoneAddr, 60002))
	m.scaleRound(ctx)
	if id, want := screenID(), ScreenID("roth@"+phoneMAC); id != want {
		t.Errorf("restarted stream: screen %q, want %q", id, want)
	}

	// Resumed: the device may be another; nothing is told.
	m.End(ctx)
	rtsp()
	m.Begin(ctx, roth)
	m.NoteResume()
	rtsp(netip.AddrPortFrom(otherAddr, 50005))
	m.scaleRound(ctx)
	if id := screenID(); id != "" {
		t.Errorf("re-resolved a resumed stream: %q", id)
	}
}

// TestBeginTellsTheClientApart: the client is the one address in
// Moonlight's /launch at Begin's start and still at its end.
func TestBeginTellsTheClientApart(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	roth := session.Request{Op: "begin", Client: "roth", Width: 2796, Height: 1290, FPS: 120}
	for _, c := range []struct {
		name    string
		samples [][]netip.Addr
		macs    bool
		key     string
	}{
		{"one client", [][]netip.Addr{{phoneAddr}, {phoneAddr}, {phoneAddr}}, true, "roth@" + phoneMAC},
		{"a poll that came and went", [][]netip.Addr{{phoneAddr, otherAddr}, {phoneAddr}, {phoneAddr}}, true, "roth@" + phoneMAC},
		{"clear only at the end", [][]netip.Addr{{phoneAddr, otherAddr}, {phoneAddr, otherAddr}, {phoneAddr}}, true, "roth@" + phoneMAC},
		{"no MAC", [][]netip.Addr{{phoneAddr}, {phoneAddr}, {phoneAddr}}, false, "roth@" + phoneAddr.String()},
		{"two all along", [][]netip.Addr{{phoneAddr, otherAddr}, {phoneAddr, otherAddr}, {phoneAddr, otherAddr}}, true, ""},
		{"nobody", [][]netip.Addr{{}, {}, {}}, true, ""},
	} {
		h.mu.Lock()
		h.peerSamples = c.samples
		h.macs = nil
		if c.macs {
			h.macs = map[netip.Addr]string{phoneAddr: phoneMAC, otherAddr: otherMAC}
		}
		h.mu.Unlock()
		m.Begin(ctx, roth)
		if s := m.CurrentSession(); s.Screen == nil || s.Screen.ID != ScreenID(c.key) {
			t.Errorf("%s: screen %+v, want the key %q", c.name, s.Screen, c.key)
		}
		m.End(ctx)
	}
}

// TestSessionBeginCarriesTheScreen: every session.begin carries the
// screen, also when Begin cannot switch the display.
func TestSessionBeginCarriesTheScreen(t *testing.T) {
	m, h, _, hub := newScaleTest(t)
	ctx := context.Background()
	evs, cancel := hub.Subscribe()
	defer cancel()
	want := ScreenRef{ID: ScreenID("iPhone"), Name: "iPhone", Kind: KindPhone, KindFrom: FromName, UIScale: 2.7, GameDPI: 168}

	m.beginBudget = 50 * time.Millisecond
	m.op.LockCtx(ctx)
	resp := m.Begin(ctx, iPhone)
	m.op.Unlock()
	if s := beginEvent(t, evs); resp.OK || s.Screen == nil || *s.Screen != want {
		t.Errorf("display busy: %+v, screen %+v", resp, s.Screen)
	}
	m.End(ctx)

	h.mu.Lock()
	h.gpu = GPUInfo{Vendor: "virtual", Name: "Virtio GPU", Driver: "virtio_gpu"}
	h.mu.Unlock()
	m.init(ctx)
	m.Begin(ctx, iPhone)
	if s := beginEvent(t, evs); s.Screen == nil || *s.Screen != want {
		t.Errorf("no GPU: screen %+v", s.Screen)
	}
}

// TestPairHint: what pairing learned about a device is its hint, and the
// kind picked there is the user's.
func TestPairHint(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	h.mu.Lock()
	h.peers = []netip.Addr{otherAddr}
	h.mu.Unlock()
	ipad := "Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X)"
	mac := "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0)"
	roth := session.Request{Op: "begin", Client: "roth", Width: 1920, Height: 1080, FPS: 60}

	// Nothing for loopback or nonsense.
	m.PairHint(net.ParseIP("127.0.0.1"), "tv", ipad)
	m.PairHint(nil, "tv", ipad)
	if s := m.screensSnapshot(); len(s.Hints) != 0 {
		t.Errorf("hints = %+v", s.Hints)
	}
	// A desktop browser's User-Agent alone says too little.
	m.PairHint(net.IP(otherAddr.AsSlice()), "", mac)
	if s := m.screensSnapshot(); len(s.Hints) != 0 {
		t.Errorf("hint from a Mac's User-Agent: %+v", s.Hints)
	}
	// An iPad's does; it does not replace what the browser said itself.
	if k := m.BrowserKind(net.IP(otherAddr.AsSlice())); k != "" {
		t.Errorf("BrowserKind without a hint = %q", k)
	}
	m.PairHint(net.IP(otherAddr.AsSlice()), "", ipad)
	if hint := m.screensSnapshot().Hints[otherMAC]; hint.Kind != KindTablet || hint.You {
		t.Errorf("hint = %+v", hint)
	}
	if k := m.BrowserKind(net.IP(otherAddr.AsSlice())); k != "tablet" {
		t.Errorf("BrowserKind = %q", k)
	}
	if k := m.BrowserKind(net.ParseIP("127.0.0.1")); k != "" {
		t.Errorf("BrowserKind of loopback = %q", k)
	}
	m.Begin(ctx, roth)
	if s := m.CurrentSession(); s.Screen.Kind != KindTablet || s.Screen.KindFrom != FromBrowser {
		t.Errorf("screen = %+v", s.Screen)
	}
	m.End(ctx)

	// "What is it?" at pairing: the user's pick, which the device's next
	// session makes its screen's own.
	m.PairHint(net.IP(otherAddr.AsSlice()).To16(), "tv", ipad)
	m.Begin(ctx, roth)
	if s := m.CurrentSession(); s.Screen.Kind != KindTV || s.Screen.KindFrom != FromYou {
		t.Errorf("screen = %+v", s.Screen)
	}
	if sc, _ := storedScreen(t, ScreenID("roth@"+otherMAC)); sc.Kind != KindTV {
		t.Errorf("stored screen = %+v", sc)
	}
	if hint := m.screensSnapshot().Hints[otherMAC]; hint.You {
		t.Errorf("hint after the session = %+v", hint)
	}
}

// screensSnapshot is a copy of screens.json in memory.
func (m *Manager) screensSnapshot() Screens {
	m.screensMu.Lock()
	defer m.screensMu.Unlock()
	s := m.screensLocked()
	b, _ := json.Marshal(s)
	var c Screens
	json.Unmarshal(b, &c)
	return c
}

// TestRunScales drives the scaler from the real Run loop: Begin over the
// session socket, the scaler woken right after it.
func TestRunScales(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	m.now = time.Now
	short, err := os.MkdirTemp("", "vs")
	if err != nil {
		t.Fatal(err)
	}
	saveRun := config.RunDir
	config.RunDir = short
	t.Cleanup(func() { config.RunDir = saveRun; os.RemoveAll(short) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool {
		c, err := net.Dial("unix", config.SessionSock())
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	resp, err := session.Call(ctx, config.SessionSock(), iPhone)
	if err != nil || !resp.OK {
		t.Fatalf("begin = %+v, %v", resp, err)
	}
	waitFor(t, func() bool { return h.ui.snapshot().current == 2.7 && dpiProp(h, ":1") == "Xft.dpi:\t168\n" })
	cancel()
	<-done
	// The scaler lives as long as vosd's context, apart from Run.
	waitFor(t, func() bool {
		h.ui.mu.Lock()
		defer h.ui.mu.Unlock()
		return h.ui.closed > 0
	})
}

func TestParseXpropString(t *testing.T) {
	for _, c := range []struct {
		out, value string
		set, ok    bool
	}{
		{"RESOURCE_MANAGER:  not found.\n", "", false, true},
		{`RESOURCE_MANAGER(STRING) = "Xft.dpi:\t144\n"` + "\n", "Xft.dpi:\t144\n", true, true},
		{`RESOURCE_MANAGER(STRING) = "a\\b\"c\001\377"`, "a\\b\"c\x01\xff", true, true},
		{`RESOURCE_MANAGER(STRING) = "raw é"`, "raw é", true, true},
		{`RESOURCE_MANAGER(STRING) = "a", "b"`, "a", true, false},
		{`RESOURCE_MANAGER(UTF8_STRING) = "Xft.dpi:\t144\n"`, "", true, false},
		{`RESOURCE_MANAGER(STRING) = "unterminated`, "", true, false},
		{`RESOURCE_MANAGER(STRING) = "bad \q"`, "", true, false},
		{`RESOURCE_MANAGER(STRING) = "short \01"`, "", true, false},
		{`RESOURCE_MANAGER_X(STRING) = "x"`, "", true, false},
		{"", "", true, false},
	} {
		v, set, ok := parseXpropString(c.out, resourceManager)
		if set != c.set || ok != c.ok || (ok && v != c.value) {
			t.Errorf("%q = %q %v %v; want %q %v %v", c.out, v, set, ok, c.value, c.set, c.ok)
		}
	}
	// What the fake prints, as xprop does, reads back.
	for _, v := range []string{"Xft.dpi:\t96\n", "*x:\t\"q\"\\\n\x7f\x1b", ""} {
		if got, set, ok := parseXpropString(resourceManager+"(STRING) = "+xpropQuote(v), resourceManager); !set || !ok || got != v {
			t.Errorf("round trip %q = %q %v %v", v, got, set, ok)
		}
	}
}

// TestScalePassesANewerGeneration: a marker newer than vosd's (a clock
// that went back across a restart of vosd) does not block it for good.
func TestScalePassesANewerGeneration(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	ahead := wantGen(m) + 1000
	h.ui.set(func(f *fakeSteamUI) { f.gen = ahead })
	m.scaleRound(ctx)
	if g := wantGen(m); g <= ahead {
		t.Fatalf("generation %d, Steam's %d", g, ahead)
	}
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.current != 2.7 || ui.gen != wantGen(m) {
		t.Errorf("Steam = %.2f gen %d", ui.current, ui.gen)
	}
}

func TestSteamUIStatus(t *testing.T) {
	for err, want := range map[error]string{
		nil:                              steamUIOK,
		steamui.ErrNotReady:              steamUIStarting,
		steamui.ErrNoTarget:              steamUIStarting,
		&steamui.EvalError{Text: "x"}:    steamUIStarting,
		steamui.ErrUnsupported:           steamUIUnsupported,
		steamui.ErrNoDebugger:            steamUINoDebugger,
		&steamui.ListenerError{Port: 1}:  steamUINoDebugger,
		context.DeadlineExceeded:         steamUINoDebugger,
		errors.New("connection refused"): steamUINoDebugger,
	} {
		if got := steamUIStatus(err); got != want {
			t.Errorf("%v = %q, want %q", err, got, want)
		}
	}
}

// TestScaleKeepsTrying: a Steam that ignores sets is set again every
// round until it takes the value.
func TestScaleKeepsTrying(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	h.ui.set(func(f *fakeSteamUI) { f.ignore = 3 })
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); h.ui.count("set") != 2 || !ui.auto {
		t.Fatalf("first round: %v", ui.calls)
	}
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); h.ui.count("set") != 4 || ui.current != 2.7 {
		t.Errorf("second round: %.2f %v", ui.current, ui.calls)
	}
	if sc, _ := storedScreen(t, ScreenID("iPhone")); sc.UIScale != 2.7 {
		t.Errorf("ui_scale = %v", sc.UIScale)
	}
}

// TestScalePickAtPairingCanBeUndone: the kind picked at pairing becomes
// the screen's own pick at its first session, so Automatic, Reset and
// forgetting the screen undo it, as for a kind picked in the control
// center.
func TestScalePickAtPairingCanBeUndone(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	h.mu.Lock()
	h.peers = []netip.Addr{otherAddr}
	h.mu.Unlock()
	id := ScreenID("roth@" + otherMAC)
	ipad := session.Request{Op: "begin", Client: "roth", Width: 2732, Height: 2048, FPS: 60}
	put := func(body string) map[string]any {
		t.Helper()
		w, out := callID(t, m.handleScreenPut, http.MethodPut, id, body)
		if w.Code != 200 {
			t.Fatalf("PUT %s = %d %v", body, w.Code, out)
		}
		return out
	}
	automatic := func(what string, kind any, from any) {
		t.Helper()
		if kind != "tablet" || from != "resolution" {
			t.Errorf("%s: %v from %v, want the iPad's own tablet from its resolution", what, kind, from)
		}
	}

	// An iPad, taken for a phone by mistake when it paired.
	m.PairHint(net.IP(otherAddr.AsSlice()), "phone", "")
	m.Begin(ctx, ipad)
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.Kind != KindPhone || s.Screen.KindFrom != FromYou {
		t.Fatalf("first session: %+v", s.Screen)
	}
	if sc, _ := storedScreen(t, id); sc.Kind != KindPhone {
		t.Errorf("the pick is not the screen's: %+v", sc)
	}
	if hint := m.screensSnapshot().Hints[otherMAC]; hint.You || hint.Kind != KindUnknown {
		t.Errorf("the hint still holds the pick: %+v", hint)
	}
	out := put(`{"kind":"auto"}`)
	automatic("Automatic while it streams", out["kind"], out["kind_from"])
	put(`{"kind":"phone"}`)
	out = put(`{"kind":"auto","size":1}`)
	automatic("Reset while it streams", out["kind"], out["kind_from"])
	m.End(ctx)
	m.Begin(ctx, ipad)
	if s := m.CurrentSession(); s.Screen == nil {
		t.Fatal("no screen")
	} else {
		automatic("its next session", string(s.Screen.Kind), string(s.Screen.KindFrom))
	}
	m.End(ctx)

	// Picked again, then the screen forgotten: its next session is
	// inferred anew.
	m.PairHint(net.IP(otherAddr.AsSlice()), "phone", "")
	m.Begin(ctx, ipad)
	m.End(ctx)
	if w, _ := callID(t, m.handleScreenDelete, http.MethodDelete, id, ""); w.Code != 200 {
		t.Fatalf("DELETE = %d", w.Code)
	}
	m.Begin(ctx, ipad)
	if s := m.CurrentSession(); s.Screen == nil {
		t.Fatal("no screen")
	} else {
		automatic("after forgetting the screen", string(s.Screen.Kind), string(s.Screen.KindFrom))
	}
}

// TestScaleVetoIsNeverPinned: a size changed while a handheld streams
// docked (the veto's tv) leaves its kind automatic, from the control
// center or Steam's slider, so the Deck in hand is a handheld again.
func TestScaleVetoIsNeverPinned(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	id := ScreenID("Steam Deck")
	docked := session.Request{Op: "begin", Client: "Steam Deck", Width: 3840, Height: 2160, FPS: 60}
	inHand := session.Request{Op: "begin", Client: "Steam Deck", Width: 1280, Height: 800, FPS: 60}
	m.Begin(ctx, docked)
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.Kind != KindTV || s.Screen.KindFrom != FromStream {
		t.Fatalf("docked: %+v", s.Screen)
	}
	w, out := callID(t, m.handleScreenPut, http.MethodPut, id, `{"size":1.1}`)
	if w.Code != 200 || out["kind"] != "tv" || out["kind_from"] != "stream" || out["size"] != 1.1 {
		t.Errorf("PUT while docked = %d %v", w.Code, out)
	}
	if sc, _ := storedScreen(t, id); sc.Kind != "" || sc.Size != 1.1 {
		t.Errorf("stored = %+v, want automatic at 1.1", sc)
	}
	m.scaleRound(ctx)
	if want := ScaleFor(KindTV, md(3840, 2160, 60), 1.1, Panel{}); h.ui.snapshot().current != want {
		t.Errorf("Steam docked = %.2f, want %.2f", h.ui.snapshot().current, want)
	}
	m.End(ctx)
	m.Begin(ctx, inHand)
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.Kind != KindHandheld || s.Screen.KindFrom != FromName || s.Screen.UIScale != 1.65 {
		t.Errorf("in hand: %+v", s.Screen)
	}
	m.End(ctx)

	// Steam's slider while docked: the size that gives Steam's value back
	// there, and nothing pinned.
	m.Begin(ctx, docked)
	m.scaleRound(ctx)
	clk.advance(11 * time.Second)
	h.ui.userSets(false, 2.2)
	m.scaleRound(ctx)
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	size := AdoptSize(2.2, KindTV, md(3840, 2160, 60), Panel{})
	if sc, _ := storedScreen(t, id); sc.Kind != "" || sc.Size != size {
		t.Errorf("adopted while docked = %+v, want automatic at %v", sc, size)
	}
	if v := m.Info().Screens[0]; v.Kind != KindTV || v.KindFrom != FromStream || v.Size != size || v.UIScale != 2.2 {
		t.Errorf("screens[0] while docked = %+v", v)
	}
	m.End(ctx)
	m.Begin(ctx, inHand)
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.Kind != KindHandheld || s.Screen.UIScale != ScaleFor(KindHandheld, md(1280, 800, 60), size, Panel{}) {
		t.Errorf("in hand after the slider: %+v", s.Screen)
	}
}

// TestScaleAdoptsBeyondVosdsBand: a value the user sets in Steam beyond
// what vosd would infer for the screen (H/400 here) comes back at the
// screen's next session.
func TestScaleAdoptsBeyondVosdsBand(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	clk.advance(11 * time.Second)
	h.ui.userSets(false, 3.5) // 1290/400 = 3.23
	m.scaleRound(ctx)
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if sc, _ := storedScreen(t, ScreenID("iPhone")); sc.Kind != KindPhone || sc.UIScale != 3.5 {
		t.Fatalf("not adopted: %+v", sc)
	}
	m.End(ctx)
	h.ui.userSets(true, 0)
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	if ui := h.ui.snapshot(); ui.auto || ui.current != 3.5 {
		t.Errorf("next session: Steam auto %v %.2f, want 3.50", ui.auto, ui.current)
	}
}

// TestScaleSteamAutoGuess: while Steam cannot be asked, a steam_auto
// screen's game DPI goes by Steam's own automatic scale, which follows the
// mode's area, not Valve's 844 lines of it.
func TestScaleSteamAutoGuess(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	galaxy := session.Request{Op: "begin", Client: "Galaxy S24", Width: 3120, Height: 1440, FPS: 120}
	id := ScreenID("Galaxy S24")
	m.Begin(ctx, galaxy)
	if w, _ := callID(t, m.handleScreenPut, http.MethodPut, id, `{"steam_auto":true}`); w.Code != 200 {
		t.Fatalf("PUT = %d", w.Code)
	}
	m.End(ctx)
	h.ui.set(func(f *fakeSteamUI) { f.down = steamui.ErrNoDebugger })
	m.Begin(ctx, galaxy)
	// sqrt(3120·1440/1266000) = 1.88: 1.5 after /1.2, floored to a quarter.
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.UIScale != 0 || s.Screen.GameDPI != 144 {
		t.Errorf("session.begin screen = %+v, want game_dpi 144", s.Screen)
	}
	m.scaleRound(ctx)
	if v := dpiProp(h, ":1"); v != "Xft.dpi:\t144\n" {
		t.Errorf("Xft.dpi while Steam cannot be asked = %q", v)
	}
}

// TestScaleSizeRangeOfTheStreamingScreen: GET /display gives the screen
// streaming now the sizes within which its scale moves, and no other.
func TestScaleSizeRangeOfTheStreamingScreen(t *testing.T) {
	m, _, _, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, session.Request{Op: "begin", Client: "Pixel 8", Width: 2400, Height: 1080, FPS: 120})
	lo, hi := SizeRange(KindPhone, md(2400, 1080, 120), Panel{2400, 1080})
	if v := m.Info().Screens[0]; v.SizeMin != lo || v.SizeMax != hi || hi >= SizeMax || hi < 1.6 {
		t.Errorf("streaming: %v..%v, want %v..%v", v.SizeMin, v.SizeMax, lo, hi)
	}
	if w, _ := call(t, m.handleGet, http.MethodGet, ""); !strings.Contains(w.Body.String(), `"size_max":`) {
		t.Errorf("GET /display while streaming: %s", w.Body)
	}
	m.End(ctx)
	if v := m.Info().Screens[0]; v.SizeMin != 0 || v.SizeMax != 0 {
		t.Errorf("not streaming: %v..%v", v.SizeMin, v.SizeMax)
	}
	if w, _ := call(t, m.handleGet, http.MethodGet, ""); strings.Contains(w.Body.String(), `"size_m`) {
		t.Errorf("GET /display after the stream: %s", w.Body)
	}
}

// TestScaleSecondDeviceBehindAMAC: behind a range extender that gives
// every device its own MAC, a second generic-named device gets a screen of
// its own, and the first keeps its own and its size.
func TestScaleSecondDeviceBehindAMAC(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	roth := session.Request{Op: "begin", Client: "roth", Width: 2796, Height: 1290, FPS: 120}
	from := func(a netip.Addr) {
		h.mu.Lock()
		h.peers = []netip.Addr{a}
		h.macs = map[netip.Addr]string{phoneAddr: phoneMAC, otherAddr: phoneMAC}
		h.mu.Unlock()
	}
	screen := func() ScreenRef {
		t.Helper()
		s := m.CurrentSession()
		if s == nil || s.Screen == nil {
			t.Fatal("no screen")
		}
		return *s.Screen
	}
	first := ScreenID("roth@" + phoneMAC)
	from(phoneAddr)
	m.Begin(ctx, roth)
	if sc := screen(); sc.ID != first {
		t.Fatalf("first device: %q", sc.ID)
	}
	if w, _ := callID(t, m.handleScreenPut, http.MethodPut, first, `{"size":1.2}`); w.Code != 200 {
		t.Fatalf("PUT = %d", w.Code)
	}
	m.End(ctx)

	from(otherAddr)
	m.Begin(ctx, roth)
	second := ScreenID("roth@" + phoneMAC + "@" + otherAddr.String())
	if sc := screen(); sc.ID != second || sc.UIScale != ScaleFor(KindPhone, iPhoneM, 1, Panel{2796, 1290}) {
		t.Errorf("second device: %+v, want its own %q at size 1", sc, second)
	}
	m.End(ctx)

	from(phoneAddr)
	m.Begin(ctx, roth)
	if sc := screen(); sc.ID != first || sc.UIScale != ScaleFor(KindPhone, iPhoneM, 1.2, Panel{2796, 1290}) {
		t.Errorf("first device again: %+v", sc)
	}
	if st, _ := storedScreen(t, first); st.IP != phoneAddr.String() || st.Size != 1.2 {
		t.Errorf("first device's screen: %+v", st)
	}
}

// TestScaleSharedAddress: devices behind a router that NATs them share its
// address and MAC; once two names were seen there, browsers there give no
// hints, pairing keeps nothing for it, and a generic name there tells no
// device apart.
func TestScaleSharedAddress(t *testing.T) {
	m, _, _, _ := newScaleTest(t)
	ctx := context.Background()
	router := net.IP(phoneAddr.AsSlice())
	ipadUA := "Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X)"
	hint := func() int {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/display/hint", strings.NewReader(`{"w":430,"h":932,"dpr":3,"touch":5}`))
		r.RemoteAddr = phoneAddr.String() + ":50000"
		r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Mobile/15E148")
		w := httptest.NewRecorder()
		m.handleHint(w, r)
		return w.Code
	}
	m.Begin(ctx, session.Request{Op: "begin", Client: "Work laptop", Width: 1920, Height: 1200, FPS: 60})
	m.End(ctx)
	if m.SharedAddr(router) {
		t.Fatal("one device at the address is no shared address")
	}
	hint()
	if k := m.BrowserKind(router); k != "phone" {
		t.Fatalf("one device at the address: BrowserKind %q", k)
	}

	m.Begin(ctx, session.Request{Op: "begin", Client: "Living room TV", Width: 3840, Height: 2160, FPS: 60})
	m.End(ctx)
	if !m.SharedAddr(router) || m.SharedAddr(net.IP(otherAddr.AsSlice())) || m.SharedAddr(net.ParseIP("127.0.0.1")) {
		t.Fatal("SharedAddr")
	}
	if k := m.BrowserKind(router); k != "" {
		t.Errorf("BrowserKind at a shared address = %q", k)
	}
	before := m.screensSnapshot().Hints
	if code := hint(); code != 200 {
		t.Errorf("POST /display/hint = %d", code)
	}
	m.PairHint(router, "tv", "")
	m.PairHint(router, "", ipadUA)
	if after := m.screensSnapshot().Hints; len(after) != len(before) || after[phoneMAC].You || after[phoneMAC].At != before[phoneMAC].At {
		t.Errorf("kept for a shared address: %+v", after)
	}
	m.Begin(ctx, session.Request{Op: "begin", Client: "roth", Width: 1920, Height: 1080, FPS: 60})
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.ID != "" || s.Screen.KindFrom == FromBrowser || s.Screen.KindFrom == FromYou {
		t.Errorf("a generic name at a shared address: %+v", s.Screen)
	}
}
