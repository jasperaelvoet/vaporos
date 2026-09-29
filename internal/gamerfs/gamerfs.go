// Package gamerfs reads and writes files, as root, inside a directory tree
// that an untrusted user owns: the gaming user's home (config.GamerHome)
// and runtime directory (/run/user/<uid>). Steam and every game run as
// that user, so at any moment the tree may hold planted symlinks, FIFOs,
// device links or huge files, and its directories may be renamed or
// swapped for symlinks between two system calls.
//
// Every function resolves rel beneath root without following a symbolic
// link in any component, and never looks a checked directory up by name a
// second time:
//
//   - Reads open the file with openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|
//     RESOLVE_NO_MAGICLINKS) on Linux, or, where openat2 is missing, walk
//     one component at a time with openat(O_NOFOLLOW). They open with
//     O_NONBLOCK, so a FIFO cannot hang the caller, and accept only a
//     regular file (checked with fstat on the open descriptor).
//   - Writes walk the directories as descriptors (openat with O_DIRECTORY|
//     O_NOFOLLOW, mkdirat for missing ones), create a temporary file in the
//     final directory with O_CREAT|O_EXCL|O_NOFOLLOW, set its owner and mode
//     through the descriptor, fsync it and renameat it over the target.
//     A symlink at the target is replaced, never followed; its target is
//     left untouched.
//
// A symlink anywhere else in rel is an error (ELOOP or ENOTDIR), as is a
// component that exists but is not a directory. Once a directory is open,
// a later swap can only redirect the operation to a directory the user
// already controls.
//
// root itself is trusted and is resolved by name: callers pass
// config.GamerHome or the user's runtime directory, whose parents the user
// cannot write. root must be a real directory (its last component is
// opened with O_NOFOLLOW) owned by root, by the calling process, by
// config.GamerUID or by the uid given to WriteFile or MkdirAll; anything
// else is refused.
//
// rel is a relative, slash-separated path that stays beneath root
// (filepath.IsLocal): no leading slash and no ".." that climbs out.
// Errors are *fs.PathError values wrapping the system error, so
// errors.Is(err, fs.ErrNotExist) works as usual.
//
// VaporOS runs on Linux. The same descriptor walk runs on every Unix
// (tests run on macOS); other systems get an error.
package gamerfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

var (
	// ErrTooLarge means a file is larger than the limit given to ReadFile.
	ErrTooLarge = errors.New("file too large")
	// ErrNotRegular means the path names a FIFO, device, socket or
	// directory where a regular file was expected.
	ErrNotRegular = errors.New("not a regular file")
)

// Option adjusts WriteFile.
type Option func(*options)

type options struct {
	dirPerm os.FileMode
}

// DirPerm sets the mode of directories WriteFile creates (default 0700).
// Directories that already exist keep their owner and mode.
func DirPerm(perm os.FileMode) Option {
	return func(o *options) { o.dirPerm = perm.Perm() }
}

// Open opens the regular file rel beneath root for reading. The caller
// may also fchmod or fchown it through the returned *os.File, which is
// safe: the descriptor is the file that was checked.
func Open(root, rel string) (*os.File, error) {
	parts, err := split(root, rel, false)
	if err != nil {
		return nil, err
	}
	return openFile(root, parts)
}

// ReadFile returns the contents of the regular file rel beneath root. A
// file larger than max bytes is an error wrapping ErrTooLarge; a FIFO,
// device, socket or directory one wrapping ErrNotRegular.
func ReadFile(root, rel string, max int64) ([]byte, error) {
	if max < 0 {
		return nil, fmt.Errorf("gamerfs: negative size limit %d", max)
	}
	f, err := Open(root, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	tooLarge := &fs.PathError{Op: "read", Path: f.Name(), Err: ErrTooLarge}
	if fi.Size() > max {
		return nil, tooLarge
	}
	// The file may still grow while it is read.
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, tooLarge
	}
	return data, nil
}

// WriteFile atomically replaces the file rel beneath root with data, mode
// perm (permission bits only, umask not applied), owned by uid:gid. Missing
// parent directories are created one at a time, owned by uid:gid with mode
// 0700 (see DirPerm). A uid or gid of -1 leaves that part of the owner as
// the calling process, as with chown(2).
//
// Readers never see a partial file or one with the wrong owner or mode:
// the temporary file gets all three before it is renamed into place, and
// both it and the directory are fsynced.
func WriteFile(root, rel string, data []byte, perm os.FileMode, uid, gid int, opts ...Option) error {
	parts, err := split(root, rel, false)
	if err != nil {
		return err
	}
	o := options{dirPerm: 0o700}
	for _, opt := range opts {
		opt(&o)
	}
	return writeFile(root, parts, data, perm.Perm(), mkdirSpec{perm: o.dirPerm, uid: uid, gid: gid})
}

// MkdirAll makes sure the directory rel beneath root exists, creating
// each missing component owned by uid:gid (-1 leaves it as the caller's)
// with mode perm (permission bits only, umask not applied). Directories
// that already exist keep their owner and mode. An empty rel or "." only
// checks root.
func MkdirAll(root, rel string, perm os.FileMode, uid, gid int) error {
	parts, err := split(root, rel, true)
	if err != nil {
		return err
	}
	return mkdirAll(root, parts, mkdirSpec{perm: perm.Perm(), uid: uid, gid: gid})
}

// mkdirSpec is how directories are created on the way to a file.
type mkdirSpec struct {
	perm     os.FileMode
	uid, gid int
}

// split checks rel and returns its components. With dirOK, rel may name
// root itself ("" or "."), giving no components.
func split(root, rel string, dirOK bool) ([]string, error) {
	if dirOK && (rel == "" || rel == ".") {
		return nil, nil
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if !filepath.IsLocal(rel) || clean == "." {
		return nil, &fs.PathError{Op: "open", Path: filepath.Join(root, rel), Err: errors.New("path is not beneath the root")}
	}
	return strings.Split(clean, "/"), nil
}

// trustedRootOwner reports whether a root directory owned by owner may be
// used by a caller acting for uid (-1 when there is none).
func trustedRootOwner(owner, uid int) bool {
	return owner == 0 || owner == os.Geteuid() || owner == config.GamerUID || (uid >= 0 && owner == uid)
}

// join is root/parts[0]/…/parts[n-1], for error messages.
func join(root string, parts []string) string {
	return filepath.Join(append([]string{root}, parts...)...)
}
