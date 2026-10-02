package buildcheck

import (
	"io/fs"
	"path"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// verdict is what the allowlist says about one path of an image.
type verdict struct {
	perm   string // the permission category shipping it needs
	forbid string // why it may not be shipped at all
}

// hookDirs are the directories whose files the base's boot reads, with the
// permission each one takes. Files go directly inside them.
var hookDirs = map[string]string{
	"usr/lib/udev/rules.d":             descriptor.PermUdev,
	"usr/lib/sysctl.d":                 descriptor.PermSysctl,
	"usr/lib/modules-load.d":           descriptor.PermModules,
	"usr/lib/tmpfiles.d":               descriptor.PermTmpfiles,
	"usr/share/polkit-1/rules.d":       descriptor.PermPolkit,
	"usr/share/polkit-1/actions":       descriptor.PermPolkit,
	"usr/share/dbus-1/system.d":        descriptor.PermDBus,
	"usr/share/dbus-1/system-services": descriptor.PermDBus,
}

// reviewedDirs are *.d directories that something reads as a hook
// directory but that were reviewed as harmless on VaporOS. Add one only
// with the reason it is safe.
var reviewedDirs = map[string]string{}

// forbiddenDirs are places only the base may write to, and why.
var forbiddenDirs = []struct{ dir, why string }{
	{"usr/local", "usr/local is not part of any image"},
	{"usr/share/vos", "usr/share/vos belongs to the base"},
	{"usr/lib/security", "PAM modules belong to the base"},
	{"usr/share/vulkan", "Vulkan drivers and layers belong to the base"},
	{"usr/lib/binfmt.d", "binfmt handlers belong to the base"},
	{"usr/lib/environment.d", "the session environment belongs to the base"},
	{"usr/lib/modprobe.d", "module options come only from module_options"},
	{"usr/lib/sysusers.d", "users and groups belong to the base (use DynamicUser= or add the user there)"},
	{"usr/lib/kernel", "kernel install hooks belong to the base"},
	{"usr/lib/initcpio", "initramfs hooks belong to the base"},
	{"usr/share/libalpm", "pacman hooks belong to the base"},
	{"usr/lib/NetworkManager", "network configuration belongs to the base"},
}

// unitSuffixes are the systemd unit types.
var unitSuffixes = []string{".service", ".socket", ".device", ".mount", ".automount", ".swap", ".target", ".path", ".timer", ".slice", ".scope"}

// depDirSuffixes are the directories that add dependencies to a unit.
var depDirSuffixes = []string{".wants", ".requires", ".upholds"}

// classify applies the allowlist to rel, a slash path inside the image of
// extension id, whose type is mode.
func classify(id, rel string, mode fs.FileMode) verdict {
	name := path.Base(rel)
	isDir := mode.IsDir()
	switch {
	case !under(rel, "usr"):
		return verdict{forbid: "outside usr/"}
	case rel == "usr":
		return verdict{}
	case name == "ld.so.conf.d":
		return verdict{forbid: "library search paths belong to the base"}
	case !isDir && isKernelModule(name):
		return verdict{forbid: "kernel modules are not allowed in extensions"}
	case under(rel, "usr/lib/vos"):
		own := "usr/lib/vos/ext/" + id
		if under(rel, own) || (isDir && (rel == "usr/lib/vos" || rel == "usr/lib/vos/ext")) {
			return verdict{}
		}
		return verdict{forbid: "usr/lib/vos belongs to the base; the extension's own files go in " + own + "/"}
	case under(rel, "usr/lib/systemd"):
		switch {
		case rel == "usr/lib/systemd" && isDir:
			return verdict{}
		case under(rel, "usr/lib/systemd/system"):
			return unitVerdict(rel, "usr/lib/systemd/system", isDir, descriptor.PermService)
		case under(rel, "usr/lib/systemd/user"):
			return unitVerdict(rel, "usr/lib/systemd/user", isDir, descriptor.PermUserService)
		}
		return verdict{forbid: "only units and their drop-ins may go under usr/lib/systemd (generators, hooks, presets, network and manager configuration belong to the base)"}
	case under(rel, "usr/lib/udev"):
		switch {
		case rel == "usr/lib/udev" && isDir:
			return verdict{}
		case under(rel, "usr/lib/udev/rules.d"):
			return hookVerdict(rel, "usr/lib/udev/rules.d", isDir)
		case under(rel, "usr/lib/udev/hwdb.d"):
			return verdict{forbid: "the hardware database (hwdb.d) belongs to the base"}
		}
		return verdict{forbid: "only udev rules (rules.d) may go under usr/lib/udev"}
	case under(rel, "usr/share/polkit-1"):
		switch {
		case rel == "usr/share/polkit-1" && isDir:
			return verdict{}
		case under(rel, "usr/share/polkit-1/rules.d"):
			return hookVerdict(rel, "usr/share/polkit-1/rules.d", isDir)
		case under(rel, "usr/share/polkit-1/actions"):
			return hookVerdict(rel, "usr/share/polkit-1/actions", isDir)
		}
		return verdict{forbid: "only polkit rules.d and actions may go under usr/share/polkit-1"}
	case under(rel, "usr/share/dbus-1"):
		switch {
		case rel == "usr/share/dbus-1" && isDir:
			return verdict{}
		case under(rel, "usr/share/dbus-1/system.d"):
			return hookVerdict(rel, "usr/share/dbus-1/system.d", isDir)
		case under(rel, "usr/share/dbus-1/system-services"):
			return hookVerdict(rel, "usr/share/dbus-1/system-services", isDir)
		}
		return verdict{forbid: "only the system bus's policy (system.d) and services (system-services) may go under usr/share/dbus-1"}
	case under(rel, "usr/share/steam/compatibilitytools.d"):
		if isDir {
			return verdict{}
		}
		return verdict{perm: descriptor.PermCompatTool}
	}
	for dir := range hookDirs {
		if under(rel, dir) {
			return hookVerdict(rel, dir, isDir)
		}
	}
	for _, f := range forbiddenDirs {
		if under(rel, f.dir) {
			return verdict{forbid: f.why}
		}
	}
	if strings.HasSuffix(name, ".d") && hookDepth(rel) && (isDir || mode&fs.ModeSymlink != 0) {
		if _, ok := reviewedDirs[rel]; ok {
			return verdict{}
		}
		return verdict{forbid: "an unknown hook directory; add it to reviewedDirs in internal/extensions/buildcheck once it is known to be safe"}
	}
	return verdict{}
}

// hookDepth reports whether rel sits directly in usr/lib, usr/share or one
// of their subdirectories, where hook directories live.
func hookDepth(rel string) bool {
	p := strings.Split(rel, "/")
	return (len(p) == 3 || len(p) == 4) && (p[1] == "lib" || p[1] == "share")
}

// hookVerdict is the verdict for a path in one of hookDirs: files directly
// inside take the directory's permission, nothing nests.
func hookVerdict(rel, dir string, isDir bool) verdict {
	switch {
	case rel == dir && isDir:
		return verdict{}
	case rel == dir:
		return verdict{forbid: "not a directory"}
	case path.Dir(rel) != dir || isDir:
		return verdict{forbid: "nothing nests inside " + dir}
	}
	return verdict{perm: hookDirs[dir]}
}

// unitVerdict is the verdict for a path in a unit directory: unit files,
// <unit>.d/*.conf drop-ins and <unit>.wants-style directories. Whose unit a
// drop-in belongs to is checked once the whole tree is known.
func unitVerdict(rel, dir string, isDir bool, perm string) verdict {
	if rel == dir {
		if isDir {
			return verdict{}
		}
		return verdict{forbid: "not a directory"}
	}
	p := strings.Split(strings.TrimPrefix(rel, dir+"/"), "/")
	switch len(p) {
	case 1:
		if isDir {
			if _, ok := dropInUnit(p[0]); ok {
				return verdict{}
			}
			if _, ok := depDirUnit(p[0]); ok {
				return verdict{}
			}
			return verdict{forbid: "not a unit's drop-in or dependency directory"}
		}
		if isUnitName(p[0]) {
			return verdict{perm: perm}
		}
		return verdict{forbid: "not a unit file"}
	case 2:
		if _, ok := dropInUnit(p[0]); ok && !isDir {
			if !strings.HasSuffix(p[1], ".conf") {
				return verdict{forbid: "drop-ins end in .conf"}
			}
			return verdict{perm: perm}
		}
		if _, ok := depDirUnit(p[0]); ok && !isDir {
			return verdict{perm: perm}
		}
	}
	return verdict{forbid: "not a unit, drop-in or dependency link"}
}

// dropInUnit returns the unit a drop-in directory (<unit>.d) is for.
func dropInUnit(name string) (string, bool) {
	u, ok := strings.CutSuffix(name, ".d")
	return u, ok && isUnitName(u)
}

// depDirUnit returns the unit a .wants, .requires or .upholds directory
// belongs to.
func depDirUnit(name string) (string, bool) {
	for _, s := range depDirSuffixes {
		if u, ok := strings.CutSuffix(name, s); ok && isUnitName(u) {
			return u, true
		}
	}
	return "", false
}

func isUnitName(name string) bool {
	for _, s := range unitSuffixes {
		if p, ok := strings.CutSuffix(name, s); ok && p != "" {
			return !strings.ContainsAny(p, "/ \t")
		}
	}
	return false
}

// unitTemplate returns the template of an instance name (foo@bar.service
// is an instance of foo@.service).
func unitTemplate(name string) (string, bool) {
	at := strings.IndexByte(name, '@')
	dot := strings.LastIndexByte(name, '.')
	if at < 0 || dot < at || at+1 == dot {
		return "", false
	}
	return name[:at+1] + name[dot:], true
}

func isKernelModule(name string) bool {
	for _, s := range []string{".ko", ".ko.zst", ".ko.xz", ".ko.gz"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}
