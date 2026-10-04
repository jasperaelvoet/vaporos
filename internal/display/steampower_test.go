package display

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/display/steamui"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// asleep is Steam in its suspend screen, its preparation at progress.
func asleep(h *fakeHost, progress int) {
	h.ui.set(func(f *fakeSteamUI) { f.sleep.Suspending, f.sleep.Progress = true, progress })
}

func steamAsked(m *Manager) *steamRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.steamReq
}

func forgetSteamAsk(m *Manager) {
	m.mu.Lock()
	m.steamReq = nil
	m.mu.Unlock()
}

func newSteamPID(h *fakeHost) {
	h.mu.Lock()
	h.steamPID += 100
	h.mu.Unlock()
}

// syncBuf is a log's output the test reads.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuf) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.b.String(), s)
}

func captureLog(t *testing.T) *syncBuf {
	b := &syncBuf{}
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return b
}

// Without a session and with scaling off, a Steam stuck asleep is woken
// at the first check 5 s or more after the one that first saw it, never
// on first sight, and the checks come every 30 s.
func TestSteamSleepWakesIdle(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	logs := captureLog(t)
	if w, _ := call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":false}`); w.Code != 200 {
		t.Fatalf("PUT settings = %d", w.Code)
	}
	asleep(h, steamui.SuspendComplete)
	m.scaleRound(ctx)
	if h.ui.powerCount("sleep") != 1 || h.ui.powerCount("wake") != 0 {
		t.Fatalf("first sight: %q", h.ui.powerCalls())
	}
	for range 5 {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if h.ui.powerCount("sleep") != 1 || h.ui.powerCount("wake") != 0 {
		t.Fatalf("within 30 s: %q", h.ui.powerCalls())
	}
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if h.ui.powerCount("wake") != 1 || h.ui.sleepNow().Suspending {
		t.Fatalf("30 s later: %q, %+v", h.ui.powerCalls(), h.ui.sleepNow())
	}
	if logs.count("Steam's interface was stuck asleep (VaporOS can't suspend); woke it") != 1 {
		t.Error("the wake was not logged")
	}
	clk.advance(30 * time.Second)
	m.scaleRound(ctx)
	if h.ui.powerCount("sleep") != 3 || h.ui.powerCount("wake") != 1 || steamAsked(m) != nil {
		t.Errorf("once awake: %q, restart %+v", h.ui.powerCalls(), steamAsked(m))
	}
	if d := m.Info(); d.SteamUI != steamUIOff {
		t.Errorf("steam_ui = %q", d.SteamUI)
	}
}

// During a session it looks every round: woken 5 s after first sight,
// not sooner.
func TestSteamSleepWakesDuringASession(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	asleep(h, steamui.SuspendInvalid)
	m.scaleRound(ctx)
	clk.advance(4 * time.Second)
	m.scaleRound(ctx)
	if h.ui.powerCount("sleep") != 2 || h.ui.powerCount("wake") != 0 {
		t.Fatalf("before 5 s: %q", h.ui.powerCalls())
	}
	clk.advance(time.Second)
	m.scaleRound(ctx)
	if h.ui.powerCount("wake") != 1 || h.ui.sleepNow().Suspending {
		t.Fatalf("at 5 s: %q", h.ui.powerCalls())
	}
	// The scale went on as ever.
	if ui := h.ui.snapshot(); ui.current != 2.7 || ui.gen != wantGen(m) {
		t.Errorf("Steam's scale: %.2f gen %d", ui.current, ui.gen)
	}
	// Asleep again later: a new episode, woken again.
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	asleep(h, steamui.SuspendComplete)
	m.scaleRound(ctx)
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if h.ui.powerCount("wake") != 2 || steamAsked(m) != nil {
		t.Errorf("a second episode: %q, restart %+v", h.ui.powerCalls(), steamAsked(m))
	}
}

// While Steam still prepares the suspend (a cloud sync, a game), it is
// left alone for 2 minutes.
func TestSteamSleepWaitsForAPrepareInFlight(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	for i := range 24 { // 0 s to 115 s
		asleep(h, 2+i%4)
		m.scaleRound(ctx)
		clk.advance(5 * time.Second)
	}
	if h.ui.powerCount("wake") != 0 {
		t.Fatalf("woken within 2 minutes: %q", h.ui.powerCalls())
	}
	m.scaleRound(ctx)
	if h.ui.powerCount("wake") != 1 {
		t.Fatalf("not woken at 2 minutes: %q", h.ui.powerCalls())
	}
}

