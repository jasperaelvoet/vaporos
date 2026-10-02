package extensions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/fsverity"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

const (
	bootedVersion = "20260901.000000"
	otherVersion  = "20260801.000000"
)

// env is an installed system in temp dirs: the store, /run/vos, the image's
// catalog and image.json, a kernel command line booting slot a, and a
// directory source for config.update.source.
type env struct {
	t      *testing.T
	src    string
	cfg    *config.Config
	sealer *fakeSealer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	vars := []*string{&config.StateDir, &config.RunDir, &config.ExtCatalogPath, &config.ImageInfoPath,
		&config.ProcCmdline, &config.KeysDir, &config.ExtDescriptorsDir, &config.GamerHome,
		&config.CompatToolsDir, &config.ExtMountedLibDir, &gamerRuntimeDir}
	saved := make([]string, len(vars))
	for i, v := range vars {
		saved[i] = *v
	}
	savedEntry := slotEntry
	t.Cleanup(func() {
		for i, v := range vars {
			*v = saved[i]
		}
		slotEntry = savedEntry
	})
	config.StateDir = filepath.Join(dir, "state")
	config.RunDir = filepath.Join(dir, "run")
	config.ExtCatalogPath = filepath.Join(dir, "usr", "extensions.list")
	config.ImageInfoPath = filepath.Join(dir, "usr", "image.json")
	config.ProcCmdline = filepath.Join(dir, "cmdline")
	config.KeysDir = filepath.Join(dir, "keys")
	config.ExtDescriptorsDir = filepath.Join(dir, "usr", "share", "vos", "extensions")
	config.GamerHome = filepath.Join(dir, "home", "vapor")
	config.CompatToolsDir = filepath.Join(dir, "usr", "share", "steam", "compatibilitytools.d")
	config.ExtMountedLibDir = filepath.Join(dir, "usr", "lib", "vos", "ext")
	gamerRuntimeDir = filepath.Join(dir, "run", "user", "1000")
	t.Setenv("XDG_RUNTIME_DIR", gamerRuntimeDir)
	slotEntry = func(string) (*boot.Entry, error) { return nil, nil } // slot b was never written
	e := &env{t: t, src: filepath.Join(dir, "src"), sealer: fakeStore(t)}
	for _, d := range []string{config.StateDir, config.RunDir, e.src, config.KeysDir, config.GamerHome, gamerRuntimeDir} {
		must(t, os.MkdirAll(d, 0o755))
	}
	writeFile(t, config.ImageInfoPath, fmt.Sprintf(`{"version":%q,"channel":"main"}`, bootedVersion))
	writeFile(t, config.ProcCmdline, "vos.slot=a quiet\n")
	e.cfg = config.Defaults()
	e.cfg.Update.Source = e.src
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(data), 0o644))
}

// fakeSealer stands in for the kernel's fs-verity behind store.Put and
// store.Has: an image counts as sealed while the file it put is under its
// final name with the catalog's size, whatever happens to its bytes later
// (the kernel only notices those when it reads them).
type fakeSealer struct {
	mu       sync.Mutex
	sealed   map[string]bool // paths
	puts     []string        // ids, in order
	attempts int             // Puts of an image not sealed yet
	err      error           // Put fails with it after fetching
	before   error           // Put fails with it before fetching (store.ErrNoSpace)
	writeErr error           // writes to the image fail with it (a full disk)
	free     int64           // what storeFree reports
}

func fakeStore(t *testing.T) *fakeSealer {
	f := &fakeSealer{sealed: map[string]bool{}, free: -1}
	p, h, fr := putImage, hasImage, storeFree
	t.Cleanup(func() { putImage, hasImage, storeFree = p, h, fr })
	putImage = f.put
	hasImage = f.has
	storeFree = func() (int64, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.free, nil
	}
	return f
}

