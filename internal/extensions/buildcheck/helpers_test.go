package buildcheck

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// chowned gives a test file (by host path) another owner. Every other file
// reads as root's, as the build's base and images are, whoever runs the
// tests.
var chowned = map[string]string{}

func init() {
	ownerOf = func(host string, _ fs.FileInfo) string {
		if o, ok := chowned[host]; ok {
			return o
		}
		return "0:0"
	}
}

// chown makes the file at host read as owned by o for the rest of the test.
func chown(t *testing.T, host, o string) {
	t.Helper()
	chowned[host] = o
	t.Cleanup(func() { delete(chowned, host) })
}

// writeTree creates files under root: a value starting with "@" makes a
// symlink to the rest, a key ending in "/" a directory.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		os.Remove(p)
		var err error
		if target, ok := strings.CutPrefix(content, "@"); ok {
			err = os.Symlink(target, p)
		} else {
			err = os.WriteFile(p, []byte(content), 0o755)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// elfSpec describes a minimal ELF file: just the headers, a .dynstr, a
// .dynamic (unless static) and the section name table, which is all
// debug/elf and the checks read.
type elfSpec struct {
	class   elf.Class
	machine elf.Machine
	typ     elf.Type
	needed  []string
	runpath string
	rpath   string
	static  bool
}

func lib64(needed ...string) string {
	return string(makeELF(elfSpec{class: elf.ELFCLASS64, machine: elf.EM_X86_64, typ: elf.ET_DYN, needed: needed}))
}

func lib32(needed ...string) string {
	return string(makeELF(elfSpec{class: elf.ELFCLASS32, machine: elf.EM_386, typ: elf.ET_DYN, needed: needed}))
}

func makeELF(s elfSpec) []byte {
	is64 := s.class == elf.ELFCLASS64
	dynstr := []byte{0}
	str := func(v string) uint64 {
		off := len(dynstr)
		dynstr = append(append(dynstr, v...), 0)
		return uint64(off)
	}
	type dyn struct {
		tag elf.DynTag
		val uint64
	}
	var dyns []dyn
	for _, n := range s.needed {
		dyns = append(dyns, dyn{elf.DT_NEEDED, str(n)})
	}
	if s.runpath != "" {
		dyns = append(dyns, dyn{elf.DT_RUNPATH, str(s.runpath)})
	}
	if s.rpath != "" {
		dyns = append(dyns, dyn{elf.DT_RPATH, str(s.rpath)})
	}
	dyns = append(dyns, dyn{elf.DT_NULL, 0})
	var dynamic bytes.Buffer
	for _, d := range dyns {
		if is64 {
			binary.Write(&dynamic, binary.LittleEndian, elf.Dyn64{Tag: int64(d.tag), Val: d.val})
		} else {
			binary.Write(&dynamic, binary.LittleEndian, elf.Dyn32{Tag: int32(d.tag), Val: uint32(d.val)})
		}
	}
	shstr := []byte("\x00.dynstr\x00.dynamic\x00.shstrtab\x00")
	const nameDynstr, nameDynamic, nameShstrtab = 1, 9, 18

	ehsize, shentsize, dynent := 52, 40, 8
	if is64 {
		ehsize, shentsize, dynent = 64, 64, 16
	}
	type section struct {
		name, typ, link, entsize uint32
		data                     []byte
	}
	secs := []section{{}}
	secs = append(secs, section{name: nameDynstr, typ: uint32(elf.SHT_STRTAB), data: dynstr})
	if !s.static {
		secs = append(secs, section{name: nameDynamic, typ: uint32(elf.SHT_DYNAMIC), link: 1, entsize: uint32(dynent), data: dynamic.Bytes()})
	}
	secs = append(secs, section{name: nameShstrtab, typ: uint32(elf.SHT_STRTAB), data: shstr})

	var body bytes.Buffer
	offs := make([]int, len(secs))
	for i, sec := range secs[1:] {
		for (ehsize+body.Len())%8 != 0 {
			body.WriteByte(0)
		}
		offs[i+1] = ehsize + body.Len()
		body.Write(sec.data)
	}
	for (ehsize+body.Len())%8 != 0 {
		body.WriteByte(0)
	}
	shoff := ehsize + body.Len()

	var out bytes.Buffer
	ident := [elf.EI_NIDENT]byte{0x7f, 'E', 'L', 'F', byte(s.class), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)}
	if is64 {
		binary.Write(&out, binary.LittleEndian, elf.Header64{
			Ident: ident, Type: uint16(s.typ), Machine: uint16(s.machine), Version: uint32(elf.EV_CURRENT),
			Shoff: uint64(shoff), Ehsize: uint16(ehsize), Shentsize: uint16(shentsize),
			Shnum: uint16(len(secs)), Shstrndx: uint16(len(secs) - 1),
		})
	} else {
		binary.Write(&out, binary.LittleEndian, elf.Header32{
			Ident: ident, Type: uint16(s.typ), Machine: uint16(s.machine), Version: uint32(elf.EV_CURRENT),
			Shoff: uint32(shoff), Ehsize: uint16(ehsize), Shentsize: uint16(shentsize),
			Shnum: uint16(len(secs)), Shstrndx: uint16(len(secs) - 1),
		})
	}
	out.Write(body.Bytes())
	for i, sec := range secs {
		if is64 {
			binary.Write(&out, binary.LittleEndian, elf.Section64{
				Name: sec.name, Type: sec.typ, Off: uint64(offs[i]), Size: uint64(len(sec.data)),
				Link: sec.link, Addralign: 1, Entsize: uint64(sec.entsize),
			})
		} else {
			binary.Write(&out, binary.LittleEndian, elf.Section32{
				Name: sec.name, Type: sec.typ, Off: uint32(offs[i]), Size: uint32(len(sec.data)),
				Link: sec.link, Addralign: 1, Entsize: sec.entsize,
			})
		}
	}
	return out.Bytes()
}

