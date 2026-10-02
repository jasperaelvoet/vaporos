//go:build unix

package gamerfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestReadDirNames(t *testing.T) {
	root := t.TempDir()
	msgs := filepath.Join(root, "vos", "ext-messages")
	mustWrite(t, filepath.Join(msgs, "1727863200000000000.json"), `{"level":"warning","text":"x"}`)
	mustWrite(t, filepath.Join(msgs, "1727863200000000001.json"), `{}`)
	if err := os.Mkdir(filepath.Join(msgs, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	names, err := ReadDirNames(root, "vos/ext-messages", 0)
	slices.Sort(names)
	if err != nil || !slices.Equal(names, []string{"1727863200000000000.json", "1727863200000000001.json", "sub"}) {
		t.Fatalf("names %q, %v", names, err)
	}
	if names, err := ReadDirNames(root, "vos/ext-messages", 1); err != nil || len(names) != 1 {
		t.Fatalf("at most one: %q, %v", names, err)
	}
	if names, err := ReadDirNames(msgs, ".", 0); err != nil || len(names) != 3 {
		t.Fatalf("the root itself: %q, %v", names, err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if names, err := ReadDirNames(root, "empty", 5); err != nil || len(names) != 0 {
		t.Fatalf("empty: %q, %v", names, err)
	}
	if _, err := ReadDirNames(root, "missing", 0); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}

	// A symlinked directory is refused, wherever it is.
	elsewhere := t.TempDir()
	mustWrite(t, filepath.Join(elsewhere, "secret"), "x")
	mustSymlink(t, elsewhere, filepath.Join(root, "link"))
	if _, err := ReadDirNames(root, "link", 0); err == nil {
		t.Fatal("listed a symlinked directory")
	}
	if err := os.RemoveAll(filepath.Join(root, "vos")); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, elsewhere, filepath.Join(root, "vos"))
	if _, err := ReadDirNames(root, "vos/ext-messages", 0); err == nil {
		t.Fatal("listed through a symlinked parent")
	}
}

func TestRemove(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "vos", "ext-messages", "1.json")
	mustWrite(t, f, "{}")
	if err := Remove(root, "vos/ext-messages/1.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(f); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
	if err := Remove(root, "vos/ext-messages/1.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}

	// A symlink is removed itself; its target stays.
	target := filepath.Join(t.TempDir(), "keep")
	mustWrite(t, target, "x")
	mustSymlink(t, target, filepath.Join(root, "vos", "ext-messages", "2.json"))
	if err := Remove(root, "vos/ext-messages/2.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the target went: %v", err)
	}

	// No directory, and nothing through a symlinked parent.
	if err := Remove(root, "vos/ext-messages"); err == nil {
		t.Fatal("removed a directory")
	}
	other := t.TempDir()
	mustWrite(t, filepath.Join(other, "victim"), "x")
	mustSymlink(t, other, filepath.Join(root, "linked"))
	if err := Remove(root, "linked/victim"); err == nil {
		t.Fatal("removed through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(other, "victim")); err != nil {
		t.Fatalf("victim went: %v", err)
	}
	if err := Remove(root, "../x"); err == nil {
		t.Fatal("removed outside the root")
	}
}

func TestOpenOrCreate(t *testing.T) {
	withUmask(t, 0o077)
	root := t.TempDir()
	f, err := OpenOrCreate(root, "vos-steam.lock", 0o640, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	st := stat(t, filepath.Join(root, "vos-steam.lock"))
	if st.Mode&0o777 != 0o640 || int(st.Uid) != os.Geteuid() {
		t.Fatalf("mode %o uid %d", st.Mode&0o777, st.Uid)
	}

	// An existing file keeps its mode and content.
	path := filepath.Join(root, "vos-steam.lock")
	if err := os.WriteFile(path, []byte("held"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err = OpenOrCreate(root, "vos-steam.lock", 0o644, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if st := stat(t, path); st.Mode&0o777 != 0o600 {
		t.Fatalf("existing file's mode changed: %o", st.Mode&0o777)
	}
	if b, _ := os.ReadFile(path); string(b) != "held" {
		t.Fatalf("content %q", b)
	}

	// A planted symlink is not followed, nor is a FIFO opened for long.
	target := filepath.Join(t.TempDir(), "victim")
	mustWrite(t, target, "x")
	mustSymlink(t, target, filepath.Join(root, "link.lock"))
	if _, err := OpenOrCreate(root, "link.lock", 0o600, -1, -1); err == nil {
		t.Fatal("opened through a symlink")
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo.lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := OpenOrCreate(root, "fifo.lock", 0o600, -1, -1)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("fifo: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO hangs OpenOrCreate")
	}
	if _, err := OpenOrCreate(root, "missing-dir/x.lock", 0o600, -1, -1); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
}
