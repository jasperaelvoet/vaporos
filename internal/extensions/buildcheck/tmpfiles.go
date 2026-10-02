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
// system and home data areas, and its directories in /run, /var/cache and
// /var/log. Its tmpfiles.d lines may touch nothing else.
func ownAreas(id string) []string {
	return []string{
		path.Join(filepath.ToSlash(config.ExtDataDir()), id),
		path.Join(filepath.ToSlash(config.GamerHome), config.ExtGamerDataSubdir, id),
		"/run/" + id,
		"/var/cache/" + id,
		"/var/log/" + id,
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

func (c *checker) checkTmpfiles() {
	ents, _, err := readDirIn(c.o.Tree, tmpfilesDir)
	if err != nil {
		c.bad("reading %s: %v", tmpfilesDir, err)
		return
	}
	own := ownAreas(c.o.ID)
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
		for i, line := range strings.Split(string(b), "\n") {
			if p := tmpfilesProblem(line, own); p != "" {
				c.bad("%s:%d: %s", file, i+1, p)
			}
		}
	}
}

// tmpfilesProblem checks one tmpfiles.d line: a known type that creates
// no device node and sets no extended attribute, a path in one of own, no
// setuid or setgid mode, and an L target or C source in own or /usr.
func tmpfilesProblem(line string, own []string) string {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return ""
	}
	var f [6]string
	rest := line
	for i := range f {
		var ok bool
		if f[i], rest, ok = tmpfilesField(rest); !ok {
			return "unbalanced quotes or a backslash escape, which this check does not read"
		}
	}
	typ, name, mode := f[0], f[1], f[2]
	arg := strings.TrimSpace(rest)
	if typ == "" || name == "" {
		return "not a type and a path"
	}
	why, known := tmpfilesTypes[typ[0]]
	switch {
	case !known || strings.Trim(typ[1:], "+!-=~^$") != "":
		return fmt.Sprintf("unknown type %q", typ)
	case why != "":
		return fmt.Sprintf("type %s %s", typ, why)
	}
	p, ok := expandSpecifiers(name)
	if !ok || !strings.HasPrefix(p, "/") {
		return fmt.Sprintf("%s: not an absolute path with only the %%S %%C %%L %%t %%T %%V specifiers", name)
	}
	if p = path.Clean(p); !inAreas(p, own) {
		return fmt.Sprintf("%s is outside the extension's own paths (%s)", name, strings.Join(own, ", "))
	}
	if m := strings.TrimLeft(mode, "~:"); mode != "" && mode != "-" {
		v, err := strconv.ParseUint(m, 8, 32)
		switch {
		case err != nil:
			return fmt.Sprintf("%s: mode %q is not octal", name, mode)
		case v&0o6000 != 0:
			return fmt.Sprintf("%s: mode %s sets the setuid or setgid bit", name, mode)
		}
	}
	if (typ[0] != 'L' && typ[0] != 'C') || arg == "" || arg == "-" {
		return "" // C and L without an argument use /usr/share/factory
	}
	what := "target"
	src, ok := expandSpecifiers(arg)
	if typ[0] == 'C' {
		what = "source"
	} else if ok && !strings.HasPrefix(src, "/") {
		src = path.Join(path.Dir(p), src)
	}
	if src = path.Clean(src); !ok || !strings.HasPrefix(src, "/") || !(inAreas(src, own) || under(src, "/usr")) {
		return fmt.Sprintf("%s: its %s %s is outside the extension's own paths and /usr", name, what, arg)
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
