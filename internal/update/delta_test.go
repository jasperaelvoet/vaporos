package update

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// tuneRanges sets the block download knobs for one test.
func tuneRanges(t *testing.T, gap, max int64, workers int) {
	t.Helper()
	oldGap, oldMax, oldWorkers := rangeGap, maxRange, rangeWorkers
	t.Cleanup(func() { rangeGap, maxRange, rangeWorkers = oldGap, oldMax, oldWorkers })
	rangeGap, maxRange, rangeWorkers = gap, max, workers
}

// erofsBlocks returns n random blocks whose first one carries an erofs
// superblock giving the image's size, as mkfs.erofs writes it.
func erofsBlocks(seed uint64, n int) [][]byte {
	blocks := make([][]byte, n)
	for i := range blocks {
		blocks[i] = randomBytes(seed<<20|uint64(i), BlockSize)
	}
	sb := blocks[0][1024:]
	binary.LittleEndian.PutUint32(sb[0:], erofsMagic)
	sb[12] = 12
	binary.LittleEndian.PutUint32(sb[36:], uint32(n))
	return blocks
}

func join(parts ...[][]byte) []byte {
	var b []byte
	for _, p := range parts {
		for _, blk := range p {
			b = append(b, blk...)
		}
	}
	return b
}

// imagePair is an old image and the next one, built the way consecutive
// erofs images differ: a new superblock, blocks inserted (which shifts
// everything after them), one changed, some dropped and some appended.
// fresh counts the new image's blocks that the old one has nowhere.
type imagePair struct {
	old, new []byte
	fresh    int
}

func makeImagePair() imagePair {
	old := erofsBlocks(1, 120)
	changed := bytes.Clone(old[60])
	changed[100] ^= 0xff
	parts := [][][]byte{
		erofsBlocks(2, 1)[:1], // the superblock
		old[1:30],
		erofsBlocks(3, 3), // inserted
		old[30:60],
		{changed},
		old[61:100],
		old[110:120],
		erofsBlocks(4, 5), // appended
	}
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	newSB := parts[0][0][1024:]
	binary.LittleEndian.PutUint32(newSB[36:], uint32(n))
	return imagePair{old: join(old), new: join(parts...), fresh: 1 + 3 + 1 + 5}
}

// putSlot writes b at the start of slot's partition.
func (e *testEnv) putSlot(slot string, b []byte) {
	e.t.Helper()
	f, err := os.OpenFile(e.slotDev(slot), os.O_WRONLY, 0)
	e.must(err)
	_, err = f.WriteAt(b, 0)
	e.must(err)
	e.must(f.Close())
}

// boundedRanges are the storage requests for root that asked for a range
// with an end, i.e. block downloads.
func (f *fakeRegistry) boundedRanges() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.rootRanges {
		if r != "" && !strings.HasSuffix(r, "-") {
			out = append(out, r)
		}
	}
	return out
}

func TestStageBlocksFromOCI(t *testing.T) {
	e := setup(t)
	tuneRanges(t, 0, 3*BlockSize, 3)
	pair := makeImagePair()
	e.putSlot("a", pair.old)
	img := e.makeImageRoot(newVersion, 200, pair.new, true, nil)
	f := newFakeRegistry(t, img)

	var progress []Progress
	if _, err := Stage(context.Background(), e.cfg(f.spec()), Options{
		Progress: func(p Progress) { progress = append(progress, p) },
	}); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)

	// Only the blocks the old image lacks came over the network, as ranges.
	want := int64(pair.fresh * BlockSize)
	if got := f.rootServed.Load(); got != want {
		t.Fatalf("root bytes downloaded: %d, want %d", got, want)
	}
	f.mu.Lock()
	all := f.rootRanges
	f.mu.Unlock()
	if bounded := f.boundedRanges(); len(bounded) == 0 || len(bounded) != len(all) {
		t.Fatalf("root requests %q", all)
	}
	// Range requests reuse the storage URL rather than ask the registry
	// each time: at most one registry request per worker that started
	// before the first redirect came back.
	if n := f.blobReqs["root.erofs"]; n > rangeWorkers || n >= len(all) {
		t.Fatalf("%d registry requests for %d ranges", n, len(all))
	}

	last := -1
	var final Progress
	for _, p := range progress {
		if p.Percent < last {
			t.Fatalf("progress went back: %+v after %d%%", p, last)
		}
		last = p.Percent
		if p.Phase == "write" {
			final = p
		}
	}
	if final.Bytes != want || final.Total != want || last != 100 {
		t.Fatalf("last write progress %+v, ended at %d%%", final, last)
	}
}

