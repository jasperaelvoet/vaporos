package extensions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// The store calls, as variables so tests can stand in for the kernel's
// fs-verity, which only Linux has, and for a full disk.
var (
	putImage  = store.Put
	hasImage  = store.Has
	storeFree = store.Free
)

// source is where images come from: an update.Source, or a test's fake.
type source interface {
	Manifest(ctx context.Context) (*manifest.Manifest, error)
	Fetch(ctx context.Context, a manifest.Artifact, w io.Writer, onChunk func(done int64) error) error
	FetchBlob(ctx context.Context, sha256 string, size int64, w io.Writer, onChunk func(done int64) error) error
	Remote() bool // reached over the network, not a local directory
	String() string
}

// openSource opens a source spec at an image version (update.OpenSourceAt);
// a variable for tests.
var openSource = func(spec, version string) (source, error) {
	src, err := update.OpenSourceAt(spec, version)
	if err != nil {
		return nil, err
	}
	return src, nil
}

// fetchImage seals e's image into the store from src: the file by name
// (ext-<id>.raw) first and, when the source answered that it has no such
// file or served other bytes for it, the blob by digest, which a registry
// still serves after the version's tag is gone or moved. A source that
// could not be reached, a write to the disk that failed and a seal that
// failed are not tried again by digest: the blob would fare no better.
// progress gets the bytes of the attempt under way.
func fetchImage(ctx context.Context, src source, e catalog.Entry, progress func(done int64)) error {
	a := manifest.Artifact{Name: manifest.ExtensionFile(e.ID), Size: e.Size, SHA256: e.SHA256}
	nameErr, err := putFrom(ctx, e, progress, func(w io.Writer, onChunk func(int64) error) error {
		return src.Fetch(ctx, a, w, onChunk)
	})
	if err == nil || nameErr == nil || ctx.Err() != nil || unreachable(src, nameErr) {
		return err
	}
	blobErr, berr := putFrom(ctx, e, progress, func(w io.Writer, onChunk func(int64) error) error {
		return src.FetchBlob(ctx, e.SHA256, e.Size, w, onChunk)
	})
	switch {
	case berr == nil:
		return nil
	case errors.Is(blobErr, update.ErrNoBlobs):
		return err
	case blobErr == nil || unreachable(src, blobErr):
		// The disk, the seal or the network failed the second time: that is
		// what counts now, not what the source answered by name.
		return fmt.Errorf("%v; by digest: %w", err, berr)
	}
	return fmt.Errorf("%w; by digest: %w", err, berr)
}

// putFrom runs one Put whose bytes download writes, and returns the
// download's own error (nil when it went through, never started, or
// stopped because the disk refused a write) with Put's.
func putFrom(ctx context.Context, e catalog.Entry, progress func(int64), download func(io.Writer, func(int64) error) error) (fetchErr, err error) {
	err = putImage(ctx, e, func(w io.Writer, onChunk func(int64) error) error {
		sw := &storeWriter{w: w}
		fetchErr = download(sw, chunks(progress, onChunk))
		if sw.err != nil {
			fetchErr = nil
			return &writeError{sw.err}
		}
		return fetchErr
	})
	return fetchErr, err
}

// storeWriter remembers the first error writing to the store gave, so a
// full or failing disk is not taken for a failed download.
type storeWriter struct {
	w   io.Writer
	err error
}

func (s *storeWriter) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if err != nil && s.err == nil {
		s.err = err
	}
	return n, err
}

// writeError is a write to the store that failed.
type writeError struct{ err error }

func (e *writeError) Error() string { return "writing it to the disk: " + e.err.Error() }
func (e *writeError) Unwrap() error { return e.err }

// noSpace reports whether err says the image does not fit: no room for it
// and the reserve before the download, or a full disk during it.
func noSpace(err error) bool {
	var we *writeError
	return errors.Is(err, store.ErrNoSpace) || (errors.As(err, &we) && errors.Is(we, syscall.ENOSPC))
}

// unreachable reports whether err says src could not be reached, or failed
// the way a network does until the download gave up, rather than
// answering: the same would happen to every other file. A file that ended
// early counts only from a remote source: in a directory it is that
// image's problem, not the source's.
func unreachable(src source, err error) bool {
	return update.IsNetworkError(err) || (src.Remote() && errors.Is(err, io.ErrUnexpectedEOF))
}

func chunks(progress func(int64), onChunk func(int64) error) func(int64) error {
	return func(done int64) error {
		if progress != nil {
			progress(done)
		}
		return onChunk(done)
	}
}

// sealed reports whether e's image is sealed in the store. An error (a
// file that cannot be checked) counts as not sealed: it is fetched again.
func sealed(e catalog.Entry) bool {
	ok, err := hasImage(e)
	return err == nil && ok
}
