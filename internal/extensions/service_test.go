package extensions

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// A fresh install: enabled names the core image, which booted unproven.
// The pass fetches nothing it has, writes the booted slot file and proposes
// the set for a trial.
func TestPassProposesAfterFetching(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	truckers := newImage(t, "truckersmp", "", 3000, false, "proton")
	cat := e.catalog(proton, truckers)
	e.serve(proton)
	var enabled *store.Set
	locked(t, func() (err error) { enabled, err = store.WriteEnabled([]string{"proton"}, nil); return err })
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: enabled.Name,
		Skipped: []store.Skipped{{ID: "proton", Reason: store.SkipUnproven}}})

	s, b := e.service()
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("pass wants a retry with every image fetched")
	}
	if got := e.sealer.putIDs(); !slices.Equal(got, []string{"proton"}) {
		t.Fatalf("fetched %v, want proton only (truckersmp is not wanted)", got)
	}
	p, err := store.Pending()
	must(t, err)
	if p == nil || !slices.Equal(p.IDs, []string{"proton"}) || p.Tries != store.ProposeTries {
		t.Fatalf("pending = %+v", p)
	}
	sl, err := store.ReadSlot("a")
	must(t, err)
	if sl == nil || sl.Version != bootedVersion || !sameExtensions(sl.Extensions, slotExtensions(cat)) {
		t.Fatalf("slot a = %+v", sl)
	}
	if x := sl.Extensions["truckersmp"]; x.Name != manifest.ExtensionFile("truckersmp") || !slices.Equal(x.Requires, []string{"proton"}) {
		t.Fatalf("slot a truckersmp = %+v", x)
	}

	st := s.Status()
	if !st.RestartNeeded || st.Pending != p.Name || st.Version != bootedVersion || st.Busy {
		t.Fatalf("status = %+v", st)
	}
	if x := e.state(s, "proton"); x.State != StateRestartNeeded || !x.Core || x.Skipped != store.SkipUnproven {
		t.Fatalf("proton = %+v", x)
	}
	if x := e.state(s, "truckersmp"); x.State != StateNotInstalled || x.Wanted {
		t.Fatalf("truckersmp = %+v", x)
	}

	// Nothing changed: a second pass writes nothing new.
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("retry")
	}
	if p2, _ := store.Pending(); p2 == nil || p2.Name != p.Name {
		t.Fatalf("pending changed to %+v", p2)
	}
}

// A missing image never shrinks the set, and the failure shows on its card.
func TestPassKeepsWhatBootedWhileAnImageIsMissing(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	e.catalog(proton, cc)
	e.seal(cc)
	var enabled *store.Set
	locked(t, func() (err error) {
		if err = store.WriteWanted([]string{"coolercontrol"}); err != nil {
			return err
		}
		enabled, err = store.WriteEnabled([]string{"proton", "coolercontrol"}, nil)
		return err
	})
	// proton mounted from an image that is gone since; the source lacks it.
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: enabled.Name, Mounted: mountedAs(proton, cc)})

	s, b := e.service()
	if retry := s.pass(t.Context(), b); !retry {
		t.Fatal("no retry while proton is missing")
	}
	if p, _ := store.Pending(); p != nil {
		t.Fatalf("proposed %+v while what booted is still desired", p)
	}
	x := e.state(s, "proton")
	if x.State != StateNeedsAttention || !strings.Contains(x.Error, "ext-proton.raw") || !x.Mounted {
		t.Fatalf("proton = %+v", x)
	}
	if x := e.state(s, "coolercontrol"); x.State != StateInstalled || !x.Wanted {
		t.Fatalf("coolercontrol = %+v", x)
	}

	// Once the source has it, the next pass fetches it and clears the error.
	e.serve(proton)
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("retry after fetching")
	}
	if x := e.state(s, "proton"); x.State != StateInstalled || x.Error != "" {
		t.Fatalf("proton = %+v", x)
	}
}

