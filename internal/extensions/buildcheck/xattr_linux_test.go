//go:build linux

package buildcheck

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestXattrs(t *testing.T) {
	tree, d := newDemo(t)
	file := filepath.Join(tree, "usr/share/doc/demo/README")
	// tmpfs before Linux 6.6 refuses user.* attributes with EPERM.
	if err := unix.Lsetxattr(file, "user.demo", []byte("1"), 0); errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) {
		t.Skip("no user extended attributes on this filesystem")
	} else if err != nil {
		t.Fatal(err)
	}
	wantClean(t, run(t, tree, newBase(t), d))

	if xattrBlindness() != "" {
		t.Skip("setting trusted.* and security.capability needs CAP_SYS_ADMIN in the initial user namespace")
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

// Without CAP_SYS_ADMIN in the initial user namespace the kernel hides
// trusted.* attributes, so a clean result would mean nothing about them.
func TestXattrBlindnessWarns(t *testing.T) {
	tree, d := newDemo(t)
	r := run(t, tree, newBase(t), d)
	warned := slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, "CAP_SYS_ADMIN") })
	if os.Geteuid() != 0 && !warned {
		t.Fatalf("no warning without root: %q", r.Warnings)
	}
	if warned != (xattrBlindness() != "") {
		t.Fatalf("warned %v, blindness %q", warned, xattrBlindness())
	}
}
