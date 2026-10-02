package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// fakeSource serves files by name and blobs by sha256 from memory, or the
// error set for them; it records what was asked of it.
type fakeSource struct {
	mu          sync.Mutex
	manifestErr error
	byName      map[string]served // by file name
	blobs       map[string]served // by sha256; nil: no blobs (not a registry)
	local       bool              // a directory, not reached over the network
	calls       []string
}

type served struct {
	data []byte
	err  error // after data, if any
}

// useSource makes every source vosd opens src.
func useSource(t *testing.T, src *fakeSource) {
	o := openSource
	t.Cleanup(func() { openSource = o })
	openSource = func(string, string) (source, error) { return src, nil }
}

func (f *fakeSource) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeSource) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeSource) Manifest(context.Context) (*manifest.Manifest, error) {
	f.record("manifest")
	if f.manifestErr != nil {
		return nil, f.manifestErr
	}
	return &manifest.Manifest{}, nil
}

func (f *fakeSource) Fetch(_ context.Context, a manifest.Artifact, w io.Writer, onChunk func(int64) error) error {
	f.record("name " + a.Name)
	f.mu.Lock()
	s, ok := f.byName[a.Name]
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s: HTTP 404 Not Found", a.Name)
	}
	return serveTo(s, a.SHA256, w, onChunk)
}

func (f *fakeSource) FetchBlob(_ context.Context, sha string, _ int64, w io.Writer, onChunk func(int64) error) error {
	f.record("blob " + sha[:8])
	f.mu.Lock()
	s, ok := f.blobs[sha]
	none := f.blobs == nil
	f.mu.Unlock()
	switch {
	case none:
		return update.ErrNoBlobs
	case !ok:
		return fmt.Errorf("blob %s: HTTP 404 Not Found", sha[:8])
	}
	return serveTo(s, sha, w, onChunk)
}

func (f *fakeSource) Remote() bool   { return !f.local }
func (f *fakeSource) String() string { return "fake" }

// serveTo writes s as Source.Fetch does: in chunks, then the checksum.
func serveTo(s served, want string, w io.Writer, onChunk func(int64) error) error {
	var done int64
	for data := s.data; len(data) > 0; {
		n := min(len(data), 1000)
		if _, err := w.Write(data[:n]); err != nil {
			return err
		}
		done += int64(n)
		if err := onChunk(done); err != nil {
			return err
		}
		data = data[n:]
	}
	if s.err != nil {
		return s.err
	}
	if sum := sha256.Sum256(s.data); hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("%w: sha256 %x, want %s", update.ErrChecksum, sum, want)
	}
	return nil
}

var errRefused = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

// gaveUp is a download that failed with err until update gave up.
func gaveUp(err error) error { return fmt.Errorf("%w after 10 attempts: %w", update.ErrGaveUp, err) }

