//go:build linux

package buildcheck

import (
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

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
