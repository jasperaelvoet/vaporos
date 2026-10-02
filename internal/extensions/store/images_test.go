package store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
)

func TestPutSeals(t *testing.T) {
	v := setup(t)
	img := newImage(t, "proton", 10_000)
	put(t, img)

	got, err := os.ReadFile(ImagePath(img.entry.SHA256))
	check(t, err)
	if !bytes.Equal(got, img.data) {
		t.Fatal("sealed image has other bytes")
	}
	eq(t, "Has", has(t, img.entry), true)
	noTemps(t, config.ExtImagesDir())

	// Sealed already: not fetched again.
	err = Put(t.Context(), img.entry, func(io.Writer, func(int64) error) error {
		t.Fatal("fetched a sealed image again")
		return nil
	})
	check(t, err)
	eq(t, "enables", v.enables, 1)
}

func TestPutFailures(t *testing.T) {
	img := newImage(t, "proton", 5_000)
	boom := errors.New("connection reset")
	tests := []struct {
		name  string
		entry func(catalog.Entry) catalog.Entry
		fetch func(cancel func()) func(io.Writer, func(int64) error) error
		want  error
	}{
		{
			name:  "short",
			fetch: func(func()) func(io.Writer, func(int64) error) error { return serve(img.data[:4_000]) },
			want:  ErrMismatch,
		},
		{
			name:  "long",
			fetch: func(func()) func(io.Writer, func(int64) error) error { return serve(append(img.data, 'x')) },
			want:  ErrMismatch,
		},
		{
			name:  "wrong sha256",
			entry: func(e catalog.Entry) catalog.Entry { e.SHA256 = hex64('a'); return e },
			want:  ErrMismatch,
		},
		{
			name:  "wrong fsverity",
			entry: func(e catalog.Entry) catalog.Entry { e.FSVerity = hex64('b'); return e },
			want:  ErrMismatch,
		},
		{
			name: "fetch error",
			fetch: func(func()) func(io.Writer, func(int64) error) error {
				return func(w io.Writer, _ func(int64) error) error {
					w.Write(img.data[:100])
					return boom
				}
			},
			want: boom,
		},
		{
			name: "cancelled between chunks",
			fetch: func(cancel func()) func(io.Writer, func(int64) error) error {
				return func(w io.Writer, onChunk func(int64) error) error {
					w.Write(img.data[:100])
					cancel()
					return onChunk(100)
				}
			},
			want: context.Canceled,
		},
		{
			name: "cancelled while writing",
			fetch: func(cancel func()) func(io.Writer, func(int64) error) error {
				return func(w io.Writer, _ func(int64) error) error {
					cancel()
					_, err := w.Write(img.data)
					return err
				}
			},
			want: context.Canceled,
		},
		{
			name: "cancelled after the last byte",
			fetch: func(cancel func()) func(io.Writer, func(int64) error) error {
				return func(w io.Writer, _ func(int64) error) error {
					w.Write(img.data)
					cancel()
					return nil
				}
			},
			want: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup(t)
			e := img.entry
			if tt.entry != nil {
				e = tt.entry(e)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fetch := serve(img.data)
			if tt.fetch != nil {
				fetch = tt.fetch(cancel)
			}
			err := Put(ctx, e, fetch)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Put = %v, want %v", err, tt.want)
			}
			if n := entries(t, config.ExtImagesDir()); len(n) != 0 {
				t.Errorf("images dir holds %v after a failed Put", n)
			}
		})
	}
}

func TestPutWithoutVerity(t *testing.T) {
	v := setup(t)
	v.enableErr = ErrUnsupported
	img := newImage(t, "proton", 3_000)
	if err := Put(t.Context(), img.entry, serve(img.data)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Put = %v, want ErrUnsupported", err)
	}
	if n := entries(t, config.ExtImagesDir()); len(n) != 0 {
		t.Errorf("images dir holds %v", n)
	}
}

// An image that would eat into the reserve is not fetched at all; the
// check looks at the store's filesystem even before the store exists.
func TestPutNeedsRoom(t *testing.T) {
	setup(t)
	img := newImage(t, "proton", 3_000)
	var asked string
	free := ExtReserve + 2_999
	freeBytes = func(dir string) (int64, error) { asked = dir; return free, nil }
	err := Put(t.Context(), img.entry, func(io.Writer, func(int64) error) error {
		t.Fatal("fetched an image that does not fit")
		return nil
	})
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("Put = %v, want ErrNoSpace", err)
	}
	eq(t, "statfs of", asked, config.ExtImagesDir())
	if n := entries(t, config.ExtImagesDir()); len(n) != 0 {
		t.Errorf("images dir holds %v", n)
	}
	free++
	put(t, img)

	check(t, os.RemoveAll(config.ExtDir()))
	if _, err := Free(); err != nil || asked != config.StateDir {
		t.Errorf("Free looked at %q (%v), want the nearest existing directory %q", asked, err, config.StateDir)
	}
	free = -1
	check(t, CheckSpace(1<<50))
}

