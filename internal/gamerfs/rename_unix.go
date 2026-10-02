//go:build unix

package gamerfs

import (
	"io/fs"

	"golang.org/x/sys/unix"
)

func rename(root string, parts []string, name string) error {
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
	if err := unix.Renameat(dfd, parts[last], dfd, name); err != nil {
		return &fs.PathError{Op: "rename", Path: join(root, parts), Err: err}
	}
	return nil
}
