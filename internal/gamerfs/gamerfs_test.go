//go:build unix

package gamerfs

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// resolvers runs f once per way reads resolve paths: openat2 (Linux) and
// the openat walk (everywhere, and Linux without openat2).
func resolvers(t *testing.T, f func(t *testing.T)) {
	modes := []bool{true}
	if runtime.GOOS == "linux" {
		modes = []bool{false, true}
	}
	for _, walk := range modes {
		name := "openat2"
		if walk {
			name = "walk"
		}
		t.Run(name, func(t *testing.T) {
			old := forceWalk
			forceWalk = walk
			t.Cleanup(func() { forceWalk = old })
			f(t)
		})
	}
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// stat returns path's stat without following a symlink.
func stat(t *testing.T, path string) *syscall.Stat_t {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t)
}

func isRoot() bool { return os.Geteuid() == 0 }

// withUmask runs the rest of the test under umask m, to show that modes
// are set exactly rather than masked.
func withUmask(t *testing.T, m int) {
	old := unix.Umask(m)
	t.Cleanup(func() { unix.Umask(old) })
}

func TestReadFile(t *testing.T) {
	resolvers(t, func(t *testing.T) {
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, ".config", "sunshine", "sunshine.conf"), "hello")
		got, err := ReadFile(root, ".config/sunshine/sunshine.conf", 1<<20)
		if err != nil || string(got) != "hello" {
			t.Fatalf("ReadFile = %q, %v", got, err)
		}
		// A lexically clean detour stays beneath root and is fine.
		if got, err := ReadFile(root, ".config/x/../sunshine/sunshine.conf", 1<<20); err != nil || string(got) != "hello" {
			t.Errorf("ReadFile via a/../b = %q, %v", got, err)
		}
		if _, err := ReadFile(root, ".config/sunshine/missing", 1<<20); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("missing file: %v, want ErrNotExist", err)
		}
		if _, err := ReadFile(root, "nodir/file", 1<<20); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("missing directory: %v, want ErrNotExist", err)
		}
		if _, err := ReadFile(root, ".config", 1<<20); !errors.Is(err, ErrNotRegular) {
			t.Errorf("directory: %v, want ErrNotRegular", err)
		}
	})
}

func TestReadFileRefusesSymlinks(t *testing.T) {
	resolvers(t, func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		mustWrite(t, filepath.Join(outside, "secret"), "SECRET")
		mustWrite(t, filepath.Join(root, "real"), "inside")

		mustSymlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "link"))
		mustSymlink(t, "real", filepath.Join(root, "inner-link")) // even one that stays inside
		mustSymlink(t, outside, filepath.Join(root, "dirlink"))
		mustSymlink(t, "/dev/zero", filepath.Join(root, "zero"))
		os.Mkdir(filepath.Join(root, "d"), 0o700)
		mustSymlink(t, outside, filepath.Join(root, "d", "deeper"))

		for _, rel := range []string{"link", "inner-link", "dirlink/secret", "zero", "d/deeper/secret"} {
			got, err := ReadFile(root, rel, 1<<20)
			if err == nil {
				t.Errorf("ReadFile(%s) followed a symlink: %q", rel, got)
			}
			if errors.Is(err, fs.ErrNotExist) {
				t.Errorf("ReadFile(%s) = %v; a symlink is not a missing file", rel, err)
			}
		}
	})
}

func TestReadFileFIFODoesNotBlock(t *testing.T) {
	resolvers(t, func(t *testing.T) {
		root := t.TempDir()
		if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
			t.Skip("mkfifo:", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := ReadFile(root, "fifo", 1<<20)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotRegular) {
				t.Errorf("FIFO: %v, want ErrNotRegular", err)
			}
		case <-time.After(5 * time.Second):
			// Unblock the stuck open so the test binary can exit.
			if f, err := os.OpenFile(filepath.Join(root, "fifo"), os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				f.Close()
			}
			t.Fatal("ReadFile blocked on a FIFO")
		}
	})
}