func (f *fakeSealer) set(fn func(f *fakeSealer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// failWriter is a disk that refuses every write.
type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

func (f *fakeSealer) has(e catalog.Entry) (bool, error) {
	path := store.ImagePath(e.SHA256)
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sealed[path] && fi.Size() == e.Size, nil
}

func (f *fakeSealer) put(ctx context.Context, e catalog.Entry, fetch func(io.Writer, func(int64) error) error) error {
	if ok, err := f.has(e); err != nil || ok {
		return err
	}
	f.mu.Lock()
	f.attempts++
	before, writeErr := f.before, f.writeErr
	f.mu.Unlock()
	if before != nil {
		return fmt.Errorf("extension %s: %w", e.ID, before)
	}
	var buf bytes.Buffer
	var w io.Writer = &buf
	if writeErr != nil {
		w = failWriter{writeErr}
	}
	if err := fetch(w, func(int64) error { return ctx.Err() }); err != nil {
		return fmt.Errorf("extension %s: %w", e.ID, err)
	}
	sum := sha256.Sum256(buf.Bytes())
	if int64(buf.Len()) != e.Size || hex.EncodeToString(sum[:]) != e.SHA256 {
		return fmt.Errorf("extension %s: %w", e.ID, store.ErrMismatch)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return fmt.Errorf("extension %s: %w", e.ID, f.err)
	}
	path := store.ImagePath(e.SHA256)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o444); err != nil {
		return err
	}
	f.sealed[path] = true
	f.puts = append(f.puts, e.ID)
	return nil
}

func (f *fakeSealer) putIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.puts...)
}

// image is a fake extension image: its bytes and catalog entry.
type image struct {
	data  []byte
	entry catalog.Entry
}

func newImage(t *testing.T, id, salt string, size int, core bool, requires ...string) image {
	t.Helper()
	data := bytes.Repeat([]byte(id+salt+"\x00"), size/(len(id+salt)+1)+1)[:size]
	sum := sha256.Sum256(data)
	d, _, err := fsverity.Digest(bytes.NewReader(data))
	must(t, err)
	return image{data: data, entry: catalog.Entry{
		ID: id, SHA256: hex.EncodeToString(sum[:]), Size: int64(size), FSVerity: d, Core: core, Requires: requires,
	}}
}

func (e *env) catalog(imgs ...image) *catalog.Catalog {
	c := &catalog.Catalog{Dispatcher: catalog.Dispatcher}
	for _, img := range imgs {
		c.Entries = append(c.Entries, img.entry)
	}
	writeFile(e.t, config.ExtCatalogPath, string(c.Format()))
	return c
}

// serve puts img in the directory source under its published name.
func (e *env) serve(img image) {
	writeFile(e.t, filepath.Join(e.src, "ext-"+img.entry.ID+".raw"), string(img.data))
}

// seal puts img in the store as if fetched earlier.
func (e *env) seal(img image) {
	e.t.Helper()
	must(e.t, putImage(context.Background(), img.entry, func(w io.Writer, _ func(int64) error) error {
		_, err := w.Write(img.data)
		return err
	}))
}

func (e *env) report(rep store.BootReport) {
	e.t.Helper()
	b, err := json.Marshal(rep)
	must(e.t, err)
	writeFile(e.t, config.ExtBootPath(), string(b)+"\n")
}

func mountedAs(imgs ...image) []store.Mounted {
	var out []store.Mounted
	for _, img := range imgs {
		out = append(out, store.Mounted{ID: img.entry.ID, SHA256: img.entry.SHA256, FSVerity: img.entry.FSVerity})
	}
	return out
}

// locked runs fn under the store lock, as every store write needs.
func locked(t *testing.T, fn func() error) {
	t.Helper()
	unlock, err := store.Lock(context.Background())
	must(t, err)
	defer unlock()
	must(t, fn())
}

func (e *env) service() (*Service, *booted) {
	e.t.Helper()
	b, err := loadBooted()
	must(e.t, err)
	return NewService(e.cfg), b
}

func (e *env) state(s *Service, id string) ExtensionStatus {
	e.t.Helper()
	x, ok := find(s, id)
	if !ok {
		e.t.Fatalf("no status for %s", id)
	}
	return x
}

func find(s *Service, id string) (ExtensionStatus, bool) {
	for _, x := range s.Status().Extensions {
		if x.ID == id {
			return x, true
		}
	}
	return ExtensionStatus{}, false
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