// Which download is tried after which failure, which error is returned,
// and whether the service then holds the source's bytes for bad.
func TestFetchImageFallback(t *testing.T) {
	proton := newImage(t, "proton", "", 5000, true)
	other := newImage(t, "proton", "other", 5000, true)
	name := manifest.ExtensionFile("proton")
	good, wrong := served{data: proton.data}, served{data: other.data}
	gone := map[string]served{}
	tests := []struct {
		name      string
		byName    map[string]served
		blobs     map[string]served
		writeErr  error
		putErr    error // Put fails with it after the download (its sync, close or seal)
		local     bool  // a directory source
		wantCalls []string
		ok        bool
		bad       bool
		down      bool // unreachable
		full      bool
	}{
		{name: "by name", byName: map[string]served{name: good}, blobs: gone,
			wantCalls: []string{"name " + name}, ok: true},
		{name: "tag moved: by digest", byName: map[string]served{name: wrong}, blobs: map[string]served{proton.entry.SHA256: good},
			wantCalls: []string{"name " + name, "blob " + proton.entry.SHA256[:8]}, ok: true},
		{name: "tag gone: by digest", blobs: map[string]served{proton.entry.SHA256: good},
			wantCalls: []string{"name " + name, "blob " + proton.entry.SHA256[:8]}, ok: true},
		{name: "other bytes, no blobs", byName: map[string]served{name: wrong},
			wantCalls: []string{"name " + name, "blob " + proton.entry.SHA256[:8]}, bad: true},
		{name: "other bytes, blob gone", byName: map[string]served{name: wrong}, blobs: gone,
			wantCalls: []string{"name " + name, "blob " + proton.entry.SHA256[:8]}, bad: true},
		{name: "gone, blob other bytes", blobs: map[string]served{proton.entry.SHA256: wrong},
			wantCalls: []string{"name " + name, "blob " + proton.entry.SHA256[:8]}, bad: true},
		{name: "gone everywhere", blobs: gone,
			wantCalls: []string{"name " + name, "blob " + proton.entry.SHA256[:8]}},
		{name: "other bytes, then the network", byName: map[string]served{name: wrong},
			blobs:     map[string]served{proton.entry.SHA256: {err: gaveUp(errRefused)}},
			wantCalls: []string{"name " + name, "blob " + proton.entry.SHA256[:8]}, down: true},
		{name: "network: no blob", byName: map[string]served{name: {data: proton.data[:100], err: io.ErrUnexpectedEOF}},
			blobs: map[string]served{proton.entry.SHA256: good}, wantCalls: []string{"name " + name}, down: true},
		{name: "short file in a directory: not the network", byName: map[string]served{name: {data: proton.data[:100], err: io.ErrUnexpectedEOF}},
			local: true, wantCalls: []string{"name " + name, "blob " + proton.entry.SHA256[:8]}},
		{name: "server trouble: no blob", byName: map[string]served{name: {err: gaveUp(errors.New("HTTP 503"))}},
			blobs: map[string]served{proton.entry.SHA256: good}, wantCalls: []string{"name " + name}, down: true},
		{name: "full disk: no blob", byName: map[string]served{name: good}, blobs: map[string]served{proton.entry.SHA256: good},
			writeErr: syscall.ENOSPC, wantCalls: []string{"name " + name}, full: true},
		{name: "failing disk: no blob", byName: map[string]served{name: good}, blobs: map[string]served{proton.entry.SHA256: good},
			writeErr: syscall.EIO, wantCalls: []string{"name " + name}},
		{name: "disk full on sync: no blob", byName: map[string]served{name: good}, blobs: map[string]served{proton.entry.SHA256: good},
			putErr: &fs.PathError{Op: "sync", Path: "/images/.tmp-proton", Err: syscall.ENOSPC}, wantCalls: []string{"name " + name}, full: true},
		{name: "disk full sealing: no blob", byName: map[string]served{name: good}, blobs: map[string]served{proton.entry.SHA256: good},
			putErr: fmt.Errorf("enable fs-verity: %w", syscall.ENOSPC), wantCalls: []string{"name " + name}, full: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.sealer.writeErr, e.sealer.err = tt.writeErr, tt.putErr
			src := &fakeSource{byName: tt.byName, blobs: tt.blobs, local: tt.local}
			err := fetchImage(t.Context(), src, proton.entry, nil)
			if got := src.asked(); !slices.Equal(got, tt.wantCalls) {
				t.Errorf("asked %q, want %q", got, tt.wantCalls)
			}
			if (err == nil) != tt.ok {
				t.Fatalf("fetchImage = %v", err)
			}
			if err == nil {
				return
			}
			s, _ := e.service()
			s.note(proton.entry, true, err)
			if s.bad[proton.entry.SHA256] != tt.bad || unreachable(src, err) != tt.down || noSpace(err) != tt.full {
				t.Errorf("%v: bad %v, unreachable %v, no space %v", err, s.bad[proton.entry.SHA256], unreachable(src, err), noSpace(err))
			}
			if _, full := s.full[proton.entry.SHA256]; full != tt.full {
				t.Errorf("full = %v", full)
			}
			want := noDownloadText
			switch {
			case tt.full:
				want = noSpaceText
			case tt.bad:
				want = badImageText
			}
			if text := s.errs[proton.entry.ID]; text != want {
				t.Errorf("card reason %q, want %q (never the error %v)", text, want, err)
			}
		})
	}
}

