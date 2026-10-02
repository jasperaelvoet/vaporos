package extensions

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

func TestBreakerAllows(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	rec := func(ago time.Duration, fp, version string) autoRecord {
		return autoRecord{At: at.Add(-ago), Set: "4", Fingerprint: fp, Version: version}
	}
	for _, c := range []struct {
		name string
		f    autoFile
		want bool
	}{
		{"none yet", autoFile{}, true},
		{"this set once", autoFile{[]autoRecord{rec(time.Hour, "aa", "v1")}}, false},
		{"this set once, from another version", autoFile{[]autoRecord{rec(time.Hour, "aa", "v0")}}, true},
		{"this set twice", autoFile{[]autoRecord{rec(3*time.Hour, "aa", "v0"), rec(time.Hour, "aa", "v1")}}, false},
		{"three others today", autoFile{[]autoRecord{rec(time.Hour, "b", "v1"), rec(2*time.Hour, "c", "v1"), rec(3*time.Hour, "d", "v1")}}, false},
		{"three others, one yesterday", autoFile{[]autoRecord{rec(25*time.Hour, "b", "v1"), rec(2*time.Hour, "c", "v1"), rec(3*time.Hour, "d", "v1")}}, true},
		{"one in the future counts", autoFile{[]autoRecord{rec(-time.Hour, "b", "v1"), rec(2*time.Hour, "c", "v1"), rec(3*time.Hour, "d", "v1")}}, false},
	} {
		if got := c.f.allows("aa", "v1", at); got != c.want {
			t.Errorf("%s: allows = %v, want %v", c.name, got, c.want)
		}
	}
}

// autoRig is a rig with CoolerControl added and its set pending, and the
// auto-restart wired to a fake reboot on a boot that has settled.
type autoRig struct {
	*rig
	clock   time.Time
	reboots []string
	ok      bool
	next    NextBoot
	health  bool
	uptime  time.Duration
}

func newAutoRig(t *testing.T) *autoRig {
	t.Helper()
	a := &autoRig{rig: newRig(t), clock: time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC), ok: true, health: true, uptime: time.Minute}
	saved := now
	t.Cleanup(func() { now = saved })
	now = func() time.Time { return a.clock }
	if code, body := a.do("POST", "/extensions/coolercontrol", `{"password":"`+rigPassword+`"}`); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	a.pass()
	a.wireAuto()
	return a
}

func (a *autoRig) wireAuto() {
	a.s.SetAutoRestart(func(_ context.Context, msg string) (bool, error) {
		a.reboots = append(a.reboots, msg)
		return a.ok, nil
	}, func() NextBoot { return a.next })
	a.s.cc.auto.healthDone = func(context.Context) bool { return a.health }
	a.s.cc.auto.uptime = func() (time.Duration, error) { return a.uptime, nil }
}

// idle is one pass of the idle policy, idle for d.
func (a *autoRig) idle(d time.Duration) {
	a.s.IdleTick(a.t.Context(), IdleTick{Idle: d, ShutdownIn: -1})
}

