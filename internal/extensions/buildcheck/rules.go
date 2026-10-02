package buildcheck

import (
	"fmt"
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
	"usr/share/polkit-1/rules.d":       descriptor.PermPolkit,
	"usr/share/polkit-1/actions":       descriptor.PermPolkit,
	"usr/share/dbus-1/system.d":        descriptor.PermDBus,
	"usr/share/dbus-1/system-services": descriptor.PermDBus,
}

// reviewedDirs are *.d directories that something reads as a hook
// directory but that were reviewed as harmless on VaporOS. Add one only
// with the reason it is safe.
var reviewedDirs = map[string]string{}

// noTmpfiles is why an image ships no tmpfiles.d: a line can reach any path
// on the box through symlinks, globs and copies, while a service gets its own
// directories from systemd and vosd makes the data areas.
const noTmpfiles = "tmpfiles.d is not allowed in extensions; use StateDirectory= or RuntimeDirectory="

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
	{"usr/lib/tmpfiles.d", noTmpfiles},
	{"usr/share/user-tmpfiles.d", noTmpfiles},
	{"usr/lib/kernel", "kernel install hooks belong to the base"},
	{"usr/lib/initcpio", "initramfs hooks belong to the base"},
	{"usr/share/libalpm", "pacman hooks belong to the base"},
	{"usr/lib/NetworkManager", "network configuration belongs to the base"},
	{"usr/lib/credstore", "systemd imports credentials from it (sysctl, tmpfiles, sysusers and SSH keys among them), which belong to the base"},
	{"usr/lib/credstore.encrypted", "systemd imports credentials from it (sysctl, tmpfiles, sysusers and SSH keys among them), which belong to the base"},
	{"usr/lib/firmware", "firmware (updates/ included) belongs to the base: the kernel loads it for every device"},
	{"usr/share/p11-kit", "p11-kit's modules and trust belong to the base"},
	{"usr/lib/pkcs11", "PKCS#11 modules belong to the base"},
	{"usr/share/ca-certificates", "certificate trust belongs to the base"},
	{"usr/lib/gio/modules", "GIO modules load into every GLib program, and the base's giomodule.cache would not list them"},
	{"usr/lib/gdk-pixbuf-2.0", "pixbuf loaders load only through the base's loaders.cache, which would not list them"},
	{"usr/share/glib-2.0/schemas", "GSettings reads only the base's gschemas.compiled, which would not include them"},
}

// warnAreas are places an image may ship to, but where what it ships does
// not work as its package expects: a warning, not a failure.
var warnAreas = []struct{ dir, why string }{
	{"usr/share/mime/packages", "the base's MIME database (update-mime-database) does not include these types"},
	{"usr/share/icons", "the base's icon theme caches do not list these icons, so some programs will not find them"},
}

// unitSuffixes are the systemd unit types.
var unitSuffixes = []string{".service", ".socket", ".device", ".mount", ".automount", ".swap", ".target", ".path", ".timer", ".slice", ".scope"}

// depDirSuffixes are the directories that add dependencies to a unit.
var depDirSuffixes = []string{".wants", ".requires", ".upholds"}

// runtimeUnitTypes are the unit types whose names systemd and generators
// make at runtime, from mount points, fstab, devices, cgroups and logins
// (efi.mount, var-lib-vos.mount, user-1000.slice): whatever name an
// extension picked could be one of the base's on some box.
var runtimeUnitTypes = map[string]bool{"mount": true, "automount": true, "swap": true, "slice": true, "scope": true, "device": true}

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
	switch u := namedUnit(p[0], isDir || len(p) > 1); {
	case u != "" && prefixStem(u):
		return verdict{forbid: "a unit name that ends in '-' before '@' or its type makes systemd apply its drop-ins to every unit with that prefix"}
	case u != "" && runtimeUnitTypes[unitType(u)]:
		return verdict{forbid: fmt.Sprintf("systemd and generators name .%s units at runtime (efi.mount, var-lib-vos.mount, user-1000.slice), so an extension ships none, nor drop-ins or dependencies for one", unitType(u))}
	}
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

// namedUnit returns the unit an entry of a unit directory names: a unit
// file or alias, or the unit of a drop-in or dependency directory.
func namedUnit(name string, isDir bool) string {
	if !isDir {
		if isUnitName(name) {
			return name
		}
		return ""
	}
	if u, ok := dropInUnit(name); ok {
		return u
	}
	u, _ := depDirUnit(name)
	return u
}

// prefixStem reports whether a unit name's stem (before '@' or its type)
// ends in '-': systemd reads foo-.service.d/ for every foo-*.service.
func prefixStem(unit string) bool {
	stem := unit[:strings.LastIndexByte(unit, '.')]
	if at := strings.IndexByte(stem, '@'); at >= 0 {
		stem = stem[:at]
	}
	return strings.HasSuffix(stem, "-")
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

// unitType returns a unit name's type ("service", "socket", ...).
func unitType(name string) string {
	return name[strings.LastIndexByte(name, '.')+1:]
}

// instanceOf names template's instance with the instance string of name
// (foo@.service and bar@x.service give foo@x.service).
func instanceOf(template, name string) (string, bool) {
	if !isTemplate(template) {
		return "", false
	}
	at, dot := strings.IndexByte(name, '@'), strings.LastIndexByte(name, '.')
	if at < 0 || dot <= at+1 || unitType(template) != unitType(name) {
		return "", false
	}
	return strings.Replace(template, "@.", "@"+name[at+1:dot]+".", 1), true
}

// isTemplate reports whether a unit name is a template (foo@.service).
func isTemplate(name string) bool {
	at := strings.IndexByte(name, '@')
	return at > 0 && at+1 == strings.LastIndexByte(name, '.')
}

func isKernelModule(name string) bool {
	for _, s := range []string{".ko", ".ko.zst", ".ko.xz", ".ko.gz"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}
