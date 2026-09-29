package sunshine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/events"
)

// Sunshine's lines as journalctl --output=cat shows them.
const (
	lineConnected    = "[2026-09-29 21:00:01.000]: Info: CLIENT CONNECTED"
	lineDisconnected = "[2026-09-29 21:00:02.000]: Info: CLIENT DISCONNECTED"
	linePingTimeout  = "[2026-09-29 21:00:03.000]: Info: 192.168.1.30: Ping Timeout"
)

func TestClientTrackCountsControlConnections(t *testing.T) {
	t0 := time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC)
	steps := []struct {
		line      string
		ev        clientEvent
		connected int
		idle      bool
	}{
		{"[…]: Info: New streaming session started [active sessions: 1]", clientNone, 0, false},
		{"[…]: Error: Initial Ping Timeout", clientNone, 0, false},
		// Connected before anyone looked: its leaving still counts.
		{lineDisconnected, clientLeft, 0, true},
		{lineConnected, clientConnected, 1, false},
		{lineConnected, clientConnected, 2, false},
		{linePingTimeout, clientLeft, 1, false},
		{lineDisconnected, clientLeft, 0, true},
		{lineDisconnected, clientLeft, 0, true}, // floored at zero
		{lineConnected, clientConnected, 1, false},
		{"[…]: Info: Process terminated", clientReset, 0, true},
		{lineConnected, clientConnected, 1, false},
		{"[…]: Info: Sunshine version: 2026.928.101500 commit: 0123abc", clientReset, 0, true},
		{fbError, clientNone, 0, true},
	}
	var c clientTrack
	if c.idle() {
		t.Fatal("idle before any connection line")
	}
	for i, st := range steps {
		at := t0.Add(time.Duration(i) * time.Minute)
		ev := c.observe(st.line, at)
		if ev != st.ev || c.connected != st.connected || c.idle() != st.idle {
			t.Fatalf("step %d %q: event %v, connected %d, idle %v; want %v, %d, %v",
				i, st.line, ev, c.connected, c.idle(), st.ev, st.connected, st.idle)
		}
		if ev != clientNone && c.idle() && !c.since.Equal(at) {
			t.Errorf("step %d: idle since %v, want %v", i, c.since, at)
		}
	}
}

func TestJournalTimestamps(t *testing.T) {
	want := time.Unix(1759179791, 512345000)
	if at, ok := journalTime("1759179791.512345 vapor sunshine[42]: " + lineConnected); !ok || !at.Equal(want) {
		t.Errorf("short-unix line: %v %v", at, ok)
	}
	for _, line := range []string{
		lineConnected,                      // --output=cat
		"192.168.1.30: Ping Timeout",       // digits and dots, but not a timestamp
		"1759179791 vapor sunshine[42]: x", // no fraction
		"-- Boot 0123 --",
		"",
	} {
		if at, ok := journalTime(line); ok {
			t.Errorf("%q parsed as %v", line, at)
		}
	}

	start, now := want.Add(time.Second), want.Add(time.Minute)
	if at, live := lineAt("1759179791.512345 vapor sunshine[42]: x", start, now); live || !at.Equal(want) {
		t.Errorf("history line: %v live=%v", at, live)
	}
	if at, live := lineAt(fmt.Sprintf("%d.000000 vapor sunshine[42]: x", start.Unix()+5), start, now); !live || !at.Equal(now) {
		t.Errorf("new line: %v live=%v", at, live)
	}
	if at, live := lineAt(lineConnected, start, now); !live || !at.Equal(now) {
		t.Errorf("line without a timestamp: %v live=%v", at, live)
	}
}

func TestBusyWaitsForTheClientToReconnect(t *testing.T) {
	h := newHarness(t)
	clock := &fakeClock{t: time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC)}
	h.s.now = clock.now
	busy := func(want bool, wantWhy string) {
		t.Helper()
		if b, why := h.s.Busy(); b != want || why != wantWhy {
			t.Errorf("Busy() = %v %q, want %v %q", b, why, want, wantWhy)
		}
	}
	const waiting = "Moonlight stream (waiting for the client to reconnect)"

	h.f.setBusy(true)
	busy(true, "Moonlight stream") // nothing known about clients yet
	h.s.noteBusy(true)
	h.s.observeClients(lineConnected, clock.now(), true)
	busy(true, "Moonlight stream")

	clock.add(time.Minute)
	h.s.observeClients(lineDisconnected, clock.now(), true)
	busy(true, waiting)
	clock.add(abandonAfter - time.Second)
	busy(true, waiting)
	clock.add(time.Second)
	busy(false, "") // abandoned: the idle timer may run even if closing fails

	// A client that left long ago does not make a new launch abandoned:
	// the wait counts from when the app was seen running.
	h.s.noteBusy(false)
	h.s.noteBusy(true)
	busy(true, waiting)

	h.f.setBusy(false)
	busy(false, "")
}

