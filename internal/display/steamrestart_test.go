package display

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

// gamingBox is a headless box with gamescope and a Steam (pid 4242)
// that started an hour before the manager's clock.
func gamingBox(t *testing.T, monitor bool) (*Manager, *fakeHost, *clock) {
	t.Helper()
	m, h, clk, _ := newTestManager(t, monitor)
	ctx := context.Background()
	m.init(ctx)
	if monitor {
		// A stream keeps gamescope on a box with a monitor; it just ended.
		m.Begin(ctx, session.Request{Client: "Deck", Width: 1280, Height: 800, FPS: 90})
		m.End(ctx)
	}
	m.reconcile(ctx, false)
	h.mu.Lock()
	h.steamPID = 4242
	h.gsJob = clk.now().Add(-time.Hour)
	h.gsMain = h.gsJob.Add(time.Second)
	h.mu.Unlock()
	h.resetCalls()
	if !h.isActive(GamescopeUnit, true) {
		t.Fatal("gamescope is not running")
	}
	return m, h, clk
}

func steamCalls(h *fakeHost) []string {
	var out []string
	for _, c := range h.callLog() {
		if strings.HasPrefix(c, "steam -shutdown ") || c == "restart "+GamescopeUnit || c == "start "+GamescopeUnit || c == "stop "+GamescopeUnit {
			out = append(out, c)
		}
	}
	return out
}

func request(m *Manager, reason string, user bool) *steamRequest {
	m.RestartSteam(reason, user)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.steamReq
}

func TestRestartSteamShutsSteamDown(t *testing.T) {
	m, h, _ := gamingBox(t, false)
	h.steamExits = true
	out, err := m.restartSteam(context.Background(), request(m, "extensions changed", false))
	if err != nil || out != steamRestarted {
		t.Fatalf("outcome %v, %v", out, err)
	}
	if got := steamCalls(h); !slices.Equal(got, []string{"steam -shutdown 4242"}) {
		t.Fatalf("calls %q", got)
	}
	if !m.op.TryLock() {
		t.Fatal("m.op still held")
	}
	m.op.Unlock()
}

// A Steam that does not shut down is restarted with gamescope's unit.
func TestRestartSteamFallsBackToTheUnit(t *testing.T) {
	m, h, clk := gamingBox(t, false)
	out, err := m.restartSteam(context.Background(), request(m, "extensions changed", false))
	if err != nil || out != steamRestarted {
		t.Fatalf("outcome %v, %v", out, err)
	}
	if got := steamCalls(h); !slices.Equal(got, []string{"steam -shutdown 4242", "restart " + GamescopeUnit}) {
		t.Fatalf("calls %q", got)
	}

	// So is one whose shutdown command fails, and gamescope without a
	// Steam holding its pipe (each a change made after the last start).
	h.resetCalls()
	clk.advance(time.Minute)
	h.mu.Lock()
	h.shutdownErr = errors.New("runuser: exit 1")
	h.steamPID = 4242
	h.mu.Unlock()
	m.restartSteam(context.Background(), request(m, "again", false))
	if got := h.callLog(); !slices.Equal(got, []string{"steam -shutdown 4242", "restart " + GamescopeUnit}) {
		t.Fatalf("calls %q", got)
	}
	h.resetCalls()
	clk.advance(time.Minute)
	h.mu.Lock()
	h.steamPID = 0
	h.mu.Unlock()
	m.restartSteam(context.Background(), request(m, "again", false))
	if got := h.callLog(); !slices.Equal(got, []string{"restart " + GamescopeUnit}) {
		t.Fatalf("calls %q", got)
	}
}

func TestRestartSteamWaitsOrSkips(t *testing.T) {
	ctx := context.Background()
	t.Run("a game runs", func(t *testing.T) {
		m, h, _ := gamingBox(t, false)
		h.mu.Lock()
		h.busy = "a Steam game is running"
		h.mu.Unlock()
		if out, _ := m.restartSteam(ctx, request(m, "x", true)); out != steamNotNow || len(steamCalls(h)) != 0 {
			t.Fatalf("outcome %v, calls %q", out, steamCalls(h))
		}
	})
	t.Run("a stream runs", func(t *testing.T) {
		m, h, _ := gamingBox(t, false)
		m.Begin(ctx, session.Request{Client: "Deck", Width: 1280, Height: 800, FPS: 90})
		h.resetCalls()
		if out, _ := m.restartSteam(ctx, request(m, "x", true)); out != steamNotNow || len(steamCalls(h)) != 0 {
			t.Fatalf("outcome %v, calls %q", out, steamCalls(h))
		}
	})
	t.Run("the display is switching", func(t *testing.T) {
		m, h, _ := gamingBox(t, false)
		m.op.TryLock()
		defer m.op.Unlock()
		if out, _ := m.restartSteam(ctx, request(m, "x", true)); out != steamNotNow || len(steamCalls(h)) != 0 {
			t.Fatalf("outcome %v, calls %q", out, steamCalls(h))
		}
	})
	t.Run("a monitor is attached", func(t *testing.T) {
		m, h, _ := gamingBox(t, true)
		if out, _ := m.restartSteam(ctx, request(m, "x", true)); out != steamNotNeeded || len(steamCalls(h)) != 0 {
			t.Fatalf("outcome %v, calls %q", out, steamCalls(h))
		}
	})
	t.Run("gamescope is down", func(t *testing.T) {
		m, h, _ := gamingBox(t, false)
		h.setActive(GamescopeUnit, true, false)
		if out, _ := m.restartSteam(ctx, request(m, "x", true)); out != steamNotNeeded || len(steamCalls(h)) != 0 {
			t.Fatalf("outcome %v, calls %q", out, steamCalls(h))
		}
	})
	t.Run("Steam started after the change", func(t *testing.T) {
		m, h, clk := gamingBox(t, false)
		req := request(m, "x", false)
		clk.advance(time.Second)
		h.mu.Lock()
		h.gsJob = clk.now()
		h.mu.Unlock()
		if out, _ := m.restartSteam(ctx, req); out != steamNotNeeded || len(steamCalls(h)) != 0 {
			t.Fatalf("outcome %v, calls %q", out, steamCalls(h))
		}
	})
}

