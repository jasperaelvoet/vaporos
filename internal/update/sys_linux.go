//go:build linux && (amd64 || arm64)

package update

import (
	"os"
	"syscall"
)

// exclusiveFlag makes opening a slot partition fail with EBUSY while it is
// mounted: on Linux, O_EXCL on a block device means "nobody else has it".
const exclusiveFlag = syscall.O_EXCL

// dropCache evicts f's cached pages (POSIX_FADV_DONTNEED), so the read-back
// that follows comes from the disk rather than from the page cache the
// write went through.
func dropCache(f *os.File) {
	const fadvDontNeed = 4
	syscall.Syscall6(syscall.SYS_FADVISE64, f.Fd(), 0, 0, fadvDontNeed, 0, 0)
}

// diskFree returns the bytes available to us on the filesystem at path.
func diskFree(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
