//go:build unix

package gamerfs

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func readDirNames(root string, parts []string, max int) ([]string, error) {
	rootfd, err := openRoot(root, -1)
	if err != nil {
		return nil, err
	}
	dfd, err := walkDirs(rootfd, root, parts, nil)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dfd), join(root, parts))
	defer f.Close()
	names, err := f.Readdirnames(max)
	if errors.Is(err, io.EOF) {
		err = nil // an empty directory, asked for at most max
	}
	return names, err
}

func remove(root string, parts []string) error {
	rootfd, err := openRoot(root, -1)
	if err != nil {
		return err
	}
	last := len(parts) - 1
	dfd, err := walkDirs(rootfd, root, parts[:last], nil)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	if err := unix.Unlinkat(dfd, parts[last], 0); err != nil {
		return &fs.PathError{Op: "remove", Path: join(root, parts), Err: err}
	}
	return nil
}

func openOrCreate(root string, parts []string, perm os.FileMode, uid, gid int) (*os.File, error) {
	rootfd, err := openRoot(root, uid)
	if err != nil {
		return nil, err
	}
	last := len(parts) - 1
	dfd, err := walkDirs(rootfd, root, parts[:last], nil)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dfd)
	path := join(root, parts)
	// O_EXCL tells whether this call made the file (and so may set its
	// owner); it never follows a symlink, and the open after it refuses one.
	fd, err := unix.Openat(dfd, parts[last], readFlags|unix.O_CREAT|unix.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(dfd, parts[last], readFlags, 0)
	}
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	fail := func(op string, err error) (*os.File, error) {
		unix.Close(fd)
		return nil, &fs.PathError{Op: op, Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail("stat", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fail("open", ErrNotRegular)
	}
	if created {
		if uid >= 0 || gid >= 0 {
			if err := unix.Fchown(fd, uid, gid); err != nil {
				return fail("chown", err)
			}
		}
		if err := unix.Fchmod(fd, uint32(perm)); err != nil {
			return fail("chmod", err)
		}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		return fail("fcntl", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}
