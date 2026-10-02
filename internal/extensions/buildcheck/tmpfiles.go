package buildcheck

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

const tmpfilesDir = "usr/lib/tmpfiles.d"

// ownAreas are the paths on the box that belong to extension id: its
// system and home data areas and its runtime directory. Its tmpfiles.d
// lines may touch nothing else.
func ownAreas(id string) []string {
	return []string{
		path.Join(filepath.ToSlash(config.ExtDataDir()), id),
		path.Join(filepath.ToSlash(config.GamerHome), config.ExtGamerDataSubdir, id),
		path.Join(filepath.ToSlash(config.ExtRunDir), id),
	}
}

// tmpfilesSpecifiers are the specifiers a line may use, as
// systemd-tmpfiles expands them for the system.
var tmpfilesSpecifiers = map[byte]string{'S': "/var/lib", 'C': "/var/cache", 'L': "/var/log", 't': "/run", 'T': "/tmp", 'V': "/var/tmp", '%': "%"}

// tmpfilesTypes are the line types systemd-tmpfiles knows, with why an
// image may not use one ("" when it may).
var tmpfilesTypes = map[byte]string{
	'f': "", 'w': "", 'd': "", 'D': "", 'e': "", 'v': "", 'q': "", 'Q': "", 'p': "", 'L': "", 'C': "",
	'x': "", 'X': "", 'r': "", 'R': "", 'z': "", 'Z': "", 'h': "", 'H': "", 'a': "", 'A': "",
	'c': "creates a device node",
	'b': "creates a device node",
	't': "sets extended attributes, which can grant file capabilities",
	'T': "sets extended attributes, which can grant file capabilities",
}

const (
	// globTypes are the line types whose path systemd-tmpfiles expands as
	// a glob.
	globTypes = "wexXrRzZaAhH"
	// unescapedTypes are the line types whose argument systemd-tmpfiles
	// unescapes C-style before use. This check refuses a backslash there
	// rather than read escapes: an L target or C source is a path.
	unescapedTypes = "fwLC"
)

// tmpfilesLine is a tmpfiles.d line that parseTmpfiles accepted.
type tmpfilesLine struct {
	where string // file:line
	typ   string // with its modifiers
	name  string // the path as written
	path  string // the path, specifiers expanded
	arg   string // an L target or C source, specifiers expanded (the factory copy without one)
	shown string // that argument as written
}

func (c *checker) checkTmpfiles() {
	ents, _, err := readDirIn(c.o.Tree, tmpfilesDir)
	if err != nil {
		c.bad("reading %s: %v", tmpfilesDir, err)
		return
	}
	own := ownAreas(c.o.ID)
	var lines []tmpfilesLine
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		file := tmpfilesDir + "/" + e.Name()
		b, err := readIn(c.o.Tree, file, 1<<20)
		if err != nil {
			c.bad("%s: %v", file, err)
			continue
		}
		for i, text := range strings.Split(string(b), "\n") {
			l, p := parseTmpfiles(text, own)
			l.where = fmt.Sprintf("%s:%d", file, i+1)
			switch {
			case p != "":
				c.bad("%s: %s", l.where, p)
			case l.typ != "":
				lines = append(lines, l)
			}
		}
	}
	layers := []string{c.o.Tree}
	for i := len(c.o.Others) - 1; i >= 0; i-- {
		layers = append(layers, c.o.Others[i])
	}
	v := newBoxView(append(layers, c.o.Base), own, lines)
	for _, l := range lines {
		if p := v.lineProblem(l); p != "" {
			c.bad("%s: %s", l.where, p)
		}
	}
}