func TestReadFileSizeLimit(t *testing.T) {
	resolvers(t, func(t *testing.T) {
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, "f"), strings.Repeat("x", 100))
		if got, err := ReadFile(root, "f", 100); err != nil || len(got) != 100 {
			t.Errorf("at the limit: %d bytes, %v", len(got), err)
		}
		if _, err := ReadFile(root, "f", 99); !errors.Is(err, ErrTooLarge) {
			t.Errorf("over the limit: %v, want ErrTooLarge", err)
		}
		if _, err := ReadFile(root, "f", -1); err == nil {
			t.Error("negative limit accepted")
		}
		mustWrite(t, filepath.Join(root, "empty"), "")
		if got, err := ReadFile(root, "empty", 0); err != nil || len(got) != 0 {
			t.Errorf("empty file: %q, %v", got, err)
		}
	})
}

func TestBadRelPaths(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)
	for _, rel := range []string{"", ".", "..", "../x", "a/../../x", "/etc/passwd", filepath.Join(root, "x")} {
		if _, err := ReadFile(root, rel, 10); err == nil {
			t.Errorf("ReadFile(%q) accepted", rel)
		}
		if err := WriteFile(root, rel, []byte("x"), 0o600, -1, -1); err == nil {
			t.Errorf("WriteFile(%q) accepted", rel)
		}
	}
	for _, rel := range []string{"..", "../x", "/tmp/x"} {
		if err := MkdirAll(root, rel, 0o700, -1, -1); err == nil {
			t.Errorf("MkdirAll(%q) accepted", rel)
		}
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 1 {
		t.Errorf("something was written next to root: %v", entries)
	}
	if err := MkdirAll(root, "", 0o700, -1, -1); err != nil {
		t.Errorf(`MkdirAll(root, "") = %v`, err)
	}
}

func TestRootChecks(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	mustWrite(t, filepath.Join(home, "f"), "x")
	mustSymlink(t, home, filepath.Join(base, "link"))

	// root's last component must not be a symlink.
	if _, err := ReadFile(filepath.Join(base, "link"), "f", 10); err == nil {
		t.Error("symlinked root accepted for reading")
	}
	if err := WriteFile(filepath.Join(base, "link"), "g", []byte("x"), 0o600, -1, -1); err == nil {
		t.Error("symlinked root accepted for writing")
	}
	if _, err := os.Stat(filepath.Join(home, "g")); err == nil {
		t.Error("wrote through a symlinked root")
	}
	if _, err := ReadFile(filepath.Join(home, "f"), "x", 10); err == nil {
		t.Error("a file accepted as root")
	}
	if _, err := ReadFile(filepath.Join(base, "missing"), "x", 10); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing root: %v, want ErrNotExist", err)
	}

	if !isRoot() {
		t.Log("not root: skipping the root-owner checks")
		return
	}
	if err := os.Chown(home, 4242, 4242); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(home, "f", 10); err == nil {
		t.Error("root owned by a stranger accepted")
	}
	if err := WriteFile(home, "g", []byte("x"), 0o600, 4242, 4242); err != nil {
		t.Errorf("root owned by the target uid refused: %v", err)
	}
	old := config.GamerUID
	config.GamerUID = 4242
	defer func() { config.GamerUID = old }()
	if _, err := ReadFile(home, "f", 10); err != nil {
		t.Errorf("root owned by the gaming user refused: %v", err)
	}
}

