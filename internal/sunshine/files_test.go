package sunshine

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

func TestReadRegularRefusesSymlinkedDir(t *testing.T) {
	isolate(t)
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(outside, "sunshine"), 0o700)
	os.WriteFile(filepath.Join(outside, "sunshine", "sunshine.conf"), []byte("SECRET"), 0o600)
	os.Symlink(outside, filepath.Join(config.GamerHome, ".config"))
	if b, err := readRegular(confPath()); err == nil {
		t.Fatalf("read %q through a symlinked directory", b)
	}
	// A pacman database path outside the home still reads.
	db := filepath.Join(t.TempDir(), "desc")
	os.WriteFile(db, []byte("%NAME%\nsunshine\n"), 0o644)
	if b, err := readRegular(db); err != nil || len(b) == 0 {
		t.Errorf("readRegular(%s) = %q, %v", db, b, err)
	}
}

func TestReadRegularFIFODoesNotBlock(t *testing.T) {
	isolate(t)
	os.MkdirAll(sunshineDir(), 0o700)
	if err := syscall.Mkfifo(logPath(), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readRegular(logPath())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, gamerfs.ErrNotRegular) {
			t.Errorf("FIFO: %v, want ErrNotRegular", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readRegular blocked on a FIFO")
	}
}

func TestReadRegularSizeLimit(t *testing.T) {
	isolate(t)
	os.MkdirAll(sunshineDir(), 0o700)
	if err := os.WriteFile(confPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(confPath(), maxFileSize+1); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegular(confPath()); !errors.Is(err, gamerfs.ErrTooLarge) {
		t.Errorf("oversized file: %v, want ErrTooLarge", err)
	}
}

func TestWriteGamerFileCreatesUserDirs(t *testing.T) {
	isolate(t)
	if changed, err := writeGamerFile(confPath(), []byte("x"), 0o600); err != nil || !changed {
		t.Fatalf("writeGamerFile = %v, %v", changed, err)
	}
	for _, p := range []string{filepath.Join(config.GamerHome, ".config"), sunshineDir(), confPath()} {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o700)
		if p == confPath() {
			want = 0o600
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", p, fi.Mode().Perm(), want)
		}
		if runningAsRoot() {
			if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != config.GamerUID || int(st.Gid) != config.GamerUID {
				t.Errorf("%s owner = %d:%d, want the gaming user", p, st.Uid, st.Gid)
			}
		}
	}
	if _, err := writeGamerFile(filepath.Join(t.TempDir(), "x"), []byte("x"), 0o600); err == nil {
		t.Error("wrote outside the gaming user's home")
	}
	if _, err := writeGamerFile(config.GamerHome, []byte("x"), 0o600); err == nil {
		t.Error("wrote the home itself")
	}
}

// An identical file is kept, but its owner is only fixed when that cannot
// reach a file outside the home: a hard link to a file elsewhere is
// replaced instead of chmodded.
func TestWriteGamerFileHardLinkIsReplaced(t *testing.T) {
	isolate(t)
	os.MkdirAll(sunshineDir(), 0o700)
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("same"), 0o644)
	os.Chmod(victim, 0o644)
	if err := os.Link(victim, confPath()); err != nil {
		t.Skip("link:", err)
	}
	changed, err := writeGamerFile(confPath(), []byte("same"), 0o600)
	if err != nil || !changed {
		t.Fatalf("writeGamerFile = %v, %v; want a rewrite", changed, err)
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o644 {
		t.Errorf("hard-linked file's mode changed to %v", fi.Mode().Perm())
	}
	a, _ := os.Stat(victim)
	b, _ := os.Stat(confPath())
	if os.SameFile(a, b) {
		t.Error("sunshine.conf is still the hard link")
	}
	if fi, _ := os.Stat(confPath()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestWriteGamerFileFixesOwnerOfIdenticalFile(t *testing.T) {
	if !runningAsRoot() {
		t.Skip("needs root to chown")
	}
	isolate(t)
	os.MkdirAll(sunshineDir(), 0o700)
	os.WriteFile(confPath(), []byte("same"), 0o644)
	os.Chown(confPath(), 0, 0)
	changed, err := writeGamerFile(confPath(), []byte("same"), 0o600)
	if err != nil || changed {
		t.Fatalf("writeGamerFile = %v, %v", changed, err)
	}
	fi, _ := os.Stat(confPath())
	if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != config.GamerUID || fi.Mode().Perm() != 0o600 {
		t.Errorf("owner %d mode %v, want %d and 0600", st.Uid, fi.Mode().Perm(), config.GamerUID)
	}
}