// A pending set out of tries fails before any download, even one that
// cannot start; one whose image is missing, which its trials could not
// mount, is only removed, also when the download brings the image.
func TestPassFailsAStalePendingFirst(t *testing.T) {
	for _, missing := range []bool{false, true} {
		e := newEnv(t)
		proton := newImage(t, "proton", "", 5000, true)
		cc := newImage(t, "coolercontrol", "", 2000, false)
		tool := newImage(t, "tool", "", 1000, false)
		cat := e.catalog(proton, cc, tool)
		e.seal(proton)
		e.cfg.Update.Source = "oci://"
		if missing {
			e.cfg.Update.Source = e.src
			e.serve(cc)
		} else {
			e.seal(cc)
		}
		var enabled *store.Set
		locked(t, func() (err error) {
			if enabled, err = store.WriteEnabled([]string{"proton"}, nil); err != nil {
				return err
			}
			if err = store.WriteWanted([]string{"coolercontrol", "tool"}); err != nil {
				return err
			}
			set, err := store.WriteSet([]string{"proton", "coolercontrol"}, nil, 0)
			if err != nil {
				return err
			}
			return os.Symlink("sets/"+set.Name, config.ExtPendingLink())
		})
		e.report(store.BootReport{Mode: store.ModeEnabled, Set: enabled.Name, Reason: store.ReasonTriesUsed, Mounted: mountedAs(proton)})

		s, b := e.service()
		if retry := s.pass(t.Context(), b); !retry {
			t.Fatal("no retry while tool is missing")
		}
		failed, err := store.Failed()
		must(t, err)
		fp := store.Fingerprint(store.Pairs(cat, []string{"proton", "coolercontrol"}), nil)
		if failed[fp] == missing {
			t.Fatalf("missing %v: failed = %v", missing, failed)
		}
		p, _ := store.Pending()
		if x := e.state(s, "tool"); x.State != StateNeedsAttention || x.Error == "" {
			t.Fatalf("tool = %+v", x)
		}
		if !missing {
			if p != nil {
				t.Fatalf("pending = %+v", p)
			}
			if x := e.state(s, "coolercontrol"); x.State != StateNeedsAttention {
				t.Fatalf("coolercontrol = %+v", x)
			}
			continue
		}
		// It is proposed again, now that its image is here.
		if p == nil || !slices.Equal(p.IDs, []string{"proton", "coolercontrol"}) || p.Tries != store.ProposeTries {
			t.Fatalf("pending = %+v", p)
		}
	}
}

// The failed set blocks its own re-proposal and says so.
func TestPassBlockedSetNeedsAttention(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	cat := e.catalog(proton, cc)
	e.seal(proton)
	e.seal(cc)
	var enabled *store.Set
	locked(t, func() (err error) {
		if enabled, err = store.WriteEnabled([]string{"proton"}, nil); err != nil {
			return err
		}
		if err = store.WriteWanted([]string{"coolercontrol"}); err != nil {
			return err
		}
		return store.AddFailed(store.Fingerprint(store.Pairs(cat, []string{"proton", "coolercontrol"}), nil))
	})
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: enabled.Name, Mounted: mountedAs(proton)})

	s, b := e.service()
	s.pass(t.Context(), b)
	if p, _ := store.Pending(); p != nil {
		t.Fatalf("re-proposed a failed set: %+v", p)
	}
	if x := e.state(s, "coolercontrol"); x.State != StateNeedsAttention {
		t.Fatalf("coolercontrol = %+v", x)
	}
	if x := e.state(s, "proton"); x.State != StateInstalled {
		t.Fatalf("proton = %+v", x)
	}
}

