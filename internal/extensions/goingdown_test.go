package extensions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// downRig is a box booted from slot a with an ESP in a temp dir, whose
// gamescope stop, prepare, display hold and restart only record that they
// ran.
type downRig struct {
	t     *testing.T
	s     *Service
	esp   string
	mu    sync.Mutex
	calls []string
}

func newDownRig(t *testing.T) *downRig {
	t.Helper()
	e := newEnv(t)
	r := &downRig{t: t, s: NewService(e.cfg), esp: t.TempDir()}
	esp, read, stop, unwrap := config.ESP, readBootEntries, stopGamescope, unwrapAsGamer
	waits := []*time.Duration{&downReadWait, &downHoldWait, &downStopWait, &downPrepareWait, &downHoldFor}
	saved := make([]time.Duration, len(waits))
	for i, w := range waits {
		saved[i] = *w
	}
	t.Cleanup(func() {
		goingDown.Store(false) // a restart that went ahead leaves it set
		config.ESP, readBootEntries, stopGamescope, unwrapAsGamer = esp, read, stop, unwrap
		for i, w := range waits {
			*w = saved[i]
		}
	})
	config.ESP = r.esp
	must(t, os.MkdirAll(filepath.Join(r.esp, "loader", "entries"), 0o755))
	stopGamescope = func(context.Context) error { r.record("stop"); return nil }
	unwrapAsGamer = func(context.Context) (string, error) {
		r.record("prepare --unwrap")
		return "vos steam: prepare: done (--unwrap); changed config/config.vdf", nil
	}
	r.s.SetDisplayHold(func(context.Context) (func(), error) {
		r.record("hold")
		return func() { r.record("release") }, nil
	})
	return r
}

func (r *downRig) record(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *downRig) taken() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

// entries makes the ESP hold exactly these entries: file name, slot.
func (r *downRig) entries(nameSlot ...string) {
	r.t.Helper()
	dir := filepath.Join(r.esp, "loader", "entries")
	old, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
	for _, f := range old {
		must(r.t, os.Remove(f))
	}
	for i := 0; i < len(nameSlot); i += 2 {
		version, _, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(nameSlot[i], "vos-"), ".conf"), "+")
		writeFile(r.t, filepath.Join(dir, nameSlot[i]),
			fmt.Sprintf("title VaporOS\nversion %s\nsort-key vapor\noptions vos.slot=%s quiet\n", version, nameSlot[i+1]))
	}
}

// restart runs GoingDown with a restart that records itself and fails with
// err.
func (r *downRig) restart(err error) error {
	return r.s.GoingDown(func() error { r.record("reboot"); return err })
}

func slotFile(t *testing.T, version string, withExtensions bool) {
	t.Helper()
	exts := map[string]manifest.Extension{}
	if withExtensions {
		exts["proton"] = manifest.Extension{Name: "ext-proton.raw", Size: 100, SHA256: strings.Repeat("a", 64), FSVerity: strings.Repeat("b", 64), Core: true}
	}
	locked(t, func() error { return store.WriteSlot("b", version, exts) })
}

const (
	newerVersion = "20261001.000000"
	olderVersion = "20260701.000000"
)

var goingBackCalls = []string{"hold", "stop", "prepare --unwrap", "reboot"}

// A rollback waiting for the restart to an image built before extensions
// (no slots/b.json for its version, or one that lists none): Steam is
// stopped and unwrapped, in that order, before the restart, and the
// display stays held once it was asked for.
func TestGoingDownBackToAnImageWithoutExtensions(t *testing.T) {
	for _, c := range []struct {
		what string
		slot func(t *testing.T)
	}{
		{"no slot file", func(*testing.T) {}},
		{"a slot file without extensions", func(t *testing.T) { slotFile(t, otherVersion, false) }},
		{"a slot file for another version", func(t *testing.T) { slotFile(t, olderVersion, true) }},
	} {
		t.Run(c.what, func(t *testing.T) {
			r := newDownRig(t)
			l := captureLogs(t)
			c.slot(t)
			r.entries("vos-"+bootedVersion+"+0-1.conf", "a", "vos-"+otherVersion+".conf", "b")
			if err := r.restart(nil); err != nil {
				t.Fatal(err)
			}
			if got := r.taken(); !slices.Equal(got, goingBackCalls) {
				t.Fatalf("calls %q, want %q", got, goingBackCalls)
			}
			for _, want := range []string{
				"extensions: the next start boots VaporOS " + otherVersion + " (slot b), built before extensions",
				"extensions: stopped vos-gamescope.service",
				"extensions: vos steam prepare as vapor: vos steam: prepare: done (--unwrap)",
			} {
				if l.count(want) != 1 {
					t.Errorf("log lacks %q:\n%s", want, l.buf.String())
				}
			}
		})
	}
}

