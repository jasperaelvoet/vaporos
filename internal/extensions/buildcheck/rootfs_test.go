package buildcheck

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

func TestResolveInStaysInRoot(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"usr/lib/x":       "x",
		"usr/lib64":       "@/usr/lib",
		"usr/lib/rel":     "@../lib/x",
		"usr/lib/escape":  "@../../../../../../etc/hostname",
		"usr/lib/abs":     "@/etc/hostname",
		"etc/hostname":    "inside",
		"loop/a":          "@b",
		"loop/b":          "@a",
		"usr/lib/notadir": "@x",
	})
	for name, want := range map[string]string{
		"/usr/lib64/x":         "usr/lib/x",
		"usr/lib64/x":          "usr/lib/x",
		"/../../usr/lib/x":     "usr/lib/x",
		"usr/lib/rel":          "usr/lib/x",
		"usr/lib/escape":       "etc/hostname",
		"usr/lib/abs":          "etc/hostname",
		"usr/lib64/../lib64/x": "usr/lib/x",
	} {
		got, err := resolveIn(root, name, true)
		if err != nil || got != filepath.Join(root, filepath.FromSlash(want)) {
			t.Errorf("%s: %s %v, want %s", name, got, err, want)
		}
	}
	if got, err := resolveIn(root, "usr/lib64", false); err != nil || got != filepath.Join(root, "usr", "lib64") {
		t.Errorf("not following the last element: %s %v", got, err)
	}
	if _, err := resolveIn(root, "loop/a", true); err == nil {
		t.Error("a symlink loop resolved")
	}
	if _, err := resolveIn(root, "usr/lib/notadir/y", true); !errors.Is(err, errNotDir) {
		t.Errorf("through a file: %v", err)
	}
	if _, err := resolveIn(root, "usr/nope/x", true); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if !existsIn(root, "/usr/lib64/x") || existsIn(root, "/usr/lib64/y") {
		t.Error("existsIn")
	}
	if b, err := readIn(root, "usr/lib/abs", 100); err != nil || string(b) != "inside" {
		t.Errorf("readIn %q %v", b, err)
	}
	if _, err := readIn(root, "etc/hostname", 3); err == nil {
		t.Error("readIn ignored the limit")
	}
}