// A Steam that stays asleep is woken once, and two checks later gets a
// restart, once per Steam process.
func TestSteamSleepRestartsAStuckSteam(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone)
	h.ui.set(func(f *fakeSteamUI) { f.sleepSticky = true })
	asleep(h, steamui.SuspendComplete)
	round := func() {
		m.scaleRound(ctx)
		clk.advance(5 * time.Second)
	}
	round()
	round() // woken
	round()
	if h.ui.powerCount("wake") != 1 || steamAsked(m) != nil {
		t.Fatalf("one check after the wake: %q, restart %+v", h.ui.powerCalls(), steamAsked(m))
	}
	round()
	if r := steamAsked(m); r == nil || r.reason != "Steam is stuck asleep" || r.user {
		t.Fatalf("two checks after the wake: %+v", r)
	}
	forgetSteamAsk(m)
	for range 10 {
		round()
	}
	if h.ui.powerCount("wake") != 1 || steamAsked(m) != nil {
		t.Errorf("woken or asked again: %q, restart %+v", h.ui.powerCalls(), steamAsked(m))
	}
	// A new Steam process starts over: first sight, then the wake.
	newSteamPID(h)
	round()
	if h.ui.powerCount("wake") != 1 {
		t.Errorf("woken a new Steam on first sight: %q", h.ui.powerCalls())
	}
	round()
	if h.ui.powerCount("wake") != 2 {
		t.Errorf("a new Steam not woken: %q", h.ui.powerCalls())
	}
	round()
	round()
	if steamAsked(m) == nil {
		t.Error("no restart for the new Steam")
	}
}

// A wake that fails counts as one: a Steam still asleep two checks later
// gets a restart. One whose InitiateResume went away after the read gets
// it at once.
func TestSteamSleepFailedWake(t *testing.T) {
	ctx := context.Background()
	m, h, clk, _ := newScaleTest(t)
	m.Begin(ctx, iPhone)
	h.ui.set(func(f *fakeSteamUI) { f.resumeErr = errors.New("evaluate: TypeError") })
	asleep(h, steamui.SuspendComplete)
	round := func() {
		m.scaleRound(ctx)
		clk.advance(5 * time.Second)
	}
	round()
	round() // the wake, which fails
	round()
	if h.ui.powerCount("wake") != 1 || steamAsked(m) != nil {
		t.Fatalf("one check after the wake: %q, restart %+v", h.ui.powerCalls(), steamAsked(m))
	}
	round()
	if r := steamAsked(m); r == nil || r.reason != "Steam is stuck asleep" || h.ui.powerCount("wake") != 1 {
		t.Fatalf("two checks after the failed wake: %+v, %q", r, h.ui.powerCalls())
	}

	m, h, clk, _ = newScaleTest(t)
	m.Begin(ctx, iPhone)
	h.ui.set(func(f *fakeSteamUI) { f.resumeErr = steamui.ErrNoSleepAPI })
	asleep(h, steamui.SuspendComplete)
	m.scaleRound(ctx)
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if r := steamAsked(m); r == nil || r.reason != "Steam is stuck asleep" || h.ui.powerCount("wake") != 1 {
		t.Fatalf("no InitiateResume at the wake: %+v, %q", r, h.ui.powerCalls())
	}
}

// A store without InitiateResume cannot be woken: a restart, once.
func TestSteamSleepWithoutInitiateResume(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	h.ui.set(func(f *fakeSteamUI) { f.sleep.CanResume = false })
	asleep(h, steamui.SuspendComplete)
	m.scaleRound(ctx)
	if steamAsked(m) != nil {
		t.Fatal("asked on first sight")
	}
	clk.advance(30 * time.Second)
	m.scaleRound(ctx)
	if r := steamAsked(m); r == nil || r.reason != "Steam is stuck asleep" || h.ui.powerCount("wake") != 0 {
		t.Fatalf("request %+v, %q", r, h.ui.powerCalls())
	}
	forgetSteamAsk(m)
	for range 5 {
		clk.advance(30 * time.Second)
		m.scaleRound(ctx)
	}
	if steamAsked(m) != nil {
		t.Error("asked twice for the same Steam")
	}
}

