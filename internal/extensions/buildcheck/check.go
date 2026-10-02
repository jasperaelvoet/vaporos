// Package buildcheck is the build's side of extensions (docs/CONTRACTS.md
// "Extensions", Image and Catalog): `vos ext check-tree` holds an
// extension's image tree against an allowlist, its descriptor and the base
// it was built on, and `vos ext catalog` writes the catalog, the manifest
// entries and the descriptors the image ships. The build fails on any
// problem, so what an extension can change on the box is what this package
// lets through.
package buildcheck

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// Options name what check-tree looks at.
type Options struct {
	ID         string
	Descriptor *descriptor.Descriptor // the source descriptor, validated
	Tree       string                 // the image root, holding usr/
	Base       string                 // the assembled OS root
	Others     []string               // trees of the extensions built before it
}

// Result is what the build records for an image that passed: check-tree's
// --json output, which `vos ext catalog` reads back as <id>.build.json.
type Result struct {
	Permissions []string `json:"permissions"` // in descriptor.Permissions order
	RunsAsRoot  bool     `json:"runs_as_root"`
	Warnings    []string `json:"warnings"`
}

// Report is a check's outcome: any problem fails the build.
type Report struct {
	Result
	Problems []string
}

var uaccessRe = regexp.MustCompile(`TAG\s*\+=\s*"uaccess"`)

type checker struct {
	o        Options
	problems []string
	warnings []string
	found    map[string]string // permission -> the first path that needs it
	system   *unitScope
	user     *unitScope
	elfs     []string
	root     bool              // a system service runs as root
	roots    []string          // the base, then the other trees
	dirIn    []map[string]bool // per root: directories of the tree it has as directories
}

// CheckTree runs every rule of the contract's Image paragraph.
func CheckTree(o Options) *Report {
	c := &checker{
		o:      o,
		found:  map[string]string{},
		system: newUnitScope("system", true),
		user:   newUnitScope("user", false),
		roots:  append([]string{o.Base}, o.Others...),
	}
	for range c.roots {
		c.dirIn = append(c.dirIn, map[string]bool{".": true})
	}
	if o.Descriptor == nil {
		c.bad("no descriptor")
		return c.report()
	}
	if o.Descriptor.ID != o.ID {
		c.bad("the descriptor's id is %q, not %q", o.Descriptor.ID, o.ID)
	}
	for _, d := range append([]string{o.Tree}, c.roots...) {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			c.bad("%s: not a directory", d)
			return c.report()
		}
	}
	c.walk()
	c.checkPermissions()
	c.checkUnits()
	c.checkSysctl()
	c.checkELF()
	c.checkStrip()
	c.checkOwnFiles()
	return c.report()
}

func (c *checker) bad(format string, a ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, a...))
}

func (c *checker) report() *Report {
	r := &Report{Problems: c.problems, Result: Result{Permissions: []string{}, RunsAsRoot: c.root, Warnings: c.warnings}}
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	for _, p := range descriptor.Permissions {
		if _, ok := c.found[p]; ok {
			r.Permissions = append(r.Permissions, p)
		}
	}
	return r
}

func (c *checker) walk() {
	if fi, err := os.Lstat(filepath.Join(c.o.Tree, "usr")); err != nil || !fi.IsDir() {
		c.bad("usr: missing or not a directory")
	}
	err := filepath.WalkDir(c.o.Tree, func(host string, d fs.DirEntry, err error) error {
		rel, rerr := filepath.Rel(c.o.Tree, host)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if err != nil {
			c.bad("%s: %v", rel, err)
			return nil
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			c.bad("%s: %v", rel, err)
			return nil
		}
		mode := info.Mode()
		if !under(rel, "usr") {
			c.bad("%s: outside usr/", rel)
			return skip(mode)
		}
		if p := modeProblem(mode); p != "" {
			c.bad("%s: %s", rel, p)
		}
		xs, err := forbiddenXattrs(host)
		if err != nil {
			c.bad("%s: listing extended attributes: %v", rel, err)
		}
		for _, x := range xs {
			c.bad("%s: carries the %s extended attribute", rel, x)
		}
		v := classify(c.o.ID, rel, mode)
		if v.forbid != "" {
			if !mode.IsDir() || holdsFiles(host) {
				c.bad("%s: %s", rel, v.forbid)
			}
			return skip(mode)
		}
		if v.perm != "" {
			if _, ok := c.found[v.perm]; !ok {
				c.found[v.perm] = rel
			}
		}
		c.collide(rel, host, info)
		c.note(rel, host, info)
		return nil
	})
	if err != nil {
		c.bad("walking the tree: %v", err)
	}
}

func skip(mode fs.FileMode) error {
	if mode.IsDir() {
		return fs.SkipDir
	}
	return nil
}