// The other slot's version gets its images too, and GC keeps both
// versions' images while it removes an old one nothing lists.
func TestPassFetchesTheOtherSlotAndCollects(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	oldProton := newImage(t, "proton", "old", 4000, true)
	stale := newImage(t, "proton", "stale", 3000, true)
	e.catalog(proton)
	e.seal(proton)
	e.seal(stale)
	old := time.Now().Add(-2 * time.Hour)
	must(t, os.Chtimes(store.ImagePath(stale.entry.SHA256), old, old))
	e.serve(oldProton) // the directory serves the other version's file
	must(t, store.WriteSlot("b", otherVersion, map[string]manifest.Extension{"proton": {
		Name: "ext-proton.raw", Size: oldProton.entry.Size, SHA256: oldProton.entry.SHA256,
		FSVerity: oldProton.entry.FSVerity, Core: true}}))
	var enabled *store.Set
	locked(t, func() (err error) { enabled, err = store.WriteEnabled([]string{"proton"}, nil); return err })
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: enabled.Name, Mounted: mountedAs(proton)})

	s, b := e.service()
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("retry")
	}
	for _, img := range []image{proton, oldProton} {
		if !exists(store.ImagePath(img.entry.SHA256)) {
			t.Errorf("image %s gone", img.entry.SHA256[:8])
		}
	}
	if exists(store.ImagePath(stale.entry.SHA256)) {
		t.Error("GC kept an image no catalog lists")
	}
	if x := e.state(s, "proton"); x.State != StateInstalled || x.Error != "" {
		t.Fatalf("proton = %+v (the other slot's download must not show)", x)
	}
}

// A source whose bytes do not seal is not downloaded again, nor is any
// image on a disk without fs-verity.
func TestPassStopsRetryingWhatCannotSeal(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	e.catalog(proton)
	e.serve(newImage(t, "proton", "other", 5000, true)) // another version's file
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoSet})

	s, b := e.service()
	if retry := s.pass(t.Context(), b); !retry {
		t.Fatal("no retry after the first failure")
	}
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("the same bytes are fetched again")
	}
	if x := e.state(s, "proton"); x.State != StateNeedsAttention || !strings.Contains(x.Error, "checksum") {
		t.Fatalf("proton = %+v", x)
	}
	s.retryAll() // someone asked: Reconcile
	e.serve(proton)
	s.pass(t.Context(), b)
	if x := e.state(s, "proton"); x.State != StateRestartNeeded {
		t.Fatalf("after Reconcile proton = %+v", x)
	}

	e2 := newEnv(t)
	e2.catalog(proton)
	e2.serve(proton)
	e2.report(store.BootReport{Mode: store.ModeEnabled, Reason: "no-set no-verity"})
	s2, b2 := e2.service()
	if retry := s2.pass(t.Context(), b2); retry {
		t.Fatal("retry without fs-verity")
	}
	if got := e2.sealer.putIDs(); len(got) != 0 {
		t.Fatalf("fetched %v without fs-verity", got)
	}
	if st := s2.Status(); st.Error != noVerityText || st.Extensions[0].State != StateNeedsAttention {
		t.Fatalf("status = %+v", st)
	}

	e3 := newEnv(t)
	e3.catalog(proton)
	e3.serve(proton)
	e3.sealer.err = store.ErrUnsupported
	s3, b3 := e3.service()
	s3.pass(t.Context(), b3)
	if retry := s3.pass(t.Context(), b3); retry {
		t.Fatal("retry after the disk refused to seal")
	}
	if x := e3.state(s3, "proton"); x.Error != noVerityText {
		t.Fatalf("proton = %+v", x)
	}
}

func TestBootedSlotFile(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cat := e.catalog(proton)
	// A stage wrote it from the manifest, with the build key: it is kept.
	staged := slotExtensions(cat)
	x := staged["proton"]
	x.Key = strings.Repeat("ab", 16)
	staged["proton"] = x
	must(t, store.WriteSlot("a", bootedVersion, staged))
	s, b := e.service()
	must(t, s.writeBootedSlot(t.Context(), b))
	sl, err := store.ReadSlot("a")
	must(t, err)
	if !b.slotDone || sl.Extensions["proton"].Key == "" {
		t.Fatalf("rewrote a matching slot file: %+v", sl)
	}
	// Another version's file is replaced.
	must(t, store.WriteSlot("a", otherVersion, staged))
	b.slotDone = false
	must(t, s.writeBootedSlot(t.Context(), b))
	if sl, _ := store.ReadSlot("a"); sl.Version != bootedVersion || sl.Extensions["proton"].Key != "" {
		t.Fatalf("slot a = %+v", sl)
	}
	// The live system (no slot) writes none.
	writeFile(t, config.ProcCmdline, "vos.mode=live\n")
	_, b = e.service()
	if b.slot != "" {
		t.Fatalf("slot %q on the live system", b.slot)
	}
}

