package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// The five files every source serves (docs/CONTRACTS.md "Update format").
const (
	ManifestName  = "manifest.json"
	SignatureName = "manifest.json.sig"
)

const (
	maxManifestSize  = 1 << 20
	maxSignatureSize = 4 << 10
	maxRedirects     = 5
)

// ChunkSize is the unit of every read and write while streaming an image:
// large sequential writes are what a slot partition wants.
const ChunkSize = 4 << 20

// ErrChecksum means a file's content does not match the signed manifest.
var ErrChecksum = errors.New("checksum mismatch")

// ErrRetriesExhausted means a download of a remote source kept failing in
// a way worth retrying (network errors, stalls, server errors) until it
// gave up; the last failure is wrapped too.
var ErrRetriesExhausted = errors.New("giving up")

// errNoRanges means a server answered a byte range with the whole file.
var errNoRanges = errors.New("the server does not support byte ranges")

// Retry policy for downloads. Variables so tests can shrink them.
var (
	maxAttempts  = 10               // consecutive failures without progress
	stallTimeout = 60 * time.Second // no bytes for this long: reconnect
	backoff      = func(attempt int) time.Duration {
		return min(time.Second<<min(attempt-1, 5), 30*time.Second)
	}
)

// httpClient is shared by every remote source. It has no overall timeout
// (an image takes minutes); stalls are caught per connection instead.
var httpClient = &http.Client{Transport: newTransport()}

// An HTTP/2 connection that goes quiet this long is pinged, and closed
// when the ping gets no answer in time. Otherwise a dead connection stays
// in the pool, and every retry of every request on it stalls again.
// Variables so tests can shorten them.
var (
	h2PingAfter   = 15 * time.Second
	h2PingTimeout = 15 * time.Second
)

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          8,
		ForceAttemptHTTP2:     true,
		HTTP2:                 &http.HTTP2Config{SendPingTimeout: h2PingAfter, PingTimeout: h2PingTimeout},
	}
}

// Source is where images come from: an OCI registry, an HTTP(S)
// directory, a local directory or the live medium. Each serves the same
// five files. The installer uses Source as well: Manifest, then WriteRoot
// and FetchBootFiles.
type Source struct {
	f fetcher

	mu sync.Mutex
	m  *manifest.Manifest
}

// fetcher is one kind of source.
type fetcher interface {
	// open returns name's content from byte offset on: up to byte end
	// (exclusive), or to the end of the file when end < 0. A bounded open
	// that the source cannot serve as a range fails with errNoRanges rather
	// than return the whole file.
	open(ctx context.Context, name string, offset, end int64) (io.ReadCloser, error)
	// remote reports whether failures are worth retrying (network) or not
	// (a local file reads the same the second time).
	remote() bool
	String() string
}

// digester is implemented by fetchers that know every file's digest before
// downloading it (OCI layers), so a mismatch fails before any download.
type digester interface {
	digest(ctx context.Context, name string) (digest string, size int64, err error)
}

// OpenSource parses a source spec:
//
//	oci://registry/repo[:tag|@sha256:…]  tag defaults to channel, then "main"
//	oci+http://registry/repo             the same over plain HTTP (dev registries)
//	http(s)://host/dir/                  the five files by name
//	file:///dir, /dir, dir               a local directory
//	live                                 the live medium (/run/vos/medium/vos)
//
// Nothing is fetched until Manifest or Open is called.
func OpenSource(spec, channel string) (*Source, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return nil, errors.New("no update source configured")
	case spec == "live":
		return LiveSource(), nil
	case strings.HasPrefix(spec, "oci://"), strings.HasPrefix(spec, "oci+http://"):
		f, err := newOCI(spec, channel)
		if err != nil {
			return nil, err
		}
		return &Source{f: f}, nil
	case strings.HasPrefix(spec, "http://"), strings.HasPrefix(spec, "https://"):
		f, err := newHTTPDir(spec)
		if err != nil {
			return nil, err
		}
		return &Source{f: f}, nil
	case strings.HasPrefix(spec, "file://"):
		u, err := url.Parse(spec)
		if err != nil {
			return nil, fmt.Errorf("update source %q: %w", spec, err)
		}
		return dirSource(u.Path)
	case strings.Contains(spec, "://"):
		return nil, fmt.Errorf("unsupported update source %q", spec)
	}
	return dirSource(spec)
}

