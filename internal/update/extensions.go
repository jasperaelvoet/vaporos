package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// The store calls Stage makes, as variables so tests can fake them:
// sealing needs fs-verity, which neither the dev Mac nor a tmpfs has.
var (
	putImage = store.Put
	hasImage = store.Has
	// storeFree returns the bytes free on the store's filesystem, or -1
	// when that is unknown.
	storeFree = func() (int64, error) { return diskFree(existingDir(config.ExtImagesDir())) }
)

// extReserve is what the data partition keeps free after the extension
// images an update fetches.
const extReserve int64 = 2 << 30

// errNoSpace means the extension images an update needs do not fit.
var errNoSpace = errors.New("not enough free space on the data partition")

// imagesFor splits the extension images m's image mounts (wanted ∪ its
// core, with their requirements, docs/CONTRACTS.md "Write order" step 0)
// into those the store holds sealed and those it lacks, in catalog order.
// An image whose state cannot be read counts as missing: Put says why.
func imagesFor(m *manifest.Manifest, wanted []string) (have, missing []catalog.Entry) {
	if len(m.Extensions) == 0 {
		return nil, nil
	}
	cat := catalog.FromManifest(m)
	for _, id := range cat.Closure(append(wanted, cat.Core()...)) {
		e, _ := cat.Get(id)
		if ok, err := hasImage(e); err == nil && ok {
			have = append(have, e)
		} else {
			missing = append(missing, e)
		}
	}
	return have, missing
}

func entriesSize(es []catalog.Entry) int64 {
	var n int64
	for _, e := range es {
		n += e.Size
	}
	return n
}

// checkStoreSpace fails when need bytes of images plus the reserve do not
// fit on the store's filesystem. Unknown free space passes.
func checkStoreSpace(need int64) error {
	if need <= 0 {
		return nil
	}
	free, err := storeFree()
	if err != nil || free < 0 || free >= need+extReserve {
		return nil
	}
	return fmt.Errorf("%w for the extension images: %s free, %s needed (%s of images and %s to spare)",
		errNoSpace, humanBytes(free), humanBytes(need+extReserve), humanBytes(need), humanBytes(extReserve))
}

// existingDir is dir or its nearest ancestor that exists: the store may
// not have been created yet.
func existingDir(dir string) string {
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// fetchImages seals es into the store from src, calling progress with the
// bytes fetched so far, and returns the ones now sealed. A data partition
// that cannot seal (no fs-verity) ends it early without an error: the new
// image then starts without them, as the running one does, and vosd
// fetches them once it can.
func fetchImages(ctx context.Context, src *Source, m *manifest.Manifest, es []catalog.Entry, progress func(done int64)) ([]catalog.Entry, error) {
	var base int64
	var sealed []catalog.Entry
	for _, e := range es {
		a := m.Extensions[e.ID].Artifact()
		err := putImage(ctx, e, func(w io.Writer, onChunk func(done int64) error) error {
			return src.Fetch(ctx, a, w, func(done int64) error {
				if progress != nil {
					progress(base + done)
				}
				if err := onChunk(done); err != nil {
					return err
				}
				return ctx.Err()
			})
		})
		if errors.Is(err, store.ErrUnsupported) {
			log.Printf("update: %v; VaporOS %s starts without its extensions", err, m.Version)
			return sealed, nil
		}
		if err != nil {
			return sealed, err
		}
		sealed = append(sealed, e)
		base += e.Size
		if progress != nil {
			progress(base)
		}
	}
	return sealed, nil
}

// unhookIdleSlot is Write order step 1: it removes the idle slot's entries
// and records the new image's extension catalog in ext/slots/<idle>.json,
// under ext.lock. It returns the images of sealed that GC removed since
// they were fetched (no slot file named them yet); the slot file keeps
// them now, so they can be fetched again.
func unhookIdleSlot(ctx context.Context, esp, idle string, m *manifest.Manifest, sealed []catalog.Entry) ([]catalog.Entry, error) {
	unlock, err := store.Lock(ctx)
	if err != nil {
		return nil, fmt.Errorf("extension store: %w", err)
	}
	defer unlock()
	if err := boot.RemoveSlotEntries(esp, idle); err != nil {
		return nil, err
	}
	if err := store.WriteSlot(idle, m.Version, m.Extensions); err != nil {
		return nil, fmt.Errorf("extension store: %w", err)
	}
	var lost []catalog.Entry
	for _, e := range sealed {
		if ok, err := hasImage(e); err == nil && !ok {
			lost = append(lost, e)
		}
	}
	return lost, nil
}
