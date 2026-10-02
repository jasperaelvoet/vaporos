//go:build linux

package buildcheck

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestXattrs(t *testing.T) {
	tree, d := newDemo(t)
	file := filepath.Join(tree, "usr/share/doc/demo/README")
	if err := unix.Lsetxattr(file, "user.demo", []byte("1"), 0); errors.Is(err, unix.ENOTSUP) {
		t.Skip("no extended attributes on this filesystem")
	} else if err != nil {
		t.Fatal(err)
	}
	wantClean(t, run(t, tree, newBase(t), d))

	if os.Geteuid() != 0 {
		t.Skip("setting trusted.* and security.capability needs root")
	}
	if err := unix.Lsetxattr(filepath.Join(tree, "usr/share/doc/demo"), "trusted.overlay.opaque", []byte("y"), 0); err != nil {
		t.Fatal(err)
	}
	// An empty capability set is enough: the attribute itself is refused.
	caps := make([]byte, 20)
	caps[3] = 0x02 // VFS_CAP_REVISION_2, no flags
	if err := unix.Lsetxattr(filepath.Join(tree, "usr/bin/demo"), "security.capability", caps, 0); err != nil {
		t.Fatal(err)
	}
	r := run(t, tree, newBase(t), d)
	wantProblem(t, r, "usr/share/doc/demo: carries the trusted.overlay.opaque extended attribute")
	wantProblem(t, r, "usr/bin/demo: carries the security.capability extended attribute")
}