// modeProblem says why a file of this type or mode cannot be in an image.
func modeProblem(m fs.FileMode) string {
	switch {
	case m&fs.ModeCharDevice != 0:
		return "is a character device (or an overlayfs whiteout)"
	case m&fs.ModeDevice != 0:
		return "is a block device"
	case m&(fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0:
		return "is not a regular file, directory or symlink"
	case m.IsDir():
		return ""
	case m&fs.ModeSetuid != 0:
		return "is setuid"
	case m&fs.ModeSetgid != 0:
		return "is setgid"
	}
	return ""
}

// forbiddenXattr reports whether an image may not carry an extended
// attribute: file capabilities grant privileges, and overlayfs's own
// attributes would change how the merged /usr reads.
func forbiddenXattr(name string) bool {
	return name == "security.capability" || strings.HasPrefix(name, "trusted.overlay.")
}

// holdsFiles reports whether a directory has anything but directories in
// it. An empty directory in a forbidden place changes nothing on the box.
func holdsFiles(host string) bool {
	found := errors.New("found")
	err := filepath.WalkDir(host, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return found
		}
		return nil
	})
	return err != nil
}

// collide checks rel against the base and the other trees. Directories
// merge; anything else replaces what is below it on the merged /usr, which
// is only allowed for a file identical to another extension's (both
// carrying the same package's file).
func (c *checker) collide(rel, host string, info fs.FileInfo) {
	parent := path.Dir(rel)
	for i, root := range c.roots {
		if !c.dirIn[i][parent] {
			continue // root has no such directory, so nothing to collide with
		}
		other := filepath.Join(root, filepath.FromSlash(rel))
		ofi, err := os.Lstat(other)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			c.bad("%s: %v", rel, err)
			continue
		}
		switch {
		case info.IsDir() && ofi.IsDir():
			c.dirIn[i][rel] = true
		case i == 0 && info.IsDir():
			c.bad("%s: a directory where the base has a %s", rel, kind(ofi.Mode()))
		case i == 0:
			c.bad("%s: the base already ships it", rel)
		case !identical(host, other, info, ofi):
			c.bad("%s: the extension in %s ships it too, with other content", rel, root)
		}
	}
}

func kind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m.IsRegular():
		return "file"
	case m.IsDir():
		return "directory"
	}
	return "special file"
}

// identical reports whether two regular files have the same mode and bytes,
// or two symlinks the same target.
func identical(a, b string, ai, bi fs.FileInfo) bool {
	switch {
	case ai.Mode()&fs.ModeSymlink != 0 && bi.Mode()&fs.ModeSymlink != 0:
		ta, err1 := os.Readlink(a)
		tb, err2 := os.Readlink(b)
		return err1 == nil && err2 == nil && ta == tb
	case ai.Mode().IsRegular() && bi.Mode().IsRegular():
		if ai.Size() != bi.Size() || ai.Mode() != bi.Mode() {
			return false
		}
		return sameBytes(a, b)
	}
	return false
}

func sameBytes(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil || eb != nil {
			return (ea == io.EOF || ea == io.ErrUnexpectedEOF) && (eb == io.EOF || eb == io.ErrUnexpectedEOF)
		}
	}
}

// note records what the checks after the walk need.
func (c *checker) note(rel, host string, info fs.FileInfo) {
	mode := info.Mode()
	isLink := mode&fs.ModeSymlink != 0
	switch {
	case strings.HasPrefix(rel, c.system.dir+"/"):
		c.system.note(rel, mode.IsDir(), isLink)
	case strings.HasPrefix(rel, c.user.dir+"/"):
		c.user.note(rel, mode.IsDir(), isLink)
	case strings.HasPrefix(rel, "usr/lib/udev/rules.d/") && mode.IsRegular():
		b, err := readLimited(host, 4<<20)
		if err != nil {
			c.bad("%s: %v", rel, err)
			return
		}
		for _, line := range strings.Split(string(b), "\n") {
			if l := strings.TrimSpace(line); !strings.HasPrefix(l, "#") && uaccessRe.MatchString(l) {
				c.warnings = append(c.warnings, rel+`: TAG+="uaccess" has no effect on VaporOS, which has no seat session`)
				break
			}
		}
	}
	if mode.IsRegular() && info.Size() >= 52 && !c.elfExempt(rel) {
		c.elfs = append(c.elfs, rel)
	}
}

func (c *checker) elfExempt(rel string) bool {
	for _, ex := range c.o.Descriptor.ELFExempt {
		if under(rel, ex) {
			return true
		}
	}
	return false
}