func TestWriteFileCreatesDirsAndSetsModes(t *testing.T) {
	withUmask(t, 0o077)
	root := t.TempDir()
	uid, gid := -1, -1
	if isRoot() {
		uid, gid = 4242, 4243
	}
	if err := os.Mkdir(filepath.Join(root, "existing"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(root, "existing"), 0o751)
	if err := WriteFile(root, "existing/new/deeper/file", []byte("data"), 0o644, uid, gid); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "existing/new/deeper/file")); err != nil || string(b) != "data" {
		t.Fatalf("content = %q, %v", b, err)
	}
	wantUID, wantGID := os.Geteuid(), -1
	if isRoot() {
		wantUID, wantGID = uid, gid
	}
	check := func(rel string, mode os.FileMode) {
		t.Helper()
		fi, err := os.Lstat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("%s mode = %v, want %v", rel, fi.Mode().Perm(), mode)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if int(st.Uid) != wantUID || (wantGID >= 0 && int(st.Gid) != wantGID) {
			t.Errorf("%s owner = %d:%d, want %d:%d", rel, st.Uid, st.Gid, wantUID, wantGID)
		}
	}
	check("existing/new", 0o700)
	check("existing/new/deeper", 0o700)
	check("existing/new/deeper/file", 0o644)
	// An existing directory keeps its mode and owner.
	if fi, _ := os.Stat(filepath.Join(root, "existing")); fi.Mode().Perm() != 0o751 {
		t.Errorf("existing directory mode changed to %v", fi.Mode().Perm())
	}
	if st := stat(t, filepath.Join(root, "existing")); int(st.Uid) != os.Geteuid() {
		t.Errorf("existing directory owner changed to %d", st.Uid)
	}

	// DirPerm, and replacing a file changes its mode and owner too.
	if err := WriteFile(root, "other/file", []byte("1"), 0o600, uid, gid, DirPerm(0o750)); err != nil {
		t.Fatal(err)
	}
	check("other", 0o750)
	if err := WriteFile(root, "other/file", []byte("2"), 0o640, uid, gid); err != nil {
		t.Fatal(err)
	}
	check("other/file", 0o640)

	// Only permission bits are applied.
	if err := WriteFile(root, "suid", []byte("x"), 0o4755, uid, gid); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "suid")); fi.Mode()&os.ModeSetuid != 0 {
		t.Error("setuid bit applied")
	}
	assertNoTemps(t, root)
}

func TestWriteFileOwnFileAsCaller(t *testing.T) {
	// Passing the caller's own ids works without privileges.
	root := t.TempDir()
	if err := WriteFile(root, "f", []byte("x"), 0o600, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if st := stat(t, filepath.Join(root, "f")); int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid() {
		t.Errorf("owner = %d:%d", st.Uid, st.Gid)
	}
}

func TestWriteFileReplacesSymlinkTarget(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	mustWrite(t, victim, "precious")
	os.Chmod(victim, 0o644)
	mustSymlink(t, victim, filepath.Join(root, "conf"))
	mustSymlink(t, "/dev/null", filepath.Join(root, "null"))

	if err := WriteFile(root, "conf", []byte("new"), 0o600, -1, -1); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "null", []byte("new"), 0o600, -1, -1); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Errorf("symlink target modified: %q", b)
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o644 {
		t.Errorf("symlink target mode changed: %v", fi.Mode().Perm())
	}
	for _, name := range []string{"conf", "null"} {
		fi, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !fi.Mode().IsRegular() {
			t.Errorf("%s not replaced by a regular file: %v, %v", name, fi, err)
		}
	}
	if fi, err := os.Stat("/dev/null"); err != nil || fi.Mode()&os.ModeDevice == 0 {
		t.Errorf("/dev/null damaged: %v, %v", fi, err)
	}
}

func TestWriteFileReplacesHardLinkAndFIFO(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	mustWrite(t, victim, "precious")
	if err := os.Link(victim, filepath.Join(root, "linked")); err != nil {
		t.Skip("link:", err)
	}
	if err := WriteFile(root, "linked", []byte("new"), 0o600, -1, -1); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Errorf("hard-linked file modified: %q", b)
	}

	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() { done <- WriteFile(root, "fifo", []byte("x"), 0o600, -1, -1) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteFile blocked on a FIFO")
	}
	if fi, _ := os.Lstat(filepath.Join(root, "fifo")); !fi.Mode().IsRegular() {
		t.Errorf("FIFO not replaced: %v", fi.Mode())
	}
}