// messages returns the published system.message payloads.
func (r *recorder) messages() []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]string
	for _, e := range r.events {
		if e.Topic == "system.message" {
			var m map[string]string
			json.Unmarshal(e.Data, &m)
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeSunshine) closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// startRun runs the service with a fake clock and a fake journal, with
// its files in place so that prepare has nothing to change.
func startRun(t *testing.T, h *harness, clock *fakeClock, follow func(context.Context) (<-chan string, error)) {
	t.Helper()
	h.s.pollEvery = 10 * time.Millisecond
	h.s.now = clock.now
	writeCreds(t, h.f.user, h.f.pass)
	writeState(t, h.f.user)
	if _, err := h.s.writeConf(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.writeApps(); err != nil {
		t.Fatal(err)
	}
	h.s.follow = follow
	h.s.subscribe = events.NewHub().Subscribe
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func noVersionRuns(t *testing.T, h *harness) {
	t.Helper()
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	for _, c := range h.rec.asGamer {
		if slices.Contains(c, "--version") {
			t.Errorf("ran %q, which rotates Sunshine's log", c)
		}
	}
}

func TestRunClosesAnAbandonedApp(t *testing.T) {
	h := newHarness(t)
	clock := &fakeClock{t: time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC)}
	lines := make(chan string)
	var follows atomic.Int32
	startRun(t, h, clock, func(context.Context) (<-chan string, error) {
		follows.Add(1)
		return lines, nil
	})
	// feed hands Run a journal line and waits until it was handled: Run
	// takes the next line only after finishing the previous one.
	feed := func(line string) {
		t.Helper()
		for _, l := range []string{line, ""} {
			select {
			case lines <- l:
			case <-time.After(5 * time.Second):
				t.Fatalf("Run stopped reading the journal at %q", line)
			}
		}
	}
	idleTicks := func() { time.Sleep(50 * time.Millisecond) }

	h.f.setBusy(true) // Moonlight launched Steam
	waitFor(t, "journal follower", func() bool { return follows.Load() == 1 })
	feed(lineConnected)
	if b, why := h.s.Busy(); !b || why != "Moonlight stream" {
		t.Errorf("streaming: Busy() = %v %q", b, why)
	}

	// Disconnected without quitting, then resumed within the window.
	feed(lineDisconnected)
	clock.add(abandonAfter - time.Minute)
	feed(lineConnected)
	clock.add(2 * abandonAfter)
	idleTicks()
	if n := h.f.closes(); n != 0 {
		t.Fatalf("closed the app of a connected client (%d)", n)
	}

	// Gone for good: the client stopped answering pings.
	feed(linePingTimeout)
	clock.add(abandonAfter - time.Second)
	idleTicks()
	if n := h.f.closes(); n != 0 {
		t.Fatalf("closed the app %v early", time.Second)
	}
	clock.add(time.Second)
	waitFor(t, "POST /api/apps/close", func() bool { return h.f.closes() == 1 })
	waitFor(t, "system.message", func() bool { return len(h.rec.messages()) == 1 })
	if m := h.rec.messages()[0]; m["level"] != "info" || !strings.Contains(m["text"], fmt.Sprint(abandonMinutes)) {
		t.Errorf("system.message = %v", m)
	}
	if b, why := h.s.Busy(); b {
		t.Errorf("abandoned stream still busy: %q", why)
	}

	// Sunshine has not ended the app yet: no second close before the retry.
	idleTicks()
	if n := h.f.closes(); n != 1 {
		t.Errorf("closed %d times", n)
	}
	// It ends the app (and runs `vos session end`): the follower stops and
	// forgets the clients.
	h.f.setBusy(false)
	waitFor(t, "client count reset", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		return !h.s.clients.known && h.s.busySince.IsZero()
	})
	if calls := h.rec.calls(); len(calls) != 0 {
		t.Errorf("closing an abandoned app restarted Sunshine: %q", calls)
	}
	noVersionRuns(t, h)
}

// journalLine is a line as the real follower reads it (short-unix).
func journalLine(at time.Time, msg string) string {
	return fmt.Sprintf("%d.%06d vapor sunshine[4242]: [%s]: %s",
		at.Unix(), at.Nanosecond()/1000, at.Format("2006-01-02 15:04:05.000"), msg)
}

func TestRunReplaysTheJournalHistory(t *testing.T) {
	h := newHarness(t)
	clock := &fakeClock{t: time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC)}
	var replayed atomic.Int32
	h.f.setBusy(true) // vosd (re)started while an app was running
	startRun(t, h, clock, func(context.Context) (<-chan string, error) {
		now := clock.now()
		ago := func(d time.Duration) time.Time { return now.Add(-d) }
		hist := []string{
			// A previous Sunshine whose client never logged its disconnect.
			journalLine(ago(3*time.Hour), "Info: CLIENT CONNECTED"),
			journalLine(ago(2*time.Hour), "Info: Sunshine version: 2026.928.101500 commit: 0123abc"),
			journalLine(ago(40*time.Minute), "Info: CLIENT CONNECTED"),
			journalLine(ago(35*time.Minute), "Info: CLIENT CONNECTED"), // a second device
			journalLine(ago(30*time.Minute), "Info: CLIENT DISCONNECTED"),
			journalLine(ago(20*time.Minute), "Info: 192.168.1.30: Ping Timeout"),
		}
		// A plane-loss storm long past must not trip the watchdog now.
		for i := range 300 {
			hist = append(hist, journalLine(ago(15*time.Minute).Add(time.Duration(i)*16*time.Millisecond),
				"Warning: Couldn't get drm fb for plane [95]: No such file or directory"))
		}
		ch := make(chan string, len(hist))
		for _, l := range hist {
			ch <- l
		}
		replayed.Store(int32(len(hist)))
		return ch, nil
	})
	waitFor(t, "replayed client count", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		return replayed.Load() > 0 && h.s.clients.idle() && !h.s.busySince.IsZero()
	})
	h.s.mu.Lock()
	since := h.s.clients.since
	h.s.mu.Unlock()
	if want := clock.now().Add(-20 * time.Minute); !since.Equal(want) {
		t.Errorf("idle since %v, want %v (the replayed ping timeout)", since, want)
	}

	// Abandoned for 20 minutes already, but vosd only now saw the app:
	// it still waits the full window from here.
	if b, why := h.s.Busy(); !b || !strings.Contains(why, "reconnect") {
		t.Errorf("Busy() = %v %q", b, why)
	}
	clock.add(abandonAfter - time.Second)
	time.Sleep(50 * time.Millisecond)
	if n := h.f.closes(); n != 0 {
		t.Fatalf("closed early (%d)", n)
	}
	clock.add(time.Second)
	waitFor(t, "POST /api/apps/close", func() bool { return h.f.closes() == 1 })
	if calls := h.rec.calls(); len(calls) != 0 {
		t.Errorf("the replayed freeze restarted Sunshine: %q", calls)
	}
	for _, m := range h.rec.messages() {
		if m["level"] != "info" {
			t.Errorf("unexpected system.message %v", m)
		}
	}
	noVersionRuns(t, h)
}

