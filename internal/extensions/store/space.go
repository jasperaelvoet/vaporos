package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// ExtReserve is what the data partition keeps free after the extension
// images it fetches: Steam's downloads and the store's own writes need
// room more than an extension does.
const ExtReserve int64 = 2 << 30

// ErrNoSpace means an image does not fit on the data partition with
// ExtReserve to spare.
var ErrNoSpace = errors.New("not enough free space on the data partition")

// freeBytes returns the bytes free on the filesystem of dir, or -1 when
// that is unknown; a variable so tests can fill the disk.
var freeBytes = statFree

// Free returns the bytes free on the store's filesystem (the images
// directory, or its nearest ancestor while it does not exist), or -1 when
// that is unknown.
func Free() (int64, error) {
	dir := config.ExtImagesDir()
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return freeBytes(dir)
}

// CheckSpace fails with ErrNoSpace when size bytes and ExtReserve do not
// fit on the store's filesystem. Unknown free space passes.
func CheckSpace(size int64) error {
	free, err := Free()
	if err != nil || free < 0 || free >= size+ExtReserve {
		return nil
	}
	return fmt.Errorf("%w: %s free, %s needed (%s for the image and %s to spare)",
		ErrNoSpace, human(free), human(size+ExtReserve), human(size), human(ExtReserve))
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n>>10)
}
