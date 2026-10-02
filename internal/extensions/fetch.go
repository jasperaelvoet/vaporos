package extensions

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// The store calls, as variables so tests can stand in for the kernel's
// fs-verity, which only Linux has.
var (
	putImage = store.Put
	hasImage = store.Has
)

// fetchImage seals e's image into the store from src: the file by name
// (ext-<id>.raw) first and, when that download fails, the blob by digest,
// which a registry still serves after the version's tag is gone. progress
// gets the bytes of the attempt under way.
func fetchImage(ctx context.Context, src *update.Source, e catalog.Entry, progress func(done int64)) error {
	a := manifest.Artifact{Name: manifest.ExtensionFile(e.ID), Size: e.Size, SHA256: e.SHA256}
	var fetchErr error
	err := putImage(ctx, e, func(w io.Writer, onChunk func(int64) error) error {
		fetchErr = src.Fetch(ctx, a, w, chunks(progress, onChunk))
		return fetchErr
	})
	// Only a failed download is worth a second one; a failed seal is not.
	if err == nil || fetchErr == nil || ctx.Err() != nil {
		return err
	}
	berr := putImage(ctx, e, func(w io.Writer, onChunk func(int64) error) error {
		return src.FetchBlob(ctx, e.SHA256, e.Size, w, chunks(progress, onChunk))
	})
	switch {
	case berr == nil:
		return nil
	case errors.Is(berr, update.ErrNoBlobs):
		return err
	}
	return fmt.Errorf("%w; by digest: %v", err, berr)
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
