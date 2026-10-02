package buildcheck

import (
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The loader's own directories, searched on disk after ld.so.cache: an
// extension's library resolves from these. The cache comes from the base's
// ld.so.conf and lists only the base's libraries (ldconfig never runs on
// the box), so the ld.so.conf directories count for the base alone.
var (
	defaultLibDirs64 = []string{"/usr/lib", "/usr/lib/x86_64-linux-gnu"}
	defaultLibDirs32 = []string{"/usr/lib32"}
)

// libResolver finds an ELF's DT_NEEDED libraries the way the dynamic loader
// would on the booted system, inside the extension's tree and the base.
type libResolver struct {
	tree, base string
	confDirs   []string // from the base's ld.so.conf
	classes    map[string]elf.Class
}

func newLibResolver(tree, base string) *libResolver {
	return &libResolver{tree: tree, base: base, confDirs: ldConfDirs(base), classes: map[string]elf.Class{}}
}

// check returns the problems and warnings for the file at rel in the tree.
// Files that are not dynamically linked x86 programs or libraries are fine.
func (r *libResolver) check(rel string) (problems, warnings []string) {
	host := filepath.Join(r.tree, filepath.FromSlash(rel))
	f, err := os.Open(host)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", rel, err)}, nil
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || !bytes.Equal(magic[:], []byte(elf.ELFMAG)) {
		return nil, nil
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: unreadable ELF (%v); its libraries were not checked", rel, err)}
	}
	if (ef.Type != elf.ET_EXEC && ef.Type != elf.ET_DYN) || !hostMachine(ef) {
		return nil, nil
	}
	if ef.SectionByType(elf.SHT_DYNAMIC) == nil {
		for _, p := range ef.Progs {
			if p.Type == elf.PT_DYNAMIC {
				return nil, []string{fmt.Sprintf("%s: dynamic ELF without section headers; its libraries were not checked", rel)}
			}
		}
		return nil, nil
	}
	needed, err := ef.ImportedLibraries()
	if err != nil {
		return []string{fmt.Sprintf("%s: reading DT_NEEDED: %v", rel, err)}, nil
	}
	runpath, _ := ef.DynString(elf.DT_RUNPATH)
	if len(runpath) == 0 {
		runpath, _ = ef.DynString(elf.DT_RPATH)
	}
	dirs := expandRunpath(runpath, "/"+path.Dir(rel), ef.Class)
	for _, lib := range needed {
		if !r.find(lib, ef.Class, dirs) {
			problems = append(problems, fmt.Sprintf("%s: needs %s (not found)", rel, lib))
		}
	}
	return problems, nil
}

func hostMachine(f *elf.File) bool {
	return (f.Class == elf.ELFCLASS64 && f.Machine == elf.EM_X86_64) ||
		(f.Class == elf.ELFCLASS32 && f.Machine == elf.EM_386)
}

// find reports whether lib resolves: from the object's run path (in the
// tree or the base), the loader's default directories (tree, then base),
// then the base's ld.so.conf directories.
func (r *libResolver) find(lib string, class elf.Class, runpath []string) bool {
	if strings.Contains(lib, "/") {
		return r.has(r.tree, lib, class) || r.has(r.base, lib, class)
	}
	defaults := defaultLibDirs64
	if class == elf.ELFCLASS32 {
		defaults = defaultLibDirs32
	}
	for _, d := range runpath {
		if r.has(r.tree, d+"/"+lib, class) || r.has(r.base, d+"/"+lib, class) {
			return true
		}
	}
	for _, d := range defaults {
		if r.has(r.tree, d+"/"+lib, class) {
			return true
		}
	}
	for _, d := range append(slices.Clone(defaults), r.confDirs...) {
		if r.has(r.base, d+"/"+lib, class) {
			return true
		}
	}
	return false
}

// has reports whether name exists in root as a file, or a symlink that
// resolves (in either root, as on the merged /usr), that is not an ELF of
// the other class: the loader skips those.
func (r *libResolver) has(root, name string, class elf.Class) bool {
	host, err := resolveIn(root, name, false)
	if err != nil {
		return false
	}
	fi, err := os.Lstat(host)
	if err != nil || fi.IsDir() {
		return false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(host)
		if err != nil {
			return false
		}
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir("/"+strings.TrimPrefix(name, "/")), target)
		}
		host = ""
		for _, rt := range []string{root, r.tree, r.base} {
			if h, err := resolveIn(rt, target, true); err == nil {
				if fi, err := os.Stat(h); err == nil && fi.Mode().IsRegular() {
					host = h
					break
				}
			}
		}
		if host == "" {
			return false
		}
	} else if !fi.Mode().IsRegular() {
		return false
	}
	c, ok := r.classes[host]
	if !ok {
		c = elfClass(host)
		r.classes[host] = c
	}
	return c == elf.ELFCLASSNONE || c == class
}

// elfClass returns a file's ELF class, or ELFCLASSNONE when it is not ELF.
func elfClass(host string) elf.Class {
	f, err := os.Open(host)
	if err != nil {
		return elf.ELFCLASSNONE
	}
	defer f.Close()
	var id [5]byte
	if _, err := io.ReadFull(f, id[:]); err != nil || !bytes.Equal(id[:4], []byte(elf.ELFMAG)) {
		return elf.ELFCLASSNONE
	}
	return elf.Class(id[4])
}

// expandRunpath splits DT_RUNPATH/DT_RPATH entries and expands $ORIGIN and
// $LIB. Entries the loader would take relative to the working directory, or
// with tokens that depend on the CPU, are left out.
func expandRunpath(entries []string, origin string, class elf.Class) []string {
	lib := "lib"
	if class == elf.ELFCLASS32 {
		lib = "lib32"
	}
	var out []string
	for _, e := range entries {
		for _, d := range strings.Split(e, ":") {
			d = strings.NewReplacer("${ORIGIN}", origin, "$ORIGIN", origin, "${LIB}", lib, "$LIB", lib).Replace(d)
			if !strings.HasPrefix(d, "/") || strings.Contains(d, "$") {
				continue
			}
			out = append(out, path.Clean(d))
		}
	}
	return out
}

// ldConfDirs returns the directories the base's etc/ld.so.conf lists,
// following include lines.
func ldConfDirs(base string) []string {
	var dirs []string
	seen := map[string]bool{}
	var parse func(file string, depth int)
	parse = func(file string, depth int) {
		if depth > 8 || seen["conf:"+file] {
			return
		}
		seen["conf:"+file] = true
		b, err := readIn(base, file, 1<<20)
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(b), "\n") {
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			f := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' || r == '\r' })
			if len(f) == 0 {
				continue
			}
			switch f[0] {
			case "include":
				for _, pat := range f[1:] {
					if !strings.HasPrefix(pat, "/") {
						pat = path.Join(path.Dir(file), pat)
					}
					for _, inc := range globIn(base, pat) {
						parse(inc, depth+1)
					}
				}
			case "hwcap":
			default:
				for _, d := range f {
					if strings.HasPrefix(d, "/") && !seen[path.Clean(d)] {
						seen[path.Clean(d)] = true
						dirs = append(dirs, path.Clean(d))
					}
				}
			}
		}
	}
	parse("/etc/ld.so.conf", 0)
	return dirs
}

// globIn expands a pattern whose last element may hold wildcards, inside
// root, in sorted order.
func globIn(root, pattern string) []string {
	dir, pat := path.Split(path.Clean(pattern))
	if strings.ContainsAny(dir, "*?[") {
		return nil
	}
	ents, _, err := readDirIn(root, dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if ok, _ := path.Match(pat, e.Name()); ok {
			out = append(out, path.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}
