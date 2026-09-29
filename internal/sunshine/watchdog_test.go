package sunshine

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/events"
)

const fbError = "[2026-09-29 21:03:11.512]: Warning: Couldn't get drm fb for plane [95]: No such file or directory"

func TestPlaneWatchNeedsASustainedFreeze(t *testing.T) {
	t0 := time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

	// A modeset blip: a burst of errors within a fraction of a second.
	var p planeWatch
	for i := range 30 {
		if p.observe(fbError, at(i*16)) {
			t.Fatal("a 0.5 s blip counted as a freeze")
		}
	}

	// Errors at 60 fps for two seconds: frozen.
	p.reset()
	fired := -1
	for i := range 200 {
		if p.observe(fbError, at(5000+i*16)) {
			fired = i
			break
		}
	}
	if fired < 0 || fired*16 < int(watchMinSpan/time.Millisecond) {
		t.Fatalf("freeze detected at frame %d", fired)
	}

	// Sparse errors never reach 5 within 10 s.
	p.reset()
	for i := range 20 {
		if p.observe(fbError, at(i*3000)) {
			t.Fatalf("sparse errors triggered at %d", i)
		}
	}

	// Once a second (a rate-limited log) for 5 s: frozen at the 5th.
	p.reset()
	for i := range 5 {
		got := p.observe("Couldn't get drm plane [3]: Invalid argument", at(i*1000))
		if got != (i == 4) {
			t.Fatalf("second %d: observe = %v", i, got)
		}
	}

	// Unrelated lines are ignored.
	p.reset()
	for i := range 50 {
		if p.observe("Info: New streaming session started", at(i*100)) {
			t.Fatal("ordinary log line counted")
		}
	}
}

// fakeClock is a settable clock safe to read from the Run goroutine.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunRecoversAFrozenStream(t *testing.T) {
	h := newHarness(t)
	h.s.pollEvery = 10 * time.Millisecond
	clock := &fakeClock{t: time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC)}
	h.s.now = clock.now
	// Existing credentials, already known to Sunshine: prepare must not
	// restart anything.
	writeCreds(t, h.f.user, h.f.pass)
	writeState(t, h.f.user)
	if _, err := h.s.writeConf(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.writeApps(); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string)
	var follows sync.WaitGroup
	follows.Add(1)
	followed := 0
	var mu sync.Mutex
	h.s.follow = func(ctx context.Context) (<-chan string, error) {
		mu.Lock()
		defer mu.Unlock()
		followed++
		if followed == 1 {
			follows.Done()
			return lines, nil
		}
		return make(chan string), nil
	}
	hub := events.NewHub()
	h.s.subscribe = hub.Subscribe

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Idle: nobody follows the journal.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if followed != 0 {
		t.Fatal("journal followed without a stream")
	}
	mu.Unlock()
	if calls := h.rec.calls(); len(calls) != 0 {
		t.Fatalf("prepare restarted Sunshine without a change: %q", calls)
	}

	// A stream starts; the display manager reports the session.
	h.f.setBusy(true)
	hub.Publish("session.begin", map[string]any{"client": "iPhone", "mode": "2556x1179@120", "hdr": true})
	follows.Wait()
	waitFor(t, "session", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		return h.s.session != nil && h.s.session.Mode == "2556x1179@120"
	})

	// Capture loses its plane: errors at ~60 fps until the watchdog acts.
	closed := func() bool {
		h.f.mu.Lock()
		defer h.f.mu.Unlock()
		return h.f.closed > 0
	}
	sent := 0
	for ; sent < 300 && !closed(); sent++ {
		clock.add(16 * time.Millisecond)
		select {
		case lines <- fbError:
		case <-time.After(50 * time.Millisecond): // the watchdog stopped reading
		}
	}
	waitFor(t, "recovery", closed)
	if span := time.Duration(sent) * 16 * time.Millisecond; span < watchMinSpan {
		t.Errorf("recovered after %v of errors, before the %v minimum", span, watchMinSpan)
	}
	waitFor(t, "restart", func() bool { return slices.Contains(h.rec.calls(), "restart "+unitName) })
	waitFor(t, "system.message", func() bool { return slices.Contains(h.rec.topics(), "system.message") })
	var msg map[string]string
	for _, e := range h.rec.events {
		if e.Topic == "system.message" {
			json.Unmarshal(e.Data, &msg)
		}
	}
	if msg["level"] != "warning" || msg["text"] == "" {
		t.Errorf("system.message = %v", msg)
	}

	// Cooldown: no second journal follower right away, even if the stream
	// is still reported busy.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if followed != 1 {
		t.Errorf("followed %d times during the cooldown", followed)
	}
	mu.Unlock()
	clock.add(watchCooldown + time.Second)
	waitFor(t, "watchdog re-armed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return followed == 2
	})
}
