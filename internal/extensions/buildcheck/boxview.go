package buildcheck

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// maxPaths bounds how many paths one glob may lead to.
const maxPaths = 256

// boxView is the box as systemd-tmpfiles sees an extension's lines: /usr is
// the image over the extensions built before it and the base, and the
// extension's own areas hold the symlinks its L lines make. Nothing else is
// known, so other paths resolve as written.
type boxView struct {
	layers []string          // host roots of /usr's layers, topmost first
	own    []string          // the extension's own areas
	links  map[string]string // cleaned path -> target, of its L lines
}

func newBoxView(layers, own []string, lines []tmpfilesLine) *boxView {
	v := &boxView{layers: layers, own: own, links: map[string]string{}}
	for _, l := range lines {
		if k := path.Clean(l.path); l.typ[0] == 'L' && v.links[k] == "" {
			v.links[k] = l.arg
		}
	}
	return v
}

// allowed reports whether a resolved path is one an extension's lines may
// end at: its own areas, or /usr, which is read-only.
func (v *boxView) allowed(p string) bool {
	return inAreas(p, v.own) || under(p, "/usr")
}

// resolve returns the paths name leads to on the box, the way the kernel
// walks it: every symlink on the way is followed (the last element's only
// with followLast), and ".." goes up from where a symlink led. With glob, a
// glob element also stands for each of the extension's links it matches.
func (v *boxView) resolve(name string, followLast, glob bool) ([]string, error) {
	var out []string
	var walk func(cur, todo []string, hops int) error
	walk = func(cur, todo []string, hops int) error {
		for len(todo) > 0 {
			c := todo[0]
			todo = todo[1:]
			switch c {
			case ".":
				continue
			case "..":
				if len(cur) > 0 {
					cur = cur[:len(cur)-1]
				}
				continue
			}
			dir := "/" + path.Join(cur...)
			if glob && strings.ContainsAny(c, "*?[") {
				if !inAreas(dir, v.own) {
					return fmt.Errorf("a glob in %s, where a symlink leads, which this check does not expand", dir)
				}
				for _, l := range sortedKeys(v.links) {
					if ok, _ := path.Match(c, path.Base(l)); ok && path.Dir(l) == dir {
						if err := walk(slices.Clone(cur), append([]string{path.Base(l)}, todo...), hops); err != nil {
							return err
						}
					}
				}
			}
			target, isLink, err := v.readlink(path.Join(dir, c))
			if err != nil {
				return err
			}
			if !isLink || (len(todo) == 0 && !followLast) {
				cur = append(cur, c)
				continue
			}
			if hops++; hops > maxLinks {
				return errors.New("too many levels of symbolic links")
			}
			if strings.HasPrefix(target, "/") {
				cur = nil
			}
			todo = append(splitPath(target), todo...)
		}
		if len(out) == maxPaths {
			return errors.New("a glob that matches too many of its symlinks")
		}
		out = append(out, "/"+path.Join(cur...))
		return nil
	}
	err := walk(nil, splitPath(name), 0)
	return out, err
}

// readlink returns p's target when p is a symlink on the box: one of the
// extension's L lines, or one in /usr's topmost layer that has p.
func (v *boxView) readlink(p string) (string, bool, error) {
	if t, ok := v.links[p]; ok {
		return t, true, nil
	}
	if !under(p, "/usr") {
		return "", false, nil
	}
	for _, root := range v.layers {
		fi, host, err := lstatLayer(root, p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return "", false, err
		case fi.Mode()&fs.ModeSymlink == 0:
			return "", false, nil
		}
		t, err := os.Readlink(host)
		return t, err == nil, err
	}
	return "", false, nil
}

// lstatLayer lstats p in one layer without following any symlink on the
// way, so nothing on the build host is read: a parent that is not a plain
// directory there means the layer does not have p.
func lstatLayer(root, p string) (fs.FileInfo, string, error) {
	host := root
	parts := splitPath(p)
	for i, c := range parts {
		host = filepath.Join(host, c)
		fi, err := os.Lstat(host)
		if err != nil {
			return nil, "", err
		}
		if i == len(parts)-1 {
			return fi, host, nil
		}
		if !fi.IsDir() {
			return nil, "", fs.ErrNotExist
		}
	}
	return nil, "", fs.ErrNotExist
}
