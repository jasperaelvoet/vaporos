package buildcheck

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxLinks = 40

var errNotDir = errors.New("not a directory")

// resolveIn resolves name inside root the way the booted system sees it:
// symlinks are followed within root (absolute targets start again at root)
// and ".." never leaves it, so nothing on the build host is ever read.
// Absolute names are relative to root too. With followLast false the last
// element is not followed (lstat semantics). It returns the host path.
func resolveIn(root, name string, followLast bool) (string, error) {
	var cur []string
	todo := splitPath(name)
	links := 0
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
		rel := path.Join(path.Join(cur...), c)
		host := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(host)
		if err != nil {
			return "", err
		}
		last := len(todo) == 0
		if fi.Mode()&fs.ModeSymlink == 0 || (last && !followLast) {
			if !last && !fi.IsDir() {
				return "", &fs.PathError{Op: "resolve", Path: name, Err: errNotDir}
			}
			cur = append(cur, c)
			continue
		}
		if links++; links > maxLinks {
			return "", &fs.PathError{Op: "resolve", Path: name, Err: errors.New("too many levels of symbolic links")}
		}
		target, err := os.Readlink(host)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(target, "/") {
			cur = cur[:0]
		}
		todo = append(splitPath(target), todo...)
	}
	return filepath.Join(root, filepath.FromSlash(path.Join(cur...))), nil
}

// existsIn reports whether name exists inside root, without following its
// last element.
func existsIn(root, name string) bool {
	host, err := resolveIn(root, name, false)
	if err != nil {
		return false
	}
	_, err = os.Lstat(host)
	return err == nil
}

// readIn reads a file inside root, following symlinks within it.
func readIn(root, name string, limit int64) ([]byte, error) {
	host, err := resolveIn(root, name, true)
	if err != nil {
		return nil, err
	}
	return readLimited(host, limit)
}

func readLimited(host string, limit int64) ([]byte, error) {
	f, err := os.Open(host)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", host)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", host, limit)
	}
	return b, nil
}

// readDirIn lists a directory inside root, following symlinks within it.
// A missing directory is empty.
func readDirIn(root, name string) ([]fs.DirEntry, string, error) {
	host, err := resolveIn(root, name, true)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errNotDir) {
			return nil, "", nil
		}
		return nil, "", err
	}
	ents, err := os.ReadDir(host)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, host, nil
	}
	return ents, host, err
}

func splitPath(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// under reports whether rel is dir or inside it.
func under(rel, dir string) bool {
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}
