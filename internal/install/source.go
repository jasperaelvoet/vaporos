package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// Source kinds (sourceSpec.kind).
const (
	srcLive = "live" // the medium the live ISO booted from
	srcDir  = "dir"  // a local directory with the five update files
	srcHTTP = "http" // an http(s) directory with the same files
	srcOCI  = "oci"  // an OCI registry (oci://registry/repo[:tag])
)

// sourceSpec is a parsed Options.Source.
type sourceSpec struct {
	kind string
	dir  string // srcDir: the absolute directory
	raw  string // srcHTTP, srcOCI: the spec for update.OpenSource
}

// parseSource checks an image source without touching the system:
//
//	"" or "live"                  the live medium
//	/abs/dir, file:///abs/dir     a local directory
//	http(s)://host/dir/           a web server with the same files
//	oci://registry/repo[:tag]     a registry (oci+http:// for a plain-HTTP one)
//
// Relative directories are refused: the web installer has no meaningful
// working directory.
func parseSource(s string) (sourceSpec, error) {
	switch {
	case s == "" || s == srcLive:
		return sourceSpec{kind: srcLive}, nil
	case strings.HasPrefix(s, "oci://"), strings.HasPrefix(s, "oci+http://"):
		// OpenSource only parses an OCI spec; nothing is fetched yet.
		if _, err := update.OpenSource(s, ""); err != nil {
			return sourceSpec{}, err
		}
		return sourceSpec{kind: srcOCI, raw: s}, nil
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return sourceSpec{}, fmt.Errorf("invalid source URL %q", s)
		}
		return sourceSpec{kind: srcHTTP, raw: s}, nil
	case strings.HasPrefix(s, "file://"):
		u, err := url.Parse(s)
		if err != nil || (u.Host != "" && u.Host != "localhost") || !filepath.IsAbs(u.Path) {
			return sourceSpec{}, fmt.Errorf("invalid source URL %q: use file:///absolute/dir", s)
		}
		return sourceSpec{kind: srcDir, dir: filepath.Clean(u.Path)}, nil
	case filepath.IsAbs(s):
		return sourceSpec{kind: srcDir, dir: filepath.Clean(s)}, nil
	}
	return sourceSpec{}, fmt.Errorf("unsupported source %q: use an absolute directory, an http(s) URL or oci://registry/repository", s)
}

// checkChannel validates an OCI channel (a tag); "" means the default.
func checkChannel(ch string) error {
	if ch != "" && !update.ValidChannel(ch) {
		return fmt.Errorf("invalid channel %q", ch)
	}
	return nil
}

// defaultChannel is the channel an oci:// source without one installs
// from: the live image's own (image.json), else main. "Install the newest
// version" from the ISO of a branch then means that branch's newest.
func defaultChannel() string {
	if ii, err := config.LoadImageInfo(); err == nil && update.ValidChannel(ii.Channel) {
		return ii.Channel
	}
	return "main"
}

// imageSource is where an install reads VaporOS from. Fetching, resuming,
// signature and sha256 checks are the update package's Source, which
// `vos update` uses too: the live medium, a directory, an http(s) server
// and a registry all go through one verified code path.
type imageSource struct {
	*update.Source
	spec sourceSpec
}

// openSource opens spec. channel picks the OCI tag when the spec has none
// ("" = defaultChannel); other kinds ignore it. Nothing is fetched yet.
func openSource(spec sourceSpec, channel string) (*imageSource, error) {
	var (
		src *update.Source
		err error
	)
	switch spec.kind {
	case srcLive:
		src = update.LiveSource()
	case srcDir:
		if fi, serr := os.Stat(spec.dir); serr != nil || !fi.IsDir() {
			return nil, fmt.Errorf("no VaporOS image found at %s: not a directory", spec.dir)
		}
		src, err = update.OpenSource(spec.dir, "")
	case srcOCI:
		if channel == "" {
			channel = defaultChannel()
		}
		src, err = update.OpenSource(spec.raw, channel)
	default:
		src, err = update.OpenSource(spec.raw, "")
	}
	if err != nil {
		return nil, err
	}
	return &imageSource{Source: src, spec: spec}, nil
}

func (s *imageSource) String() string {
	if s.spec.kind == srcLive {
		return "the installation medium"
	}
	return s.Source.String()
}

// dir is the local directory holding the files, or "" for remote ones.
func (s *imageSource) dir() string {
	switch s.spec.kind {
	case srcLive:
		return config.LiveMedium
	case srcDir:
		return s.spec.dir
	}
	return ""
}