// The test base: a few units, sysctl settings, libraries and ld.so.conf.
func newBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	writeTree(t, base, map[string]string{
		"usr/bin/vos":    "vos",
		"usr/sbin":       "@bin",
		"usr/share/doc/": "",
		"usr/lib/systemd/system/multi-user.target":   "[Unit]\n",
		"usr/lib/systemd/system/shutdown.target":     "[Unit]\n",
		"usr/lib/systemd/system/vosd.service":        "[Service]\nExecStart=/usr/bin/vos daemon\n",
		"usr/lib/systemd/system/getty@.service":      "[Service]\n",
		"etc/systemd/system/local.service":           "[Service]\n",
		"usr/lib/systemd/user/vos-gamescope.service": "[Service]\n",
		"usr/lib/sysctl.d/99-vos.conf":               "kernel.sysrq = 0\nvm.max_map_count = 1048576\n",
		"etc/sysctl.conf":                            "fs.inotify.max_user_watches = 524288\n",
		"etc/sysctl.d/99-sysctl.conf":                "@../sysctl.conf",
		"etc/ld.so.conf":                             "# ld.so.conf\ninclude ld.so.conf.d/*.conf\n",
		"etc/ld.so.conf.d/extra.conf":                "/usr/lib/extra\n",
		"usr/lib/libc.so.6":                          lib64(),
		"usr/lib/libm.so.6":                          "@libm-2.40.so",
		"usr/lib/libm-2.40.so":                       lib64("libc.so.6"),
		"usr/lib/libonly64.so.1":                     lib64(),
		"usr/lib/extra/libextra.so.1":                lib64(),
		"usr/lib32/libc.so.6":                        lib32(),
		"usr/lib32/libwrong.so.1":                    lib64(),
	})
	return base
}

const demoDescriptor = `{
  "schema": 1,
  "id": "demo",
  "name": "Demo",
  "summary": "A test extension.",
  "category": "system",
  "upstream": {"name": "Demo", "url": "https://example.com/demo", "license": "MIT"},
  "packages": ["extra/demo"],
  "strip": ["usr/lib/demo/stripped"],
  "elf_exempt": ["usr/lib/demo/runtime"],
  "permissions": ["service"],
  "services": [{"unit": "demo.service", "scope": "system"}],
  "settings": [{"key": "fast", "type": "bool", "label": "Fast", "restart": true}],
  "module_options": [{"module": "demo", "param": "fast", "setting": "fast"}],
  "fetch": [{"url": "https://example.com/tool.bin", "sha256": "0000000000000000000000000000000000000000000000000000000000000001", "license": "MIT", "dest": "tool.bin"}]
}`

func parseDescriptor(t *testing.T, s string) *descriptor.Descriptor {
	t.Helper()
	d, err := descriptor.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// newDemo returns a tree for demoDescriptor that passes every check.
func newDemo(t *testing.T) (string, *descriptor.Descriptor) {
	t.Helper()
	tree := t.TempDir()
	writeTree(t, tree, map[string]string{
		"usr/bin/demo":                                   lib64("libc.so.6"),
		"usr/lib/demo/runtime/game":                      lib64("libsteamonly.so.1"),
		"usr/share/doc/demo/README":                      "demo",
		"usr/lib/systemd/system/demo.service":            "[Unit]\nDescription=Demo\nAfter=network.target\n[Service]\nExecStart=/usr/bin/demo\nUser=demo\n",
		"usr/lib/systemd/system/demo.service.d/vos.conf": "[Service]\nTimeoutStartSec=30s\n",
		"usr/lib/vos/ext/demo/extension.json":            demoDescriptor,
		"usr/lib/vos/ext/demo/packages.txt":              "demo 1.0-1\n",
		"usr/lib/vos/ext/demo/module-options":            "demo fast\n",
		"usr/lib/vos/ext/demo/tool.bin":                  "tool",
	})
	return tree, parseDescriptor(t, demoDescriptor)
}

// declare sets the demo's permissions, in its descriptor and in the copy
// the image carries.
func declare(t *testing.T, tree string, perms ...string) *descriptor.Descriptor {
	t.Helper()
	q := make([]string, len(perms))
	for i, p := range perms {
		q[i] = `"` + p + `"`
	}
	text := strings.Replace(demoDescriptor, `"permissions": ["service"]`, `"permissions": [`+strings.Join(q, ", ")+`]`, 1)
	writeTree(t, tree, map[string]string{"usr/lib/vos/ext/demo/extension.json": text})
	return parseDescriptor(t, text)
}

func run(t *testing.T, tree, base string, d *descriptor.Descriptor, others ...string) *Report {
	t.Helper()
	return CheckTree(Options{ID: d.ID, Descriptor: d, Tree: tree, Base: base, Others: others})
}

func wantProblem(t *testing.T, r *Report, substr string) {
	t.Helper()
	for _, p := range r.Problems {
		if strings.Contains(p, substr) {
			return
		}
	}
	t.Errorf("no problem containing %q in:\n%s", substr, strings.Join(r.Problems, "\n"))
}

// warningsOf returns a report's warnings without the one about this
// process's privileges, which depends on who runs the tests.
func warningsOf(r *Report) []string {
	var out []string
	for _, w := range r.Warnings {
		if !strings.Contains(w, "CAP_SYS_ADMIN") {
			out = append(out, w)
		}
	}
	return out
}

func wantClean(t *testing.T, r *Report) {
	t.Helper()
	if len(r.Problems) > 0 {
		t.Errorf("unexpected problems:\n%s", strings.Join(r.Problems, "\n"))
	}
}
