package extensions

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// running is a store whose enabled set {proton} booted whole.
func (e *env) running(proton image) *store.Set {
	e.t.Helper()
	e.seal(proton)
	var enabled *store.Set
	locked(e.t, func() (err error) { enabled, err = store.WriteEnabled([]string{"proton"}, nil); return err })
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: enabled.Name, Mounted: mountedAs(proton)})
	return enabled
}

// A slot file that cannot be written is tried again by a later pass.
func TestPassRetriesTheBootedSlotFile(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	e.catalog(proton)
	e.running(proton)
	writeFile(t, config.ExtSlotsDir(), "not a directory")

	s, b := e.service()
	if retry := s.pass(t.Context(), b); !retry || b.slotDone {
		t.Fatalf("retry %v, slot done %v with the slot file unwritable", retry, b.slotDone)
	}
	must(t, os.Remove(config.ExtSlotsDir()))
	if retry := s.pass(t.Context(), b); retry || !b.slotDone {
		t.Fatalf("retry %v, slot done %v", retry, b.slotDone)
	}
	if sl, err := store.ReadSlot("a"); err != nil || sl == nil || sl.Version != bootedVersion {
		t.Fatalf("slot a = %+v, %v", sl, err)
	}
}

// An image that does not fit, before or during its download, is not
// downloaded again (nor by digest) until more space is free or someone
// asks; the card says why.
func TestPassWaitsForFreeSpace(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(f *fakeSealer)
	}{
		{"no room for it", func(f *fakeSealer) { f.before = fmt.Errorf("%w: 1 GiB free", store.ErrNoSpace) }},
		{"disk full while writing", func(f *fakeSealer) { f.writeErr = syscall.ENOSPC }},
		{"disk full while sealing", func(f *fakeSealer) { f.err = fmt.Errorf("enable fs-verity: %w", syscall.ENOSPC) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			proton := newImage(t, "proton", "", 5000, true)
			e.catalog(proton)
			e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoSet})
			src := &fakeSource{byName: map[string]served{"ext-proton.raw": {data: proton.data}},
				blobs: map[string]served{proton.entry.SHA256: {data: proton.data}}}
			useSource(t, src)
			e.sealer.set(func(f *fakeSealer) { f.free = 100; c.set(f) })

			s, b := e.service()
			if retry := s.pass(t.Context(), b); !retry {
				t.Fatal("no retry")
			}
			x := e.state(s, "proton")
			if x.State != StateNeedsAttention || x.Error != noSpaceText {
				t.Fatalf("proton = %+v", x)
			}
			attempts := func() int {
				var n int
				e.sealer.set(func(f *fakeSealer) { n = f.attempts })
				return n
			}
			if attempts() != 1 || slices.ContainsFunc(src.asked(), func(c string) bool { return strings.HasPrefix(c, "blob") }) {
				t.Fatalf("%d attempts, asked %q: once, never by digest", attempts(), src.asked())
			}
			if retry := s.pass(t.Context(), b); !retry || attempts() != 1 {
				t.Fatalf("retry %v, %d attempts with no more space free", retry, attempts())
			}
			s.retryAll() // Reconcile
			s.pass(t.Context(), b)
			if attempts() != 2 {
				t.Fatalf("%d attempts after Reconcile", attempts())
			}
			e.sealer.set(func(f *fakeSealer) { f.free, f.before, f.writeErr, f.err = 100<<30, nil, nil, nil })
			if retry := s.pass(t.Context(), b); retry {
				t.Fatal("retry with room")
			}
			if x := e.state(s, "proton"); x.State != StateRestartNeeded || x.Error != "" {
				t.Fatalf("proton = %+v", x)
			}
		})
	}
}

// The pass before the downloads leaves a pending set alone whose image is
// still to come: the set it would propose after them is the same one.
func TestPassKeepsAPendingWhoseImageIsComing(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	e.catalog(proton, cc)
	e.running(proton)
	e.serve(cc)
	var pending *store.Set
	locked(t, func() (err error) {
		if err = store.WriteWanted([]string{"coolercontrol"}); err != nil {
			return err
		}
		pending, err = store.Propose([]string{"proton", "coolercontrol"}, nil)
		return err
	})

	s, b := e.service()
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("retry")
	}
	p, err := store.Pending()
	must(t, err)
	if p == nil || p.Name != pending.Name || p.Tries != store.ProposeTries || !sealed(cc.entry) {
		t.Fatalf("pending = %+v (was %s), coolercontrol sealed %v", p, pending.Name, sealed(cc.entry))
	}
}

