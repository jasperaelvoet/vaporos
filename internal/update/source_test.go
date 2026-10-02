package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

func TestOCISourceTokenManifestAndResume(t *testing.T) {
	e := setup(t)
	img := e.makeImage("20260930.000000", 200, 300<<10, nil)
	f := newFakeRegistry(t, img)
	f.failRoot.Store(true)

	src, err := OpenSource(f.spec(), "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m, err := src.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "20260930.000000" {
		t.Fatalf("version %s", m.Version)
	}
	if n := f.tokenReqs.Load(); n != 1 {
		t.Fatalf("token requests: %d", n)
	}

	var buf bytes.Buffer
	if err := src.Fetch(ctx, m.Artifact(manifest.Root), &buf, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), img.root()) {
		t.Fatal("root content differs")
	}
	half := len(img.root()) / 2
	f.mu.Lock()
	ranges, sigs := f.rootRanges, f.rootSigs
	f.mu.Unlock()
	if len(ranges) != 2 || ranges[0] != "" || ranges[1] != "bytes="+strconv.Itoa(half)+"-" {
		t.Fatalf("storage saw ranges %q", ranges)
	}
	// The resume went back to the registry for a fresh signed URL.
	if f.rootBlobReqs.Load() != 2 || sigs[0] == sigs[1] {
		t.Fatalf("blob requests %d, signatures %q", f.rootBlobReqs.Load(), sigs)
	}
}

