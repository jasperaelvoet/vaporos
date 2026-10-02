package buildcheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

func TestCleanTreePasses(t *testing.T) {
	tree, d := newDemo(t)
	r := run(t, tree, newBase(t), d)
	wantClean(t, r)
	if !reflect.DeepEqual(r.Permissions, []string{"service"}) || r.RunsAsRoot || len(r.Warnings) != 0 {
		t.Fatalf("result %+v", r.Result)
	}
}

func TestClassify(t *testing.T) {
	const dir, file, link = fs.ModeDir | 0o755, fs.FileMode(0o644), fs.ModeSymlink | 0o777
	for _, c := range []struct {
		rel    string
		mode   fs.FileMode
		perm   string
		forbid string
	}{
		{"usr/bin/demo", file, "", ""},
		{"usr/share/doc/demo", dir, "", ""},
		{"etc/demo.conf", file, "", "outside usr/"},
		{"usr/local/bin/x", file, "", "usr/local"},
		{"usr/lib/vos", dir, "", ""},
		{"usr/lib/vos/ext", dir, "", ""},
		{"usr/lib/vos/ext/demo/anything/deep", file, "", ""},
		{"usr/lib/vos/ext/other/x", file, "", "usr/lib/vos belongs to the base"},
		{"usr/lib/vos/nftables.nft", file, "", "usr/lib/vos belongs to the base"},
		{"usr/share/vos/extensions/demo.json", file, "", "usr/share/vos"},
		{"usr/lib/systemd/system/demo.service", file, descriptor.PermService, ""},
		{"usr/lib/systemd/system/demo@.service", file, descriptor.PermService, ""},
		{"usr/lib/systemd/system/demo.timer", file, descriptor.PermService, ""},
		{"usr/lib/systemd/system/demo.service.d", dir, "", ""},
		{"usr/lib/systemd/system/demo.service.d/vos.conf", file, descriptor.PermService, ""},
		{"usr/lib/systemd/system/demo.service.d/notes.txt", file, "", "drop-ins end in .conf"},
		{"usr/lib/systemd/system/demo.service.wants/x.service", link, descriptor.PermService, ""},
		{"usr/lib/systemd/system/README", file, "", "not a unit file"},
		{"usr/lib/systemd/system/sub", dir, "", "not a unit's drop-in"},
		{"usr/lib/systemd/user/demo.service", file, descriptor.PermUserService, ""},
		{"usr/lib/systemd/system-generators/x", file, "", "only units"},
		{"usr/lib/systemd/system-shutdown/x", file, "", "only units"},
		{"usr/lib/systemd/system.conf.d/x.conf", file, "", "only units"},
		{"usr/lib/systemd/network/50-x.network", file, "", "only units"},
		{"usr/lib/udev/rules.d/70-demo.rules", file, descriptor.PermUdev, ""},
		{"usr/lib/udev/hwdb.d/x.hwdb", file, "", "hwdb.d"},
		{"usr/lib/udev/demo-helper", file, "", "only udev rules"},
		{"usr/lib/sysctl.d/50-demo.conf", file, descriptor.PermSysctl, ""},
		{"usr/lib/sysctl.d/sub/x.conf", file, "", "nothing nests"},
		{"usr/lib/modules-load.d/demo.conf", file, descriptor.PermModules, ""},
		{"usr/lib/tmpfiles.d/demo.conf", file, descriptor.PermTmpfiles, ""},
		{"usr/lib/tmpfiles.d", link, "", "not a directory"},
		{"usr/share/polkit-1/rules.d/demo.rules", file, descriptor.PermPolkit, ""},
		{"usr/share/polkit-1/actions/demo.policy", file, descriptor.PermPolkit, ""},
		{"usr/share/polkit-1/other/x", file, "", "only polkit"},
		{"usr/share/dbus-1/system.d/demo.conf", file, descriptor.PermDBus, ""},
		{"usr/share/dbus-1/system-services/demo.service", file, descriptor.PermDBus, ""},
		{"usr/share/dbus-1/services/demo.service", file, "", "system bus"},
		{"usr/share/steam/compatibilitytools.d/tool/proton", file, descriptor.PermCompatTool, ""},
		{"usr/share/steam/compatibilitytools.d/tool", dir, "", ""},
		{"usr/lib/security/pam_demo.so", file, "", "PAM"},
		{"usr/share/vulkan/implicit_layer.d/x.json", file, "", "Vulkan"},
		{"usr/lib/binfmt.d/x.conf", file, "", "binfmt"},
		{"usr/lib/environment.d/x.conf", file, "", "environment"},
		{"usr/lib/modprobe.d/x.conf", file, "", "module_options"},
		{"usr/lib/sysusers.d/x.conf", file, "", "users and groups"},
		{"usr/lib/kernel/install.d/x", file, "", "kernel"},
		{"usr/lib/initcpio/hooks/x", file, "", "initramfs"},
		{"usr/share/libalpm/hooks/x.hook", file, "", "pacman"},
		{"usr/lib/NetworkManager/conf.d/x.conf", file, "", "network"},
		{"usr/lib/ld.so.conf.d", dir, "", "library search paths"},
		{"usr/lib/demo/ld.so.conf.d", dir, "", "library search paths"},
		{"usr/lib/modules/6.1/extra/demo.ko", file, "", "kernel modules"},
		{"usr/lib/modules/6.1/extra/demo.ko.zst", file, "", "kernel modules"},
		{"usr/lib/vos/ext/demo/demo.ko.xz", file, "", "kernel modules"},
		{"usr/lib/pam.d", dir, "", "unknown hook directory"},
		{"usr/share/X11/xorg.conf.d", dir, "", "unknown hook directory"},
		{"usr/lib/demo/conf.d", link, "", "unknown hook directory"},
		{"usr/lib/demo/plugins/conf.d", dir, "", ""},
		{"usr/share/demo/notes.d", file, "", ""},
	} {
		v := classify("demo", c.rel, c.mode)
		if v.perm != c.perm || (c.forbid == "") != (v.forbid == "") || !strings.Contains(v.forbid, c.forbid) {
			t.Errorf("%s: %+v, want perm %q forbid %q", c.rel, v, c.perm, c.forbid)
		}
	}
}