func TestStageBlocksFromDir(t *testing.T) {
	e := setup(t)
	pair := makeImagePair()
	e.putSlot("a", pair.old)
	img := e.makeImageRoot(newVersion, 200, pair.new, true, nil)
	var total int64
	if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{
		Progress: func(p Progress) {
			if p.Phase == "write" && p.Total > 0 {
				total = p.Total
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
	// The fresh blocks are further apart than the default 64 KiB gap.
	if total != int64(pair.fresh*BlockSize) {
		t.Fatalf("downloaded %d bytes, want %d", total, pair.fresh*BlockSize)
	}
}

// A stage that stops part way leaves its blocks in the idle slot, and the
// next one downloads only what is still missing.
func TestStageBlocksResume(t *testing.T) {
	e := setup(t)
	tuneRanges(t, 0, BlockSize, 1)
	pair := makeImagePair()
	e.putSlot("a", pair.old)
	img := e.makeImageRoot(newVersion, 200, pair.new, true, nil)
	f := newFakeRegistry(t, img)

	// The network goes away after the first block.
	f.rangeLimit.Store(1)
	_, err := Stage(context.Background(), e.cfg(f.spec()), Options{})
	var fe *fallbackError
	if err == nil || errors.As(err, &fe) {
		t.Fatalf("first stage: %v", err)
	}
	if e.state().LastError == "" {
		t.Fatal("no last_error")
	}
	if b := e.entry("b"); b != nil {
		t.Fatalf("the half-written slot has an entry: %+v", b)
	}
	first := f.rootServed.Load()
	if first != BlockSize {
		t.Fatalf("first stage downloaded %d bytes", first)
	}

	f.rangeLimit.Store(0)
	if _, err := Stage(context.Background(), e.cfg(f.spec()), Options{}); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
	if got, want := f.rootServed.Load()-first, int64((pair.fresh-1)*BlockSize); got != want {
		t.Fatalf("second stage downloaded %d bytes, want %d", got, want)
	}
}

// Blocks the idle slot already holds where the new image wants them are
// neither copied nor downloaded.
func TestStageBlocksKeepsIdleSlot(t *testing.T) {
	e := setup(t)
	tuneRanges(t, 0, 8*BlockSize, 2)
	pair := makeImagePair()
	e.putSlot("a", pair.old)
	e.putSlot("b", pair.new[:40*BlockSize]) // the superblock and the inserted blocks
	img := e.makeImageRoot(newVersion, 200, pair.new, true, nil)
	f := newFakeRegistry(t, img)
	if _, err := Stage(context.Background(), e.cfg(f.spec()), Options{}); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
	if got, want := f.rootServed.Load(), int64((pair.fresh-4)*BlockSize); got != want {
		t.Fatalf("downloaded %d bytes, want %d", got, want)
	}
}

// Whatever keeps the block index from finishing, the whole root is
// streamed instead and the stage succeeds.
func TestStageBlocksFallsBack(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(e *testEnv, img *image, f *fakeRegistry)
		ranged bool // block downloads were tried first
	}{
		{"no ranges", func(e *testEnv, img *image, f *fakeRegistry) { f.noRanges.Store(true) }, true},
		{"too much missing", func(e *testEnv, img *image, f *fakeRegistry) { e.putSlot("a", make([]byte, slotSize)) }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			tuneRanges(t, 0, 4*BlockSize, 2)
			pair := makeImagePair()
			e.putSlot("a", pair.old)
			img := e.makeImageRoot(newVersion, 200, pair.new, true, nil)
			f := newFakeRegistry(t, img)
			c.mutate(e, img, f)
			if _, err := Stage(context.Background(), e.cfg(f.spec()), Options{}); err != nil {
				t.Fatal(err)
			}
			e.checkStaged(img)
			f.mu.Lock()
			ranges := f.rootRanges
			f.mu.Unlock()
			if len(ranges) == 0 || ranges[len(ranges)-1] != "" {
				t.Fatalf("root requests %q: no whole download at the end", ranges)
			}
			if got := len(f.boundedRanges()) > 0; got != c.ranged {
				t.Fatalf("root requests %q", ranges)
			}
		})
	}
}

// An index that is wrong in any way never ends up in the slot: the stage
// streams the whole root.
func TestStageBlocksBadIndex(t *testing.T) {
	pair := makeImagePair()
	cases := map[string]func(idx []byte) []byte{
		"malformed": func([]byte) []byte { return []byte("not an index") },
		// A block the index names wrongly is downloaded and fails its check.
		"wrong block": func(idx []byte) []byte {
			idx = bytes.Clone(idx)
			idx[indexHeaderSize+31*blockHashSize] ^= 1
			return idx
		},
		// An index of another image: none of its blocks are here.
		"other image": func([]byte) []byte {
			other := randomBytes(9, len(pair.new))
			var b bytes.Buffer
			WriteIndex(bytes.NewReader(other), int64(len(other)), &b)
			return b.Bytes()
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			tuneRanges(t, 0, 4*BlockSize, 2)
			e.putSlot("a", pair.old)
			good := e.makeImageRoot(newVersion, 200, pair.new, true, nil)
			badIdx := mutate(good.files["root.erofs.idx"])
			img := e.makeImageRoot(newVersion, 200, pair.new, false, func(m *manifest.Manifest) {
				m.Artifacts[manifest.Index] = manifest.Artifact{Name: "root.erofs.idx", Size: int64(len(badIdx)), SHA256: strings.TrimPrefix(sha(badIdx), "sha256:")}
			})
			img.files["root.erofs.idx"] = badIdx
			if _, err := Stage(context.Background(), e.cfg(e.srcDir(img)), Options{}); err != nil {
				t.Fatal(err)
			}
			e.checkStaged(img)
		})
	}

	// The manifest names an index the source does not have.
	t.Run("missing", func(t *testing.T) {
		e := setup(t)
		e.putSlot("a", pair.old)
		img := e.makeImageRoot(newVersion, 200, pair.new, true, nil)
		dir := e.srcDir(img)
		e.must(os.Remove(filepath.Join(dir, "root.erofs.idx")))
		if _, err := Stage(context.Background(), e.cfg(dir), Options{}); err != nil {
			t.Fatal(err)
		}
		e.checkStaged(img)
	})
}

