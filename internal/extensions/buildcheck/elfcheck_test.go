package buildcheck

import (
	"bytes"
	"debug/elf"
	"reflect"
	"strings"
	"testing"
)

func TestMakeELFReadsBack(t *testing.T) {
	for _, s := range []elfSpec{
		{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_EXEC, needed: []string{"libc.so.6", "libz.so.1"}, runpath: "$ORIGIN/../lib"},
		{class: elf.ELFCLASS32, machine: elf.EM_386, typ: elf.ET_DYN, needed: []string{"libc.so.6"}, rpath: "/opt/x"},
	} {
		f, err := elf.NewFile(bytes.NewReader(makeELF(s)))
		if err != nil {
			t.Fatal(err)
		}
		libs, err := f.ImportedLibraries()
		if err != nil || !reflect.DeepEqual(libs, s.needed) || f.Class != s.class {
			t.Fatalf("%v %v %v", libs, err, f.Class)
		}
		rp, _ := f.DynString(elf.DT_RUNPATH)
		r, _ := f.DynString(elf.DT_RPATH)
		if strings.Join(rp, "") != s.runpath || strings.Join(r, "") != s.rpath {
			t.Fatalf("runpath %q rpath %q", rp, r)
		}
	}
}

func TestELFResolution(t *testing.T) {
	base := newBase(t)
	exe := func(needed ...string) string {
		return string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_EXEC, needed: needed}))
	}
	for name, c := range map[string]struct {
		files   map[string]string
		missing []string // "path: lib"
	}{
		"base libraries": {files: map[string]string{
			"usr/bin/x": exe("libc.so.6", "libm.so.6", "libextra.so.1"),
		}},
		"missing": {files: map[string]string{
			"usr/bin/x": exe("libc.so.6", "libnope.so.1"),
		}, missing: []string{"usr/bin/x: libnope.so.1"}},
		"own library": {files: map[string]string{
			"usr/bin/x":              exe("libdemo.so.1"),
			"usr/lib/libdemo.so.1":   "@libdemo.so.1.2",
			"usr/lib/libdemo.so.1.2": lib64("libc.so.6"),
		}},
		"own library's own need": {files: map[string]string{
			"usr/lib/libdemo.so.1": lib64("libgone.so.3"),
		}, missing: []string{"usr/lib/libdemo.so.1: libgone.so.3"}},
		"own library in an ld.so.conf directory": {files: map[string]string{
			"usr/bin/x":                      exe("libtreeonly.so.1"),
			"usr/lib/extra/libtreeonly.so.1": lib64(),
		}, missing: []string{"usr/bin/x: libtreeonly.so.1"}},
		"runpath with $ORIGIN": {files: map[string]string{
			"usr/lib/demo/bin/x":            string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_EXEC, needed: []string{"libpriv.so.1"}, runpath: "$ORIGIN/../lib:/nowhere"})),
			"usr/lib/demo/lib/libpriv.so.1": lib64(),
		}},
		"rpath with ${ORIGIN}": {files: map[string]string{
			"usr/lib/demo/bin/x":            string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_EXEC, needed: []string{"libpriv.so.1"}, rpath: "${ORIGIN}/../lib"})),
			"usr/lib/demo/lib/libpriv.so.1": lib64(),
		}},
		"runpath wins over rpath": {files: map[string]string{
			"usr/lib/demo/bin/x":            string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_EXEC, needed: []string{"libpriv.so.1"}, runpath: "/usr/lib/demo/none", rpath: "$ORIGIN/../lib"})),
			"usr/lib/demo/lib/libpriv.so.1": lib64(),
		}, missing: []string{"usr/lib/demo/bin/x: libpriv.so.1"}},
		"runpath into the base": {files: map[string]string{
			"usr/bin/x": string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_EXEC, needed: []string{"libextra.so.1"}, runpath: "/usr/lib/extra"})),
		}},
		"32-bit": {files: map[string]string{
			"usr/bin/x32": lib32("libc.so.6", "libonly64.so.1", "libwrong.so.1"),
		}, missing: []string{"usr/bin/x32: libonly64.so.1", "usr/bin/x32: libwrong.so.1"}},
		"symlink into the base": {files: map[string]string{
			"usr/bin/x":             exe("libalias.so.1"),
			"usr/lib/libalias.so.1": "@/usr/lib/libc.so.6",
		}},
		"dangling symlink": {files: map[string]string{
			"usr/bin/x":            exe("libdead.so.1"),
			"usr/lib/libdead.so.1": "@libdead.so.1.0",
		}, missing: []string{"usr/bin/x: libdead.so.1"}},
		"static": {files: map[string]string{
			"usr/bin/x": string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_EXEC, needed: []string{"libnope.so.1"}, static: true})),
		}},
		"object file": {files: map[string]string{
			"usr/lib/demo/x.o": string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_REL, needed: []string{"libnope.so.1"}})),
		}},
		"other machine": {files: map[string]string{
			"usr/lib/demo/arm": string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_AARCH64, typ: elf.ET_EXEC, needed: []string{"libnope.so.1"}})),
		}},
		"exempt": {files: map[string]string{
			"usr/lib/demo/runtime/deep/x": exe("libnope.so.1"),
		}},
		"absolute need": {files: map[string]string{
			"usr/bin/x": exe("/usr/lib/libc.so.6", "/usr/lib/libnope.so.1"),
		}, missing: []string{"usr/bin/x: /usr/lib/libnope.so.1"}},
	} {
		t.Run(name, func(t *testing.T) {
			tree, d := newDemo(t)
			writeTree(t, tree, c.files)
			r := run(t, tree, base, d)
			var got []string
			for _, p := range r.Problems {
				if path, lib, ok := strings.Cut(p, ": needs "); ok {
					got = append(got, path+": "+strings.TrimSuffix(lib, " (not found)"))
				} else {
					t.Errorf("unexpected problem %q", p)
				}
			}
			if !reflect.DeepEqual(got, c.missing) {
				t.Fatalf("missing %q, want %q", got, c.missing)
			}
		})
	}
}