func TestOutsideUsr(t *testing.T) {
	tree, d := newDemo(t)
	writeTree(t, tree, map[string]string{"etc/demo.conf": "x", "opt/demo/": ""})
	r := run(t, tree, newBase(t), d)
	wantProblem(t, r, "etc: outside usr/")
	wantProblem(t, r, "opt: outside usr/")
}

func TestForbiddenDirReportedOnceAndOnlyWithFiles(t *testing.T) {
	tree, d := newDemo(t)
	writeTree(t, tree, map[string]string{"usr/lib/udev/hwdb.d/empty/": ""})
	wantClean(t, run(t, tree, newBase(t), d))

	writeTree(t, tree, map[string]string{"usr/lib/udev/hwdb.d/a.hwdb": "x", "usr/lib/udev/hwdb.d/b.hwdb": "x"})
	r := run(t, tree, newBase(t), d)
	if len(r.Problems) != 1 || !strings.HasPrefix(r.Problems[0], "usr/lib/udev/hwdb.d: ") {
		t.Fatalf("problems %q", r.Problems)
	}
}

func TestPermissionsMustMatch(t *testing.T) {
	tree, d := newDemo(t)
	d = declare(t, tree, "service", "udev")
	writeTree(t, tree, map[string]string{"usr/lib/sysctl.d/50-demo.conf": "kernel.demo = 1\n"})
	r := run(t, tree, newBase(t), d)
	wantProblem(t, r, `usr/lib/sysctl.d/50-demo.conf: needs the "sysctl" permission`)
	wantProblem(t, r, `declares the "udev" permission, but nothing in the image needs it`)
	if !reflect.DeepEqual(r.Permissions, []string{"service", "sysctl"}) {
		t.Fatalf("permissions %q", r.Permissions)
	}
}

func TestCollisions(t *testing.T) {
	base := newBase(t)
	other := t.TempDir()
	writeTree(t, other, map[string]string{
		"usr/lib/shared/same.txt": "same",
		"usr/lib/shared/diff.txt": "theirs",
		"usr/lib/shared/link":     "@same.txt",
		"usr/lib/shared/link2":    "@same.txt",
		"usr/lib/shared/sub":      "a file",
	})
	tree, d := newDemo(t)
	writeTree(t, tree, map[string]string{
		"usr/bin/vos":               "mine",
		"usr/sbin/demo":             "x",
		"usr/lib/libc.so.6/":        "",
		"usr/lib/shared/same.txt":   "same",
		"usr/lib/shared/diff.txt":   "mine",
		"usr/lib/shared/link":       "@same.txt",
		"usr/lib/shared/link2":      "@diff.txt",
		"usr/lib/shared/sub/x":      "x",
		"usr/lib/shared/unique.txt": "x",
	})
	r := run(t, tree, base, d, other)
	wantProblem(t, r, "usr/bin/vos: the base already ships it")
	wantProblem(t, r, "usr/sbin: a directory where the base has a symlink")
	wantProblem(t, r, "usr/lib/libc.so.6: a directory where the base has a file")
	wantProblem(t, r, "usr/lib/shared/diff.txt: the extension in "+other+" ships it too")
	wantProblem(t, r, "usr/lib/shared/link2: the extension in "+other+" ships it too")
	wantProblem(t, r, "usr/lib/shared/sub: the extension in "+other+" ships it too")
	for _, p := range r.Problems {
		for _, ok := range []string{"same.txt", "shared/link:", "unique.txt", "usr/sbin/demo"} {
			if strings.Contains(p, ok) {
				t.Errorf("unexpected problem %q", p)
			}
		}
	}
}

