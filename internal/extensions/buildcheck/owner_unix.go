//go:build unix

package buildcheck

import (
	"fmt"
	"io/fs"
	"syscall"
)

// owner returns a file's "uid:gid".
func owner(fi fs.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Uid, st.Gid)
	}
	return ""
}