// waitSteam waits for the restart maybeRestartSteam started to end.
func waitSteam(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		busy := m.steamBusy
		m.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the Steam restart did not end")
		}
		time.Sleep(time.Millisecond)
	}
}

func pending(m *Manager) *steamRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.steamReq
}

// One restart per change, vosd's own at most every steamEvery; a person's
// request is not held back.
func TestRestartSteamDebounce(t *testing.T) {
	m, h, clk := gamingBox(t, false)
	h.steamExits = true
	ctx := context.Background()

	m.RestartSteam("extensions changed", false)
	m.RestartSteam("a Steam account signed in", false)
	m.maybeRestartSteam(ctx)
	waitSteam(t, m)
	if got := steamCalls(h); len(got) != 1 || pending(m) != nil {
		t.Fatalf("calls %q, pending %+v", got, pending(m))
	}

	h.resetCalls()
	clk.advance(time.Minute)
	m.RestartSteam("extensions changed", false)
	m.maybeRestartSteam(ctx)
	waitSteam(t, m)
	if len(steamCalls(h)) != 0 || pending(m) == nil {
		t.Fatalf("restarted again within %s: %q", m.steamEvery, steamCalls(h))
	}
	m.RestartSteam("Restart Steam", true)
	m.maybeRestartSteam(ctx)
	waitSteam(t, m)
	if len(steamCalls(h)) != 1 || pending(m) != nil {
		t.Fatalf("a person's request waited: %q", steamCalls(h))
	}

	// While busy the request stays, and goes through once idle.
	h.resetCalls()
	clk.advance(m.steamEvery)
	h.mu.Lock()
	h.busy = "Steam is downloading"
	h.mu.Unlock()
	m.RestartSteam("extensions changed", false)
	m.maybeRestartSteam(ctx)
	waitSteam(t, m)
	if len(steamCalls(h)) != 0 || pending(m) == nil {
		t.Fatalf("restarted while busy: %q", steamCalls(h))
	}
	h.mu.Lock()
	h.busy = ""
	h.mu.Unlock()
	m.maybeRestartSteam(ctx)
	waitSteam(t, m)
	if len(steamCalls(h)) != 1 || pending(m) != nil {
		t.Fatalf("not restarted once idle: %q", steamCalls(h))
	}
}

// Run's policy loop takes a request up on its next pass.
func TestRunRestartsSteam(t *testing.T) {
	m, h, _ := gamingBox(t, false)
	h.steamExits = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	m.RestartSteam("extensions changed", false)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(h.callLog(), "steam -shutdown 4242") {
		if time.Now().After(deadline) {
			t.Fatalf("no restart: %q", h.callLog())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSteamJSONCurrent(t *testing.T) {
	setupPaths(t)
	saved := config.ExtCatalogPath
	t.Cleanup(func() { config.ExtCatalogPath = saved })
	config.ExtCatalogPath = t.TempDir() + "/extensions.list"
	if !steamJSONCurrent() {
		t.Fatal("an image without extensions waits")
	}
	mustWrite(t, config.ExtCatalogPath, "dispatcher 1\n")
	mustWrite(t, config.ExtBootPath(), `{"mode":"enabled","set":"7","tries_left":0,"reason":"","mounted":[],"skipped":[]}`)
	if steamJSONCurrent() {
		t.Fatal("no steam.json counts as current")
	}
	mustWrite(t, config.ExtSteamPath(), `{"set":"6","dispatcher":true}`)
	if steamJSONCurrent() {
		t.Fatal("the last boot's steam.json counts as current")
	}
	mustWrite(t, config.ExtSteamPath(), `{"set":"7","dispatcher":true}`)
	if !steamJSONCurrent() {
		t.Fatal("this boot's steam.json does not count")
	}

	// The first gamescope start waits for it, once.
	m, h, _, _ := newTestManager(t, false)
	m.steamGateWait = 5 * time.Second
	mustWrite(t, config.ExtBootPath(), `{"mode":"enabled","set":"7","tries_left":0,"reason":"","mounted":[],"skipped":[]}`)
	mustWrite(t, config.ExtSteamPath(), `{"set":"6"}`)
	go func() {
		time.Sleep(50 * time.Millisecond)
		mustWrite(t, config.ExtSteamPath(), `{"set":"7"}`)
	}()
	start := time.Now()
	m.init(context.Background())
	m.reconcile(context.Background(), false)
	if waited := time.Since(start); waited < 40*time.Millisecond || waited > 4*time.Second || !h.isActive(GamescopeUnit, true) {
		t.Fatalf("waited %s, gamescope %v", waited, h.isActive(GamescopeUnit, true))
	}
	if !m.steamGated {
		t.Fatal("the gate stays open for later starts")
	}
}
