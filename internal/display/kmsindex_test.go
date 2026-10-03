package display

import (
	"context"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/display/drm"
)

func TestKMSGrabIndex(t *testing.T) {
	pl := func(id, crtc uint32) drm.Plane { return drm.Plane{ID: id, CRTCID: crtc, FBID: id + 100} }
	for _, c := range []struct {
		name   string
		lit    []drm.Plane
		crtc   uint32
		others bool
		want   int
		ok     bool
	}{
		{"composited", []drm.Plane{pl(206, 432)}, 432, false, 0, true},
		{"after a lit welcome output", []drm.Plane{pl(40, 80), pl(206, 432)}, 432, false, 1, true},
		{"two planes on the crtc", []drm.Plane{pl(200, 432), pl(206, 432)}, 432, false, 0, false},
		{"nothing on the crtc", []drm.Plane{pl(40, 80)}, 432, false, 0, false},
		{"no crtc", nil, 0, false, 0, false},
		{"another card lit", []drm.Plane{pl(206, 432)}, 432, true, 0, false},
	} {
		got, ok := kmsgrabIndex(c.lit, c.crtc, c.others)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %d,%v want %d,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestCaptureIndexOnlyWhileGaming(t *testing.T) {
	m, h, _, _ := newTestManager(t, true) // a monitor: the welcome screen
	ctx := context.Background()
	h.mu.Lock()
	h.kmsIndex, h.kmsOK = 0, true
	h.mu.Unlock()
	m.init(ctx)
	m.reconcile(ctx, false)
	if _, ok := m.CaptureIndex(); ok {
		t.Error("answered while the welcome screen runs")
	}

	m, h, _, _ = newTestManager(t, false) // headless: gamescope
	h.mu.Lock()
	h.kmsIndex, h.kmsOK = 0, true
	h.mu.Unlock()
	m.init(ctx)
	m.reconcile(ctx, false)
	if i, ok := m.CaptureIndex(); !ok || i != 0 {
		t.Errorf("gaming: %d,%v", i, ok)
	}
}
