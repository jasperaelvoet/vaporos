package sunshine

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// From a box where Sunshine started 30 ms before gamescope's first modeset.
const failedRunLog = `[2026-10-01 20:00:11.783]: Info: Sunshine version: 2026.929.125923 commit: 019b1ba661d829e334706557a3e2bdc3d2374591
[2026-10-01 20:00:11.784]: Info: Dropped DRM master for /dev/dri/card0
[2026-10-01 20:00:11.785]: Error: [wayland] Environment variable WAYLAND_DISPLAY has not been defined
[2026-10-01 20:00:11.785]: Error: Unable to initialize capture method
[2026-10-01 20:00:11.785]: Error: Platform failed to initialize
[2026-10-01 20:00:13.387]: Error: Video failed to find working encoder
[2026-10-01 20:00:13.389]: Info: Configuration UI available at [https://localhost:47990]
`

const goodRunLog = `[2026-10-01 16:17:56.386]: Info: Sunshine version: 2026.929.125923 commit: 019b1ba661d829e334706557a3e2bdc3d2374591
[2026-10-01 16:17:56.404]: Info: Found monitor for DRM screencasting
[2026-10-01 16:17:57.101]: Info: Configuration UI available at [https://localhost:47990]
`

func TestCaptureState(t *testing.T) {
	for _, tc := range []struct {
		name string
		log  string
		want captureVerdict
	}{
		{"empty", "", capturePending},
		{"failed", failedRunLog, captureFailed},
		{"good", goodRunLog, captureOK},
		{"starting", "[x]: Info: Sunshine version: 1\n[x]: Info: Found monitor for DRM screencasting\n", capturePending},
		{"good after a failed run", failedRunLog + goodRunLog, captureOK},
		{"failed after a good run", goodRunLog + failedRunLog, captureFailed},
	} {
		if got := captureState(tc.log); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRestartsSunshineWhoseCaptureFailedAtStartup(t *testing.T) {
	h := newHarness(t)
	writeCreds(t, h.f.user, h.f.pass)
	writeState(t, h.f.user)
	clock := &fakeClock{t: time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)}
	h.s.now = clock.now
	h.s.prepare(context.Background())
	h.rec.systemctl = nil

	journal := failedRunLog // polls run on this goroutine
	h.s.journalTail = func(context.Context, int) (string, error) { return journal, nil }
	restart := []string{"restart " + unitName}

	h.s.poll(context.Background())
	if calls := h.rec.calls(); len(calls) != 0 {
		t.Fatalf("restarted before gamescope had a moment: %q", calls)
	}
	clock.add(captureRetryMin)
	h.s.poll(context.Background())
	if calls := h.rec.calls(); !reflect.DeepEqual(calls, restart) {
		t.Fatalf("failed capture: systemctl %q", calls)
	}

	// The new run fails too (still no lit display): back off.
	journal = failedRunLog + failedRunLog
	clock.add(captureRetryMin)
	h.s.poll(context.Background())
	if n := len(h.rec.calls()); n != 2 {
		t.Fatalf("second try: %d calls", n)
	}
	clock.add(captureRetryMin)
	h.s.poll(context.Background())
	if n := len(h.rec.calls()); n != 2 {
		t.Errorf("did not back off: %q", h.rec.calls())
	}

	// The next run sets up capture: nothing more to do, ever.
	journal = failedRunLog + goodRunLog
	clock.add(captureRetryMax)
	h.s.poll(context.Background())
	journal += failedRunLog // a later run vosd did not start
	for range 3 {
		clock.add(captureRetryMax)
		h.s.poll(context.Background())
	}
	if n := len(h.rec.calls()); n != 2 {
		t.Errorf("restarted a good run: %q", h.rec.calls())
	}
}
