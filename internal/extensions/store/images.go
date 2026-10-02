package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// freshGrace is how long GC leaves an image file alone after its last
// write: a Put may still be filling a temp file, and a sealed image may be
// fetched ahead of the wanted, set or slot file that will keep it (Put runs
// without the lock).
const freshGrace = time.Hour

// ImagePath is where the sealed image with this sha256 lives.
func ImagePath(sha256 string) string {
	return filepath.Join(config.ExtImagesDir(), sha256+".raw")
}

// imagesDir returns the images directory, first replacing a symlink there
// with a real directory: the initramfs takes a symlinked images/ for
// missing, so vosd never uses one, and its images are fetched again.
func imagesDir() (string, error) {
	dir := config.ExtImagesDir()
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return dir, nil
	}
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return dir, err
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, syncDir(filepath.Dir(dir))
}

func checkEntry(e catalog.Entry) error {
	if !manifest.ValidExtensionID(e.ID) || !isHex64(e.SHA256) || !isHex64(e.FSVerity) || e.Size <= 0 {
		return fmt.Errorf("invalid catalog entry for extension %q", e.ID)
	}
	return nil
}

// Put fetches e's image and seals it into the store. fetch writes the bytes
// to w and may call onChunk with the bytes done so far (update.Source.Fetch
// has this shape); onChunk fails once ctx ends. The bytes go to a temp file
// in the images directory and get their final name only once their size,
// sha256 and fs-verity digest match e and the kernel has sealed them. Any
// failure removes the temp file. An image already sealed is not fetched
// again, and one that would leave less than ExtReserve free is not fetched
// at all (ErrNoSpace).
func Put(ctx context.Context, e catalog.Entry, fetch func(w io.Writer, onChunk func(done int64) error) error) error {
	if err := checkEntry(e); err != nil {
		return err
	}
	if ok, err := Has(e); err != nil || ok {
		return err
	}
	dir, err := imagesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := CheckSpace(e.Size); err != nil {
		return fmt.Errorf("extension %s: %w", e.ID, err)
	}
	f, err := os.CreateTemp(dir, tempPrefix+e.ID+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = seal(ctx, e, f, fetch)
	if err == nil {
		err = os.Rename(tmp, ImagePath(e.SHA256))
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("extension %s: %w", e.ID, err)
	}
	return syncDir(dir)
}

// seal fills f (and closes it), then seals the file through a read-only
// descriptor: the kernel refuses FS_IOC_ENABLE_VERITY while any writer is
// open.
func seal(ctx context.Context, e catalog.Entry, f *os.File, fetch func(io.Writer, func(int64) error) error) error {
	w := &imageWriter{ctx: ctx, f: f, h: sha256.New(), max: e.Size}
	err := fetch(w, func(int64) error { return ctx.Err() })
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && w.n != e.Size {
		err = fmt.Errorf("%w: got %d of %d bytes", ErrMismatch, w.n, e.Size)
	}
	if err == nil {
		if got := hex.EncodeToString(w.h.Sum(nil)); got != e.SHA256 {
			err = fmt.Errorf("%w: sha256 %s, want %s", ErrMismatch, got, e.SHA256)
		}
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	r, err := os.Open(f.Name())
	if err != nil {
		return err
	}
	defer r.Close()
	if err := enableVerity(r); err != nil {
		return fmt.Errorf("enable fs-verity: %w", err)
	}
	got, err := measureVerity(r)
	if err != nil {
		return fmt.Errorf("measure fs-verity: %w", err)
	}
	if got != e.FSVerity {
		return fmt.Errorf("%w: fs-verity %s, want %s", ErrMismatch, got, e.FSVerity)
	}
	// The Merkle tree and the verity flag must be on disk before the name.
	return r.Sync()
}

// imageWriter hashes and counts what fetch writes, and stops it at the
// catalog size or when ctx ends.
type imageWriter struct {
	ctx context.Context
	f   *os.File
	h   hash.Hash
	n   int64
	max int64
}

func (w *imageWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.max-w.n {
		return 0, fmt.Errorf("%w: more than %d bytes", ErrMismatch, w.max)
	}
	n, err := w.f.Write(p)
	w.h.Write(p[:n])
	w.n += int64(n)
	return n, err
}

// Has reports whether e's image is in the store and sealed: the file under
// its final name, in a real images directory (imagesDir), has the
// fs-verity attribute and measures to e.FSVerity. A file there that fails
// this is deleted (so it is fetched again) and Has reports false.
func Has(e catalog.Entry) (bool, error) {
	if err := checkEntry(e); err != nil {
		return false, err
	}
	if _, err := imagesDir(); err != nil {
		return false, err
	}
	path := ImagePath(e.SHA256)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() || fi.Size() != e.Size {
		return false, discard(path)
	}
	ok, err := sealedAs(path, e.FSVerity)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, discard(path)
	}
	return true, nil
}

// sealedAs reports whether path carries fs-verity with digest want.
func sealedAs(path, want string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	sealed, err := verityAttr(f)
	if err != nil || !sealed {
		return false, err
	}
	got, err := measureVerity(f)
	if errors.Is(err, errNotSealed) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == want, nil
}

func discard(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// CleanTemp removes the temp files of downloads that stopped: those nothing
// has written to for an hour.
func CleanTemp() error {
	_, err := cleanImageTemps(time.Now())
	return err
}

func cleanImageTemps(now time.Time) ([]string, error) {
	dir, err := imagesDir()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, ent := range ents {
		if !strings.HasPrefix(ent.Name(), tempPrefix) {
			continue
		}
		fi, err := ent.Info()
		if err != nil || now.Sub(fi.ModTime()) < freshGrace {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, ent.Name())); err != nil {
			return removed, err
		}
		removed = append(removed, filepath.Join("images", ent.Name()))
	}
	return removed, nil
}