// parseTmpfiles reads one tmpfiles.d line as written: a known type that
// creates no device node and sets no extended attribute, a path in one of
// own, no setuid or setgid mode, and an L or C argument this check can
// read. Blanks and comments come back without a type. Where the line leads
// on the box is lineProblem's.
func parseTmpfiles(text string, own []string) (l tmpfilesLine, problem string) {
	text = strings.TrimSpace(text)
	if text == "" || text[0] == '#' {
		return l, ""
	}
	var f [6]string
	rest := text
	for i := range f {
		var ok bool
		if f[i], rest, ok = tmpfilesField(rest); !ok {
			return l, "unbalanced quotes or a backslash escape, which this check does not read"
		}
	}
	typ, name, mode := f[0], f[1], f[2]
	arg := strings.TrimSpace(rest)
	if typ == "" || name == "" {
		return l, "not a type and a path"
	}
	why, known := tmpfilesTypes[typ[0]]
	switch {
	case !known || strings.Trim(typ[1:], "+!-=~^$") != "":
		return l, fmt.Sprintf("unknown type %q", typ)
	case why != "":
		return l, fmt.Sprintf("type %s %s", typ, why)
	}
	p, ok := expandSpecifiers(name)
	if !ok || !strings.HasPrefix(p, "/") {
		return l, fmt.Sprintf("%s: not an absolute path with only the %%S %%C %%L %%t %%T %%V specifiers", name)
	}
	if !inAreas(path.Clean(p), own) {
		return l, fmt.Sprintf("%s is outside the extension's own paths (%s)", name, strings.Join(own, ", "))
	}
	if m := strings.TrimLeft(mode, "~:"); mode != "" && mode != "-" {
		v, err := strconv.ParseUint(m, 8, 32)
		switch {
		case err != nil:
			return l, fmt.Sprintf("%s: mode %q is not octal", name, mode)
		case v&0o6000 != 0:
			return l, fmt.Sprintf("%s: mode %s sets the setuid or setgid bit", name, mode)
		}
	}
	l.typ, l.name, l.path = typ, name, p
	if strings.IndexByte(unescapedTypes, typ[0]) >= 0 && strings.Contains(arg, `\`) {
		return l, fmt.Sprintf("%s: a backslash in its argument, which systemd-tmpfiles unescapes and this check does not read", name)
	}
	if typ[0] != 'L' && typ[0] != 'C' {
		return l, ""
	}
	if strings.ContainsAny(typ[1:], "~^") {
		return l, fmt.Sprintf("%s: the ~ and ^ modifiers would make its argument something other than a path", name)
	}
	what := "target"
	if typ[0] == 'C' {
		what = "source"
	}
	if arg == "" || arg == "-" {
		l.arg = "/usr/share/factory" + p
		l.shown = l.arg
		return l, ""
	}
	src, ok := expandSpecifiers(arg)
	if !ok || (typ[0] == 'C' && !strings.HasPrefix(src, "/")) {
		return l, fmt.Sprintf("%s: its %s %s is outside the extension's own paths and /usr", name, what, arg)
	}
	l.arg, l.shown = src, arg
	return l, ""
}

// lineProblem checks where a line leads on the box: its path, and an L
// target or C source, must end in the extension's own areas or in /usr
// once every symlink is followed, and an L line's link is not made through
// a symlink.
func (v *boxView) lineProblem(l tmpfilesLine) string {
	isL := l.typ[0] == 'L'
	rs, err := v.resolve(l.path, !isL, strings.IndexByte(globTypes, l.typ[0]) >= 0)
	if err != nil {
		return fmt.Sprintf("%s: %v", l.name, err)
	}
	for _, r := range rs {
		switch {
		case isL && r != path.Clean(l.path):
			return fmt.Sprintf("%s: an L line's link is made through a symlink, at %s", l.name, r)
		case !v.allowed(r):
			return fmt.Sprintf("%s leads to %s through a symlink, outside the extension's own paths and /usr", l.name, r)
		}
	}
	if !isL && l.typ[0] != 'C' {
		return ""
	}
	what, target := "source", l.arg
	if isL {
		what = "target"
		if !strings.HasPrefix(target, "/") {
			target = path.Dir(path.Clean(l.path)) + "/" + target
		}
	}
	rs, err = v.resolve(target, true, false)
	if err != nil {
		return fmt.Sprintf("%s: its %s %s: %v", l.name, what, l.shown, err)
	}
	for _, r := range rs {
		switch {
		case v.allowed(r):
		case r == path.Clean(target):
			return fmt.Sprintf("%s: its %s %s is outside the extension's own paths and /usr", l.name, what, l.shown)
		default:
			return fmt.Sprintf("%s: its %s %s leads to %s, outside the extension's own paths and /usr", l.name, what, l.shown, r)
		}
	}
	return ""
}

// tmpfilesField splits the next whitespace-separated field off s, reading
// quotes as systemd does. Escapes are refused rather than misread.
func tmpfilesField(s string) (field, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t")
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\\':
			return "", "", false
		case quote != 0 && ch == quote:
			quote = 0
		case quote != 0:
			b.WriteByte(ch)
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == ' ' || ch == '\t':
			return b.String(), s[i:], true
		default:
			b.WriteByte(ch)
		}
	}
	return b.String(), "", quote == 0
}

// expandSpecifiers expands tmpfilesSpecifiers; any other specifier fails.
func expandSpecifiers(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 == len(s) {
			return "", false
		}
		v, ok := tmpfilesSpecifiers[s[i+1]]
		if !ok {
			return "", false
		}
		b.WriteString(v)
		i++
	}
	return b.String(), true
}

func inAreas(p string, areas []string) bool {
	for _, a := range areas {
		if under(p, a) {
			return true
		}
	}
	return false
}
