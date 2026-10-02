package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

const newVersion = "20260930.000000"

// checkStaged asserts that img sits in slot b, bootable with 3 tries, with
// the new image's cmdline plus the machine's, and that update-state says so.
func (e *testEnv) checkStaged(img *image) {
	e.t.Helper()
	slot, err := os.ReadFile(e.slotDev("b"))
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
			slotBefore, _ := os.ReadFile(e.slotDev("b"))
			_, err := Stage(context.Background(), e.cfg(e.srcDir(img)), c.opts)
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if c.msg != "" && (err == nil || !strings.Contains(err.Error(), c.msg)) {
				t.Fatalf("got %v, want %q", err, c.msg)
			}
			slotAfter, _ := os.ReadFile(e.slotDev("b"))
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
	if h := e.state().Held; h == nil || h.Version != bootedVersion || h.RollbackIndex != bootedRollback {
		t.Fatalf("held %+v", h)
	}

	// Booted into b, roll forward again: a was marked bad by the first
	// rollback, so it gets fresh tries rather than a clean entry. b, older
	// and running, keeps its clean entry: marked bad it would sort after a
	// failed a among the bad entries, and a would boot forever.
	e.bootSlot("b")
	e.write(config.ImageInfoPath, `{"version":"`+oldIdleVersion+`","rollback_index":50}`)
	if v, err := Rollback(false); err != nil || v != bootedVersion {
		t.Fatalf("second rollback: %q %v", v, err)
	}
	if a := e.entry("a"); a.Name() != "vos-"+bootedVersion+"+3.conf" || !a.Bootable() {
		t.Fatalf("slot a: %s", a.Name())
	}
	if b := e.entry("b"); b.Name() != "vos-"+oldIdleVersion+".conf" {
		t.Fatalf("slot b: %s", b.Name())
	}
	if h := e.state().Held; h != nil {
		t.Fatalf("held after rolling forward: %+v", h)
	}
	// a fails all its tries: systemd-boot comes back to b.
	if got := e.failUntilFallback(3); got.Slot != "b" {
		t.Fatalf("after a failed: boots %s", got.Name())
	}
}

// sdBootNext models systemd-boot's choice (boot_entry_compare): entries
// with tries left first, then the newest version, then fewer tries done.
// It picks one even when every entry is out of tries.
func (e *testEnv) sdBootNext() boot.Entry {
	e.t.Helper()
	es, err := boot.Entries(config.ESP)
	e.must(err)
	if len(es) == 0 {
		e.t.Fatal("no boot entries")
	}
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.Bootable() != b.Bootable() {
			return a.Bootable()
		}
		if c := boot.CompareVersions(a.Version, b.Version); c != 0 {
			return c > 0
		}
		return a.Done < b.Done
	})
	return es[0]
}

// failUntilFallback boots whatever systemd-boot picks and fails it (the
// counter goes down, as systemd-boot renames it before booting), until an
// entry of another slot comes up; it gives up after limit+2 boots.
func (e *testEnv) failUntilFallback(limit int) boot.Entry {
	e.t.Helper()
	first := e.sdBootNext()
	for range limit + 2 {
		next := e.sdBootNext()
		if next.Slot != first.Slot {
			return next
		}
		if !next.Counting {
			e.t.Fatalf("%s has no counter: it would boot forever", next.Name())
		}
		left, done := max(next.Left-1, 0), next.Done+1
		e.must(os.Rename(next.Path, filepath.Join(filepath.Dir(next.Path),
			fmt.Sprintf("%s+%d-%d.conf", next.Base(), left, done))))
	}
	e.t.Fatalf("still booting slot %s after %d failed boots: %s", first.Slot, limit+2, e.sdBootNext().Name())
	return boot.Entry{}
}