// Steam's idle sleep at its default (an hour) is set to Never, at most
// once a minute while Steam keeps showing it, and logged once.
func TestSteamSleepSetsNever(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	logs := captureLog(t)
	m.Begin(ctx, iPhone)
	h.ui.set(func(f *fakeSteamUI) { f.sleep.AC, f.sleep.Battery, f.keepsSleep = 3600, 3600, true })
	m.scaleRound(ctx)
	if h.ui.powerCount("nosleep") != 1 {
		t.Fatalf("not set: %q", h.ui.powerCalls())
	}
	for range 11 { // to 55 s
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if h.ui.powerCount("nosleep") != 1 {
		t.Fatalf("set again within a minute: %q", h.ui.powerCalls())
	}
	clk.advance(5 * time.Second)
	m.scaleRound(ctx)
	if h.ui.powerCount("nosleep") != 2 {
		t.Fatalf("not set again after a minute: %q", h.ui.powerCalls())
	}
	if n := logs.count("Steam's idle sleep is on (AC 3600 s, battery 3600 s)"); n != 1 {
		t.Errorf("logged %d times", n)
	}
	// Steam takes it: nothing more.
	h.ui.set(func(f *fakeSteamUI) { f.keepsSleep = false })
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	if st := h.ui.sleepNow(); h.ui.powerCount("nosleep") != 3 || st.AC != 0 || st.Battery != 0 {
		t.Errorf("after Steam took it: %q, %+v", h.ui.powerCalls(), st)
	}
	// One that is on is enough; one Steam lacks the setter for is left.
	h.ui.set(func(f *fakeSteamUI) { f.sleep.Battery = 600 })
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	h.ui.set(func(f *fakeSteamUI) { f.sleep.AC, f.sleep.CanSet = 900, false })
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	if h.ui.powerCount("nosleep") != 4 {
		t.Errorf("battery on, then no setter: %q", h.ui.powerCalls())
	}
}

// Settings Steam does not show are set once per Steam process.
func TestSteamSleepSetsUnknownOncePerSteam(t *testing.T) {
	m, h, clk, _ := newScaleTest(t)
	ctx := context.Background()
	h.ui.set(func(f *fakeSteamUI) { f.sleep.AC, f.sleep.Battery, f.keepsSleep = -1, -1, true })
	for range 10 {
		m.scaleRound(ctx)
		clk.advance(time.Minute)
	}
	if h.ui.powerCount("sleep") != 10 || h.ui.powerCount("nosleep") != 1 {
		t.Fatalf("unknown: %q", h.ui.powerCalls())
	}
	newSteamPID(h)
	m.scaleRound(ctx)
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	if h.ui.powerCount("nosleep") != 2 {
		t.Fatalf("a new Steam: %q", h.ui.powerCalls())
	}
	// One shown at Never and one not shown counts as not shown.
	newSteamPID(h)
	h.ui.set(func(f *fakeSteamUI) { f.sleep.AC = 0 })
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	if h.ui.powerCount("nosleep") != 3 {
		t.Errorf("one not shown: %q", h.ui.powerCalls())
	}
}

// Nothing is asked of Steam's sleep in the welcome state, without a Steam
// process, while a Begin or the policy holds the display, or when one
// takes it while Steam is read.
func TestSteamSleepOnlyInTheGamingState(t *testing.T) {
	ctx := context.Background()

	m, h, clk, _ := newTestManager(t, true)
	m.init(ctx)
	m.reconcile(ctx, false)
	if d := m.Info(); d.State != StateWelcome {
		t.Fatalf("state %q", d.State)
	}
	h.mu.Lock()
	h.steamPID = 4242
	h.mu.Unlock()
	h.ui.set(func(f *fakeSteamUI) { f.down = nil })
	asleep(h, steamui.SuspendComplete)
	for range 3 {
		m.scaleRound(ctx)
		clk.advance(time.Minute)
	}
	if n := len(h.ui.powerCalls()); n != 0 {
		t.Errorf("welcome: %q", h.ui.powerCalls())
	}

	m, h, clk, _ = newScaleTest(t)
	asleep(h, steamui.SuspendComplete)
	h.mu.Lock()
	h.steamPID = 0
	h.mu.Unlock()
	for range 3 {
		m.scaleRound(ctx)
		clk.advance(time.Minute)
	}
	if n := len(h.ui.powerCalls()); n != 0 {
		t.Errorf("no Steam: %q", h.ui.powerCalls())
	}

	h.mu.Lock()
	h.steamPID = 4242
	h.mu.Unlock()
	if !m.op.TryLock() {
		t.Fatal("op held")
	}
	for range 3 {
		m.scaleRound(ctx)
		clk.advance(time.Minute)
	}
	m.op.Unlock()
	if n := len(h.ui.powerCalls()); n != 0 {
		t.Errorf("display held: %q", h.ui.powerCalls())
	}

	// A Begin takes the display while Steam is read: what the read saw is
	// not acted on, neither the wake nor the setting.
	m.scaleRound(ctx) // first sight
	clk.advance(time.Minute)
	h.ui.set(func(f *fakeSteamUI) {
		f.sleep.AC, f.sleep.Battery = 3600, 3600
		f.onSleep = func() {
			if !m.op.TryLock() {
				t.Error("op held")
			}
		}
	})
	m.scaleRound(ctx)
	h.ui.set(func(f *fakeSteamUI) { f.onSleep = nil })
	m.op.Unlock()
	if h.ui.powerCount("sleep") != 2 || h.ui.powerCount("wake") != 0 || h.ui.powerCount("nosleep") != 0 {
		t.Errorf("acted while a Begin held the display: %q", h.ui.powerCalls())
	}
	clk.advance(time.Minute)
	m.scaleRound(ctx)
	if h.ui.powerCount("wake") != 1 || h.ui.powerCount("nosleep") != 1 {
		t.Errorf("not acted on once the display was free: %q", h.ui.powerCalls())
	}

	// Steam restarts, or the display leaves the gaming state, while Steam
	// is read: what the old Steam showed is not acted on.
	for _, change := range []func(){
		func() { newSteamPID(h) },
		func() { m.mu.Lock(); m.state = StateWelcome; m.mu.Unlock() },
	} {
		m, h, clk, _ = newScaleTest(t)
		h.ui.set(func(f *fakeSteamUI) { f.sleep.AC, f.sleep.Battery = 3600, 3600 })
		asleep(h, steamui.SuspendComplete)
		m.scaleRound(ctx) // first sight, set to Never
		clk.advance(time.Minute)
		h.ui.set(func(f *fakeSteamUI) { f.sleep.AC, f.sleep.Battery, f.onSleep = 3600, 3600, change })
		m.scaleRound(ctx)
		if h.ui.powerCount("sleep") != 2 || h.ui.powerCount("nosleep") != 1 || h.ui.powerCount("wake") != 0 {
			t.Errorf("acted on a read of a Steam that changed: %q", h.ui.powerCalls())
		}
	}
}

// displayChanges drains evs and counts the display.changed among them.
func displayChanges(evs <-chan events.Event) int {
	n := 0
	for {
		select {
		case ev := <-evs:
			if ev.Topic == "display.changed" {
				n++
			}
		default:
			return n
		}
	}
}

// Failed calls are logged once per Steam process and episode, and never
// count toward Steam's debugger being gone or change steam_ui. Without a
// session the scale reads nothing that would mend either, and during one
// steam_ui stays as the scale's reads say.
func TestSteamSleepErrorsStayApart(t *testing.T) {
	m, h, clk, hub := newScaleTest(t)
	ctx := context.Background()
	logs := captureLog(t)
	m.scaleRound(ctx)
	if d := m.Info(); d.SteamUI != steamUIIdle {
		t.Fatalf("steam_ui = %q", d.SteamUI)
	}
	h.ui.set(func(f *fakeSteamUI) { f.sleepErr = errors.New("connection refused") })
	evs, cancel := hub.Subscribe()
	defer cancel()
	clk.advance(debuggerGrace + time.Second)
	for range 2 * debuggerFails {
		m.scaleRound(ctx)
		clk.advance(sleepIdleEvery)
	}
	if r := steamAsked(m); r != nil {
		t.Errorf("restart asked: %+v", r)
	}
	if d := m.Info(); d.SteamUI != steamUIIdle {
		t.Errorf("steam_ui = %q", d.SteamUI)
	}
	if n := displayChanges(evs); n != 0 {
		t.Errorf("display.changed published %d times", n)
	}
	if n := logs.count("reading Steam's sleep: connection refused"); n != 1 {
		t.Errorf("logged %d times", n)
	}

	// During a session the scale's own reads set steam_ui, every round.
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	displayChanges(evs) // Begin's and the scale's
	for range 2 * debuggerFails {
		clk.advance(5 * time.Second)
		m.scaleRound(ctx)
	}
	if d := m.Info(); d.SteamUI != steamUIOK {
		t.Errorf("steam_ui in a session = %q", d.SteamUI)
	}
	if n := displayChanges(evs); n != 0 {
		t.Errorf("display.changed published %d times in a session", n)
	}
	if n := logs.count("reading Steam's sleep: connection refused"); n != 1 {
		t.Errorf("logged %d times", n)
	}
	// Steam not there yet is the scale's to say.
	h.ui.set(func(f *fakeSteamUI) { f.sleepErr = steamui.ErrNoTarget })
	newSteamPID(h)
	m.scaleRound(ctx)
	if n := logs.count("reading Steam's sleep"); n != 1 {
		t.Errorf("logged a Steam that is starting: %d", n)
	}
}