func TestRehashDeletesDamagedImages(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	cc := newImage(t, "coolercontrol", "", 2000, false)
	tool := newImage(t, "tool", "", 1000, false)
	e.catalog(proton, cc, tool)
	for _, img := range []image{proton, cc, tool} {
		e.seal(img)
		e.serve(img)
	}
	var enabled *store.Set
	locked(t, func() (err error) {
		if err = store.WriteWanted([]string{"coolercontrol", "tool"}); err != nil {
			return err
		}
		enabled, err = store.WriteEnabled([]string{"proton", "coolercontrol", "tool"}, nil)
		return err
	})
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: enabled.Name, Mounted: mountedAs(proton, cc, tool)})
	// proton's bytes rotted in place (still "sealed"); coolercontrol's disk
	// fails reads with EIO; tool is fine.
	ppath, cpath := store.ImagePath(proton.entry.SHA256), store.ImagePath(cc.entry.SHA256)
	must(t, os.Chmod(ppath, 0o644))
	rotten := append([]byte{proton.data[0] ^ 1}, proton.data[1:]...)
	must(t, os.WriteFile(ppath, rotten, 0o644))
	o := openImage
	t.Cleanup(func() { openImage = o })
	openImage = func(path string) (io.ReadCloser, error) {
		if path == cpath {
			return io.NopCloser(eioReader{}), nil
		}
		return os.Open(path)
	}

	s, b := e.service()
	s.pass(t.Context(), b)
	if !s.rehash(t.Context(), b.cat) {
		t.Fatal("nothing deleted")
	}
	if exists(ppath) || exists(cpath) || !exists(store.ImagePath(tool.entry.SHA256)) {
		t.Fatalf("after the re-read: proton %v, coolercontrol %v, tool %v", exists(ppath), exists(cpath), exists(store.ImagePath(tool.entry.SHA256)))
	}
	s.mu.Lock()
	text := s.errs["proton"]
	s.mu.Unlock()
	if text != damagedText {
		t.Fatalf("proton error %q", text)
	}
	// The next pass fetches both again; the set keeps them meanwhile.
	openImage = o
	s.pass(t.Context(), b)
	if !exists(ppath) || !exists(cpath) {
		t.Fatal("damaged images not fetched again")
	}
	if p, _ := store.Pending(); p != nil {
		t.Fatalf("pending %+v: the set must not change", p)
	}
	if x := e.state(s, "proton"); x.State != StateInstalled || x.Error != "" {
		t.Fatalf("proton = %+v", x)
	}
	// Once per boot per digest: another re-read leaves it alone.
	must(t, os.Chmod(ppath, 0o644))
	must(t, os.WriteFile(ppath, rotten, 0o644))
	if s.rehash(t.Context(), b.cat) {
		t.Fatal("deleted the same digest twice in one boot")
	}
}

type eioReader struct{}

func (eioReader) Read([]byte) (int, error) { return 0, syscall.EIO }

// stepSource is a fakeSource that stops, until told to go on, before a
// download asks for anything and again after its first chunk.
type stepSource struct {
	*fakeSource
	at   chan string // "connect", then "chunk"
	next chan struct{}
}

func (s stepSource) Fetch(ctx context.Context, a manifest.Artifact, w io.Writer, onChunk func(int64) error) error {
	s.at <- "connect"
	<-s.next
	first := true
	return s.fakeSource.Fetch(ctx, a, w, func(n int64) error {
		err := onChunk(n)
		if first {
			first = false
			s.at <- "chunk"
			<-s.next
		}
		return err
	})
}