func TestAutoRestart(t *testing.T) {
	a := newAutoRig(t)
	if d := a.doc(); !d.Restart.Needed || !d.Restart.Auto {
		t.Fatalf("restart = %+v, want needed and auto", d.Restart)
	}

	// Not idle long enough, an idle shutdown on its way or due soon, a
	// boot whose health check is still running: no restart.
	a.idle(autoIdle - 15*time.Second)
	a.s.IdleTick(t.Context(), IdleTick{Idle: 3 * time.Minute, ShutdownIn: -1, PoweringOff: true})
	a.s.IdleTick(t.Context(), IdleTick{Idle: 3 * time.Minute, ShutdownIn: 4 * time.Minute})
	a.health = false
	a.idle(3 * time.Minute)
	if len(a.reboots) != 0 {
		t.Fatalf("restarted: %q", a.reboots)
	}

	// An old boot needs no health check any more; an idle shutdown far
	// enough away does not stop it.
	a.uptime = 6 * time.Minute
	a.s.IdleTick(t.Context(), IdleTick{Idle: autoIdle, ShutdownIn: 10 * time.Minute})
	if len(a.reboots) != 1 || a.reboots[0] != "Restarting to finish adding CoolerControl" {
		t.Fatalf("reboots %q", a.reboots)
	}
	f := loadAuto()
	pending, _ := store.Pending()
	if len(f.Restarts) != 1 || f.Restarts[0].Set != pending.Name || f.Restarts[0].Version != bootedVersion ||
		!f.Restarts[0].At.Equal(a.clock) || f.Restarts[0].Fingerprint != store.Fingerprint(store.Pairs(a.b.cat, pending.IDs), nil) {
		t.Fatalf("autorestart.json = %+v", f)
	}

	// The restart did not happen (the PC came back on the same boot, say):
	// once per set, then only the user's restart.
	a.clock = a.clock.Add(time.Hour)
	a.idle(time.Hour)
	if len(a.reboots) != 1 {
		t.Fatalf("restarted twice for one set: %q", a.reboots)
	}
	if d := a.doc(); !d.Restart.Needed || d.Restart.Auto {
		t.Fatalf("restart = %+v, want needed without auto", d.Restart)
	}

	// Another VaporOS version booted in between (an update's trial does
	// not try the set): one more.
	a.s.mu.Lock()
	a.s.view.version = "20261001.000000"
	a.s.mu.Unlock()
	a.idle(time.Hour)
	if len(a.reboots) != 2 {
		t.Fatalf("no second restart after another version booted: %q", a.reboots)
	}
}

// A staged update or rollback boots first: the words say so.
func TestAutoRestartWithAStagedUpdate(t *testing.T) {
	for _, c := range []struct {
		next NextBoot
		want string
	}{
		{NextBoot{Version: "20261003.091500"}, "Restarting to install VaporOS 20261003.091500; CoolerControl is added after the next restart"},
		{NextBoot{Version: "20260801.000000", Rollback: true}, "Restarting to go back to VaporOS 20260801.000000; CoolerControl is added after the next restart"},
	} {
		a := newAutoRig(t)
		a.next = c.next
		a.idle(autoIdle)
		if len(a.reboots) != 1 || a.reboots[0] != c.want {
			t.Fatalf("reboots %q, want %q", a.reboots, c.want)
		}
	}
}

// While the next start leaves the extensions out, a restart would not try
// the set: the card still waits for a restart, and the document says it
// takes the one after the next, but /status has no restart for it and
// VaporOS does not restart by itself for it.
func TestAutoRestartSkipOnce(t *testing.T) {
	a := newAutoRig(t)
	if code, _ := a.do("POST", "/extensions/skip-once", ""); code != 200 {
		t.Fatal("skip-once")
	}
	want := RestartDoc{Needed: true, Reason: "The next start is without extensions. Restart again after it to finish adding CoolerControl."}
	if d := a.doc(); !d.SkipOnce || d.Restart != want || a.s.RestartNeeded() || a.card("coolercontrol").State != StateRestartNeeded {
		t.Fatalf("document = skip_once %v, restart %+v, RestartNeeded %v", d.SkipOnce, d.Restart, a.s.RestartNeeded())
	}
	a.idle(time.Hour)
	if len(a.reboots) != 0 || exists(config.ExtAutoRestartPath()) {
		t.Fatalf("restarted with skip-once set: %q", a.reboots)
	}
	if code, _ := a.do("DELETE", "/extensions/skip-once", ""); code != 200 {
		t.Fatal("cancelling skip-once")
	}
	if d := a.doc(); d.SkipOnce || !d.Restart.Needed || !d.Restart.Auto || !a.s.RestartNeeded() {
		t.Fatalf("document = skip_once %v, restart %+v", d.SkipOnce, d.Restart)
	}
	a.idle(time.Hour)
	if len(a.reboots) != 1 {
		t.Fatalf("no restart once skip-once was taken back: %q", a.reboots)
	}
}

