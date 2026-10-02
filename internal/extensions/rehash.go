package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"runtime"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// damagedText is what an image's card says while a damaged copy is fetched
// again.
const damagedText = "Its files were damaged. VaporOS downloads them again."

// openImage opens a store image for the re-read; a variable so tests can
// make a read fail as a damaged disk does.
var openImage = func(path string) (io.ReadCloser, error) { return os.Open(path) }

// rehash reads every image this boot mounted through, at idle I/O
// priority, and deletes a damaged one: a read that fails with EIO (fs-verity
// checks every block), or bytes whose size or sha256 are not the booted
// catalog's. The desired set keeps it, so the next pass fetches it again
// and the next boot mounts the new copy. A missing file is not damage, and
// an image is deleted as damaged at most once per boot. It reports whether
// it deleted any.
func (s *Service) rehash(ctx context.Context, cat *catalog.Catalog) bool {
	rep, err := store.LoadBootReport()
	if err != nil || len(rep.Mounted) == 0 {
		return false
	}
	s.mu.Lock()
	s.rehashing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.rehashing = false
		s.mu.Unlock()
	}()

	done := make(chan bool, 1)
	go func() {
		// Never unlocked: the thread with the idle priority ends with this
		// goroutine instead of running other ones.
		runtime.LockOSThread()
		if err := idleIO(); err != nil {
			log.Printf("extensions: idle I/O priority: %v", err)
		}
		done <- s.rehashImages(ctx, cat, rep)
	}()
	return <-done
}

func (s *Service) rehashImages(ctx context.Context, cat *catalog.Catalog, rep *store.BootReport) bool {
	deleted := false
	for _, m := range rep.Mounted {
		e, ok := cat.Get(m.ID)
		if !ok || e.SHA256 != m.SHA256 || ctx.Err() != nil {
			continue
		}
		s.mu.Lock()
		seen := s.damaged[e.SHA256]
		s.mu.Unlock()
		if seen {
			continue
		}
		path := store.ImagePath(e.SHA256)
		bad, err := damaged(ctx, path, e)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("extensions: re-reading %s: %v", m.ID, err)
			}
			continue
		}
		if !bad {
			continue
		}
		log.Printf("extensions: the image of %s is damaged; fetching it again", m.ID)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("extensions: %v", err)
			continue
		}
		s.mu.Lock()
		s.damaged[e.SHA256] = true
		s.errs[e.ID] = damagedText
		s.mu.Unlock()
		deleted = true
	}
	return deleted
}

// damaged reads path through and reports whether it is not e's image. Only
// EIO and other bytes count; any other error (a missing file, ctx ending)
// is returned instead.
func damaged(ctx context.Context, path string, e catalog.Entry) (bool, error) {
	f, err := openImage(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, ctxReader{ctx, f}, make([]byte, 1<<20))
	if errors.Is(err, syscall.EIO) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return true, nil
	}
	return false, nil
}

// ctxReader stops a long read when ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, fmt.Errorf("re-read stopped: %w", err)
	}
	return c.r.Read(p)
}
