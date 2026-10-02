package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/fsverity"
)

// setup points the store at a temp dir and fakes the kernel's fs-verity,
// on a disk whose free space is not known (whatever the test machine has).
func setup(t *testing.T) *fakeVerity {
	t.Helper()
	state, run, free := config.StateDir, config.RunDir, freeBytes
	config.StateDir = t.TempDir()
	config.RunDir = filepath.Join(t.TempDir(), "run")
	freeBytes = func(string) (int64, error) { return -1, nil }
	t.Cleanup(func() { config.StateDir, config.RunDir, freeBytes = state, run, free })
	return fakeKernel(t)
}

// fakeVerity stands in for the kernel: it seals a file (by identity: device,
// inode, size and mtime, so a file written again or replaced is no longer
// sealed) and remembers the digest it measured then, as the kernel does.
type fakeVerity struct {
	mu        sync.Mutex
	sealed    []sealedFile
	enableErr error
	enables   int
}

type sealedFile struct {
	fi     os.FileInfo
	digest string
}

func fakeKernel(t *testing.T) *fakeVerity {
	v := &fakeVerity{}
	e, m, a := enableVerity, measureVerity, verityAttr
	enableVerity, measureVerity, verityAttr = v.enable, v.measure, v.attr
	t.Cleanup(func() { enableVerity, measureVerity, verityAttr = e, m, a })
	return v
}

func (v *fakeVerity) find(f *os.File) (sealedFile, bool) {
	fi, err := f.Stat()
	if err != nil {
		return sealedFile{}, false
	}
	for _, s := range v.sealed {
		if os.SameFile(fi, s.fi) && fi.Size() == s.fi.Size() && fi.ModTime().Equal(s.fi.ModTime()) {
			return s, true
		}
	}
	return sealedFile{}, false
}

func (v *fakeVerity) enable(f *os.File) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.enables++
	if v.enableErr != nil {
		return v.enableErr
	}
	// The kernel only seals through a read-only descriptor.
	var mode int
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	rc.Control(func(fd uintptr) { mode, err = unix.FcntlInt(fd, unix.F_GETFL, 0) })
	if err != nil {
		return err
	}
	if mode&unix.O_ACCMODE != unix.O_RDONLY {
		return syscall.ETXTBSY
	}
	if _, ok := v.find(f); ok {
		return syscall.EEXIST
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	d, _, err := fsverity.Digest(io.NewSectionReader(f, 0, fi.Size()))
	if err != nil {
		return err
	}
	v.sealed = append(v.sealed, sealedFile{fi: fi, digest: d})
	return nil
}

func (v *fakeVerity) measure(f *os.File) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.find(f); ok {
		return s.digest, nil
	}
	return "", errNotSealed
}

func (v *fakeVerity) attr(f *os.File) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.find(f)
	return ok, nil
}

// image is a fake extension image: its bytes and catalog entry.
type image struct {
	data  []byte
	entry catalog.Entry
}

func newImage(t *testing.T, id string, size int, requires ...string) image {
	t.Helper()
	data := bytes.Repeat([]byte(id+"\x00"), size/(len(id)+1)+1)[:size]
	sum := sha256.Sum256(data)
	d, _, err := fsverity.Digest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return image{data: data, entry: catalog.Entry{
		ID: id, SHA256: hex.EncodeToString(sum[:]), Size: int64(size), FSVerity: d, Requires: requires,
	}}
}

// serve returns a fetch function that writes data in small chunks.
func serve(data []byte) func(io.Writer, func(int64) error) error {
	return func(w io.Writer, onChunk func(int64) error) error {
		var done int64
		for len(data) > 0 {
			n := min(len(data), 1000)
			k, err := w.Write(data[:n])
			done += int64(k)
			if err != nil {
				return err
			}
			if err := onChunk(done); err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	}
}

func put(t *testing.T, img image) {
	t.Helper()
	if err := Put(t.Context(), img.entry, serve(img.data)); err != nil {
		t.Fatal(err)
	}
}

// entries lists a store directory, sorted.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func noTemps(t *testing.T, dir string) {
	t.Helper()
	for _, n := range entries(t, dir) {
		if strings.HasPrefix(n, tempPrefix) {
			t.Errorf("temp entry %s left in %s", n, dir)
		}
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func age(t *testing.T, path string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func writeReport(t *testing.T, rep string) {
	t.Helper()
	writeFile(t, config.ExtBootPath(), rep)
}

func hex64(c byte) string { return strings.Repeat(string(c), 64) }

func has(t *testing.T, e catalog.Entry) bool {
	t.Helper()
	ok, err := Has(e)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func strs(s []string) string { return fmt.Sprintf("%q", s) }