// Busy only while bytes arrive: not while a download waits for its first,
// nor once they stop coming; the card shows the download all along.
func TestBusyWhileBytesArrive(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	e.catalog(proton)
	src := stepSource{fakeSource: &fakeSource{byName: map[string]served{"ext-proton.raw": {data: proton.data}}},
		at: make(chan string), next: make(chan struct{})}
	o := openSource
	t.Cleanup(func() { openSource = o })
	openSource = func(string, string) (source, error) { return src, nil }
	var skew atomic.Int64
	n := now
	t.Cleanup(func() { now = n })
	now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }

	s, b := e.service()
	busy := func() bool { on, why := s.Busy(); return on && why == busyReason && s.Status().Busy }
	done := make(chan bool)
	go func() { done <- s.pass(context.Background(), b) }()
	if at := <-src.at; at != "connect" || busy() {
		t.Fatalf("at %s: busy %v before the first bytes", at, busy())
	}
	if x := e.state(s, "proton"); x.State != StateDownloading || x.Progress == nil || x.Progress.Total != 5000 {
		t.Fatalf("proton = %+v", x)
	}
	src.next <- struct{}{}
	if at := <-src.at; at != "chunk" || !busy() {
		t.Fatalf("at %s: not busy while bytes arrive", at)
	}
	if x := e.state(s, "proton"); x.Progress == nil || x.Progress.Bytes != 1000 {
		t.Fatalf("proton = %+v", x)
	}
	skew.Store(int64(busyStall + time.Second))
	if busy() {
		t.Fatal("busy while the download stalls")
	}
	src.next <- struct{}{}
	if retry := <-done; retry {
		t.Fatal("retry")
	}
	if busy() || !sealed(proton.entry) {
		t.Fatalf("busy %v, sealed %v after the download", busy(), sealed(proton.entry))
	}
}

func TestRun(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	e.catalog(proton)
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoSet})
	rb := retryBase
	t.Cleanup(func() { retryBase = rb })
	retryBase = 10 * time.Millisecond
	writeFile(t, config.ExtImagesDir()+"/.tmp-proton-1", "half")
	old := time.Now().Add(-2 * time.Hour)
	must(t, os.Chtimes(config.ExtImagesDir()+"/.tmp-proton-1", old, old))

	s, _ := e.service()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return")
		}
	}
	t.Cleanup(stop)
	proton1 := func(ok func(ExtensionStatus) bool) func() bool {
		return func() bool { x, found := find(s, "proton"); return found && ok(x) }
	}
	waitFor(t, proton1(func(x ExtensionStatus) bool { return x.Error != "" })) // the source lacks it
	if exists(config.ExtImagesDir() + "/.tmp-proton-1") {
		t.Error("a stale temp file survived the start")
	}
	e.serve(proton) // a retry finds it
	waitFor(t, proton1(func(x ExtensionStatus) bool { return x.State == StateRestartNeeded }))
	s.Reconcile()
	s.Reconcile() // never blocks
	stop()

	// The live system has no store.
	writeFile(t, config.ProcCmdline, "vos.mode=live\n")
	live := NewService(e.cfg)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	live.Run(ctx2)
	if ctx2.Err() != nil {
		t.Fatal("Run waited on the live system")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRetryDelay(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 5: 16 * time.Minute, 6: 30 * time.Minute, 100: 30 * time.Minute} {
		if got := retryDelay(n); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", n, got, want)
		}
	}
}

// fetchImage falls back to the blob by digest when the registry no longer
// has the version's tag.
func TestFetchImageByDigest(t *testing.T) {
	newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	var manifests, blobs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/vaporos/manifests/"):
			manifests.Add(1)
			http.NotFound(w, r)
		case r.URL.Path == "/v2/vaporos/blobs/sha256:"+proton.entry.SHA256:
			blobs.Add(1)
			w.Write(proton.data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	src, err := update.OpenSourceAt("oci+http://"+strings.TrimPrefix(srv.URL, "http://")+"/vaporos", bootedVersion)
	must(t, err)
	var last int64
	if err := fetchImage(t.Context(), src, proton.entry, func(d int64) { last = d }); err != nil {
		t.Fatal(err)
	}
	if manifests.Load() != 1 || blobs.Load() != 1 || last != proton.entry.Size || !exists(store.ImagePath(proton.entry.SHA256)) {
		t.Fatalf("manifests %d, blobs %d, progress %d", manifests.Load(), blobs.Load(), last)
	}

	// A directory has no blobs: its own error stands.
	dir, err := update.OpenSourceAt(t.TempDir(), bootedVersion)
	must(t, err)
	other := newImage(t, "other", "", 100, false)
	if err := fetchImage(t.Context(), dir, other.entry, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory: %v", err)
	}
}
