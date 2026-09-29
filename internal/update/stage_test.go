package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

const newVersion = "20260930.000000"

// checkStaged asserts that img sits in slot b, bootable with 3 tries, with
// the new image's cmdline plus the machine's, and that update-state says so.
func (e *testEnv) checkStaged(img *image) {
	e.t.Helper()
	slot, err := os.ReadFile(SlotDevice("b"))
	e.must(err)
	if len(slot) != slotSize {
		e.t.Fatalf("the slot changed size: %d", len(slot))
	}
	if !bytes.Equal(slot[:len(img.root())], img.root()) {
		e.t.Fatal("slot b does not hold the new root")
	}
	en := e.entry("b")
	if en == nil || en.Version != img.m.Version || en.Name() != "vos-"+img.m.Version+"+3.conf" {
		e.t.Fatalf("slot b entry: %+v", en)
	}
	want := "vos.slot=b " + img.m.Cmdline + " " + machineArgs
	if en.Options != want {
		e.t.Fatalf("options %q, want %q", en.Options, want)
	}
	for name, file := range map[string]string{"vmlinuz": "vmlinuz", "initramfs.img": "initramfs.img"} {
		b, err := os.ReadFile(filepath.Join(config.ESP, "vos", img.m.Version, file))
		if err != nil || !bytes.Equal(b, img.files[name]) {
			e.t.Fatalf("%s on the ESP: %q %v", file, b, err)
		}
	}
	// The old slot b kernel went with its entry.
	if _, err := os.Stat(filepath.Join(config.ESP, "vos", oldIdleVersion)); !os.IsNotExist(err) {
		e.t.Fatalf("old kernel dir: %v", err)
	}
	st := e.state()
	if st.Staged == nil || st.Staged.Version != img.m.Version || st.Staged.Slot != "b" || st.Booted != bootedVersion || st.LastError != "" {
		e.t.Fatalf("state %+v", st)
	}
	if tmp, _ := os.ReadDir(WorkDir); len(tmp) != 0 {
		e.t.Fatalf("work dir not cleaned: %v", tmp)
	}
}