// readSmall reads a whole small file (the manifest) from s.
func (s *imageSource) readSmall(ctx context.Context, name string, limit int64) ([]byte, error) {
	r := s.Open(ctx, name, -1)
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s from %s: %w", name, s, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s on %s is larger than %d bytes", name, s, limit)
	}
	return b, nil
}

// loadedImage is a manifest the installer accepted, and how.
type loadedImage struct {
	man *manifest.Manifest
	// unsigned: accepted under unsignedRule, not by a signature.
	unsigned bool
}

// loadManifest returns src's manifest once it is trusted: signed by a key
// in config.KeysDir (update.Source.Manifest, which for a registry also
// matches every layer against it), or covered by unsignedRule. Parsing
// includes manifest.Validate: schema, min_updater, a version that is safe
// as a path, and well-formed artifact names, sizes and hashes.
func loadManifest(ctx context.Context, e *env, src *imageSource) (*loadedImage, error) {
	if d := src.dir(); d != "" {
		if _, err := os.Stat(filepath.Join(d, update.ManifestName)); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no VaporOS image found on %s (%s is missing)", src, update.ManifestName)
		}
		if _, err := os.Stat(filepath.Join(d, update.SignatureName)); errors.Is(err, fs.ErrNotExist) {
			return loadUnsigned(ctx, e, src)
		}
	}
	m, err := src.Manifest(ctx)
	switch {
	case errors.Is(err, manifest.ErrBadSignature), errors.Is(err, manifest.ErrNoKeys):
		return nil, fmt.Errorf("the image on %s is not signed by a trusted key: %w", src, err)
	case err != nil:
		return nil, fmt.Errorf("reading the VaporOS image: %w", err)
	}
	return &loadedImage{man: m}, nil
}

// errUnsigned is the refusal of an image without manifest.json.sig.
var errUnsigned = errors.New("the image is not signed")

// unsignedRule is the one exception to "only signed images are
// installed". A debug build made without a signing key (build.sh warns
// "manifest.json is unsigned") must still install itself from its own
// ISO, or the dev loop could not test that ISO. So an image without
// manifest.json.sig is accepted only when all of this holds:
//
//   - it is on the live medium: never a directory, a web server or a
//     registry, where anyone could have put it;
//   - the running image, which is the medium's own, is a debug image
//     (image.json "debug": true): a release ISO installs signed images only;
//   - the manifest is that running image's (same version), not something
//     else copied onto the stick.
//
// A signature that is present but does not verify is never "unsigned": it
// is refused as tampering whatever the build. The artifacts' sizes and
// sha256 are still checked against the manifest as they are streamed, so a
// damaged medium is caught; only the manifest's origin goes unproven.
func unsignedRule(src *imageSource, running *config.ImageInfo, m *manifest.Manifest) error {
	switch {
	case src.spec.kind != srcLive:
		return fmt.Errorf("%w (%s is missing on %s); only the live medium of a debug build may install without a signature",
			errUnsigned, update.SignatureName, src)
	case running == nil || !running.Debug:
		return fmt.Errorf("%w (%s is missing on %s); only debug builds may install without a signature",
			errUnsigned, update.SignatureName, src)
	case m != nil && m.Version != running.Version:
		return fmt.Errorf("%w, and it is not the running debug image (%s on %s, running %s)",
			errUnsigned, m.Version, src, running.Version)
	}
	return nil
}

// loadUnsigned reads a manifest without a signature file, if unsignedRule
// allows it.
func loadUnsigned(ctx context.Context, e *env, src *imageSource) (*loadedImage, error) {
	running, _ := config.LoadImageInfo()
	// Refuse before reading anything when the rule cannot hold.
	if err := unsignedRule(src, running, nil); err != nil {
		return nil, err
	}
	raw, err := src.readSmall(ctx, update.ManifestName, 1<<20)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return nil, err
	}
	if err := unsignedRule(src, running, m); err != nil {
		return nil, err
	}
	e.logf("install: WARNING: %s has no %s; accepted unverified because this is debug build %s",
		src, update.SignatureName, running.Version)
	return &loadedImage{man: m, unsigned: true}, nil
}

// checkLocalRoot fails early when a local source's root.erofs is missing
// or not the size the manifest promises (a truncated copy), before any
// disk is touched. Remote sources are checked as they stream.
func checkLocalRoot(src *imageSource, m *manifest.Manifest) error {
	d := src.dir()
	if d == "" {
		return nil
	}
	root := m.Artifact(manifest.Root)
	fi, err := os.Stat(filepath.Join(d, root.Name))
	if err != nil {
		return fmt.Errorf("the image on %s is incomplete: %w", src, err)
	}
	if fi.Size() != root.Size {
		return fmt.Errorf("%s on %s is %d bytes, the manifest says %d", root.Name, src, fi.Size(), root.Size)
	}
	return nil
}