// A rollback to a newer staged image (the UI offers it) must leave the
// running entry a fallback.
func TestRollbackToStagedNewer(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	if v, err := Rollback(false); err != nil || v != newVersion {
		t.Fatalf("rollback: %q %v", v, err)
	}
	if a := e.entry("a"); !a.Bootable() {
		t.Fatalf("slot a lost its tries: %s", a.Name())
	}
	if got := e.failUntilFallback(3); got.Slot != "a" || got.Version != bootedVersion {
		t.Fatalf("after the new image failed: boots %s", got.Name())
	}
}

// With the same version in both slots (a forced reinstall), a rollback
// must not rename one slot's entry onto the other's.
func TestRollbackSameVersion(t *testing.T) {
	e := setup(t)
	a, b := e.entry("a"), e.entry("b")
	e.must(os.Rename(a.Path, filepath.Join(filepath.Dir(a.Path), "vos-"+bootedVersion+"+0-1.conf")))
	text, err := os.ReadFile(b.Path)
	e.must(err)
	e.must(os.Remove(b.Path))
	e.write(filepath.Join(filepath.Dir(b.Path), "vos-"+bootedVersion+".conf"),
		strings.ReplaceAll(string(text), oldIdleVersion, bootedVersion))
	e.bootSlot("b")

	if v, err := Rollback(false); err != nil || v != bootedVersion {
		t.Fatalf("rollback: %q %v", v, err)
	}
	if a := e.entry("a"); a == nil || a.Name() != "vos-"+bootedVersion+"+3.conf" {
		t.Fatalf("slot a: %+v", a)
	}
	if b := e.entry("b"); b == nil || b.Name() != "vos-"+bootedVersion+"+0-1.conf" {
		t.Fatalf("slot b: %+v", b)
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
	e.write(config.BootCountVar, "x")
	e.setState(&State{Staged: &Staged{Version: bootedVersion, Slot: "a"}})
	if st, _ := Reconcile(); st.Staged == nil {
		t.Fatalf("on trial: %+v", st)
	}
	// ...or until the fallback boot of the old slot finds it out of tries.
	a := e.entry("a")
	e.must(os.Rename(a.Path, filepath.Join(filepath.Dir(a.Path), "vos-"+bootedVersion+"+0-3.conf")))
	os.Remove(config.BootCountVar)
	e.bootSlot("b")
	e.write(config.ImageInfoPath, `{"version":"`+oldIdleVersion+`","rollback_index":50}`)
	if st, _ := Reconcile(); st.Staged != nil || !st.HasFailed(bootedVersion) {
		t.Fatalf("after the fallback: %+v", st)
	}
}

// testExt is an extension image for makeImageExt.
type testExt struct {
	id       string
	size     int
	core     bool
	requires []string
}

// makeImageExt is makeImage with extension images: ext-<id>.raw files and
// their entries in the signed manifest.
func (e *testEnv) makeImageExt(version string, rollback int64, rootSize int, exts ...testExt) *image {
	e.t.Helper()
	files := map[string][]byte{}
	img := e.makeImage(version, rollback, rootSize, func(m *manifest.Manifest) {
		m.Extensions = map[string]manifest.Extension{}
		for i, x := range exts {
			b := bytes.Repeat([]byte{byte(i + 1), byte(rollback)}, x.size/2)
			name := manifest.ExtensionFile(x.id)
			files[name] = b
			m.Extensions[x.id] = manifest.Extension{Name: name, Size: int64(len(b)), SHA256: strings.TrimPrefix(sha(b), "sha256:"),
				FSVerity: fmt.Sprintf("%064x", i+1), Core: x.core, Requires: x.requires}
		}
	})
	maps.Copy(img.files, files)
	return img
}

// fakeStore stands in for sealing, which needs fs-verity (neither the dev
// Mac nor a tmpfs has it): an image counts as sealed once its file is in
// place with the right size.
type fakeStore struct {
	puts        []string
	free        int64 // what storeFree reports; -1 = unknown
	unsupported bool
	afterPut    func(id string)
}

func (e *testEnv) fakeStore() *fakeStore {
	f := &fakeStore{free: -1}
	oldPut, oldHas, oldFree := putImage, hasImage, storeFree
	e.t.Cleanup(func() { putImage, hasImage, storeFree = oldPut, oldHas, oldFree })
	hasImage = func(c catalog.Entry) (bool, error) {
		fi, err := os.Stat(store.ImagePath(c.SHA256))
		return err == nil && fi.Size() == c.Size, nil
	}
	storeFree = func() (int64, error) { return f.free, nil }
	putImage = func(ctx context.Context, c catalog.Entry, fetch func(io.Writer, func(int64) error) error) error {
		f.puts = append(f.puts, c.ID)
		if ok, _ := hasImage(c); ok {
			return nil
		}
		var buf bytes.Buffer
		if err := fetch(&buf, func(int64) error { return ctx.Err() }); err != nil {
			return err
		}
		if f.unsupported {
			return store.ErrUnsupported
		}
		path := store.ImagePath(c.SHA256)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			return err
		}
		if f.afterPut != nil {
			f.afterPut(c.ID)
		}
		return nil
	}
	return f
}