// A trial `vos health` passed but could not record is promoted by vosd;
// until health has spoken, a later pass looks again.
func TestPassPromotesATrialHealthPassed(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	cat := e.catalog(proton, cc)
	e.seal(proton)
	e.seal(cc)
	var enabled, pending *store.Set
	locked(t, func() (err error) {
		if err = store.WriteWanted([]string{"coolercontrol"}); err != nil {
			return err
		}
		if enabled, err = store.WriteEnabled([]string{"proton"}, nil); err != nil {
			return err
		}
		pending, err = store.Propose([]string{"proton", "coolercontrol"}, nil)
		return err
	})
	e.report(store.BootReport{Mode: store.ModePending, Set: pending.Name, TriesLeft: 1, Mounted: mountedAs(proton, cc)})

	s, b := e.service()
	if retry := s.pass(t.Context(), b); !retry {
		t.Fatal("no retry while health is still to come")
	}
	if p, _ := store.Pending(); p == nil || p.Name != pending.Name {
		t.Fatalf("pending = %+v", p)
	}
	if en, _ := store.Enabled(); en.Name != enabled.Name {
		t.Fatalf("enabled = %+v before health", en)
	}

	must(t, store.WriteTrialOK(pending.Name))
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("retry after the promotion")
	}
	if p, _ := store.Pending(); p != nil {
		t.Fatalf("pending = %+v", p)
	}
	if en, _ := store.Enabled(); en == nil || en.Name != pending.Name {
		t.Fatalf("enabled = %+v, want set %s", en, pending.Name)
	}
	proven, err := store.Proven()
	must(t, err)
	for _, p := range store.Pairs(cat, []string{"proton", "coolercontrol"}) {
		if !proven[p] {
			t.Errorf("%v not proven", p)
		}
	}
}

// A first boot's trial of core while wanted holds more: the trial stays
// pending for `vos health` to promote, so the box has a set to fall back
// on, and the pass after the promotion proposes the rest.
func TestPassKeepsATrialTheDesiredSetAddsTo(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	e.catalog(proton, cc)
	e.seal(proton)
	e.seal(cc)
	var trial *store.Set
	locked(t, func() (err error) {
		if err = store.WriteWanted([]string{"coolercontrol"}); err != nil {
			return err
		}
		trial, err = store.Propose([]string{"proton"}, nil)
		return err
	})
	e.report(store.BootReport{Mode: store.ModePending, Set: trial.Name, TriesLeft: 1, Mounted: mountedAs(proton)})

	s, b := e.service()
	if retry := s.pass(t.Context(), b); !retry {
		t.Fatal("no retry while health is still to come")
	}
	if p, _ := store.Pending(); p == nil || p.Name != trial.Name {
		t.Fatalf("pending = %+v, want the trial %s", p, trial.Name)
	}
	if en, _ := store.Enabled(); en != nil {
		t.Fatalf("enabled = %+v before health", en)
	}

	must(t, store.WriteTrialOK(trial.Name)) // health passed it but could not record it
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("retry after the promotion")
	}
	if en, _ := store.Enabled(); en == nil || en.Name != trial.Name {
		t.Fatalf("enabled = %+v, want the trial %s", en, trial.Name)
	}
	p, err := store.Pending()
	must(t, err)
	if p == nil || p.Name == trial.Name || !slices.Equal(p.IDs, []string{"proton", "coolercontrol"}) || p.Tries != store.ProposeTries {
		t.Fatalf("pending = %+v, want a new set of proton and coolercontrol", p)
	}
	if x := e.state(s, "coolercontrol"); x.State != StateRestartNeeded {
		t.Fatalf("coolercontrol = %+v", x)
	}
}