func TestWriteFileRefusesBadComponents(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	mustSymlink(t, outside, filepath.Join(root, "dirlink"))
	mustWrite(t, filepath.Join(root, "file"), "x")
	os.Mkdir(filepath.Join(root, "adir"), 0o700)

	for _, rel := range []string{"dirlink/f", "dirlink/new/f", "file/f", "adir"} {
		if err := WriteFile(root, rel, []byte("x"), 0o600, -1, -1); err == nil {
			t.Errorf("WriteFile(%s) succeeded", rel)
		}
	}
	for _, rel := range []string{"dirlink", "dirlink/new", "file", "file/sub"} {
		if err := MkdirAll(root, rel, 0o700, -1, -1); err == nil {
			t.Errorf("MkdirAll(%s) succeeded", rel)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrote through a symlink: %v", entries)
	}
	if fi, _ := os.Lstat(filepath.Join(root, "dirlink")); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("directory symlink was replaced")
	}
	assertNoTemps(t, root)
}

func TestMkdirAll(t *testing.T) {
	withUmask(t, 0o077)
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "a"), 0o700)
	os.Chmod(filepath.Join(root, "a"), 0o711)
	if err := MkdirAll(root, "a/b/c", 0o755, -1, -1); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]os.FileMode{"a": 0o711, "a/b": 0o755, "a/b/c": 0o755} {
		fi, err := os.Lstat(filepath.Join(root, rel))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want a directory with mode %v", rel, fi, err, want)
		}
	}
	// Existing directories are fine and left alone.
	if err := MkdirAll(root, "a/b/c", 0o700, -1, -1); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "a/b/c")); fi.Mode().Perm() != 0o755 {
		t.Errorf("existing directory mode changed to %v", fi.Mode().Perm())
	}
	if isRoot() {
		if err := MkdirAll(root, "owned/x", 0o700, 4242, 4243); err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{"owned", "owned/x"} {
			if st := stat(t, filepath.Join(root, rel)); st.Uid != 4242 || st.Gid != 4243 {
				t.Errorf("%s owner = %d:%d", rel, st.Uid, st.Gid)
			}
		}
	}
}

// TestSwapRace swaps a directory for a symlink out of the tree, over and
// over, while files are written and read through it: nothing may ever
// land outside or be read from there.
func TestSwapRace(t *testing.T) {
	resolvers(t, func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		mustWrite(t, filepath.Join(outside, "b", "f"), "SECRET")
		mustWrite(t, filepath.Join(root, "a", "b", "f"), "inside")
		dir, aside := filepath.Join(root, "a"), filepath.Join(root, "a.real")

		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if os.Rename(dir, aside) != nil {
					os.RemoveAll(aside) // a directory WriteFile made meanwhile is in the way
					continue
				}
				os.Symlink(outside, dir)
				runtime.Gosched()
				os.Remove(dir)
				os.Rename(aside, dir)
			}
		}()
		deadline := time.Now().Add(300 * time.Millisecond)
		for i := 0; time.Now().Before(deadline); i++ {
			WriteFile(root, "a/b/f", []byte("inside"), 0o600, -1, -1)
			if got, err := ReadFile(root, "a/b/f", 100); err == nil && bytes.Contains(got, []byte("SECRET")) {
				t.Fatalf("read through a swapped-in symlink after %d rounds", i)
			}
		}
		close(stop)
		<-done
		if b, _ := os.ReadFile(filepath.Join(outside, "b", "f")); string(b) != "SECRET" {
			t.Errorf("file outside the tree changed: %q", b)
		}
		entries, _ := os.ReadDir(filepath.Join(outside, "b"))
		if len(entries) != 1 {
			t.Errorf("files created outside the tree: %v", entries)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 1 {
			t.Errorf("files created outside the tree: %v", entries)
		}
	})
}

func assertNoTemps(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), ".vos-") {
			t.Errorf("temporary file left behind: %s", path)
		}
		return nil
	})
}
