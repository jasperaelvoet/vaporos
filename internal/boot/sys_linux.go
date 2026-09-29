//go:build linux

package boot

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames src to dst and fails with EEXIST if dst exists
// (vfat supports RENAME_NOREPLACE, as do the filesystems tests run on).
func renameNoReplace(src, dst string) error {
	err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		return renameIfAbsent(src, dst)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}

// fsImmutableFL is FS_IMMUTABLE_FL from linux/fs.h.
const fsImmutableFL = 0x00000010

// clearImmutable drops FS_IMMUTABLE_FL, which efivarfs sets on every
// variable so a stray rm cannot brick a machine. Best effort: the removal
// that follows reports what matters.
func clearImmutable(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	flags, err := unix.IoctlGetUint32(int(f.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil || flags&fsImmutableFL == 0 {
		return
	}
	unix.IoctlSetPointerInt(int(f.Fd()), unix.FS_IOC_SETFLAGS, int(flags&^fsImmutableFL))
}