// Nothing extra when the next start boots this slot again (also on the
// first boot after the update to it, a counted try with tries left, or
// with no other entry), or another VaporOS that has the dispatcher: a
// staged update, or a rollback to an image built with extensions.
func TestGoingDownLeavesSteamAlone(t *testing.T) {
	for _, c := range []struct {
		what    string
		entries []string
		withExt bool
		slotVer string
	}{
		{"first boot after the update", []string{"vos-" + bootedVersion + "+2-1.conf", "a", "vos-" + otherVersion + ".conf", "b"}, false, ""},
		{"blessed", []string{"vos-" + bootedVersion + ".conf", "a", "vos-" + otherVersion + ".conf", "b"}, false, ""},
		{"no other slot", []string{"vos-" + bootedVersion + ".conf", "a"}, false, ""},
		{"staged update", []string{"vos-" + bootedVersion + ".conf", "a", "vos-" + newerVersion + "+3.conf", "b"}, true, newerVersion},
		{"rollback with extensions", []string{"vos-" + bootedVersion + "+0-1.conf", "a", "vos-" + otherVersion + ".conf", "b"}, true, otherVersion},
	} {
		t.Run(c.what, func(t *testing.T) {
			r := newDownRig(t)
			if c.slotVer != "" {
				slotFile(t, c.slotVer, c.withExt)
			}
			r.entries(c.entries...)
			if err := r.restart(nil); err != nil {
				t.Fatal(err)
			}
			if got := r.taken(); !slices.Equal(got, []string{"reboot"}) {
				t.Fatalf("calls %q", got)
			}
		})
	}

	r := newDownRig(t)
	writeFile(t, config.ProcCmdline, "quiet\n")
	r.entries("vos-"+bootedVersion+"+0-1.conf", "a", "vos-"+otherVersion+".conf", "b")
	if err := r.restart(nil); err != nil {
		t.Fatal(err)
	}
	if got := r.taken(); !slices.Equal(got, []string{"reboot"}) {
		t.Fatalf("outside a slot: calls %q", got)
	}
}

// The ESP is read at every restart, never from an earlier reading: a
// rollback asked for after one restart failed counts at the next.
func TestGoingDownReadsTheESPEachTime(t *testing.T) {
	r := newDownRig(t)
	r.entries("vos-"+bootedVersion+"+2-1.conf", "a", "vos-"+otherVersion+".conf", "b")
	boom := errors.New("boom")
	if err := r.restart(boom); err != boom {
		t.Fatalf("restart returned %v", err)
	}
	if got := r.taken(); !slices.Equal(got, []string{"reboot"}) {
		t.Fatalf("calls %q", got)
	}
	must(t, boot.MarkBad(r.esp, "a"))
	if err := r.restart(boom); err != boom {
		t.Fatalf("restart returned %v", err)
	}
	// A restart that fails lets the display go at once.
	if got, want := r.taken(), append(slices.Clone(goingBackCalls), "release"); !slices.Equal(got, want) {
		t.Fatalf("calls %q, want %q", got, want)
	}
}