func TestUnreadableELFWarns(t *testing.T) {
	tree, d := newDemo(t)
	writeTree(t, tree, map[string]string{"usr/lib/demo/broken": "\x7fELF\x02\x01\x01" + strings.Repeat("\xff", 60)})
	r := run(t, tree, newBase(t), d)
	wantClean(t, r)
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "usr/lib/demo/broken: unreadable ELF") {
		t.Fatalf("warnings %q", r.Warnings)
	}
}

func TestLdConfDirs(t *testing.T) {
	base := t.TempDir()
	writeTree(t, base, map[string]string{
		"etc/ld.so.conf":             "include /etc/ld.so.conf.d/*.conf # all of them\n/usr/lib/first\nhwcap 0 nosegneg\n",
		"etc/ld.so.conf.d/a.conf":    "/usr/lib/a, /usr/lib/b\n# /usr/lib/commented\n",
		"etc/ld.so.conf.d/b.conf":    "include loop.conf\n/usr/lib/a\n",
		"etc/ld.so.conf.d/loop.conf": "include b.conf\n/usr/lib/loop\n",
		"etc/ld.so.conf.d/c.txt":     "/usr/lib/ignored\n",
	})
	got := ldConfDirs(base)
	want := []string{"/usr/lib/a", "/usr/lib/b", "/usr/lib/loop", "/usr/lib/first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
	if dirs := ldConfDirs(t.TempDir()); len(dirs) != 0 {
		t.Fatalf("no ld.so.conf: %q", dirs)
	}
}

func TestExpandRunpath(t *testing.T) {
	got := expandRunpath([]string{"$ORIGIN/../lib:${ORIGIN}:/usr/$LIB/x::rel:/x/$PLATFORM"}, "/usr/lib/demo/bin", elf.ELFCLASS32)
	want := []string{"/usr/lib/demo/lib", "/usr/lib/demo/bin", "/usr/lib32/x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}
