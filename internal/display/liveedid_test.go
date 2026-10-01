package display

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/session"
)

func applyCalls(h *fakeHost) int {
	n := 0
	for _, c := range h.callLog() {
		if strings.HasPrefix(c, "apply-edid ") {
			n++
		}
	}
	return n
}

func TestSessionLearnsModeLive(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Pixel", Width: 2400, Height: 1080, FPS: 120})
	if !resp.OK || resp.Mode != "2400x1080@120" || resp.Message != "" {
		t.Fatalf("Begin = %+v", resp)
	}
	if !slices.Contains(h.callLog(), "apply-edid card1 DP-1") {
		t.Errorf("calls = %v", h.callLog())
	}
	if m.rebootNeededNow() {
		t.Error("reboot needed after a live EDID change")
	}
	// The next client with that mode finds it offered: no second change.
	m.End(ctx)
	h.resetCalls()
	if resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Pixel 2", Width: 2400, Height: 1080, FPS: 120}); resp.Mode != "2400x1080@120" {
		t.Fatalf("second Begin = %+v", resp)
	}
	if n := applyCalls(h); n != 0 {
		t.Errorf("%d EDID changes for a mode already offered", n)
	}
}

func TestSessionLiveModeFallsBack(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	h.gsStale = true // the kernel takes the EDID, gamescope never sees the mode
	// The test clock stands still: the wait ends when its context does.
	m.liveModeWait, m.composeReserve = 20*time.Millisecond, 0
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	resp := m.Begin(ctx, session.Request{Op: "begin", Client: "Pixel", Width: 2400, Height: 1080, FPS: 120})
	if !resp.OK || resp.Mode != "1920x1080@60" || !strings.Contains(resp.Message, "learned") {
		t.Fatalf("Begin = %+v", resp)
	}
	if !m.rebootNeededNow() {
		t.Error("no reboot needed after the live mode failed")
	}
	// This machine is not tried again until a reboot.
	m.End(ctx)
	h.resetCalls()
	m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 720, FPS: 90})
	if n := applyCalls(h); n != 0 {
		t.Errorf("%d EDID changes after a failed one", n)
	}
}

func TestLiveEDIDIgnored(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	h.applyNoop = true // the kernel accepts the override but keeps its EDID
	m.init(context.Background())
	w, out := call(t, m.handleAddMode, http.MethodPost, `{"mode":"2560x1080@100"}`)
	if w.Code != 200 || out["reboot_needed"] != true {
		t.Fatalf("add: %d %v", w.Code, out)
	}
}

func TestAddModeLive(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	w, out := call(t, m.handleAddMode, http.MethodPost, `{"mode":"2560x1080@100"}`)
	if w.Code != 200 || out["reboot_needed"] != false {
		t.Fatalf("add: %d %v", w.Code, out)
	}
	if n := applyCalls(h); n != 1 {
		t.Fatalf("%d EDID changes, want 1", n)
	}
	// Removing the mode gamescope shows waits for the reboot.
	h.mu.Lock()
	h.scan, h.scanOK = md(2560, 1080, 100), true
	h.mu.Unlock()
	if w, out := removeMode(t, m, "2560x1080@100"); w.Code != 200 || out["reboot_needed"] != true {
		t.Fatalf("remove: %d %v", w.Code, out)
	}
	if n := applyCalls(h); n != 1 {
		t.Errorf("the EDID changed under the mode on screen")
	}
}

func TestAddModeDuringSession(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	m.Begin(ctx, session.Request{Op: "begin", Client: "Deck", Width: 1280, Height: 800, FPS: 90})
	h.resetCalls()
	// A re-probe may blink the stream: the change waits for its end.
	w, out := call(t, m.handleAddMode, http.MethodPost, `{"mode":"2560x1080@100"}`)
	if w.Code != 200 || out["reboot_needed"] != true || applyCalls(h) != 0 {
		t.Fatalf("add while streaming: %d %v %v", w.Code, out, h.callLog())
	}
	m.End(ctx)
	if applyCalls(h) != 1 || m.rebootNeededNow() {
		t.Errorf("after the session: calls %v, reboot needed %v", h.callLog(), m.rebootNeededNow())
	}
}