// The display is let go downHoldFor after the restart was asked for,
// should the PC still run.
func TestGoingDownLetsTheDisplayGoLater(t *testing.T) {
	r := newDownRig(t)
	downHoldFor = 20 * time.Millisecond
	r.entries("vos-"+bootedVersion+"+0-1.conf", "a", "vos-"+otherVersion+".conf", "b")
	must(t, r.restart(nil))
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(r.taken(), "release") {
		if time.Now().After(deadline) {
			t.Fatal("the display was never let go")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A stop or prepare that fails, or one that hangs until its bound, a
// display that never lets go and an ESP that never answers never keep the
// restart from happening, within the bounds.
func TestGoingDownRestartsWhateverHappens(t *testing.T) {
	r := newDownRig(t)
	l := captureLogs(t)
	r.entries("vos-"+bootedVersion+"+0-1.conf", "a", "vos-"+otherVersion+".conf", "b")
	stopGamescope = func(context.Context) error {
		r.record("stop")
		return errors.New("systemctl --user -M vapor@ stop vos-gamescope.service: exit status 1: Failed")
	}
	unwrapAsGamer = func(context.Context) (string, error) {
		r.record("prepare --unwrap")
		return "", fmt.Errorf("runuser: %w: ", errors.New("exit status 1"))
	}
	must(t, r.restart(nil))
	if got := r.taken(); !slices.Equal(got, goingBackCalls) {
		t.Fatalf("failing steps: calls %q", got)
	}
	if l.count("extensions: stopping vos-gamescope.service: systemctl") != 1 ||
		l.count("extensions: vos steam prepare as vapor: exit status 1") != 1 {
		t.Errorf("log:\n%s", l.buf.String())
	}

	downHoldWait, downStopWait, downPrepareWait = 20*time.Millisecond, 20*time.Millisecond, 20*time.Millisecond
	hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	r.s.SetDisplayHold(func(ctx context.Context) (func(), error) { r.record("hold"); return nil, hang(ctx) })
	stopGamescope = func(ctx context.Context) error { r.record("stop"); return hang(ctx) }
	unwrapAsGamer = func(ctx context.Context) (string, error) { r.record("prepare --unwrap"); return "", hang(ctx) }
	start := time.Now()
	must(t, r.restart(nil))
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the restart waited %v", took)
	}
	if got := r.taken(); !slices.Equal(got, goingBackCalls) {
		t.Fatalf("hanging steps: calls %q", got)
	}

	downReadWait = 20 * time.Millisecond
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	readBootEntries = func() ([]boot.Entry, error) { <-never; return nil, nil }
	start = time.Now()
	must(t, r.restart(nil))
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the restart waited %v for the ESP", took)
	}
	if got := r.taken(); !slices.Equal(got, []string{"reboot"}) {
		t.Fatalf("ESP that never answers: calls %q", got)
	}
	if l.count("the ESP did not answer within 20ms; Steam is left as it is") != 1 {
		t.Errorf("log:\n%s", l.buf.String())
	}
}

// The dispatcher-off runner, still waiting for gamescope's unit to settle
// when a restart into an image without extensions begins, runs no prepare
// after GoingDown's unwrap; and GoingDown waits for one already running.
func TestGoingDownIsTheLastPrepare(t *testing.T) {
	r := newDownRig(t)
	r.entries("vos-"+bootedVersion+"+0-1.conf", "a", "vos-"+otherVersion+".conf", "b")
	states := make(chan string)
	ran := make(chan struct{}, 1)
	u := unwrap{
		state:   func(context.Context) string { return <-states },
		prepare: func(context.Context) (string, error) { r.record("prepare"); ran <- struct{}{}; return "", nil },
		every:   time.Millisecond, limit: time.Minute,
	}
	done := make(chan struct{})
	go func() { u.run(); close(done) }()
	states <- "deactivating"
	if err := r.restart(nil); err != nil {
		t.Fatal(err)
	}
	states <- "inactive"
	<-done
	if got := r.taken(); !slices.Equal(got, goingBackCalls) {
		t.Fatalf("calls %q, want %q (no plain prepare after the unwrap)", got, goingBackCalls)
	}

	// One already running: the unwrap comes after it.
	goingDown.Store(false)
	prepMu.Lock()
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.record("prepare")
		prepMu.Unlock()
	}()
	if err := r.restart(nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"hold", "stop", "prepare", "prepare --unwrap", "reboot"}
	if got := r.taken(); !slices.Equal(got, want) {
		t.Fatalf("calls %q, want %q", got, want)
	}
	select {
	case <-ran:
		t.Fatal("the runner's prepare ran")
	default:
	}
}
