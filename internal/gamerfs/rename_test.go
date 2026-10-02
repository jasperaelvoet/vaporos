//go:build unix

package gamerfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRename(t *testing.T) {
	root := t.TempDir()
	area := filepath.Join(root, ".local", "share", "ext", "truckersmp")
	mustWrite(t, filepath.Join(area, "files", "a.scs"), "x")
	if err := Rename(root, ".local/share/ext/truckersmp", ".trash-truckersmp-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(area); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, ".local", "share", "ext", ".trash-truckersmp-1", "files", "a.scs")); err != nil || string(b) != "x" {
		t.Fatalf("moved: %q, %v", b, err)
	}
	if err := Rename(root, ".local/share/ext/truckersmp", ".trash-truckersmp-2"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if err := Rename(root, ".local/share/none/truckersmp", "x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing parent: %v", err)
	}

	// A symlink is renamed itself; its target stays where it is.
	elsewhere := t.TempDir()
	mustWrite(t, filepath.Join(elsewhere, "keep"), "x")
	mustSymlink(t, elsewhere, filepath.Join(root, ".local", "share", "ext", "star-citizen"))
	if err := Rename(root, ".local/share/ext/star-citizen", ".trash-star-citizen-1"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, ".local", "share", "ext", ".trash-star-citizen-1")); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("the link: %v, %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "keep")); err != nil {
		t.Fatalf("the target moved: %v", err)
	}

	// Nothing through a symlinked parent, and only one component as the name.
	other := t.TempDir()
	mustWrite(t, filepath.Join(other, "victim", "f"), "x")
	mustSymlink(t, other, filepath.Join(root, "linked"))
	if err := Rename(root, "linked/victim", "gone"); err == nil {
		t.Fatal("renamed through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(other, "victim", "f")); err != nil {
		t.Fatalf("victim moved: %v", err)
	}
	for _, name := range []string{"", ".", "..", "a/b"} {
		if err := Rename(root, ".local/share/ext/.trash-truckersmp-1", name); err == nil {
			t.Fatalf("renamed to %q", name)
		}
	}
	if err := Rename(root, "../x", "y"); err == nil {
		t.Fatal("renamed outside the root")
	}
}