// LiveSource is the ISO the live system booted from.
func LiveSource() *Source { return &Source{f: dirFetcher{dir: config.LiveMedium}} }

func dirSource(dir string) (*Source, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("update source: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("update source %s is not a directory", abs)
	}
	return &Source{f: dirFetcher{dir: abs}}, nil
}

func (s *Source) String() string { return s.f.String() }

// Manifest fetches manifest.json and its signature, verifies the signature
// against the trusted keys (config.KeysDir), parses the manifest and, for
// OCI, checks that every layer is the file the manifest describes. The
// result is cached.
func (s *Source) Manifest(ctx context.Context) (*manifest.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m != nil {
		return s.m, nil
	}
	raw, err := s.readSmall(ctx, ManifestName, maxManifestSize)
	if err != nil {
		return nil, err
	}
	sig, err := s.readSmall(ctx, SignatureName, maxSignatureSize)
	if err != nil {
		return nil, err
	}
	if err := manifest.Verify(raw, sig, config.KeysDir); err != nil {
		return nil, fmt.Errorf("%s: %w", s, err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s, err)
	}
	if d, ok := s.f.(digester); ok {
		for _, key := range manifest.RequiredArtifacts {
			a := m.Artifact(key)
			dg, size, err := d.digest(ctx, a.Name)
			if err != nil {
				return nil, err
			}
			if dg != "sha256:"+a.SHA256 || size != a.Size {
				return nil, fmt.Errorf("%s: layer %s (%s, %d bytes) is not the file the manifest signs (sha256:%s, %d bytes)",
					s, a.Name, dg, size, a.SHA256, a.Size)
			}
		}
	}
	s.m = m
	return m, nil
}

// readSmall reads a whole small file, at most limit bytes.
func (s *Source) readSmall(ctx context.Context, name string, limit int64) ([]byte, error) {
	r := s.Open(ctx, name, -1)
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", s, name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: %s is larger than %d bytes", s, name, limit)
	}
	if d, ok := s.f.(digester); ok {
		dg, _, err := d.digest(ctx, name)
		if err != nil {
			return nil, err
		}
		if sum := sha256.Sum256(b); dg != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("%s: %s does not match its layer digest", s, name)
		}
	}
	return b, nil
}

// Open streams name. With size >= 0 the reader yields exactly size bytes:
// a connection that drops early is resumed where it stopped (a fresh
// request, which for OCI means a fresh redirect, with Range), and a
// connection that stalls is replaced.
func (s *Source) Open(ctx context.Context, name string, size int64) io.ReadCloser {
	return &resumeReader{ctx: ctx, f: s.f, name: name, size: size}
}

// OpenRange streams bytes [off, end) of name as byte range requests,
// resuming like Open. A source that answers a range with the whole file
// fails with errNoRanges.
func (s *Source) OpenRange(ctx context.Context, name string, off, end int64) io.ReadCloser {
	return &resumeReader{ctx: ctx, f: s.f, name: name, off: off, size: end, ranged: true}
}

// Fetch streams artifact a into w, verifying its size and sha256. onChunk,
// if set, runs after every chunk with the bytes done so far; an error from
// it stops the copy.
func (s *Source) Fetch(ctx context.Context, a manifest.Artifact, w io.Writer, onChunk func(done int64) error) error {
	r := s.Open(ctx, a.Name, a.Size)
	defer r.Close()
	if err := copyVerified(r, w, a.Size, a.SHA256, onChunk); err != nil {
		return fmt.Errorf("%s: %w", a.Name, err)
	}
	return nil
}

// copyVerified copies exactly size bytes from r to w (w may be nil) in
// ChunkSize pieces, hashing them, and fails unless the sha256 is want.
func copyVerified(r io.Reader, w io.Writer, size int64, want string, onChunk func(done int64) error) error {
	buf := make([]byte, min(int64(ChunkSize), max(size, 1)))
	h := sha256.New()
	var done int64
	for done < size {
		n := min(int64(len(buf)), size-done)
		k, err := io.ReadFull(r, buf[:n])
		if k > 0 {
			h.Write(buf[:k])
			if w != nil {
				if _, werr := w.Write(buf[:k]); werr != nil {
					return werr
				}
			}
			done += int64(k)
			if onChunk != nil {
				if cerr := onChunk(done); cerr != nil {
					return cerr
				}
			}
		}
		if err != nil {
			return fmt.Errorf("after %d of %d bytes: %w", done, size, err)
		}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%w: sha256 %s, want %s", ErrChecksum, got, want)
	}
	return nil
}