// A cancelled block stage is not a fallback: nothing more is downloaded.
func TestStageBlocksCancel(t *testing.T) {
	e := setup(t)
	tuneRanges(t, 0, BlockSize, 1)
	pair := makeImagePair()
	e.putSlot("a", pair.old)
	img := e.makeImageRoot(newVersion, 200, pair.new, true, nil)
	f := newFakeRegistry(t, img)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onRange = func(n int) {
		if n == 3 {
			cancel()
		}
	}
	_, err := Stage(ctx, e.cfg(f.spec()), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stage: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rootRanges {
		if r == "" {
			t.Fatalf("a cancelled stage streamed the whole root: %q", f.rootRanges)
		}
	}
	if n := f.rangeReqs.Load(); n != 3 {
		t.Fatalf("%d range requests after the cancel at the third", n)
	}
}

func TestMissingRanges(t *testing.T) {
	d, k := int64(download), int64(inPlace)
	plan := &blockPlan{from: []int64{d, d, k, d, k, k, k, d, d, d, d, d, 0, d}}
	size := int64(len(plan.from))*BlockSize - 1000 // a short last block
	cases := []struct {
		gap, max int64
		want     []byteRange
	}{
		{0, 1 << 20, []byteRange{{0, 2 * BlockSize}, {3 * BlockSize, 4 * BlockSize}, {7 * BlockSize, 12 * BlockSize}, {13 * BlockSize, size}}},
		{BlockSize, 1 << 20, []byteRange{{0, 4 * BlockSize}, {7 * BlockSize, size}}},
		{3 * BlockSize, 3 * BlockSize, []byteRange{{0, 2 * BlockSize}, {3 * BlockSize, 4 * BlockSize}, {7 * BlockSize, 10 * BlockSize}, {10 * BlockSize, 12 * BlockSize}, {13 * BlockSize, size}}},
	}
	for _, c := range cases {
		tuneRanges(t, c.gap, c.max, 1)
		got := missingRanges(plan, size)
		if len(got) != len(c.want) {
			t.Fatalf("gap %d max %d: %v, want %v", c.gap, c.max, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("gap %d max %d: %v, want %v", c.gap, c.max, got, c.want)
			}
		}
	}
}

func TestSeedSize(t *testing.T) {
	dir := t.TempDir()
	files := 0
	write := func(b []byte) *os.File {
		files++
		p := filepath.Join(dir, strconv.Itoa(files))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	slot := make([]byte, 64*BlockSize)
	copy(slot, join(erofsBlocks(1, 20)))
	if got := seedSize(write(slot), 5); got != 20*BlockSize {
		t.Fatalf("erofs: %d", got)
	}
	big := bytes.Clone(slot)
	binary.LittleEndian.PutUint32(big[1024+36:], 1000) // larger than the partition
	if got := seedSize(write(big), 5); got != int64(len(big)) {
		t.Fatalf("capped: %d", got)
	}
	none := make([]byte, 64*BlockSize)
	none[1024] = 1
	if got := seedSize(write(none), 7*BlockSize); got != 7*BlockSize {
		t.Fatalf("no erofs: %d", got)
	}
}

// A booted-slot block that no longer matches when it is copied is
// downloaded instead.
func TestCopyBlocksRechecks(t *testing.T) {
	pair := makeImagePair()
	var ib bytes.Buffer
	if err := WriteIndex(bytes.NewReader(pair.new), int64(len(pair.new)), &ib); err != nil {
		t.Fatal(err)
	}
	idx, err := parseIndex(ib.Bytes(), int64(len(pair.new)))
	if err != nil {
		t.Fatal(err)
	}
	idle := make([]byte, len(pair.new))
	prog := &blockProgress{rep: &reporter{}}
	plan, err := planBlocks(context.Background(), idx, bytes.NewReader(idle), bytes.NewReader(pair.old), int64(len(pair.old)), prog)
	if err != nil {
		t.Fatal(err)
	}
	if plan.toFetch != int64(pair.fresh*BlockSize) || plan.kept != 0 || plan.copied+plan.toFetch != int64(len(pair.new)) {
		t.Fatalf("plan: kept %d, copy %d, fetch %d", plan.kept, plan.copied, plan.toFetch)
	}
	changed := bytes.Clone(pair.old)
	changed[50*BlockSize+7] ^= 1 // old block 50 is new block 53
	slot, err := os.Create(filepath.Join(t.TempDir(), "slot"))
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Close()
	if err := copyBlocks(context.Background(), idx, plan, bytes.NewReader(changed), &slotWriter{f: slot}, prog); err != nil {
		t.Fatal(err)
	}
	if plan.from[53] != download || plan.toFetch != int64((pair.fresh+1)*BlockSize) {
		t.Fatalf("block 53 comes from %d; %d bytes to fetch", plan.from[53], plan.toFetch)
	}
	got := make([]byte, BlockSize)
	slot.ReadAt(got, 54*BlockSize)
	if !bytes.Equal(got, pair.new[54*BlockSize:55*BlockSize]) {
		t.Fatal("block 54 was not copied")
	}
}

// Kernel and initrd that the ESP already holds are copied from there.
func TestStageReusesBootFiles(t *testing.T) {
	e := setup(t)
	art := func(name string, b []byte) manifest.Artifact {
		return manifest.Artifact{Name: name, Size: int64(len(b)), SHA256: strings.TrimPrefix(sha(b), "sha256:")}
	}
	sameKernel := []byte("old kernel")  // what setup put on the ESP for the booted version
	otherInitrd := []byte("new initrd") // the same size as "old initrd", not the same bytes
	img := e.makeImage(newVersion, 200, 64<<10, func(m *manifest.Manifest) {
		m.Artifacts["kernel"] = art("vmlinuz", sameKernel)
		m.Artifacts["initrd"] = art("initramfs.img", otherInitrd)
	})
	img.files["vmlinuz"], img.files["initramfs.img"] = sameKernel, otherInitrd
	dir := e.srcDir(img)
	e.must(os.Remove(filepath.Join(dir, "vmlinuz"))) // only the ESP has it
	if _, err := Stage(context.Background(), e.cfg(dir), Options{}); err != nil {
		t.Fatal(err)
	}
	e.checkStaged(img)
}

func TestOpenRange(t *testing.T) {
	setup(t)
	data := randomBytes(5, 100_000)
	var calls atomic.Int32
	var mu sync.Mutex
	var ranges []string
	ignore := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		if ignore.Load() {
			r.Header.Del("Range")
		}
		if calls.Add(1) == 1 {
			// The first answer breaks off after 1000 bytes.
			w.Header().Set("Content-Range", "bytes 5000-9999/100000")
			w.Header().Set("Content-Length", "5000")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data[5000:6000])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	src, err := OpenSource(srv.URL+"/", "")
	if err != nil {
		t.Fatal(err)
	}
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(ranges)
	}
	got, err := io.ReadAll(src.OpenRange(context.Background(), "root.erofs", 5000, 10000))
	if err != nil || !bytes.Equal(got, data[5000:10000]) {
		t.Fatalf("read %d bytes, err %v", len(got), err)
	}
	if r := seen(); len(r) != 2 || r[0] != "bytes=5000-9999" || r[1] != "bytes=6000-9999" {
		t.Fatalf("ranges %q", r)
	}
	got, err = io.ReadAll(src.OpenRange(context.Background(), "root.erofs", 0, 4096))
	if r := seen(); err != nil || !bytes.Equal(got, data[:4096]) || r[2] != "bytes=0-4095" {
		t.Fatalf("from 0: %d bytes, err %v, ranges %q", len(got), err, r)
	}

	// A server that ignores ranges is not asked again.
	ignore.Store(true)
	n := len(seen())
	_, err = io.ReadAll(src.OpenRange(context.Background(), "root.erofs", 4096, 8192))
	if !errors.Is(err, errNoRanges) || len(seen()) != n+1 {
		t.Fatalf("no ranges: err %v after %d requests", err, len(seen())-n)
	}
}

// Range requests reuse a blob's storage URL until it is too old or stops
// working, and then ask the registry again.
func TestOCIRangeReusesStorageURL(t *testing.T) {
	e := setup(t)
	img := e.makeImage(newVersion, 200, 64<<10, nil)
	f := newFakeRegistry(t, img)
	src, err := OpenSource(f.spec(), "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := src.Manifest(ctx); err != nil {
		t.Fatal(err)
	}
	read := func(off, end int64) {
		t.Helper()
		got, err := io.ReadAll(src.OpenRange(ctx, "root.erofs", off, end))
		if err != nil || !bytes.Equal(got, img.root()[off:end]) {
			t.Fatalf("[%d,%d): %d bytes, err %v", off, end, len(got), err)
		}
	}
	read(0, 100)
	read(100, 5000)
	read(9000, 9001)
	if n := f.rootBlobReqs.Load(); n != 1 {
		t.Fatalf("registry requests: %d", n)
	}
	f.mu.Lock()
	f.minSig.Store(int32(f.sig) + 1) // every URL handed out so far expires
	f.mu.Unlock()
	read(5000, 6000)
	if n := f.rootBlobReqs.Load(); n != 2 {
		t.Fatalf("registry requests after expiry: %d", n)
	}
	old := storageURLTTL
	storageURLTTL = 0
	defer func() { storageURLTTL = old }()
	read(0, 10)
	if n := f.rootBlobReqs.Load(); n != 3 {
		t.Fatalf("registry requests without reuse: %d", n)
	}
	// A whole-file read never uses a stored URL.
	var buf bytes.Buffer
	storageURLTTL = time.Hour
	if err := src.Fetch(ctx, img.m.Artifact(manifest.Root), &buf, nil); err != nil {
		t.Fatal(err)
	}
	if n := f.rootBlobReqs.Load(); n != 4 {
		t.Fatalf("registry requests after a whole read: %d", n)
	}
}

// freezeProxy forwards TCP connections to target until freeze, after which
// the connections it already has go silent without closing: a dead
// connection as a broken NAT or a vanished peer leaves it.
type freezeProxy struct {
	ln     net.Listener
	mu     sync.Mutex
	frozen []chan struct{} // one per connection, closed to freeze it
	stop   chan struct{}
}

func newFreezeProxy(t *testing.T, target string) *freezeProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &freezeProxy{ln: ln, stop: make(chan struct{})}
	t.Cleanup(func() { close(p.stop); ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			freeze := make(chan struct{})
			p.mu.Lock()
			p.frozen = append(p.frozen, freeze)
			p.mu.Unlock()
			pipe := func(dst, src net.Conn) {
				buf := make([]byte, 32<<10)
				for {
					n, err := src.Read(buf)
					select {
					case <-freeze:
						<-p.stop // silent: no bytes, no close
						return
					default:
					}
					if n > 0 {
						dst.Write(buf[:n])
					}
					if err != nil {
						dst.Close()
						return
					}
				}
			}
			go pipe(s, c)
			go pipe(c, s)
		}
	}()
	return p
}