// The registry's tag now names another build's image: its layer has other
// bytes, and the blob by digest is the right one.
func TestPassFetchesByDigestWhenTheTagMoved(t *testing.T) {
	e := newEnv(t)
	proton := newImage(t, "proton", "", 5000, true)
	other := newImage(t, "proton", "other", 6000, true)
	e.catalog(proton)
	var blobs sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/vaporos/manifests/" + bootedVersion:
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
				"layers": []map[string]any{{
					"mediaType": "application/vnd.vaporos.extension.v1.erofs", "digest": "sha256:" + other.entry.SHA256,
					"size": other.entry.Size, "annotations": map[string]string{"org.opencontainers.image.title": "ext-proton.raw"},
				}},
			})
		case "/v2/vaporos/blobs/sha256:" + other.entry.SHA256:
			n, _ := blobs.LoadOrStore("other", new(atomic.Int32))
			n.(*atomic.Int32).Add(1)
			w.Write(other.data)
		case "/v2/vaporos/blobs/sha256:" + proton.entry.SHA256:
			n, _ := blobs.LoadOrStore("proton", new(atomic.Int32))
			n.(*atomic.Int32).Add(1)
			w.Write(proton.data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e.cfg.Update.Source = "oci+http://" + strings.TrimPrefix(srv.URL, "http://") + "/vaporos:main"
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonNoSet})

	s, b := e.service()
	if retry := s.pass(t.Context(), b); retry {
		t.Fatal("retry")
	}
	count := func(k string) int32 {
		n, ok := blobs.Load(k)
		if !ok {
			return 0
		}
		return n.(*atomic.Int32).Load()
	}
	if count("other") != 1 || count("proton") != 1 || !sealed(proton.entry) {
		t.Fatalf("tag's layer fetched %d times, blob by digest %d times, sealed %v", count("other"), count("proton"), sealed(proton.entry))
	}
	if x := e.state(s, "proton"); x.State != StateRestartNeeded || x.Error != "" {
		t.Fatalf("proton = %+v", x)
	}
	if s.bad[proton.entry.SHA256] {
		t.Fatal("marked bad after the blob sealed")
	}
}

func TestUnreachable(t *testing.T) {
	eof := fmt.Errorf("after 5 of 10 bytes: %w", io.ErrUnexpectedEOF)
	for _, c := range []struct {
		err   error
		local bool
		want  bool
	}{
		{errRefused, false, true},
		{fmt.Errorf("x: %w", &net.DNSError{Err: "no such host", Name: "ghcr.io"}), false, true},
		{eof, false, true},
		{eof, true, false},
		{gaveUp(eof), false, true},
		{&url.Error{Op: "Get", URL: "https://ghcr.io/token", Err: context.DeadlineExceeded}, false, true},
		{fmt.Errorf("ext-proton.raw: %w", gaveUp(errors.New("HTTP 502 Bad Gateway"))), false, true},
		{errors.New("https://ghcr.io/v2/x/blobs/sha256:00: HTTP 404 Not Found"), false, false},
		{fmt.Errorf("x: %w", update.ErrChecksum), false, false},
		{update.ErrNoBlobs, false, false},
		{&fs.PathError{Op: "open", Path: "/src/manifest.json", Err: syscall.ENOENT}, true, false},
		{&writeError{syscall.EIO}, false, false},
		{nil, false, false},
	} {
		if got := unreachable(&fakeSource{local: c.local}, c.err); got != c.want {
			t.Errorf("unreachable(%v), local %v = %v", c.err, c.local, got)
		}
	}
}
