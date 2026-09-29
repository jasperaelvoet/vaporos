package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// source is where the image comes from: the live medium, a directory, or
// an http(s) URL with the same files (docs/CONTRACTS.md "Sources").
//
// This duplicates a small part of the update package's fetch layer on
// purpose: the two were written in parallel, and the installer only needs
// plain files. Once update exports its fetcher (with OCI), use that.
type source interface {
	String() string
	// open returns the named file. size is the length the manifest
	// promises, or -1 for small files read whole.
	open(ctx context.Context, name string, size int64) (io.ReadCloser, error)
	// dir is the local directory holding the files, or "" for remote ones.
	dir() string
}

func newSource(spec string, e *env) (source, error) {
	s, err := parseSource(spec)
	if err != nil {
		return nil, err
	}
	switch s.kind {
	case "live":
		return dirSource{path: config.LiveMedium, live: true}, nil
	case "dir":
		return dirSource{path: s.dir}, nil
	}
	return &httpSource{base: s.url, env: e}, nil
}

type dirSource struct {
	path string
	live bool
}

func (s dirSource) String() string {
	if s.live {
		return "the installation medium"
	}
	return s.path
}

func (s dirSource) open(_ context.Context, name string, _ int64) (io.ReadCloser, error) {
	return os.Open(filepath.Join(s.path, name))
}

func (s dirSource) dir() string { return s.path }

type httpSource struct {
	base *url.URL
	env  *env
}

func (s *httpSource) String() string { return s.base.Redacted() }
func (s *httpSource) dir() string    { return "" }

func (s *httpSource) open(ctx context.Context, name string, size int64) (io.ReadCloser, error) {
	r := &httpReader{ctx: ctx, env: s.env, url: s.base.JoinPath(name).String(), size: size}
	// Connect now, so a missing file fails here rather than mid-copy.
	if err := r.connect(); err != nil {
		return nil, err
	}
	return r, nil
}

// httpMaxRetries and httpIdleTimeout bound how long a flaky download may
// take. Variables for tests.
var (
	httpMaxRetries  = 5
	httpIdleTimeout = 60 * time.Second
)

// httpReader streams one file and survives dropped connections by
// resuming with a Range request at the current offset. The caller hashes
// what it reads, so a resumed stream is verified like any other.
type httpReader struct {
	ctx  context.Context
	env  *env
	url  string
	size int64 // expected length; -1 when unknown
	off  int64

	body   io.ReadCloser
	cancel context.CancelFunc
	idle   *time.Timer
	fails  int
	mu     sync.Mutex
}

type statusError struct {
	url    string
	status int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("GET %s: %d %s", e.url, e.status, http.StatusText(e.status))
}

// retryable: 5xx, 408 and 429 are worth another try, other statuses are not.
func (e *statusError) retryable() bool {
	return e.status >= 500 || e.status == http.StatusRequestTimeout || e.status == http.StatusTooManyRequests
}

func (r *httpReader) connect() error {
	for {
		err := r.request()
		if err == nil {
			return nil
		}
		if !r.retry(err) {
			return err
		}
	}
}

// request opens the body at r.off.
func (r *httpReader) request() error {
	ctx, cancel := context.WithCancel(r.ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		cancel()
		return err
	}
	if r.off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", r.off))
	}
	resp, err := r.env.httpClient.Do(req)
	if err != nil {
		cancel()
		return err
	}
	switch {
	case resp.StatusCode == http.StatusPartialContent && r.off > 0:
		if start, ok := contentRangeStart(resp.Header.Get("Content-Range")); !ok || start != r.off {
			resp.Body.Close()
			cancel()
			return fmt.Errorf("GET %s: server resumed at the wrong offset", r.url)
		}
	case resp.StatusCode == http.StatusOK:
		// A server that ignores Range sends everything again; skip what
		// was already read.
		if r.off > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, r.off); err != nil {
				resp.Body.Close()
				cancel()
				return err
			}
		}
	default:
		resp.Body.Close()
		cancel()
		return &statusError{url: r.url, status: resp.StatusCode}
	}
	r.body, r.cancel = resp.Body, cancel
	// Cancelling the request context is the only way to interrupt a body
	// read that has stalled; the timer does that after a quiet minute.
	r.idle = time.AfterFunc(httpIdleTimeout, cancel)
	return nil
}