func TestStageFromOCI(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 300<<10, nil)
	f := newFakeRegistry(t, img)
	f.failRoot.Store(true) // the root download breaks half way and resumes

	evs, cancel := events.Default.Subscribe()
	defer cancel()
	var progress []Progress
	var accepted string
	res, err := Stage(context.Background(), e.cfg(f.spec()), Options{
		Progress: func(p Progress) { progress = append(progress, p) },
		Accepted: func(m *manifest.Manifest, slot string) { accepted = m.Version + "->" + slot },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot != "b" || accepted != newVersion+"->b" {
		t.Fatalf("result %+v, accepted %q", res, accepted)
	}
	e.checkStaged(img)
	if f.rootBlobReqs.Load() != 2 {
		t.Fatalf("root blob requests: %d", f.rootBlobReqs.Load())
	}

	// Progress climbs through the phases to 100%.
	phases := map[string]bool{}
	last := -1
	for _, p := range progress {
		phases[p.Phase] = true
		if p.Percent < last {
			t.Fatalf("progress went back: %+v after %d%%", p, last)
		}
		last = p.Percent
	}
	for _, ph := range []string{"check", "download", "write", "verify", "install", "done"} {
		if !phases[ph] {
			t.Errorf("no %q progress", ph)
		}
	}
	if last != 100 {
		t.Errorf("progress ended at %d%%", last)
	}
	// update.state went out.
	sawState := false
	for len(evs) > 0 {
		if ev := <-evs; ev.Topic == "update.state" && strings.Contains(string(ev.Data), `"staged":{"version":"`+newVersion) {
			sawState = true
		}
	}
	if !sawState {
		t.Error("no update.state event with the staged version")
	}
	// The newer image boots next on its own: slot a stays untouched.
	if a := e.entry("a"); a == nil || a.Counting || a.Version != bootedVersion {
		t.Fatalf("slot a entry: %+v", a)
	}

	// Staging it again is a no-op.
	_, err = Stage(context.Background(), e.cfg(f.spec()), Options{})
	if !errors.Is(err, ErrAlreadyStaged) {
		t.Fatalf("second stage: %v", err)
	}
}

func TestStageFromDir(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 200<<10, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
}

// Rejections that must leave the machine exactly as it was.
func TestStageRejectsBeforeWriting(t *testing.T) {
	cases := []struct {
		name  string
		build func(e *testEnv) *image
		state *State
		opts  Options
		want  error
		msg   string
	}{
		{name: "bad signature", want: manifest.ErrBadSignature, build: func(e *testEnv) *image {
			img := e.makeImage(newVersion, 200, 1000, nil)
			_, stranger, _ := ed25519.GenerateKey(rand.Reader)
			img.files["manifest.json.sig"] = manifest.Sign(img.files["manifest.json"], stranger)
			return img
		}},
		{name: "tampered manifest", want: manifest.ErrBadSignature, build: func(e *testEnv) *image {
			img := e.makeImage(newVersion, 200, 1000, nil)
			img.files["manifest.json"] = bytes.Replace(img.files["manifest.json"], []byte("panic=10"), []byte("panic=11"), 1)
			return img
		}},
		{name: "downgrade", want: ErrNotNewer, build: func(e *testEnv) *image {
			return e.makeImage("20260815.000000", bootedRollback-1, 1000, nil)
		}},
		{name: "same rollback index", want: ErrNotNewer, build: func(e *testEnv) *image {
			return e.makeImage(newVersion, bootedRollback, 1000, nil)
		}},
		{name: "running version", want: ErrUpToDate, build: func(e *testEnv) *image {
			return e.makeImage(bootedVersion, 200, 1000, nil)
		}},
		{name: "failed before", want: ErrFailedBefore, state: &State{Failed: []string{newVersion}}, build: func(e *testEnv) *image {
			return e.makeImage(newVersion, 200, 1000, nil)
		}},
		{name: "too big for the slot", want: ErrTooBig, build: func(e *testEnv) *image {
			return e.makeImage(newVersion, 200, slotSize+1, nil)
		}},
		{name: "wrong version", msg: "not 20261231.000000", opts: Options{Version: "20261231.000000"}, build: func(e *testEnv) *image {
			return e.makeImage(newVersion, 200, 1000, nil)
		}},
		{name: "needs a newer updater", msg: "updater", build: func(e *testEnv) *image {
			return e.makeImage(newVersion, 200, 1000, func(m *manifest.Manifest) { m.MinUpdater = 2 })
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			if c.state != nil {
				e.setState(c.state)
			}
			img := c.build(e)
			slotBefore, _ := os.ReadFile(SlotDevice("b"))
			_, err := Stage(context.Background(), e.cfg(e.srcDir(img)), c.opts)
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if c.msg != "" && (err == nil || !strings.Contains(err.Error(), c.msg)) {
				t.Fatalf("got %v, want %q", err, c.msg)
			}
			slotAfter, _ := os.ReadFile(SlotDevice("b"))
			if !bytes.Equal(slotBefore, slotAfter) {
				t.Fatal("slot b was written")
			}
			if b := e.entry("b"); b == nil || b.Version != oldIdleVersion {
				t.Fatalf("slot b entry: %+v", b)
			}
			st := e.state()
			if st.Staged != nil {
				t.Fatalf("staged %+v", st.Staged)
			}
			if IsBenign(err) != (st.LastError == "") {
				t.Fatalf("last_error %q for %v", st.LastError, err)
			}
		})
	}
}

func TestStageRejectsWrongChecksum(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 100<<10, nil)
	img.files["root.erofs"] = append([]byte{}, img.root()...)
	img.files["root.erofs"][500] ^= 0xff // same size, different bytes
	_, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("got %v", err)
	}
	// The slot was unhooked before it was written, so the garbage in it
	// can never boot.
	if b := e.entry("b"); b != nil {
		t.Fatalf("slot b still has an entry: %+v", b)
	}
	if st := e.state(); st.Staged != nil || !strings.Contains(st.LastError, "checksum") {
		t.Fatalf("state %+v", st)
	}
	if a := e.entry("a"); a == nil || a.Counting {
		t.Fatalf("slot a entry: %+v", a)
	}
}