func (e *testEnv) sealed(img *image, id string) bool {
	x := img.m.Extensions[id]
	fi, err := os.Stat(store.ImagePath(x.SHA256))
	return err == nil && fi.Size() == x.Size
}

// The extensions every test image carries: core proton, cooler and truck,
// both requiring proton.
var testExts = []testExt{
	{id: "proton", size: 40 << 10, core: true},
	{id: "cooler", size: 30 << 10, requires: []string{"proton"}},
	{id: "truck", size: 20 << 10, requires: []string{"proton"}},
}

func TestStageFetchesExtensions(t *testing.T) {
	e := setup(t)
	img := e.makeImageExt(newVersion, 200, 200<<10, testExts...)
	e.write(config.ExtWantedPath(), "cooler\n")
	fs := e.fakeStore()
	var progress []Progress
	opts := Options{Progress: func(p Progress) { progress = append(progress, p) }}
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), opts); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
	// wanted ∪ core with requirements, in catalog order; truck is not wanted.
	if strings.Join(fs.puts, " ") != "proton cooler" {
		t.Fatalf("put %v", fs.puts)
	}
	if !e.sealed(img, "proton") || !e.sealed(img, "cooler") || e.sealed(img, "truck") {
		t.Fatal("the store does not hold exactly proton and cooler")
	}
	// The idle slot's catalog is the new image's, whole.
	slot, err := store.ReadSlot("b")
	if err != nil || slot == nil || slot.Version != newVersion || len(slot.Extensions) != 3 ||
		slot.Extensions["truck"].SHA256 != img.m.Extensions["truck"].SHA256 {
		t.Fatalf("slots/b.json: %+v %v", slot, err)
	}

	// Download counts kernel, initrd and the two images, and shares the
	// first 80% with write by bytes.
	d := bootFilesSize(img.m) + img.m.Extensions["proton"].Size + img.m.Extensions["cooler"].Size
	split := int(80 * d / (d + int64(len(img.root()))))
	last, lastDownload, firstWrite := -1, Progress{}, -1
	for _, p := range progress {
		if p.Percent < last {
			t.Fatalf("progress went back: %+v after %d%%", p, last)
		}
		last = p.Percent
		switch {
		case p.Phase == "download":
			lastDownload = p
		case p.Phase == "write" && firstWrite < 0:
			firstWrite = p.Percent
		}
	}
	if lastDownload.Total != d || lastDownload.Bytes != d || lastDownload.Percent != split {
		t.Fatalf("last download progress %+v, want %d bytes at %d%%", lastDownload, d, split)
	}
	if firstWrite != split || last != 100 {
		t.Fatalf("write starts at %d%%, ends at %d%%; want %d and 100", firstWrite, last, split)
	}
}