func contentRangeStart(h string) (int64, bool) {
	rest, ok := strings.CutPrefix(h, "bytes ")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(start, 10, 64)
	return n, err == nil
}

// retry records a failure and waits before the next attempt; false means
// give up with err.
func (r *httpReader) retry(err error) bool {
	var se *statusError
	if r.ctx.Err() != nil || (errors.As(err, &se) && !se.retryable()) {
		return false
	}
	r.fails++
	if r.fails > httpMaxRetries {
		return false
	}
	r.env.logf("install: %s: %v; retrying (%d/%d)", r.url, err, r.fails, httpMaxRetries)
	return r.env.sleep(r.ctx, time.Duration(r.fails)*time.Second) == nil
}

func (r *httpReader) closeBody() {
	if r.body != nil {
		r.idle.Stop()
		r.body.Close()
		r.cancel()
		r.body = nil
	}
}

func (r *httpReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if r.size >= 0 && r.off >= r.size {
			r.closeBody()
			return 0, io.EOF
		}
		if r.body == nil {
			if err := r.connect(); err != nil {
				return 0, err
			}
		}
		if r.size >= 0 && int64(len(p)) > r.size-r.off {
			p = p[:r.size-r.off]
		}
		n, err := r.body.Read(p)
		r.off += int64(n)
		if n > 0 {
			r.fails = 0
			r.idle.Reset(httpIdleTimeout)
		}
		switch {
		case err == nil:
			return n, nil
		case err == io.EOF && (r.size < 0 || r.off >= r.size):
			r.closeBody()
			return n, io.EOF
		}
		// A dropped connection or a short body: reconnect at r.off.
		r.closeBody()
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		if n > 0 {
			return n, nil
		}
		if !r.retry(err) {
			return 0, err
		}
	}
}

func (r *httpReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeBody()
	return nil
}

// readSmall reads a whole small file (manifest, signature) from src.
func readSmall(ctx context.Context, src source, name string, limit int64) ([]byte, error) {
	r, err := src.open(ctx, name, -1)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return b, nil
}

// loadManifest fetches manifest.json and its signature from src and
// accepts it only if a trusted key (config.KeysDir) signed it.
func loadManifest(ctx context.Context, e *env, src source) (*manifest.Manifest, error) {
	b, err := readSmall(ctx, src, "manifest.json", 1<<20)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no VaporOS image found on %s (manifest.json is missing)", src)
		}
		return nil, fmt.Errorf("reading the manifest from %s: %w", src, err)
	}
	sig, err := readSmall(ctx, src, "manifest.json.sig", 64<<10)
	if err != nil {
		return nil, fmt.Errorf("reading the manifest signature from %s: %w", src, err)
	}
	if err := e.verifyManifest(b, sig, config.KeysDir); err != nil {
		return nil, fmt.Errorf("the image on %s is not signed by a trusted key: %w", src, err)
	}
	m, err := e.parseManifest(b)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := checkManifest(m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return m, nil
}

var (
	versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	sha256RE  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// bootNames are the file names boot.InstallEntry copies from its source
// directory, keyed by manifest artifact.
var bootNames = map[string]string{"kernel": "vmlinuz", "initrd": "initramfs.img"}

// checkManifest re-checks what the installer relies on, whatever
// manifest.Parse already did: the version becomes a path on the ESP and
// artifact names become paths in the source.
func checkManifest(m *manifest.Manifest) error {
	if m.Schema != manifest.Schema {
		return fmt.Errorf("unsupported schema %d", m.Schema)
	}
	if m.MinUpdater > manifest.UpdaterVersion {
		return fmt.Errorf("this image needs a newer installer (min_updater %d)", m.MinUpdater)
	}
	if !versionRE.MatchString(m.Version) {
		return fmt.Errorf("invalid version %q", m.Version)
	}
	for _, key := range []string{"root", "kernel", "initrd"} {
		a, ok := m.Artifacts[key]
		switch {
		case !ok:
			return fmt.Errorf("no %s artifact", key)
		case a.Name == "" || a.Name != filepath.Base(a.Name) || a.Name == "." || a.Name == "..":
			return fmt.Errorf("%s artifact has an invalid name %q", key, a.Name)
		case a.Size <= 0:
			return fmt.Errorf("%s artifact has an invalid size", key)
		case !sha256RE.MatchString(a.SHA256):
			return fmt.Errorf("%s artifact has an invalid sha256", key)
		}
	}
	return nil
}