func TestPutRejectsBadEntry(t *testing.T) {
	setup(t)
	img := newImage(t, "proton", 100)
	for _, e := range []catalog.Entry{
		{ID: "Proton", SHA256: img.entry.SHA256, FSVerity: img.entry.FSVerity, Size: 100},
		{ID: "proton", SHA256: "../../etc/passwd", FSVerity: img.entry.FSVerity, Size: 100},
		{ID: "proton", SHA256: img.entry.SHA256, FSVerity: "x", Size: 100},
		{ID: "proton", SHA256: img.entry.SHA256, FSVerity: img.entry.FSVerity},
	} {
		if err := Put(t.Context(), e, serve(img.data)); err == nil {
			t.Errorf("Put(%+v) succeeded", e)
		}
	}
}

func TestHasDiscardsUnsealed(t *testing.T) {
	img := newImage(t, "proton", 4_096)
	path := func() string { return ImagePath(img.entry.SHA256) }
	tests := []struct {
		name string
		make func(t *testing.T)
	}{
		{"never sealed", func(t *testing.T) { writeFile(t, path(), string(img.data)) }},
		{"wrong size", func(t *testing.T) { writeFile(t, path(), string(img.data[:100])) }},
		{"a directory", func(t *testing.T) { check(t, os.MkdirAll(filepath.Join(path(), "x"), 0o755)) }},
		{"a symlink", func(t *testing.T) {
			check(t, os.MkdirAll(filepath.Dir(path()), 0o755))
			check(t, os.Symlink("/dev/zero", path()))
		}},
		{"replaced after sealing", func(t *testing.T) {
			put(t, img)
			tmp := path() + ".new"
			writeFile(t, tmp, string(img.data))
			check(t, os.Rename(tmp, path()))
		}},
		{"rewritten after sealing", func(t *testing.T) {
			put(t, img)
			time.Sleep(10 * time.Millisecond) // a new mtime
			writeFile(t, path(), string(img.data))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup(t)
			tt.make(t)
			eq(t, "Has", has(t, img.entry), false)
			if _, err := os.Lstat(path()); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s still there: %v", path(), err)
			}
		})
	}
}

func TestHasOtherDigest(t *testing.T) {
	setup(t)
	img := newImage(t, "proton", 4_096)
	put(t, img)
	other := img.entry
	other.FSVerity = hex64('c')
	eq(t, "Has(other digest)", has(t, other), false)
	eq(t, "Has(after)", has(t, img.entry), false)
}

func TestHasMissing(t *testing.T) {
	setup(t)
	eq(t, "Has", has(t, newImage(t, "proton", 10).entry), false)
}

func TestCleanTemp(t *testing.T) {
	setup(t)
	dir := config.ExtImagesDir()
	stale := filepath.Join(dir, tempPrefix+"proton-1")
	fresh := filepath.Join(dir, tempPrefix+"proton-2")
	writeFile(t, stale, "x")
	writeFile(t, fresh, "x")
	age(t, stale, 2*time.Hour)
	check(t, CleanTemp())
	eq(t, "images", strs(entries(t, dir)), strs([]string{tempPrefix + "proton-2"}))
}

// A symlinked images/ is never used, as the initramfs takes it for missing:
// Has, Put, the temp cleanup and GC replace it with a real directory, and
// what it pointed at stays as it was.
func TestImagesDirIsReal(t *testing.T) {
	img := newImage(t, "proton", 4_096)
	link := func(t *testing.T) string {
		t.Helper()
		put(t, img)
		temp := filepath.Join(config.ExtImagesDir(), tempPrefix+"proton-1")
		writeFile(t, temp, "x")
		age(t, temp, 2*time.Hour)
		elsewhere := filepath.Join(t.TempDir(), "images")
		check(t, os.Rename(config.ExtImagesDir(), elsewhere))
		check(t, os.Symlink(elsewhere, config.ExtImagesDir()))
		return elsewhere
	}
	for _, c := range []struct {
		name string
		use  func(t *testing.T)
	}{
		{"Has", func(t *testing.T) { eq(t, "Has", has(t, img.entry), false) }},
		{"Put", func(t *testing.T) { put(t, img) }},
		{"CleanTemp", func(t *testing.T) { check(t, CleanTemp()) }},
		{"GC", func(t *testing.T) {
			_, err := GC(nil)
			check(t, err)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			setup(t)
			elsewhere := link(t)
			c.use(t)
			fi, err := os.Lstat(config.ExtImagesDir())
			check(t, err)
			if !fi.IsDir() {
				t.Fatalf("images is %v, not a directory", fi.Mode())
			}
			eq(t, "left where the link pointed", strs(entries(t, elsewhere)),
				strs([]string{tempPrefix + "proton-1", img.entry.SHA256 + ".raw"}))
			eq(t, "Has after", has(t, img.entry), c.name == "Put")
		})
	}
}