// Recording a passed trial's images as proven is best effort: the pass
// promotes it and goes on. A promotion that fails stops the pass.
func TestPassPromotesWhateverProvenDoes(t *testing.T) {
	setup := func(t *testing.T) (*env, *store.Set) {
		e := newEnv(t)
		proton := newImage(t, "proton", "", 5000, true)
		cc := newImage(t, "coolercontrol", "", 2000, false)
		e.catalog(proton, cc)
		e.seal(proton)
		e.seal(cc)
		var trial *store.Set
		locked(t, func() (err error) {
			if err = store.WriteWanted([]string{"coolercontrol"}); err != nil {
				return err
			}
			trial, err = store.Propose([]string{"proton"}, nil)
			return err
		})
		e.report(store.BootReport{Mode: store.ModePending, Set: trial.Name, TriesLeft: 1, Mounted: mountedAs(proton)})
		must(t, store.WriteTrialOK(trial.Name))
		return e, trial
	}

	e, trial := setup(t)
	must(t, os.MkdirAll(config.ExtProvenPath(), 0o755)) // cannot be written
	s, b := e.service()
	s.pass(t.Context(), b)
	if en, _ := store.Enabled(); en == nil || en.Name != trial.Name {
		t.Fatalf("enabled = %+v, want the trial %s", en, trial.Name)
	}
	if p, _ := store.Pending(); p == nil || !slices.Equal(p.IDs, []string{"proton", "coolercontrol"}) {
		t.Fatalf("pending = %+v: the pass stopped after the promotion", p)
	}
	if st := s.Status(); st.Error != "" {
		t.Fatalf("status error %q", st.Error)
	}

	e, trial = setup(t)
	writeFile(t, filepath.Join(config.ExtEnabledLink(), "x"), "") // enabled cannot be replaced
	s, b = e.service()
	if retry := s.pass(t.Context(), b); !retry {
		t.Fatal("no retry after a promotion that failed")
	}
	if p, _ := store.Pending(); p == nil || p.Name != trial.Name {
		t.Fatalf("pending = %+v, want the trial %s still", p, trial.Name)
	}
	if st := s.Status(); !strings.Contains(st.Error, "promoting set "+trial.Name) {
		t.Fatalf("status error %q", st.Error)
	}
}

// A damaged image's card says so until its image is sealed again, also
// when that happens outside the booted version's downloads (fetched for
// the other slot, or by `vos ext fetch`).
func TestPruneErrsKeepsDamagedUntilSealed(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cat := e.catalog(proton)
	s, _ := e.service()
	s.errs["proton"] = damagedText
	p := store.Plan{Want: []string{"proton"}}
	s.pruneErrs(cat, p)
	if s.errs["proton"] != damagedText {
		t.Fatal("damaged text gone while the image is not sealed")
	}
	e.seal(proton)
	s.pruneErrs(cat, p)
	if text, ok := s.errs["proton"]; ok {
		t.Fatalf("error %q once the image is sealed again", text)
	}
	s.errs["proton"] = damagedText
	s.pruneErrs(cat, store.Plan{})
	if text, ok := s.errs["proton"]; ok {
		t.Fatalf("error %q once it is no longer wanted", text)
	}
}

// A set the user changed since its trial booted is not made the fallback.
func TestPassDoesNotPromoteASetNoLongerWanted(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	e.catalog(proton, cc)
	e.seal(proton)
	e.seal(cc)
	var enabled, pending *store.Set
	locked(t, func() (err error) {
		if enabled, err = store.WriteEnabled([]string{"proton"}, nil); err != nil {
			return err
		}
		pending, err = store.Propose([]string{"proton", "coolercontrol"}, nil)
		return err
	})
	e.report(store.BootReport{Mode: store.ModePending, Set: pending.Name, TriesLeft: 1, Mounted: mountedAs(proton, cc)})
	must(t, store.WriteTrialOK(pending.Name)) // wanted no longer lists coolercontrol

	s, b := e.service()
	s.pass(t.Context(), b)
	if en, _ := store.Enabled(); en == nil || en.Name != enabled.Name {
		t.Fatalf("enabled = %+v, want set %s", en, enabled.Name)
	}
	if p, _ := store.Pending(); p != nil && p.Name == pending.Name {
		t.Fatalf("the trial set is still pending: %+v", p)
	}
}

