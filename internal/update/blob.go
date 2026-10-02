package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNoBlobs means a source cannot fetch a file by digest: only an OCI
// registry can.
var ErrNoBlobs = errors.New("this source cannot fetch files by digest")

// OpenSourceAt opens spec for one image version: an OCI registry at the tag
// version, replacing any tag or digest spec names; any other source as it
// is (it serves one version, which callers check).
func OpenSourceAt(spec, version string) (*Source, error) {
	if !tagRe.MatchString(version) {
		return nil, fmt.Errorf("invalid version %q", version)
	}
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "oci://") || strings.HasPrefix(spec, "oci+http://") {
		spec = withoutRef(spec)
	}
	return OpenSource(spec, version)
}

// withoutRef drops the ":tag" or "@digest" from an OCI source spec. A
// registry's ":port" stays: it comes before the first '/'.
func withoutRef(spec string) string {
	scheme, rest, _ := strings.Cut(spec, "://")
	host, path, ok := strings.Cut(rest, "/")
	if !ok {
		return spec
	}
	path, _, _ = strings.Cut(path, "@")
	if i := strings.LastIndexByte(path, ':'); i > strings.LastIndexByte(path, '/') {
		path = path[:i]
	}
	return scheme + "://" + host + "/" + path
}

// FetchBlob streams the blob with this sha256 from the source's repository
// into w, verifying its size and sha256 and resuming like Fetch. A blob
// needs no tag, so any tag that still holds it will do (docs/CONTRACTS.md
// "Extensions"). Only an OCI source can: others fail with ErrNoBlobs.
func (s *Source) FetchBlob(ctx context.Context, sha256 string, size int64, w io.Writer, onChunk func(done int64) error) error {
	o, ok := s.f.(*ociFetcher)
	if !ok {
		return ErrNoBlobs
	}
	digest := "sha256:" + sha256
	if !digestRe.MatchString(digest) || size < 0 {
		return fmt.Errorf("invalid blob %q (%d bytes)", sha256, size)
	}
	r := &resumeReader{ctx: ctx, f: blobFetcher{o}, name: digest, size: size}
	defer r.Close()
	if err := copyVerified(r, w, size, sha256, onChunk); err != nil {
		return fmt.Errorf("%s blob %s: %w", o, digest, err)
	}
	return nil
}

// blobFetcher opens an OCI repository's blobs by digest (the name), with
// the registry's token, redirect and resume handling.
type blobFetcher struct{ o *ociFetcher }

func (b blobFetcher) open(ctx context.Context, digest string, offset int64) (io.ReadCloser, error) {
	resp, err := b.o.do(ctx, b.o.base()+"/v2/"+b.o.repo+"/blobs/"+digest, nil, offset)
	if err != nil {
		return nil, err
	}
	return finish(ctx, resp, offset)
}

func (b blobFetcher) remote() bool   { return true }
func (b blobFetcher) String() string { return b.o.String() }