// checkPermissions compares what the image needs with what the descriptor
// declares; a difference either way fails, so a package that starts
// shipping something new is a reviewed change.
func (c *checker) checkPermissions() {
	declared := c.o.Descriptor.Permissions
	for _, p := range descriptor.Permissions {
		at, needed := c.found[p]
		switch {
		case needed && !slices.Contains(declared, p):
			c.bad("%s: needs the %q permission, which the descriptor does not declare", at, p)
		case !needed && slices.Contains(declared, p):
			c.bad("the descriptor declares the %q permission, but nothing in the image needs it", p)
		}
	}
}

func (c *checker) checkUnits() {
	sys := c.system.check(c.o.Tree, c.o.Base)
	usr := c.user.check(c.o.Tree, c.o.Base)
	c.problems = append(c.problems, sys.problems...)
	c.problems = append(c.problems, usr.problems...)
	c.root = sys.runsAsRoot
	for i, s := range c.o.Descriptor.Services {
		scope := c.system
		if s.Scope == "user" {
			scope = c.user
		}
		if !scope.ships(s.Unit) {
			c.bad("services[%d]: %s is not a unit in %s", i, s.Unit, scope.dir)
		}
	}
}

func (c *checker) checkSysctl() {
	mine, err := readSysctl(c.o.Tree, "usr/lib/sysctl.d")
	if err != nil {
		c.bad("reading the image's sysctl.d: %v", err)
		return
	}
	if len(mine) == 0 {
		return
	}
	base, err := readSysctl(c.o.Base, "usr/lib/sysctl.d", "etc/sysctl.d")
	if err != nil {
		c.bad("reading the base's sysctl.d: %v", err)
	}
	type source struct {
		settings []sysctlSetting
		who      string
	}
	sources := []source{{base, "the base"}}
	for _, o := range c.o.Others {
		s, err := readSysctl(o, "usr/lib/sysctl.d")
		if err != nil {
			c.bad("reading %s's sysctl.d: %v", o, err)
		}
		sources = append(sources, source{s, "the extension in " + o})
	}
	for _, m := range mine {
		if isNetSysctl(m.key) {
			c.bad("%s: sets %s, a network setting", m.file, m.key)
			continue
		}
		for _, src := range sources {
			for _, s := range src.settings {
				if sysctlOverlap(m.key, s.key) {
					c.bad("%s: sets %s, which %s sets too (%s)", m.file, m.key, src.who, s.file)
				}
			}
		}
	}
}

func (c *checker) checkELF() {
	r := newLibResolver(c.o.Tree, c.o.Base)
	for _, rel := range c.elfs {
		p, w := r.check(rel)
		c.problems = append(c.problems, p...)
		c.warnings = append(c.warnings, w...)
	}
}

func (c *checker) checkStrip() {
	for _, p := range c.o.Descriptor.Strip {
		if existsIn(c.o.Tree, p) {
			c.bad("%s: listed in strip, but still in the image", p)
		}
	}
}

// checkOwnFiles checks usr/lib/vos/ext/<id>/: the descriptor, the package
// list, the module options the initramfs allows (exactly the descriptor's)
// and the fetched files.
func (c *checker) checkOwnFiles() {
	d := c.o.Descriptor
	dir := "usr/lib/vos/ext/" + c.o.ID
	read := func(name string) ([]byte, bool) {
		rel := dir + "/" + name
		host, err := resolveIn(c.o.Tree, rel, false)
		if err == nil {
			var fi fs.FileInfo
			if fi, err = os.Lstat(host); err == nil && !fi.Mode().IsRegular() {
				err = errors.New("not a regular file")
			}
		}
		if err != nil {
			c.bad("%s: %v", rel, err)
			return nil, false
		}
		b, err := readLimited(host, descriptor.MaxSize)
		if err != nil {
			c.bad("%s: %v", rel, err)
			return nil, false
		}
		return b, true
	}
	if b, ok := read("extension.json"); ok {
		shipped, err := descriptor.Parse(b)
		switch {
		case err != nil:
			c.bad("%s/extension.json: %v", dir, err)
		case !reflect.DeepEqual(shipped, d):
			c.bad("%s/extension.json: differs from the extension's descriptor", dir)
		}
	}
	read("packages.txt")
	if b, ok := read("module-options"); ok {
		var want, got []string
		for _, m := range d.ModuleOptions {
			want = append(want, m.Module+" "+m.Param)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
				got = append(got, strings.Join(f, " "))
			}
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(slices.Compact(want), slices.Compact(got)) {
			c.bad("%s/module-options: lists %q, the descriptor's module_options are %q", dir, got, want)
		}
	}
	for i, f := range d.Fetch {
		host, err := resolveIn(c.o.Tree, dir+"/"+f.Dest, false)
		if err == nil {
			var fi fs.FileInfo
			if fi, err = os.Lstat(host); err == nil && !fi.Mode().IsRegular() {
				err = errors.New("not a regular file")
			}
		}
		if err != nil {
			c.bad("fetch[%d]: %s/%s: %v", i, dir, f.Dest, err)
		}
	}
}
