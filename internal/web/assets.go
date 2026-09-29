package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

// contentTypes maps every extension we embed to its media type. The API sends
// X-Content-Type-Options: nosniff, so a wrong or missing type would break the
// page; an unknown extension is therefore an error at startup, not a guess.
var contentTypes = map[string]string{
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".webmanifest": "application/manifest+json",
	".json":        "application/json",
	".txt":         "text/plain; charset=utf-8",
}

// compressible are the types worth gzipping; PNGs are already compressed.
var compressible = map[string]bool{".css": true, ".js": true, ".svg": true, ".webmanifest": true, ".json": true, ".txt": true}

// asset is one static file, held in memory with its precomputed variants.
type asset struct {
	body  []byte
	gz    []byte // nil when gzip would not help
	ctype string
	etag  string // strong ETag of body; the gzip variant appends "-gz"
}

// assetStore serves static files under /static/<version>/. The version is a
// hash of every asset, so a URL names exactly one build of the UI: it can be
// cached forever, and ES module imports (relative paths) inherit it without
// any rewriting.
type assetStore struct {
	version string
	files   map[string]*asset // keyed by slash path relative to static/
}

// newAssetStore holds every file in fsys that keep accepts. A directory keep
// refuses is not read at all.
func newAssetStore(fsys fs.FS, keep func(name string) bool) (*assetStore, error) {
	s := &assetStore{files: map[string]*asset{}}
	var names []string
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name != "." && !keep(name) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := path.Ext(name)
		ctype, ok := contentTypes[ext]
		if !ok {
			return fmt.Errorf("static/%s: no content type for %q", name, ext)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		a := &asset{body: body, ctype: ctype, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		if compressible[ext] {
			a.gz = gzipIfSmaller(body)
		}
		s.files[name] = a
		names = append(names, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%s\x00", n, s.files[n].etag)
	}
	s.version = hex.EncodeToString(h.Sum(nil))[:12]
	return s, nil
}

// gzipIfSmaller returns the gzip of b when it saves at least 10%.
func gzipIfSmaller(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	if buf.Len() >= len(b)*9/10 {
		return nil
	}
	return buf.Bytes()
}

// prefix is the URL prefix templates put in front of asset names.
func (s *assetStore) prefix() string { return "/static/" + s.version }

// ServeHTTP serves GET /static/<version>/<name>.
func (s *assetStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/static/")
	ver, name, ok := strings.Cut(rest, "/")
	if !ok || ver == "" {
		http.NotFound(w, r)
		return
	}
	// A page loaded before an update may still ask for its old version.
	// Serve the current file, but never let that answer be cached forever.
	s.serveFile(w, r, name, ver == s.version)
}

func (s *assetStore) serveFile(w http.ResponseWriter, r *http.Request, name string, immutable bool) {
	if !fs.ValidPath(name) || path.Clean(name) != name {
		http.NotFound(w, r)
		return
	}
	a := s.files[name]
	if a == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	if immutable {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	body, etag := a.body, a.etag
	if a.gz != nil {
		h.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			body, etag = a.gz, strings.TrimSuffix(a.etag, `"`)+`-gz"`
			h.Set("Content-Encoding", "gzip")
		}
	}
	h.Set("ETag", etag)
	// ServeContent handles HEAD, Range and If-None-Match against the ETag.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// acceptsGzip reports whether an Accept-Encoding value allows gzip, honouring
// an explicit q=0 refusal.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
			if strings.EqualFold(k, "q") {
				v = strings.TrimSpace(v)
				return strings.Trim(v, "0.") != "" // q=0, q=0.0, q=0.000 refuse
			}
		}
		return true
	}
	return false
}
