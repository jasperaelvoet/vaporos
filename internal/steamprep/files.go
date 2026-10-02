package steamprep

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// file is one of Steam's files as read: its bytes and mode, or missing.
type file struct {
	path    string
	data    []byte
	mode    fs.FileMode
	missing bool
}

// readFile reads a Steam file of at most limit bytes. A missing file is
// not an error; a symlink, FIFO or anything but a regular file is.
func readFile(path string, limit int) (*file, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return &file{path: path, missing: true, mode: 0o644}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, limit)
	}
	return &file{path: path, data: data, mode: fi.Mode().Perm()}, nil
}

// readRegular is readFile for a file that must be there.
func readRegular(path string, limit int) ([]byte, error) {
	f, err := readFile(path, limit)
	if err != nil {
		return nil, err
	}
	if f.missing {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return f.data, nil
}

// write replaces the file with data: a temp file in the same directory,
// fsynced, renamed over it, keeping its mode.
func (f *file) write(data []byte) error {
	if err := config.WriteFileAtomic(f.path, data, f.mode); err != nil {
		return err
	}
	f.data, f.missing = data, false
	return nil
}
