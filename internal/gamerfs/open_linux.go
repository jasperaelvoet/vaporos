package gamerfs

import (
	"errors"
	"io/fs"
	"strings"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// noOpenat2 is set once openat2 turned out to be missing (ENOSYS: a kernel
// older than 5.6, or a seccomp filter that hides it).
var noOpenat2 atomic.Bool

// openBeneath opens the file parts beneath rootfd with openat2, which lets
// the kernel refuse every symlink and any escape in one call, or with the
// openat walk where openat2 is missing. It takes over rootfd.
func openBeneath(rootfd int, root string, parts []string) (int, error) {
	if !forceWalk && !noOpenat2.Load() {
		how := unix.OpenHow{
			Flags:   uint64(readFlags),
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		}
		fd, err := unix.Openat2(rootfd, strings.Join(parts, "/"), &how)
		if !errors.Is(err, unix.ENOSYS) {
			unix.Close(rootfd)
			if err != nil {
				return -1, &fs.PathError{Op: "open", Path: join(root, parts), Err: err}
			}
			return fd, nil
		}
		noOpenat2.Store(true)
	}
	return openWalk(rootfd, root, parts)
}