func writeDesc(t *testing.T, db, dir, name, version string) {
	t.Helper()
	p := filepath.Join(db, "local", dir, "desc")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	desc := fmt.Sprintf("%%NAME%%\n%s\n\n%%VERSION%%\n%s\n\n%%BASE%%\n%s\n\n%%DESC%%\nSelf-hosted game stream host for Moonlight\n", name, version, name)
	if err := os.WriteFile(p, []byte(desc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPackageVersion(t *testing.T) {
	db := t.TempDir()
	if v := packageVersion(db, "sunshine"); v != "" {
		t.Errorf("empty database: %q", v)
	}
	writeDesc(t, db, "sunshine-git-r1-1", "sunshine-git", "r1-1")
	if v := packageVersion(db, "sunshine"); v != "" {
		t.Errorf("another package's version: %q", v)
	}
	writeDesc(t, db, "sunshine-2026.928.101500-2", "sunshine", "1:2026.928.101500-2")
	if v := packageVersion(db, "sunshine"); v != "2026.928.101500" {
		t.Errorf("version = %q", v)
	}
}

func TestStatusFallsBackToThePackageVersion(t *testing.T) {
	h := newHarness(t)
	writeDesc(t, h.s.pacmanDB, "sunshine-2026.928.101500-1", "sunshine", "2026.928.101500-1")
	h.s.client = NewClient("https://127.0.0.1:1", "u", "p", certPath()) // Sunshine is down
	h.s.infoURL = "http://127.0.0.1:1/serverinfo"
	if w, out := call(t, h.s.handleStatus, "GET", ""); w.Code != 200 || out["version"] != "2026.928.101500" {
		t.Errorf("status = %d %v", w.Code, out)
	}
	noVersionRuns(t, h)
}