func TestStageWrongKernelChecksum(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	img.files["vmlinuz"] = []byte("kernel 20260930.00000X") // same length
	_, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("got %v", err)
	}
	// Caught while downloading, before the idle slot was touched.
	if b := e.entry("b"); b == nil || b.Version != oldIdleVersion {
		t.Fatalf("slot b entry: %+v", b)
	}
}

func TestStageForceAndDowngrade(t *testing.T) {
	e := setup(t)
	e.setState(&State{Failed: []string{newVersion}})
	img := e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{Force: true}); err != nil {
		t.Fatalf("forced: %v", err)
	}
	e.checkStaged(img)

	// A downgrade sorts below the running entry, so the running entry is
	// marked bad to make the staged one boot next.
	e = setup(t)
	old := e.makeImage("20260815.000000", bootedRollback-1, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(old)), Options{AllowDowngrade: true}); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	e.checkStaged(old)
	if a := e.entry("a"); a == nil || a.Bootable() || a.Name() != "vos-"+bootedVersion+"+0-1.conf" {
		t.Fatalf("slot a entry: %+v", a)
	}
}

func TestStagePinnedVersion(t *testing.T) {
	e := setup(t)
	img := e.makeImage("20260815.000000", bootedRollback-1, 1000, nil)
	// Picking a version explicitly (the UI) implies allowing a downgrade.
	opts := Options{Version: "20260815.000000", AllowDowngrade: true}
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), opts); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
}

func TestStageRefusesLiveAndConcurrent(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	src := e.srcDir(img)

	l, err := lockFile(updateLockPath(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), e.cfg(src), Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("while locked: %v", err)
	}
	l.Unlock()

	e.write(config.ProcCmdline, "vos.mode=live vos.label=VOS_LIVE quiet\n")
	if _, err := Stage(context.Background(), e.cfg(src), Options{}); !errors.Is(err, ErrLive) {
		t.Fatalf("live: %v", err)
	}
}

