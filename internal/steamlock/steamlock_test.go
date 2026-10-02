//go:build unix

package steamlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func useDir(t *testing.T) string {
	t.Helper()
	old := config.GamerRuntimeDir
	t.Cleanup(func() { config.GamerRuntimeDir = old })
	config.GamerRuntimeDir = t.TempDir()
	return config.GamerRuntimeDir
}

func TestLockExcludesAndReleases(t *testing.T) {
	dir := useDir(t)
	unlock, err := Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(dir, Name))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file %v, %v", fi, err)
	}

	// Another holder (a second open file description, as another process
	// has) waits, and gives up when its context ends.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock while held: %v", err)
	}

	got := make(chan error, 1)
	go func() {
		u, err := Lock(context.Background())
		if err == nil {
			u()
		}
		got <- err
	}()
	time.Sleep(2 * poll)
	unlock()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock was not handed on")
	}
}

func TestLockRefusesAPlantedSymlink(t *testing.T) {
	dir := useDir(t)
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, Name)); err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(context.Background()); err == nil {
		t.Fatal("locked through a symlink")
	}
}

func TestLockWithoutRuntimeDir(t *testing.T) {
	old := config.GamerRuntimeDir
	t.Cleanup(func() { config.GamerRuntimeDir = old })
	config.GamerRuntimeDir = filepath.Join(t.TempDir(), "missing")
	if _, err := Lock(context.Background()); err == nil {
		t.Fatal("locked without a runtime directory")
	}
}