// The health check and the reboot each run within their own bound, and
// a lock held elsewhere is waited for a bounded time, each lock its own.
func TestAutoRestartBounds(t *testing.T) {
	a := newAutoRig(t)
	var healthLeft, rebootLeft time.Duration
	a.s.cc.auto.healthDone = func(ctx context.Context) bool {
		d, _ := ctx.Deadline()
		healthLeft = time.Until(d)
		return true
	}
	a.s.SetAutoRestart(func(ctx context.Context, msg string) (bool, error) {
		d, ok := ctx.Deadline()
		if ok {
			rebootLeft = time.Until(d)
		}
		a.reboots = append(a.reboots, msg)
		return true, nil
	}, nil)
	a.idle(autoIdle)
	if len(a.reboots) != 1 || healthLeft <= 0 || healthLeft > autoHealthWait || rebootLeft <= 0 || rebootLeft > autoRebootWait {
		t.Fatalf("reboots %q, health had %v, reboot had %v", a.reboots, healthLeft, rebootLeft)
	}
}

// A busy store lock holds the auto-restart back only for autoLockWait,
// counted from when the update lock was had.
func TestAutoRestartWaitsForTheStoreLock(t *testing.T) {
	a := newAutoRig(t)
	saved := autoLockWait
	t.Cleanup(func() { autoLockWait = saved })
	autoLockWait = 300 * time.Millisecond
	unlock, err := store.Lock(t.Context())
	must(t, err)
	start := time.Now()
	a.idle(autoIdle)
	unlock()
	if len(a.reboots) != 0 {
		t.Fatal("restarted without the store lock")
	}
	if d := time.Since(start); d < autoLockWait || d > autoLockWait+2*time.Second {
		t.Fatalf("waited %v for the store lock, want about %v", d, autoLockWait)
	}
	a.idle(autoIdle)
	if len(a.reboots) != 1 {
		t.Fatal("no restart once the lock was free")
	}
}

// A reboot the guard refused (the web UI's restart or power off is on its
// way) leaves no record behind.
func TestAutoRestartRefusedLeavesNoRecord(t *testing.T) {
	a := newAutoRig(t)
	a.ok = false
	a.idle(autoIdle)
	if len(a.reboots) != 1 || len(loadAuto().Restarts) != 0 {
		t.Fatalf("reboots %q, records %+v", a.reboots, loadAuto())
	}
	a.ok = true
	a.idle(autoIdle)
	if len(a.reboots) != 2 {
		t.Fatal("the refused try held the next one back")
	}
}

// Three auto-restarts a day at most, whatever the sets.
func TestAutoRestartDailyLimit(t *testing.T) {
	a := newAutoRig(t)
	var f autoFile
	for i := range autoPerDay {
		f.Restarts = append(f.Restarts, autoRecord{At: a.clock.Add(-time.Duration(i+1) * time.Hour), Set: "9", Fingerprint: "other", Version: "v"})
	}
	must(t, saveAuto(f))
	a.idle(autoIdle)
	if len(a.reboots) != 0 {
		t.Fatalf("restarted past the daily limit: %q", a.reboots)
	}
	a.clock = a.clock.Add(22 * time.Hour) // the oldest is a day old
	a.idle(autoIdle)
	if len(a.reboots) != 1 {
		t.Fatal("no restart once the oldest record aged out")
	}
}

// Nothing pending, or no auto-restart wired: nothing happens.
func TestAutoRestartNothingToDo(t *testing.T) {
	r := newRig(t)
	reboots := 0
	r.s.cc.auto.uptime = func() (time.Duration, error) { return time.Hour, nil }
	r.s.IdleTick(t.Context(), IdleTick{Idle: time.Hour, ShutdownIn: -1}) // not wired
	r.s.SetAutoRestart(func(context.Context, string) (bool, error) { reboots++; return true, nil }, nil)
	r.s.IdleTick(t.Context(), IdleTick{Idle: time.Hour, ShutdownIn: -1}) // nothing pending
	if reboots != 0 || exists(config.ExtAutoRestartPath()) {
		t.Fatalf("%d reboots", reboots)
	}
}

func TestReadUptime(t *testing.T) {
	saved := uptimePath
	t.Cleanup(func() { uptimePath = saved })
	uptimePath = t.TempDir() + "/uptime"
	must(t, os.WriteFile(uptimePath, []byte("350.42 1200.10\n"), 0o644))
	if d, err := readUptime(); err != nil || d != 350420*time.Millisecond {
		t.Fatalf("uptime = %v, %v", d, err)
	}
	must(t, os.WriteFile(uptimePath, []byte(""), 0o644))
	if _, err := readUptime(); err == nil {
		t.Fatal("empty file read")
	}
}
