package display

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

const setPropCall = "xprop -root -f GAMESCOPE_COMPOSITE_FORCE 32c -set GAMESCOPE_COMPOSITE_FORCE 1"

// compositeOK reports whether the fake gamescope composites with the
// property at 1.
func compositeOK(h *fakeHost) bool {
	on, prop := h.compositeState()
	return on && prop == "1"
}

func TestForceCompositeSetsBothHalves(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()

	// No gamescope yet: both halves fail, and the error says so.
	err := m.forceCompositeWithin(ctx, 0)
	if err == nil || !strings.Contains(err.Error(), "gamescopectl") || !strings.Contains(err.Error(), "xprop") {
		t.Errorf("without gamescope: %v", err)
	}

	h.setActive(GamescopeUnit, true, true)
	h.resetCalls()
	if err := m.forceCompositeWithin(ctx, 0); err != nil {
		t.Fatal(err)
	}
	calls := h.callLog()
	if !slices.Equal(calls, []string{"gamescopectl composite_force 1", setPropCall}) {
		t.Errorf("calls = %v", calls)
	}
	if !compositeOK(h) {
		t.Error("composition not forced")
	}

	// X unreachable: the convar still gets set; the error names xprop only.
	h.mu.Lock()
	h.xErr = errors.New("unable to open display")
	h.composite = false
	h.mu.Unlock()
	err = m.forceCompositeWithin(ctx, 0)
	if err == nil || strings.Contains(err.Error(), "gamescopectl") || !strings.Contains(err.Error(), "xprop") {
		t.Errorf("x down: %v", err)
	}
	if on, _ := h.compositeState(); !on {
		t.Error("convar not set without X")
	}
}

// TestForceCompositeWaitsForGamescope: right after a start, gamescope takes
// a moment to answer; forceComposite retries within composeWait.
func TestForceCompositeWaitsForGamescope(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	m.now = time.Now
	m.composeWait = 5 * time.Second
	go func() {
		time.Sleep(700 * time.Millisecond)
		h.setActive(GamescopeUnit, true, true)
	}()
	start := time.Now()
	if err := m.forceComposite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second || !compositeOK(h) {
		t.Errorf("took %s, composite ok=%v", time.Since(start), compositeOK(h))
	}
}

// TestCompositeWatchdog replays the spike: Steam rewrites
// GAMESCOPE_COMPOSITE_FORCE to 0 and the game ends up on two planes.
func TestCompositeWatchdog(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false) // headless: gamescope for streaming
	waitFor(t, func() bool { return compositeOK(h) })

	if why := m.checkComposite(ctx); why != "" {
		t.Errorf("acted while all was well: %s", why)
	}

	h.steamWrites("0")
	if p, _ := h.Planes("", ""); p != 2 {
		t.Fatalf("fake planes = %d", p)
	}
	why := m.checkComposite(ctx)
	if !strings.Contains(why, "2 planes") || !strings.Contains(why, "GAMESCOPE_COMPOSITE_FORCE is 0") {
		t.Errorf("why = %q", why)
	}
	if !compositeOK(h) {
		t.Error("composition not restored")
	}
	if p, _ := h.Planes("", ""); p != 1 {
		t.Errorf("planes after the watchdog = %d", p)
	}

	// gamescope restarted by itself (systemd Restart=always): a fresh
	// Xwayland without the property, the convar off.
	h.RestartUnit(ctx, GamescopeUnit, true)
	if why := m.checkComposite(ctx); !strings.Contains(why, "unset") || !compositeOK(h) {
		t.Errorf("after a restart: %q ok=%v", why, compositeOK(h))
	}

	// gamescope switched modes on its own: re-assert after that modeset.
	h.mu.Lock()
	h.scan = md(2560, 1440, 120)
	h.mu.Unlock()
	if why := m.checkComposite(ctx); !strings.Contains(why, "switched to 2560x1440@120") {
		t.Errorf("after gamescope's modeset: %q", why)
	}
	if why := m.checkComposite(ctx); why != "" {
		t.Errorf("same mode again: %q", why)
	}

	// Nothing observable says anything is wrong: leave it.
	h.mu.Lock()
	h.xErr = errors.New("unable to open display")
	h.mu.Unlock()
	if why := m.checkComposite(ctx); why != "" {
		t.Errorf("acted on nothing: %q", why)
	}
	h.mu.Lock()
	h.xErr = nil
	h.mu.Unlock()

	// While a session or the policy switches units, the watchdog waits.
	h.steamWrites("0")
	m.op.LockCtx(ctx)
	if why := m.checkComposite(ctx); why != "" {
		t.Errorf("acted while op was held: %q", why)
	}
	m.op.Unlock()
	if why := m.checkComposite(ctx); why == "" {
		t.Error("did not act once op was free")
	}

	// A monitor appears: the welcome screen takes over, nothing to watch.
	h.setMonitor(true)
	m.onScan(ctx)
	h.setActive(GamescopeUnit, true, true) // even if gamescope lingered
	h.steamWrites("0")
	if why := m.checkComposite(ctx); why != "" {
		t.Errorf("acted in welcome state: %q", why)
	}
}

