//go:build unix

package system

import "syscall"

// diskUsage returns the total and available (to unprivileged users, like
// df) bytes of the filesystem holding path.
func diskUsage(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	// Bsize is int64 on Linux and uint32 on macOS.
	bs := uint64(st.Bsize)
	return uint64(st.Blocks) * bs, uint64(st.Bavail) * bs, nil
}