func TestCheck(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	src := e.srcDir(img)
	res, err := Check(context.Background(), e.cfg(src), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Available == nil || res.Available.Version != newVersion || res.Available.Size != 1000 {
		t.Fatalf("result %+v", res)
	}
	st := e.state()
	if st.Available == nil || st.Available.Version != newVersion || st.Checked == "" {
		t.Fatalf("state %+v", st)
	}

	// An older image is offered but not available.
	old := e.makeImage("20260815.000000", 50, 1000, nil)
	res, err = Check(context.Background(), e.cfg(e.srcDir(old)), Options{})
	if err != nil || res.Available != nil || !errors.Is(res.Reason, ErrNotNewer) {
		t.Fatalf("older: %+v %v", res, err)
	}
	if st := e.state(); st.Available != nil {
		t.Fatalf("state still offers %+v", st.Available)
	}

	// An unreachable source is an error, recorded as such.
	if _, err := Check(context.Background(), e.cfg(filepath.Join(e.dir, "missing")), Options{}); err == nil {
		t.Fatal("missing source accepted")
	}
	if st := e.state(); !strings.HasPrefix(st.LastError, "check: ") {
		t.Fatalf("last_error %q", st.LastError)
	}
}

func TestRollback(t *testing.T) {
	e := setup(t)
	v, err := Rollback(false)
	if err != nil || v != oldIdleVersion {
		t.Fatalf("rollback: %q %v", v, err)
	}
	if a := e.entry("a"); a.Name() != "vos-"+bootedVersion+"+0-1.conf" {
		t.Fatalf("slot a: %s", a.Name())
	}
	if b := e.entry("b"); b.Name() != "vos-"+oldIdleVersion+".conf" {
		t.Fatalf("slot b: %s", b.Name())
	}

	// Booted into b, roll back again: a was marked bad by the first
	// rollback, so it gets fresh tries rather than a clean entry.
	e.bootSlot("b")
	e.write(config.ImageInfoPath, `{"version":"`+oldIdleVersion+`","rollback_index":50}`)
	if v, err := Rollback(false); err != nil || v != bootedVersion {
		t.Fatalf("second rollback: %q %v", v, err)
	}
	if a := e.entry("a"); a.Name() != "vos-"+bootedVersion+"+3.conf" || !a.Bootable() {
		t.Fatalf("slot a: %s", a.Name())
	}
	if b := e.entry("b"); b.Name() != "vos-"+oldIdleVersion+"+0-1.conf" {
		t.Fatalf("slot b: %s", b.Name())
	}
}

func TestRollbackRefuses(t *testing.T) {
	e := setup(t)
	e.setState(&State{Failed: []string{oldIdleVersion}})
	if _, err := Rollback(false); !errors.Is(err, ErrFailedBefore) {
		t.Fatalf("failed version: %v", err)
	}
	if a := e.entry("a"); a.Counting {
		t.Fatal("slot a was marked bad anyway")
	}
	if _, err := Rollback(true); err != nil {
		t.Fatalf("forced: %v", err)
	}

	// A slot that used up its tries.
	e = setup(t)
	b := e.entry("b")
	os.Rename(b.Path, filepath.Join(filepath.Dir(b.Path), "vos-"+oldIdleVersion+"+0-3.conf"))
	if _, err := Rollback(false); err == nil {
		t.Fatal("rolled back to a slot that never started")
	}

	// Nothing in the other slot.
	e = setup(t)
	e.must(boot.RemoveSlotEntries(config.ESP, "b"))
	if _, err := Rollback(true); err == nil || !strings.Contains(err.Error(), "nothing bootable") {
		t.Fatalf("empty slot: %v", err)
	}
}

func TestReconcile(t *testing.T) {
	stage := func(e *testEnv, name string) {
		b := e.entry("b")
		e.must(os.Rename(b.Path, filepath.Join(filepath.Dir(b.Path), name)))
		e.setState(&State{Staged: &Staged{Version: oldIdleVersion, Slot: "b", At: "x"}, Failed: []string{}})
	}

	// Out of tries while we run the old slot: it failed.
	e := setup(t)
	stage(e, "vos-"+oldIdleVersion+"+0-3.conf")
	st, err := Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if st.Staged != nil || !st.HasFailed(oldIdleVersion) || st.LastError == "" {
		t.Fatalf("out of tries: %+v", st)
	}

	// Not tried yet (vosd restarted before any reboot): still staged.
	e = setup(t)
	stage(e, "vos-"+oldIdleVersion+"+3.conf")
	if st, _ := Reconcile(); st.Staged == nil || len(st.Failed) != 0 {
		t.Fatalf("untried: %+v", st)
	}

	// The entry is gone: failed.
	e = setup(t)
	stage(e, "vos-"+oldIdleVersion+"+3.conf")
	e.must(boot.RemoveSlotEntries(config.ESP, "b"))
	if st, _ := Reconcile(); st.Staged != nil || !st.HasFailed(oldIdleVersion) {
		t.Fatalf("gone: %+v", st)
	}

	// It booted and is blessed: done.
	e = setup(t)
	e.setState(&State{Staged: &Staged{Version: bootedVersion, Slot: "a"}, Available: &Available{Version: bootedVersion}})
	if st, _ := Reconcile(); st.Staged != nil || st.Available != nil || len(st.Failed) != 0 || st.Booted != bootedVersion {
		t.Fatalf("booted: %+v", st)
	}

	// It booted but is still on trial: vosd starts before `vos health`
	// passes, so staged stays until health clears it...
	e = setup(t)
	e.write(BootCountVar, "x")
	e.setState(&State{Staged: &Staged{Version: bootedVersion, Slot: "a"}})
	if st, _ := Reconcile(); st.Staged == nil {
		t.Fatalf("on trial: %+v", st)
	}
	// ...or until the fallback boot of the old slot finds it out of tries.
	a := e.entry("a")
	e.must(os.Rename(a.Path, filepath.Join(filepath.Dir(a.Path), "vos-"+bootedVersion+"+0-3.conf")))
	os.Remove(BootCountVar)
	e.bootSlot("b")
	e.write(config.ImageInfoPath, `{"version":"`+oldIdleVersion+`","rollback_index":50}`)
	if st, _ := Reconcile(); st.Staged != nil || !st.HasFailed(bootedVersion) {
		t.Fatalf("after the fallback: %+v", st)
	}
}
