package install

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// flakyServer serves dir, but the first GET of each name in cut returns
// only half the body and then drops the connection.
type flakyServer struct {
	dir string
	cut map[string]bool

	mu       sync.Mutex
	requests []string
}

func (s *flakyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/images/")
	s.mu.Lock()
	s.requests = append(s.requests, name+" "+r.Header.Get("Range"))
	cut := s.cut[name]
	s.cut[name] = false
	s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if cut {
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusOK)
		w.Write(b[:len(b)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}

func TestInstallFromHTTP(t *testing.T) {
	f, _ := installMachine(t)
	srcDir := t.TempDir()
	img := writeImage(t, srcDir, 3<<20+7)
	fs := &flakyServer{dir: srcDir, cut: map[string]bool{"root.erofs": true, "vmlinuz": true}}
	srv := httptest.NewServer(fs)
	defer srv.Close()

	r := f.runner()
	var recs []progressRec
	if err := runInstall(context.Background(), f.env(r), Options{Disk: "sda", Source: srv.URL + "/images"}, recorder(&recs)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f.path("dev/sda2")); !bytes.Equal(got, img.root) {
		t.Error("slot a differs from the served image")
	}
	// Kernel and initrd were staged in a temp dir that is gone again.
	if f.entry.srcDir == config.LiveMedium || exists(f.entry.srcDir) {
		t.Errorf("entry source %q", f.entry.srcDir)
	}
	// Both cut downloads resumed where they stopped.
	fs.mu.Lock()
	reqs := strings.Join(fs.requests, "|")
	fs.mu.Unlock()
	half := strconv.Itoa(len(img.root) / 2)
	if !strings.Contains(reqs, "root.erofs bytes="+half+"-") {
		t.Errorf("root.erofs did not resume at %s: %s", half, reqs)
	}
	if !strings.Contains(reqs, "vmlinuz bytes=") {
		t.Errorf("vmlinuz did not resume: %s", reqs)
	}
}

func TestHTTPReader(t *testing.T) {
	f := newFakeSys(t)
	data := randomBytes(t, 100_000)
	var mu sync.Mutex
	fails, ignoreRange := 0, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/missing":
			http.NotFound(w, r)
		case fails > 0:
			fails--
			http.Error(w, "busy", http.StatusServiceUnavailable)
		case ignoreRange && r.Header.Get("Range") != "":
			w.Write(data)
		default:
			http.ServeContent(w, r, "x", time.Time{}, bytes.NewReader(data))
		}
	}))
	defer srv.Close()
	e := f.env(f.runner())
	src, _ := newSource(srv.URL, e)

	if _, err := src.open(context.Background(), "missing", -1); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing file: %v", err)
	}

	fails = 2
	rc, err := src.open(context.Background(), "file", int64(len(data)))
	if err != nil {
		t.Fatalf("503 twice then OK: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, %v", len(got), err)
	}

	httpMaxRetries = 1
	fails = 5
	if _, err := src.open(context.Background(), "file", 10); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("persistent 503: %v", err)
	}
	fails = 0

	// A server that ignores Range: the reader skips what it already has.
	ignoreRange = true
	r := &httpReader{ctx: context.Background(), env: e, url: srv.URL + "/file", size: int64(len(data)), off: 40_000}
	got, err = io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(got, data[40_000:]) {
		t.Errorf("resume without Range support: %d bytes, %v", len(got), err)
	}

	// A body that is longer than promised stops at the promised size.
	r = &httpReader{ctx: context.Background(), env: e, url: srv.URL + "/file", size: 1000}
	got, _ = io.ReadAll(r)
	r.Close()
	if len(got) != 1000 {
		t.Errorf("read %d bytes, want 1000", len(got))
	}
}

func TestHTTPReaderStall(t *testing.T) {
	f := newFakeSys(t)
	httpIdleTimeout = 50 * time.Millisecond
	data := randomBytes(t, 10_000)
	var mu sync.Mutex
	stalled := false
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		first := !stalled
		stalled = true
		mu.Unlock()
		if first {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Write(data[:100])
			w.(http.Flusher).Flush()
			select { // stall until the client gives up
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		http.ServeContent(w, r, "x", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	defer close(release)
	src, _ := newSource(srv.URL, f.env(f.runner()))
	rc, err := src.open(context.Background(), "f", int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stalled download: %d bytes, %v", len(got), err)
	}
}

func TestContentRangeStart(t *testing.T) {
	for in, want := range map[string]int64{"bytes 100-199/200": 100, "bytes 0-0/1": 0} {
		if got, ok := contentRangeStart(in); !ok || got != want {
			t.Errorf("contentRangeStart(%q) = %d, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "bytes */200", "items 1-2/3"} {
		if _, ok := contentRangeStart(bad); ok {
			t.Errorf("contentRangeStart(%q) accepted", bad)
		}
	}
}
