package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

func TestIsNetworkError(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{fmt.Errorf("x: %w", &net.DNSError{Err: "no such host", Name: "ghcr.io"}), true},
		{&url.Error{Op: "Get", URL: "https://ghcr.io/token", Err: context.DeadlineExceeded}, true},
		{fmt.Errorf("ext-proton.raw: %w after 10 attempts: HTTP 502 Bad Gateway", ErrGaveUp), true},
		{errors.New("ext-proton.raw: giving up after 10 attempts: HTTP 502 Bad Gateway"), false}, // only the sentinel counts
		{fmt.Errorf("after 5 of 10 bytes: %w", io.ErrUnexpectedEOF), false},
		{errors.New("https://ghcr.io/v2/x/blobs/sha256:00: HTTP 404 Not Found"), false},
		{fmt.Errorf("x: %w", ErrChecksum), false},
		{ErrNoBlobs, false},
		{&fs.PathError{Op: "open", Path: "/src/manifest.json", Err: syscall.ENOENT}, false},
		{nil, false},
	} {
		if got := IsNetworkError(c.err); got != c.want {
			t.Errorf("IsNetworkError(%v) = %v", c.err, got)
		}
	}
}

// A download that gives up says so with ErrGaveUp; a short file in a
// directory source is only an early end, from a source that is not remote.
func TestGaveUpAndRemote(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	src, err := OpenSource(srv.URL+"/", "main")
	if err != nil {
		t.Fatal(err)
	}
	a := manifest.Artifact{Name: "ext-proton.raw", Size: 10, SHA256: fmt.Sprintf("%064x", 0)}
	err = src.Fetch(t.Context(), a, io.Discard, nil)
	if !errors.Is(err, ErrGaveUp) || !IsNetworkError(err) || !src.Remote() {
		t.Fatalf("http: %v (gave up %v), remote %v", err, errors.Is(err, ErrGaveUp), src.Remote())
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, a.Name), []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	if src, err = OpenSource(dir, "main"); err != nil {
		t.Fatal(err)
	}
	err = src.Fetch(t.Context(), a, io.Discard, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) || IsNetworkError(err) || src.Remote() {
		t.Fatalf("directory: %v, network %v, remote %v", err, IsNetworkError(err), src.Remote())
	}
}