// Images the store already holds are neither fetched nor counted.
func TestStageExtensionsAlreadySealed(t *testing.T) {
	e := setup(t)
	img := e.makeImageExt(newVersion, 200, 100<<10, testExts...)
	fs := e.fakeStore()
	x := img.m.Extensions["proton"]
	e.write(store.ImagePath(x.SHA256), string(img.files[x.Name]))

	res, err := Check(context.Background(), e.cfg(e.srcDir(img)), Options{})
	if err != nil || res.Available == nil || res.Available.Size != int64(len(img.root())) {
		t.Fatalf("check: %+v %v", res, err)
	}
	e.write(config.ExtWantedPath(), "cooler\ntruck\n")
	res, err = Check(context.Background(), e.cfg(e.srcDir(img)), Options{})
	want := int64(len(img.root())) + img.m.Extensions["cooler"].Size + img.m.Extensions["truck"].Size
	if err != nil || res.Available == nil || res.Available.Size != want {
		t.Fatalf("check with cooler and truck wanted: %+v %v, want size %d", res.Available, err, want)
	}

	var total int64
	opts := Options{Progress: func(p Progress) {
		if p.Phase == "download" {
			total = p.Total
		}
	}}
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), opts); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fs.puts, " ") != "cooler truck" {
		t.Fatalf("put %v", fs.puts)
	}
	if want := bootFilesSize(img.m) + img.m.Extensions["cooler"].Size + img.m.Extensions["truck"].Size; total != want {
		t.Fatalf("download total %d, want %d", total, want)
	}
}

// Failures before the idle slot is unhooked leave it as it was.
func TestStageExtensionFailures(t *testing.T) {
	cases := map[string]struct {
		prepare func(img *image, fs *fakeStore, src string)
		msg     string
	}{
		"no space": {msg: "not enough free space", prepare: func(img *image, fs *fakeStore, src string) {
			fs.free = 2<<30 + 30<<10 // less than the proton image plus the reserve
		}},
		"missing from the source": {msg: "ext-proton.raw", prepare: func(img *image, fs *fakeStore, src string) {
			os.Remove(filepath.Join(src, "ext-proton.raw"))
		}},
		"damaged in the source": {msg: "checksum", prepare: func(img *image, fs *fakeStore, src string) {
			b := slices.Clone(img.files["ext-proton.raw"])
			b[100] ^= 0xff
			os.WriteFile(filepath.Join(src, "ext-proton.raw"), b, 0o644)
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			img := e.makeImageExt(newVersion, 200, 100<<10, testExts...)
			fs := e.fakeStore()
			src := e.srcDir(img)
			c.prepare(img, fs, src)
			_, err := Stage(context.Background(), e.cfg(src), Options{})
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("got %v, want %q", err, c.msg)
			}
			if b := e.entry("b"); b == nil || b.Version != oldIdleVersion {
				t.Fatalf("slot b entry: %+v", b)
			}
			if slot, _ := store.ReadSlot("b"); slot != nil {
				t.Fatalf("slots/b.json written: %+v", slot)
			}
			if st := e.state(); st.LastError == "" || st.Staged != nil {
				t.Fatalf("state %+v", st)
			}
		})
	}
}

// A data partition that cannot seal does not hold back the OS update.
func TestStageExtensionsUnsupported(t *testing.T) {
	e := setup(t)
	img := e.makeImageExt(newVersion, 200, 100<<10, testExts...)
	e.write(config.ExtWantedPath(), "cooler\n")
	fs := e.fakeStore()
	fs.unsupported = true
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
	if strings.Join(fs.puts, " ") != "proton" {
		t.Fatalf("kept fetching after the first refusal: %v", fs.puts)
	}
	if slot, err := store.ReadSlot("b"); err != nil || slot == nil || len(slot.Extensions) != 3 {
		t.Fatalf("slots/b.json: %+v %v", slot, err)
	}
}