func TestModeProblem(t *testing.T) {
	for m, want := range map[fs.FileMode]string{
		0o644:                             "",
		fs.ModeDir | fs.ModeSetgid:        "",
		fs.ModeSymlink | 0o777:            "",
		fs.ModeSetuid | 0o755:             "setuid",
		fs.ModeSetgid | 0o755:             "setgid",
		fs.ModeDevice | fs.ModeCharDevice: "character device",
		fs.ModeDevice:                     "block device",
		fs.ModeNamedPipe:                  "not a regular file",
		fs.ModeSocket:                     "not a regular file",
	} {
		if got := modeProblem(m); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%v: %q, want %q", m, got, want)
		}
	}
}

func TestSetuidFileFails(t *testing.T) {
	tree, d := newDemo(t)
	p := filepath.Join(tree, "usr/bin/demo")
	if err := os.Chmod(p, 0o4755); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode()&fs.ModeSetuid == 0 {
		t.Skip("this filesystem does not keep the setuid bit")
	}
	wantProblem(t, run(t, tree, newBase(t), d), "usr/bin/demo: is setuid")
}

func TestForbiddenXattr(t *testing.T) {
	for name, want := range map[string]bool{
		"security.capability":      true,
		"trusted.overlay.opaque":   true,
		"trusted.overlay.redirect": true,
		"user.demo":                false,
		"security.selinux":         false,
	} {
		if forbiddenXattr(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
}

func TestUaccessWarns(t *testing.T) {
	tree, d := newDemo(t)
	d = declare(t, tree, "service", "udev")
	writeTree(t, tree, map[string]string{
		"usr/lib/udev/rules.d/70-demo.rules": "# TAG+=\"uaccess\" in a comment does not count\nSUBSYSTEM==\"hidraw\", TAG+=\"uaccess\"\n",
		"usr/lib/udev/rules.d/71-ok.rules":   "SUBSYSTEM==\"hidraw\", GROUP=\"input\", MODE=\"0660\"\n",
	})
	r := run(t, tree, newBase(t), d)
	wantClean(t, r)
	if len(r.Warnings) != 1 || !strings.HasPrefix(r.Warnings[0], "usr/lib/udev/rules.d/70-demo.rules: ") {
		t.Fatalf("warnings %q", r.Warnings)
	}
}

func TestStripMustBeGone(t *testing.T) {
	tree, d := newDemo(t)
	writeTree(t, tree, map[string]string{"usr/lib/demo/stripped": "x"})
	wantProblem(t, run(t, tree, newBase(t), d), "usr/lib/demo/stripped: listed in strip, but still in the image")
}

func TestOwnFiles(t *testing.T) {
	base := newBase(t)
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"no descriptor":    {map[string]string{"usr/lib/vos/ext/demo/extension.json": "@missing"}, "usr/lib/vos/ext/demo/extension.json"},
		"other descriptor": {map[string]string{"usr/lib/vos/ext/demo/extension.json": strings.Replace(demoDescriptor, `"Demo"`, `"Demo 2"`, 1)}, "differs from the extension's descriptor"},
		"module options":   {map[string]string{"usr/lib/vos/ext/demo/module-options": "demo fast\namdgpu ppfeaturemask\n"}, "module-options: lists"},
		"no packages":      {map[string]string{"usr/lib/vos/ext/demo/packages.txt/": ""}, "packages.txt"},
		"no fetch":         {map[string]string{"usr/lib/vos/ext/demo/tool.bin": "@elsewhere"}, "fetch[0]"},
	} {
		t.Run(name, func(t *testing.T) {
			tree, d := newDemo(t)
			for p := range c.files {
				os.RemoveAll(filepath.Join(tree, filepath.FromSlash(strings.TrimSuffix(p, "/"))))
			}
			writeTree(t, tree, c.files)
			wantProblem(t, run(t, tree, base, d), c.want)
		})
	}
}

func TestIDMustMatch(t *testing.T) {
	tree, d := newDemo(t)
	r := CheckTree(Options{ID: "other", Descriptor: d, Tree: tree, Base: newBase(t)})
	wantProblem(t, r, `the descriptor's id is "demo", not "other"`)
}

func TestMissingDirectories(t *testing.T) {
	_, d := newDemo(t)
	r := CheckTree(Options{ID: "demo", Descriptor: d, Tree: filepath.Join(t.TempDir(), "nope"), Base: newBase(t)})
	wantProblem(t, r, "not a directory")
	tree := t.TempDir()
	r = CheckTree(Options{ID: "demo", Descriptor: d, Tree: tree, Base: newBase(t)})
	wantProblem(t, r, "usr: missing or not a directory")
}