// resumeReader reads one file from a fetcher, reconnecting at the current
// offset whenever a remote connection fails or stalls.
type resumeReader struct {
	ctx    context.Context
	f      fetcher
	name   string
	off    int64
	size   int64 // where reading stops; -1 when unknown
	ranged bool  // ask for [off, size) only

	body   io.ReadCloser
	cancel context.CancelFunc
	stall  *time.Timer
	fails  int
	eof    bool // the whole file has been read
}

func (r *resumeReader) Read(p []byte) (int, error) {
	if r.eof {
		return 0, io.EOF
	}
	if r.size >= 0 {
		if r.off >= r.size {
			r.closeBody()
			return 0, io.EOF
		}
		p = p[:min(int64(len(p)), r.size-r.off)]
	}
	for {
		if err := r.ctx.Err(); err != nil {
			r.closeBody()
			return 0, err
		}
		if r.body == nil {
			if err := r.connect(); err != nil {
				if err := r.retry(err); err != nil {
					return 0, err
				}
				continue
			}
		}
		n, err := r.body.Read(p)
		if n > 0 {
			r.off += int64(n)
			r.fails = 0
			r.stall.Reset(stallTimeout)
		}
		if err == nil {
			return n, nil
		}
		if errors.Is(err, io.EOF) {
			if r.size < 0 || r.off == r.size {
				r.closeBody()
				r.eof = true
				if n > 0 {
					return n, nil
				}
				return 0, io.EOF
			}
			err = io.ErrUnexpectedEOF // the connection ended early
		}
		r.closeBody()
		if n > 0 {
			return n, nil // hand over what arrived; the next Read reconnects
		}
		if err := r.retry(err); err != nil {
			return 0, err
		}
	}
}

// connect opens the file at the current offset. The stall timer covers
// the request too: it cancels a connection that delivers nothing for
// stallTimeout, and Read then reconnects.
func (r *resumeReader) connect() error {
	ctx, cancel := context.WithCancel(r.ctx)
	stall := time.AfterFunc(stallTimeout, cancel)
	end := int64(-1)
	if r.ranged {
		end = r.size
	}
	body, err := r.f.open(ctx, r.name, r.off, end)
	if sb, ok := body.(*skipBody); err == nil && ok {
		// Skipped bytes are arriving bytes: a long skip is not a stall.
		err = sb.skip(func() { stall.Reset(stallTimeout) })
	}
	if err != nil {
		stall.Stop()
		if body != nil {
			body.Close()
		}
		cancel()
		return err
	}
	r.body, r.cancel, r.stall = body, cancel, stall
	return nil
}

// skipBody is a whole-file answer to a Range request: its first n bytes are
// ones the reader already has. connect skips them, where the stall timer
// can see the progress.
type skipBody struct {
	io.ReadCloser
	n int64
}

