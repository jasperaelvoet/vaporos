//go:build unix

package gamerfs

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	dirFlags  = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	readFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	tmpFlags  = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NOFOLLOW | unix.O_CLOEXEC
)

// forceWalk makes reads use the openat walk even where openat2 exists,
// so tests cover both.
var forceWalk bool

// openRoot opens root, which must be a real directory with a trusted
// owner (see trustedRootOwner).
func openRoot(root string, uid int) (int, error) {
	fd, err := unix.Open(root, dirFlags, 0)
	if err != nil {
		return -1, &fs.PathError{Op: "open", Path: root, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, &fs.PathError{Op: "stat", Path: root, Err: err}
	}
	if !trustedRootOwner(int(st.Uid), uid) {
		unix.Close(fd)
		return -1, &fs.PathError{Op: "open", Path: root, Err: fmt.Errorf("owned by uid %d, not root or the gaming user", st.Uid)}
	}
	return fd, nil
}

// walkDirs descends from dfd through the directories parts, one component
// at a time without following a symlink, and returns the last one's
// descriptor. With mk, missing components are created. walkDirs takes
// over dfd: it is returned (no parts) or closed, also on error. path is
// dfd's path, for error messages.
func walkDirs(dfd int, path string, parts []string, mk *mkdirSpec) (int, error) {
	for _, name := range parts {
		path = filepath.Join(path, name)
		next, err := openDir(dfd, name, path, mk)
		unix.Close(dfd)
		if err != nil {
			return -1, err
		}
		dfd = next
	}
	return dfd, nil
}

// openDir opens the directory name in dfd without following a symlink,
// creating it first if it is missing and mk is set.
func openDir(dfd int, name, path string, mk *mkdirSpec) (int, error) {
	fd, err := unix.Openat(dfd, name, dirFlags, 0)
	if err == nil {
		return fd, nil
	}
	if mk == nil || !errors.Is(err, unix.ENOENT) {
		return -1, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	// Created owner-only; owner and mode are set through the descriptor.
	err = unix.Mkdirat(dfd, name, 0o700)
	created := err == nil
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	if fd, err = unix.Openat(dfd, name, dirFlags, 0); err != nil {
		return -1, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	if created {
		if err := adoptDir(fd, mk); err != nil {
			unix.Close(fd)
			return -1, &fs.PathError{Op: "chown", Path: path, Err: err}
		}
		_ = unix.Fsync(dfd)
	}
	return fd, nil
}

// adoptDir gives a directory this process just created its owner and mode.
// If the user swapped another directory in between mkdirat and openat,
// the one opened is not this process's and is left alone.
func adoptDir(fd int, mk *mkdirSpec) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if int(st.Uid) != os.Geteuid() {
		return nil
	}
	if mk.uid >= 0 || mk.gid >= 0 {
		if err := unix.Fchown(fd, mk.uid, mk.gid); err != nil {
			return err
		}
	}
	return unix.Fchmod(fd, uint32(mk.perm))
}

func openFile(root string, parts []string) (*os.File, error) {
	rootfd, err := openRoot(root, -1)
	if err != nil {
		return nil, err
	}
	path := join(root, parts)
	fd, err := openBeneath(rootfd, root, parts) // takes over rootfd
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	// O_NONBLOCK only mattered for opening; make it a plain file again.
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "fcntl", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// openWalk opens the file parts beneath rootfd with the openat walk. It
// takes over rootfd.
func openWalk(rootfd int, root string, parts []string) (int, error) {
	last := len(parts) - 1
	dfd, err := walkDirs(rootfd, root, parts[:last], nil)
	if err != nil {
		return -1, err
	}
	defer unix.Close(dfd)
	fd, err := unix.Openat(dfd, parts[last], readFlags, 0)
	if err != nil {
		return -1, &fs.PathError{Op: "open", Path: join(root, parts), Err: err}
	}
	return fd, nil
}

func mkdirAll(root string, parts []string, mk mkdirSpec) error {
	rootfd, err := openRoot(root, mk.uid)
	if err != nil {
		return err
	}
	dfd, err := walkDirs(rootfd, root, parts, &mk)
	if err != nil {
		return err
	}
	return unix.Close(dfd)
}

func writeFile(root string, parts []string, data []byte, perm os.FileMode, mk mkdirSpec) error {
	rootfd, err := openRoot(root, mk.uid)
	if err != nil {
		return err
	}
	last := len(parts) - 1
	dir := join(root, parts[:last])
	dfd, err := walkDirs(rootfd, root, parts[:last], &mk)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)

	name := parts[last]
	tmp, fd, err := createTemp(dfd, name)
	if err != nil {
		return &fs.PathError{Op: "create", Path: filepath.Join(dir, tmp), Err: err}
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir, tmp))
	err = func() error {
		if _, err := f.Write(data); err != nil {
			return err
		}
		// chown before chmod: chown clears set-id bits.
		if mk.uid >= 0 || mk.gid >= 0 {
			if err := f.Chown(mk.uid, mk.gid); err != nil {
				return err
			}
		}
		if err := f.Chmod(perm); err != nil {
			return err
		}
		return f.Sync()
	}()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if rerr := unix.Renameat(dfd, tmp, dfd, name); rerr != nil {
			err = &fs.PathError{Op: "rename", Path: filepath.Join(dir, name), Err: rerr}
		}
	}
	if err != nil {
		unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	if err := unix.Fsync(dfd); err != nil {
		return &fs.PathError{Op: "sync", Path: dir, Err: err}
	}
	return nil
}

// createTemp creates a new, empty, root-only file next to name in dfd.
// Its name is random, so the user can neither predict nor pre-create it.
func createTemp(dfd int, name string) (string, int, error) {
	if len(name) > 200 { // stay under NAME_MAX with the suffix
		name = name[:200]
	}
	for range 100 {
		tmp := fmt.Sprintf(".%s.vos-%016x", name, rand.Uint64())
		fd, err := unix.Openat(dfd, tmp, tmpFlags, 0o600)
		if !errors.Is(err, unix.EEXIST) {
			return tmp, fd, err
		}
	}
	return "." + name + ".vos-*", -1, unix.EEXIST
}
