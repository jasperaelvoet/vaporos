package truckersmp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// `vos ext truckersmp copy-profiles`, as the gaming user: the Linux builds
// of ETS2 and ATS keep their profiles in ~/.local/share/<game>/profiles,
// the Windows builds TruckersMP needs in the Proton prefix's Documents.
// Profiles saved in Steam Cloud follow by themselves; those saved only on
// this PC are copied, each one the prefix does not already have.

// copyResult is what one copy did, for the log and the error.
type copyResult struct {
	copied  []string // "<game>: <profile folder>"
	problem string   // the first game whose profiles could not be copied, and why
	found   bool     // any Linux profile at all
}

// copyProfiles copies the profiles; why a copy failed goes to stderr, the
// card gets a plain sentence.
func copyProfiles(home string, libs []string, stderr io.Writer) copyResult {
	var r copyResult
	failed := func(g game, err error) {
		fmt.Fprintf(stderr, "truckersmp: copying the %s profiles: %v\n", g.short, err)
		r.note(fmt.Sprintf("Couldn't copy the %s profiles. Try again.", g.short))
	}
	for _, g := range games {
		src := filepath.Join(home, nativeDocsRel(g), "profiles")
		names, err := profileNames(src)
		if err != nil || len(names) == 0 {
			continue
		}
		r.found = true
		lib := gameLibrary(libs, g)
		if lib == "" {
			r.note(fmt.Sprintf("%s isn't installed in Steam. Install it, start it once, then copy the profiles again.", g.short))
			continue
		}
		if fi, err := os.Stat(filepath.Join(lib, prefixUserRel(g))); err != nil || !fi.IsDir() {
			r.note(fmt.Sprintf("%s hasn't started with Proton yet. Start it once in Steam, then copy the profiles again.", g.short))
			continue
		}
		dst := filepath.Join(lib, prefixDocsRel(g), "profiles")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			failed(g, err)
			continue
		}
		for _, n := range names {
			if _, err := os.Lstat(filepath.Join(dst, n)); err == nil {
				continue // the prefix has one of that name: it stays
			}
			if err := copyTree(filepath.Join(src, n), dst, n); err != nil {
				failed(g, err)
				break
			}
			r.copied = append(r.copied, g.short+": "+n)
		}
	}
	return r
}

func (r *copyResult) note(text string) {
	if r.problem == "" {
		r.problem = text
	}
}

// err is what the action reports: the first problem, or that there was
// nothing to copy.
func (r copyResult) err() error {
	switch {
	case r.problem != "":
		return errors.New(r.problem)
	case !r.found:
		return errors.New("There are no Linux profiles on this PC to copy.")
	}
	return nil
}

// profileNames lists the profile folders in dir.
func profileNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// copyTree copies the folder src into parent/name: into a hidden folder
// first, renamed once whole, so the game never sees half a profile.
// Symbolic links and special files are left out.
func copyTree(src, parent, name string) error {
	tmp, err := os.MkdirTemp(parent, ".vos-copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(tmp, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		case d.Type().IsRegular():
			return copyFile(p, out)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(parent, name))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if fi, err := in.Stat(); err == nil {
		defer os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	}
	return out.Close()
}