// TestCompositeWatchdogDuringSession: with a monitor, only a session makes
// the watchdog act.
func TestCompositeWatchdogDuringSession(t *testing.T) {
	m, h, _, _ := newTestManager(t, true)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 90})
	if !resp.OK || !compositeOK(h) {
		t.Fatalf("Begin = %+v, composite ok=%v", resp, compositeOK(h))
	}
	// Begin told the watchdog about its own modeset.
	if why := m.checkComposite(ctx); why != "" {
		t.Errorf("acted right after Begin: %q", why)
	}
	h.steamWrites("0")
	if why := m.checkComposite(ctx); why == "" || !compositeOK(h) {
		t.Errorf("session: why=%q ok=%v", why, compositeOK(h))
	}
	m.End(ctx)
	h.steamWrites("0")
	if why := m.checkComposite(ctx); why != "" {
		t.Errorf("acted after the session: %q", why)
	}
}

// TestBeginBudget: however long gamescope takes, Begin answers within its
// budget (well inside the hook's 90 s), and composition is still forced.
func TestBeginBudget(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	m.now = time.Now
	m.beginBudget = 300 * time.Millisecond
	m.composeReserve = 100 * time.Millisecond
	h.ignoreMC = true // the mode never arrives; modeTimeout (60 s) alone would wait
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	start := time.Now()
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 60})
	took := time.Since(start)
	if took > time.Second {
		t.Errorf("Begin took %s with a 300ms budget", took)
	}
	if resp.OK || !strings.Contains(resp.Message, "instead of 1280x800@60") {
		t.Errorf("Begin = %+v", resp)
	}
	if !compositeOK(h) {
		t.Error("composition not forced after the timeout")
	}
	if ok, _ := m.Streaming(); !ok {
		t.Error("session dropped")
	}
}

// TestDefaultBudgetsFit: the 60 s mode ceiling and composition fit in
// Begin's budget, which fits in the session handler's, which answers
// before the hook's 90 s.
func TestDefaultBudgetsFit(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	d := newManager(m.cfg, m.h, m.hub)
	if d.modeTimeout != 60*time.Second {
		t.Errorf("mode ceiling = %s", d.modeTimeout)
	}
	if d.beginBudget >= session.HandlerTimeout || session.HandlerTimeout >= session.Timeout {
		t.Errorf("budgets: begin %s, handler %s, hook %s", d.beginBudget, session.HandlerTimeout, session.Timeout)
	}
	if d.beginBudget < d.modeTimeout+d.composeReserve {
		t.Errorf("begin budget %s leaves no room for the 60 s mode wait", d.beginBudget)
	}
	if d.compositeEvery != 5*time.Second {
		t.Errorf("watchdog every %s", d.compositeEvery)
	}
}

// TestBeginWhileBusy: when something holds the display past the budget,
// Begin answers anyway and the policy starts gamescope once it can.
func TestBeginWhileBusy(t *testing.T) {
	m, h, _, _ := newTestManager(t, true)
	m.beginBudget = 50 * time.Millisecond
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	if !h.isActive(WelcomeUnit, false) {
		t.Fatal("no welcome screen")
	}

	m.op.LockCtx(ctx) // a unit that will not stop, say
	start := time.Now()
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 90})
	if time.Since(start) > time.Second || resp.OK || !strings.Contains(resp.Message, "busy") {
		t.Errorf("Begin = %+v after %s", resp, time.Since(start))
	}
	if ok, why := m.Streaming(); !ok || !strings.Contains(why, "Deck") {
		t.Errorf("Streaming = %v %q", ok, why)
	}
	m.reconcile(ctx, false) // still held: nothing happens
	if h.isActive(GamescopeUnit, true) {
		t.Fatal("reconcile ran while op was held")
	}
	m.op.Unlock()
	m.reconcile(ctx, false)
	if !h.isActive(GamescopeUnit, true) || h.isActive(WelcomeUnit, false) {
		t.Fatalf("policy did not move to gamescope: %v", h.callLog())
	}
	waitFor(t, func() bool { return compositeOK(h) })

	// End does not hang on a busy display either.
	m.op.LockCtx(ctx)
	ectx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	m.End(ectx)
	cancel()
	m.op.Unlock()
	if ok, _ := m.Streaming(); ok {
		t.Error("session survived End")
	}
}

// TestRunCompositeWatchdog drives the watchdog from the real Run loop on a
// headless box.
func TestRunCompositeWatchdog(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	m.now = time.Now
	m.compositeEvery = 5 * time.Millisecond
	short, err := os.MkdirTemp("", "vc")
	if err != nil {
		t.Fatal(err)
	}
	saveRun := config.RunDir
	config.RunDir = short
	t.Cleanup(func() { config.RunDir = saveRun; os.RemoveAll(short) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, func() bool { return h.isActive(GamescopeUnit, true) && compositeOK(h) })
	for range 3 {
		h.steamWrites("0")
		waitFor(t, func() bool { return compositeOK(h) })
	}
	if n := strings.Count(strings.Join(h.callLog(), "\n"), setPropCall); n < 4 {
		t.Errorf("property set %d times", n)
	}
}
