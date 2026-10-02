//go:build linux

package buildcheck

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// xattrBlindness says why this process may not see every forbidden
// attribute: the kernel lists trusted.* ones only to holders of
// CAP_SYS_ADMIN in the initial user namespace.
func xattrBlindness() string {
	const why = "check-tree runs without CAP_SYS_ADMIN in the initial user namespace, so trusted.* extended attributes (trusted.overlay.*) are invisible to it and were not checked"
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil || data[0].Effective&(1<<unix.CAP_SYS_ADMIN) == 0 {
		return why
	}
	b, err := os.ReadFile("/proc/self/uid_map")
	if err != nil || strings.Join(strings.Fields(string(b)), " ") != "0 0 4294967295" {
		return why
	}
	return ""
}

// forbiddenXattrs returns the extended attributes of host (not following a
// symlink) that an image may not carry.
func forbiddenXattrs(host string) ([]string, error) {
	for {
		n, err := unix.Llistxattr(host, nil)
		if errors.Is(err, unix.ENOTSUP) {
			return nil, nil
		}
		if err != nil || n == 0 {
			return nil, err
		}
		buf := make([]byte, n)
		n, err = unix.Llistxattr(host, buf)
		if errors.Is(err, unix.ERANGE) {
			continue // the list grew between the two calls
		}
		if err != nil {
			return nil, err
		}
		var out []string
		for _, name := range strings.Split(string(buf[:n]), "\x00") {
			if name != "" && forbiddenXattr(name) {
				out = append(out, name)
			}
		}
		return out, nil
	}
}