// A source that cannot be reached is asked once per pass: not for each
// image, nor again for the other slot's.
func TestPassProbesTheSourceOnce(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	oldProton := newImage(t, "proton", "old", 4000, true)
	e.catalog(proton, cc)
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })
	must(t, store.WriteSlot("b", otherVersion, map[string]manifest.Extension{"proton": {
		Name: "ext-proton.raw", Size: oldProton.entry.Size, SHA256: oldProton.entry.SHA256,
		FSVerity: oldProton.entry.FSVerity, Core: true}}))
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoSet})
	src := &fakeSource{manifestErr: fmt.Errorf("manifest.json: %w", gaveUp(errRefused))}
	useSource(t, src)

	s, b := e.service()
	if retry := s.pass(t.Context(), b); !retry {
		t.Fatal("no retry")
	}
	if got := src.asked(); !slices.Equal(got, []string{"manifest"}) {
		t.Fatalf("asked %q", got)
	}
	for _, id := range []string{"proton", "coolercontrol"} {
		if x := e.state(s, id); x.State != StateNeedsAttention || !strings.Contains(x.Error, "cannot be reached") {
			t.Errorf("%s = %+v", id, x)
		}
	}

	// It answers the probe, then stops answering: the next image waits.
	src = &fakeSource{byName: map[string]served{"ext-proton.raw": {data: proton.data[:1000], err: io.ErrUnexpectedEOF}}}
	useSource(t, src)
	s.pass(t.Context(), b)
	if got := src.asked(); !slices.Equal(got, []string{"manifest", "name ext-proton.raw"}) {
		t.Fatalf("asked %q", got)
	}
	if x := e.state(s, "coolercontrol"); !strings.Contains(x.Error, "stopped answering") {
		t.Fatalf("coolercontrol = %+v", x)
	}
}

// A short file in a directory source is that image's problem: the source
// still serves the others.
func TestPassShortFileInADirectory(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	e.catalog(proton, cc)
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoSet})
	writeFile(t, filepath.Join(e.src, "ext-proton.raw"), string(proton.data[:1000]))
	e.serve(cc)

	s, b := e.service()
	s.pass(t.Context(), b)
	if sealed(proton.entry) || !sealed(cc.entry) {
		t.Fatalf("proton sealed %v, coolercontrol sealed %v", sealed(proton.entry), sealed(cc.entry))
	}
	if s.down != nil {
		t.Fatalf("source marked down: %v", s.down)
	}
	if x := e.state(s, "proton"); x.State != StateNeedsAttention || strings.Contains(x.Error, "answering") {
		t.Fatalf("proton = %+v", x)
	}
}

// Why an image was missing is forgotten once it no longer is wanted.
func TestPassForgetsOldErrors(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	e.catalog(proton, cc)
	e.running(proton)
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })

	s, b := e.service()
	s.pass(t.Context(), b)
	if x := e.state(s, "coolercontrol"); x.State != StateNeedsAttention || x.Error == "" {
		t.Fatalf("coolercontrol = %+v", x)
	}
	locked(t, func() error { return store.WriteWanted(nil) })
	s.pass(t.Context(), b)
	if x := e.state(s, "coolercontrol"); x.State != StateNotInstalled || x.Error != "" {
		t.Fatalf("coolercontrol = %+v", x)
	}
}

// Run's Reconcile lets a source that served other bytes be asked again.
func TestRunReconcileRetriesBadBytes(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	e.catalog(proton)
	e.serve(newImage(t, "proton", "other", 5000, true))
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoSet})
	rb := retryBase
	t.Cleanup(func() { retryBase = rb })
	retryBase = time.Hour // only Reconcile brings another pass

	s, _ := e.service()
	stop := run(t, s)
	defer stop()
	waitFor(t, func() bool { x, _ := find(s, "proton"); return strings.Contains(x.Error, "checksum") })
	e.serve(proton)
	s.Reconcile()
	waitFor(t, func() bool { x, _ := find(s, "proton"); return x.State == StateRestartNeeded })
}

// While the mounted images are re-read, Reconcile still brings a pass.
func TestRunAnswersDuringTheReread(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	e.catalog(proton, cc)
	e.running(proton)
	e.serve(cc)
	reading, release := make(chan struct{}), make(chan struct{})
	o := openImage
	t.Cleanup(func() { openImage = o })
	openImage = func(path string) (io.ReadCloser, error) {
		close(reading)
		<-release
		return os.Open(path)
	}

	s, _ := e.service()
	stop := run(t, s)
	defer stop()
	defer close(release)
	<-reading
	if busy, _ := s.Busy(); !busy {
		t.Fatal("not busy during the re-read")
	}
	locked(t, func() error { return store.WriteWanted([]string{"coolercontrol"}) })
	s.Reconcile()
	waitFor(t, func() bool { x, _ := find(s, "coolercontrol"); return x.State == StateRestartNeeded })
}

// run starts s.Run and returns what stops it.
func run(t *testing.T, s *Service) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return")
		}
	}
}