func TestOCISourceIndex(t *testing.T) {
	e := setup(t)
	img := e.makeImage("20260930.000000", 200, 1000, nil)
	f := newFakeRegistry(t, img)
	f.serveIndex = true
	src, _ := OpenSource(f.spec(), "")
	if _, err := src.Manifest(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOCISourceRejects(t *testing.T) {
	e := setup(t)
	img := e.makeImage("20260930.000000", 200, 1000, nil)

	t.Run("unknown tag", func(t *testing.T) {
		f := newFakeRegistry(t, img)
		src, _ := OpenSource(f.spec(), "nope")
		start := time.Now()
		_, err := src.Manifest(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no such tag") {
			t.Fatalf("got %v", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("a 404 was retried")
		}
	})

	t.Run("layer is not the signed file", func(t *testing.T) {
		f := newFakeRegistry(t, img)
		// Swap the root layer for other bytes: the layer digest no longer
		// matches the sha256 in the signed manifest.
		other := e.makeImage("20260930.000000", 201, 1000, nil)
		delete(f.blobs, f.rootDigest)
		layers := strings.Replace(string(f.manifest), f.rootDigest, sha(other.root()), 1)
		f.manifest = []byte(layers)
		f.rootDigest = sha(other.root())
		f.blobs[f.rootDigest] = other.root()
		src, _ := OpenSource(f.spec(), "main")
		_, err := src.Manifest(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not the file the manifest signs") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("tampered manifest", func(t *testing.T) {
		bad := *img
		bad.files = map[string][]byte{}
		for k, v := range img.files {
			bad.files[k] = v
		}
		bad.files["manifest.json"] = bytes.Replace(img.files["manifest.json"], []byte(`"rollback_index":200`), []byte(`"rollback_index":900`), 1)
		f := newFakeRegistry(t, &bad)
		src, _ := OpenSource(f.spec(), "main")
		if _, err := src.Manifest(context.Background()); !errors.Is(err, manifest.ErrBadSignature) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestParseOCISpec(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		spec, channel                  string
		scheme, host, repo, ref, print string
	}{
		{"oci://ghcr.io/jasperaelvoet/vaporos", "", "https", "ghcr.io", "jasperaelvoet/vaporos", "main", "oci://ghcr.io/jasperaelvoet/vaporos:main"},
		{"oci://ghcr.io/jasperaelvoet/vaporos", "dev-tooling", "https", "ghcr.io", "jasperaelvoet/vaporos", "dev-tooling", ""},
		{"oci://ghcr.io/a/b:20260929.123456", "main", "https", "ghcr.io", "a/b", "20260929.123456", ""},
		{"oci://localhost:5000/a/b", "", "https", "localhost:5000", "a/b", "main", ""},
		{"oci+http://10.0.0.2:5000/vos", "x", "http", "10.0.0.2:5000", "vos", "x", "oci+http://10.0.0.2:5000/vos:x"},
		{"oci://ghcr.io/a/b@" + d, "main", "https", "ghcr.io", "a/b", d, "oci://ghcr.io/a/b@" + d},
	}
	for _, c := range cases {
		o, err := newOCI(c.spec, c.channel)
		if err != nil {
			t.Errorf("%s: %v", c.spec, err)
			continue
		}
		if o.scheme != c.scheme || o.host != c.host || o.repo != c.repo || o.ref != c.ref {
			t.Errorf("%s: got %s %s %s %s", c.spec, o.scheme, o.host, o.repo, o.ref)
		}
		if c.print != "" && o.String() != c.print {
			t.Errorf("%s: String() = %s", c.spec, o.String())
		}
	}
	for _, bad := range []struct{ spec, channel string }{
		{"oci://ghcr.io", ""}, {"oci://ghcr.io/", ""}, {"oci://ghcr.io/UPPER/case", ""},
		{"oci://ghcr.io/a/b", "feature/x"}, {"oci://ghcr.io/a/b@sha256:short", ""},
	} {
		if _, err := newOCI(bad.spec, bad.channel); err == nil {
			t.Errorf("accepted %q channel %q", bad.spec, bad.channel)
		}
	}
	if _, err := OpenSource("ftp://x/y", ""); err == nil {
		t.Error("accepted ftp://")
	}
	if _, err := OpenSource("", ""); err == nil {
		t.Error("accepted an empty source")
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, p := parseChallenge(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:a/b:pull,push"`)
	if scheme != "Bearer" || p["realm"] != "https://ghcr.io/token" || p["service"] != "ghcr.io" || p["scope"] != "repository:a/b:pull,push" {
		t.Fatalf("%s %v", scheme, p)
	}
	scheme, p = parseChallenge(`Basic realm="x \"y\""`)
	if scheme != "Basic" || p["realm"] != `x "y"` {
		t.Fatalf("%s %v", scheme, p)
	}
}

// An HTTP directory whose server stalls half way: the stall timer drops
// the connection and the download resumes with Range.
func TestHTTPDirStallResumes(t *testing.T) {
	e := setup(t)
	stallTimeout = 200 * time.Millisecond
	img := e.makeImage("20260930.000000", 200, 64<<10, nil)
	var calls atomic.Int32
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dir/")
		data, ok := img.files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if name == "root.erofs" {
			ranges = append(ranges, r.Header.Get("Range"))
			if calls.Add(1) == 1 {
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				w.Write(data[:1000])
				w.(http.Flusher).Flush()
				<-r.Context().Done() // hang until the client gives up
				return
			}
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()

	src, err := OpenSource(srv.URL+"/dir", "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := src.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := src.Fetch(context.Background(), m.Artifact(manifest.Root), &buf, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), img.root()) {
		t.Fatal("content differs")
	}
	if len(ranges) != 2 || ranges[1] != "bytes=1000-" {
		t.Fatalf("ranges %q", ranges)
	}
}

// A server that ignores Range still works: the bytes already written are
// skipped.
func TestResumeWithoutRangeSupport(t *testing.T) {
	setup(t)
	data := bytes.Repeat([]byte("0123456789"), 5000)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if calls.Add(1) == 1 {
			w.Write(data[:777])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		w.Write(data) // 200, whole file
	}))
	defer srv.Close()
	f, _ := newHTTPDir(srv.URL)
	r := &resumeReader{ctx: context.Background(), f: f, name: "x", size: int64(len(data))}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, err %v", len(got), err)
	}
}

// A server without Range support resends the whole file after a drop. The
// part we already have arrives at network speed too: skipping it for longer
// than stallTimeout is progress, not a stall.
func TestResumeLongSkipWithoutRangeSupport(t *testing.T) {
	setup(t)
	oldStall := stallTimeout
	stallTimeout = 100 * time.Millisecond
	defer func() { stallTimeout = oldStall }()
	chunk := bytes.Repeat([]byte("x"), 1000)
	const chunks, dropAt = 40, 25 // 25 chunks take 250 ms: more than a stall
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(chunks*len(chunk)))
		first := calls.Add(1) == 1
		for i := range chunks {
			if first && i == dropAt {
				panic(http.ErrAbortHandler)
			}
			w.Write(chunk)
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer srv.Close()
	f, _ := newHTTPDir(srv.URL)
	r := &resumeReader{ctx: context.Background(), f: f, name: "x", size: chunks * int64(len(chunk))}
	got, err := io.ReadAll(r)
	if err != nil || len(got) != chunks*len(chunk) || calls.Load() != 2 {
		t.Fatalf("read %d bytes in %d requests, err %v", len(got), calls.Load(), err)
	}
}

// Permanent failures are not retried; transient ones are, but not forever.
func TestResumeGivesUp(t *testing.T) {
	setup(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/gone") {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	f, _ := newHTTPDir(srv.URL)

	_, err := io.ReadAll(&resumeReader{ctx: context.Background(), f: f, name: "gone", size: 10})
	var he *httpError
	if !errors.As(err, &he) || he.status != 404 || calls.Load() != 1 || errors.Is(err, ErrRetriesExhausted) {
		t.Fatalf("404: err %v after %d calls", err, calls.Load())
	}
	calls.Store(0)
	_, err = io.ReadAll(&resumeReader{ctx: context.Background(), f: f, name: "busy", size: 10})
	if err == nil || int(calls.Load()) != maxAttempts {
		t.Fatalf("503: err %v after %d calls", err, calls.Load())
	}
	// Other packages tell a source that gave up from one that answered.
	want := "busy: giving up after " + strconv.Itoa(maxAttempts) + " attempts: "
	if !errors.Is(err, ErrRetriesExhausted) || !errors.As(err, &he) || he.status != 503 || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("503: %v", err)
	}
}

func TestRedactHidesSignedURLs(t *testing.T) {
	got := redact("https://user:pw@storage.example/blob?X-Amz-Signature=secret")
	if strings.Contains(got, "secret") || strings.Contains(got, "pw") {
		t.Fatal(got)
	}
}