// freeze silences every connection the proxy has now; new ones still work.
func (p *freezeProxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frozen {
		close(f)
	}
	p.frozen = nil
}

// A download on an HTTP/2 connection that dies quietly moves to a new
// connection once a ping goes unanswered, well before the stall timeout.
func TestDeadHTTP2ConnectionIsReplaced(t *testing.T) {
	setup(t)
	oldAfter, oldTimeout := h2PingAfter, h2PingTimeout
	h2PingAfter, h2PingTimeout = 100*time.Millisecond, 100*time.Millisecond
	defer func() { h2PingAfter, h2PingTimeout = oldAfter, oldTimeout }()
	stallTimeout = time.Minute

	data := randomBytes(7, 1<<20)
	var proxy *freezeProxy
	var calls atomic.Int32
	var protos sync.Map
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protos.Store(r.Proto, true)
		if calls.Add(1) == 1 {
			proxy.freeze()
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	proxy = newFreezeProxy(t, srv.Listener.Addr().String())

	tr := newTransport()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	httpClient = &http.Client{Transport: tr}

	src, err := OpenSource("https://"+proxy.ln.Addr().String()+"/", "")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := io.ReadAll(src.OpenRange(context.Background(), "root.erofs", 4096, int64(len(data))))
	if err != nil || !bytes.Equal(got, data[4096:]) {
		t.Fatalf("read %d bytes, err %v", len(got), err)
	}
	if took := time.Since(start); took > 10*time.Second || calls.Load() != 2 {
		t.Fatalf("took %v and %d requests", took, calls.Load())
	}
	if _, ok := protos.Load("HTTP/2.0"); !ok {
		t.Fatal("the test did not run over HTTP/2")
	}
}
