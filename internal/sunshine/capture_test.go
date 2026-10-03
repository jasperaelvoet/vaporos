package sunshine

import (
	"context"
	"net"
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

// From the box: the run vosd restarted at boot mapped DP-1 while gamescope
// still scanned out on two planes; an hour later every stream failed.
const staleIndexLog = `[2026-10-03 22:04:10.867]: Info: Sunshine version: 2026.929.125923 commit: 019b1ba661d829e334706557a3e2bdc3d2374591
[2026-10-03 22:04:10.886]: Info: Mapped 'DP-1' to kmsgrab monitor index 1
[2026-10-03 22:04:10.886]: Info: Found monitor for DRM screencasting
[2026-10-03 22:04:11.101]: Info: Configuration UI available at [https://localhost:47990]
[2026-10-03 23:09:41.813]: Info: Mapped 'DP-1' to kmsgrab monitor index 1
[2026-10-03 23:09:41.816]: Error: Couldn't find monitor [1]
`

const freshIndexLog = `[2026-10-03 23:15:17.400]: Info: Sunshine version: 2026.929.125923 commit: 019b1ba661d829e334706557a3e2bdc3d2374591
[2026-10-03 23:15:17.517]: Info: Mapped 'DP-1' to kmsgrab monitor index 0
[2026-10-03 23:15:17.517]: Info: Found monitor for DRM screencasting
[2026-10-03 23:15:18.101]: Info: Configuration UI available at [https://localhost:47990]
`

func TestMappedIndex(t *testing.T) {
	for _, tc := range []struct {
		name string
		log  string
		want int
		ok   bool
	}{
		{"empty", "", 0, false},
		{"no capture", failedRunLog, 0, false},
		{"stale", staleIndexLog, 1, true},
		{"fresh run after a stale one", staleIndexLog + freshIndexLog, 0, true},
		{"run start scrolled out", "[x]: Info: Mapped 'DP-1' to kmsgrab monitor index 2\n", 2, true},
		{"new run not mapped yet", staleIndexLog + "[x]: Info: Sunshine version: 1\n", 0, false},
	} {
		got, ok := mappedIndex(tc.log)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %d,%v want %d,%v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

type fakeIndexScreens struct {
	index int
	ok    bool
}

func (*fakeIndexScreens) NoteResume()                     {}
func (*fakeIndexScreens) PairHint(net.IP, string, string) {}
func (f *fakeIndexScreens) CaptureIndex() (int, bool)     { return f.index, f.ok }

func TestRestartsSunshineWhoseKMSIndexWentStale(t *testing.T) {
	h := newHarness(t)
	writeCreds(t, h.f.user, h.f.pass)
	writeState(t, h.f.user)
	clock := &fakeClock{t: time.Date(2026, 10, 3, 22, 5, 0, 0, time.UTC)}
	h.s.now = clock.now
	h.s.prepare(context.Background())
	h.rec.systemctl = nil
	scr := &fakeIndexScreens{index: 0, ok: false} // gamescope not settled yet
	h.s.SetScreens(scr)

	journal := staleIndexLog
	h.s.journalTail = func(context.Context, int) (string, error) { return journal, nil }
	poll := func() {
		clock.add(indexCheckEvery)
		h.s.poll(context.Background())
	}
	restart := []string{"restart " + unitName}

	for range 3 {
		poll()
	}
	if calls := h.rec.calls(); len(calls) != 0 {
		t.Fatalf("acted while unsettled: %q", calls)
	}

	scr.ok = true // vosd forced composition: one plane, monitor 0
	poll()
	if calls := h.rec.calls(); len(calls) != 0 {
		t.Fatalf("restarted on the first mismatch: %q", calls)
	}
	poll()
	if calls := h.rec.calls(); !reflect.DeepEqual(calls, restart) {
		t.Fatalf("stale index: systemctl %q", calls)
	}

	// The new run maps monitor 0: all is well from then on.
	journal = staleIndexLog + freshIndexLog
	for range 10 {
		poll()
	}
	if n := len(h.rec.calls()); n != 1 {
		t.Errorf("restarted a good run: %q", h.rec.calls())
	}

	// A counting that differs from Sunshine's own never loops: restarts
	// for it back off.
	scr.index = 3
	for range 4 * 60 { // an hour of polls
		poll()
	}
	if n := len(h.rec.calls()); n < 2 || n > 7 {
		t.Errorf("restarts in an hour of a lasting mismatch: %d", n-1)
	}
}