// On a boot whose data partition got no fs-verity (boot reason no-verity)
// no image could be sealed: none is fetched, counted or reserved space for.
func TestStageNoVerity(t *testing.T) {
	e := setup(t)
	img := e.makeImageExt(newVersion, 200, 100<<10, testExts...)
	e.write(config.ExtWantedPath(), "cooler\n")
	e.write(config.ExtBootPath(), `{"mode":"enabled","reason":"no-set no-verity"}`)
	fs := e.fakeStore()
	fs.free = 1 << 20 // far less than the reserve

	res, err := Check(context.Background(), e.cfg(e.srcDir(img)), Options{})
	if err != nil || res.Available == nil || res.Available.Size != int64(len(img.root())) {
		t.Fatalf("check: %+v %v", res, err)
	}
	var total int64
	opts := Options{Progress: func(p Progress) {
		if p.Phase == "download" {
			total = p.Total
		}
	}}
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), opts); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
	if len(fs.puts) != 0 || total != bootFilesSize(img.m) {
		t.Fatalf("put %v, download total %d", fs.puts, total)
	}
	// The slot file still lists the new image's catalog.
	if slot, err := store.ReadSlot("b"); err != nil || slot == nil || len(slot.Extensions) != 3 {
		t.Fatalf("slots/b.json: %+v %v", slot, err)
	}
}

// An image GC removed before the slot file named it is fetched again once
// the slot file protects it.
func TestStageRefetchesCollectedImage(t *testing.T) {
	e := setup(t)
	img := e.makeImageExt(newVersion, 200, 100<<10, testExts...)
	e.write(config.ExtWantedPath(), "cooler\n")
	fs := e.fakeStore()
	fs.afterPut = func(id string) {
		if id == "cooler" {
			os.Remove(store.ImagePath(img.m.Extensions["proton"].SHA256))
		}
	}
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fs.puts, " ") != "proton cooler proton" || !e.sealed(img, "proton") {
		t.Fatalf("put %v", fs.puts)
	}
}

// A manifest without extensions still records the slot, with none.
func TestStageWritesEmptySlotCatalog(t *testing.T) {
	e := setup(t)
	e.write(filepath.Join(config.ExtSlotsDir(), "b.json"), `{"version":"`+oldIdleVersion+`","extensions":{}}`)
	img := e.makeImage(newVersion, 200, 1000, nil)
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
		t.Fatal(err)
	}
	slot, err := store.ReadSlot("b")
	if err != nil || slot == nil || slot.Version != newVersion || slot.Extensions == nil || len(slot.Extensions) != 0 {
		t.Fatalf("slots/b.json: %+v %v", slot, err)
	}
	var raw map[string]json.RawMessage
	if err := config.ReadJSON(filepath.Join(config.ExtSlotsDir(), "b.json"), &raw); err != nil || string(raw["extensions"]) != "{}" {
		t.Fatalf("slots/b.json: %v %v", raw, err)
	}
}

func TestStageExtensionsFromOCI(t *testing.T) {
	e := setup(t)
	img := e.makeImageExt(newVersion, 200, 100<<10, testExts...)
	f := newFakeRegistry(t, img)
	fs := e.fakeStore()
	if _, err := Stage(context.Background(), e.cfg(f.spec()), Options{}); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
	if strings.Join(fs.puts, " ") != "proton" || !e.sealed(img, "proton") {
		t.Fatalf("put %v", fs.puts)
	}
}

func TestStageSpans(t *testing.T) {
	s := stageSpans(20, 80)
	if s["download"] != [2]int{0, 16} || s["write"] != [2]int{16, 80} || s["verify"] != [2]int{80, 98} {
		t.Fatalf("spans %v", s)
	}
	if s := stageSpans(0, 0); s["download"] != [2]int{0, 0} || s["write"] != [2]int{0, 80} {
		t.Fatalf("spans before the sizes are known: %v", s)
	}
}