func (b *skipBody) skip(progress func()) error {
	buf := make([]byte, 64<<10)
	for b.n > 0 {
		k, err := b.ReadCloser.Read(buf[:min(int64(len(buf)), b.n)])
		b.n -= int64(k)
		if k > 0 {
			progress()
		}
		if b.n == 0 {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// retry decides whether err is worth another attempt and waits the
// backoff. It returns the error to give up with, or nil to go on.
func (r *resumeReader) retry(err error) error {
	if cerr := r.ctx.Err(); cerr != nil {
		return cerr
	}
	// A stall cancels only the connection's own context, so a Canceled
	// error while r.ctx is alive is a stall and worth retrying.
	if !r.f.remote() || !retryable(err) {
		return err
	}
	r.fails++
	if r.fails >= maxAttempts {
		return fmt.Errorf("%s: %w after %d attempts: %w", r.name, ErrRetriesExhausted, r.fails, err)
	}
	t := time.NewTimer(backoff(r.fails))
	defer t.Stop()
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-t.C:
		return nil
	}
}

func (r *resumeReader) closeBody() {
	if r.body == nil {
		return
	}
	r.stall.Stop()
	r.body.Close()
	r.cancel()
	r.body, r.cancel, r.stall = nil, nil, nil
}

func (r *resumeReader) Close() error {
	r.closeBody()
	return nil
}

// permanentError marks a failure that retrying cannot fix.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// httpError is an unexpected HTTP status.
type httpError struct {
	status int
	url    string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("%s: HTTP %d %s", e.url, e.status, http.StatusText(e.status))
}

func retryable(err error) bool {
	var pe *permanentError
	if errors.As(err, &pe) {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		return he.status >= 500 || he.status == http.StatusRequestTimeout || he.status == http.StatusTooManyRequests
	}
	return true // network errors, early EOF, stalls
}

// dirFetcher reads a local directory (also the live medium).
type dirFetcher struct{ dir string }

// open ignores end: the reader stops where it wants, and nothing more is read.
func (d dirFetcher) open(ctx context.Context, name string, offset, end int64) (io.ReadCloser, error) {
	f, err := os.Open(filepath.Join(d.dir, name))
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

func (d dirFetcher) remote() bool   { return false }
func (d dirFetcher) String() string { return d.dir }

// httpFetcher reads a directory served over HTTP(S).
type httpFetcher struct{ base *url.URL }

func newHTTPDir(spec string) (*httpFetcher, error) {
	u, err := url.Parse(spec)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid update source %q", spec)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawQuery, u.Fragment = "", ""
	return &httpFetcher{base: u}, nil
}

func (h *httpFetcher) open(ctx context.Context, name string, offset, end int64) (io.ReadCloser, error) {
	resp, err := get(ctx, h.base.JoinPath(name).String(), nil, offset, end)
	if err != nil {
		return nil, err
	}
	return finish(ctx, resp, offset, end)
}

func (h *httpFetcher) remote() bool   { return true }
func (h *httpFetcher) String() string { return h.base.String() }

// get sends one GET without following redirects (finish does that by hand,
// so a registry token never reaches a storage URL). offset > 0 adds Range,
// as does end >= 0, which bounds it (exclusive).
func get(ctx context.Context, u string, hdr http.Header, offset, end int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &permanentError{fmt.Errorf("invalid URL %s", redact(u))}
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "vaporos-updater/"+config.BinaryVersion)
	switch {
	case end >= 0:
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+strconv.FormatInt(end-1, 10))
	case offset > 0:
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	c := *httpClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		// Signed storage URLs carry credentials in the query: keep them
		// out of errors and logs.
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = redact(ue.URL)
		}
		return nil, err
	}
	return resp, nil
}

// finish follows redirects (without the original headers) and turns the
// final response into a body that starts at offset. A bounded request
// (end >= 0) must come back as a range.
func finish(ctx context.Context, resp *http.Response, offset, end int64) (io.ReadCloser, error) {
	for hop := 0; isRedirect(resp.StatusCode); hop++ {
		loc, err := resp.Location()
		drain(resp)
		if err != nil {
			return nil, fmt.Errorf("redirect without a location: %w", err)
		}
		if hop == maxRedirects {
			return nil, &permanentError{fmt.Errorf("too many redirects at %s", redact(loc.String()))}
		}
		if resp, err = get(ctx, loc.String(), nil, offset, end); err != nil {
			return nil, err
		}
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if end >= 0 {
			// Reading on would download the file from its start for every
			// range.
			resp.Body.Close()
			return nil, &permanentError{fmt.Errorf("%s: %w", redact(resp.Request.URL.String()), errNoRanges)}
		}
		if offset > 0 {
			// The server ignored Range: the reader skips what it has.
			return &skipBody{ReadCloser: resp.Body, n: offset}, nil
		}
		return resp.Body, nil
	case http.StatusPartialContent:
		cr := resp.Header.Get("Content-Range")
		if start, ok := contentRangeStart(cr); !ok || start != offset {
			resp.Body.Close()
			return nil, fmt.Errorf("server resumed at %q, want byte %d", cr, offset)
		}
		return resp.Body, nil
	}
	err := &httpError{status: resp.StatusCode, url: redact(resp.Request.URL.String())}
	drain(resp)
	return nil, err
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// contentRangeStart parses the first byte of "bytes START-END/TOTAL".
func contentRangeStart(cr string) (int64, bool) {
	rest, ok := strings.CutPrefix(cr, "bytes ")
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

// drain reads a little of an unwanted body so the connection can be
// reused, then closes it.
func drain(resp *http.Response) {
	io.CopyN(io.Discard, resp.Body, 64<<10)
	resp.Body.Close()
}

// redact drops the query and credentials from a URL for messages.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}
