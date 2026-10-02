package buildcheck

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// unitScope collects one unit directory of an image: the system manager's
// or vapor's user manager's.
type unitScope struct {
	dir      string   // in the image, e.g. usr/lib/systemd/system
	baseDirs []string // where the base keeps the same scope's units
	system   bool

	units   map[string]string   // unit name -> rel of its unit file
	aliases map[string]string   // unit name -> rel of a symlink to another unit
	dropIns map[string][]string // the name a drop-in directory is for -> rels of its *.conf drop-ins
	depDirs map[string]string   // unit name -> rel of its .wants/.requires/.upholds
	deps    []string            // entries of those directories: the units they add
	linked  []string            // drop-ins that are symlinks
	target  map[string]string   // alias -> the unit it names (filled by check)
}

func newUnitScope(scope string, system bool) *unitScope {
	return &unitScope{
		dir:      "usr/lib/systemd/" + scope,
		baseDirs: []string{"usr/lib/systemd/" + scope, "etc/systemd/" + scope},
		system:   system,
		units:    map[string]string{},
		aliases:  map[string]string{},
		dropIns:  map[string][]string{},
		depDirs:  map[string]string{},
		target:   map[string]string{},
	}
}

// note records an allowed path of the scope.
func (s *unitScope) note(rel string, isDir, isLink bool) {
	p := strings.Split(strings.TrimPrefix(rel, s.dir+"/"), "/")
	switch {
	case len(p) == 1 && !isDir && isLink:
		s.aliases[p[0]] = rel
	case len(p) == 1 && !isDir:
		s.units[p[0]] = rel
	case len(p) == 1:
		if u, ok := depDirUnit(p[0]); ok {
			s.depDirs[u] = rel
		}
	case len(p) == 2:
		if u, ok := dropInUnit(p[0]); ok && isLink {
			s.linked = append(s.linked, rel)
		} else if ok {
			s.dropIns[u] = append(s.dropIns[u], rel)
		} else if _, ok := depDirUnit(p[0]); ok {
			s.deps = append(s.deps, rel)
		}
	}
}

// canonical returns the unit a name means: an alias's unit, or the
// matching instance of an aliased template.
func (s *unitScope) canonical(name string) string {
	if t, ok := s.target[name]; ok {
		return t
	}
	if tmpl, ok := unitTemplate(name); ok {
		if t, ok := s.target[tmpl]; ok {
			if inst, ok := instanceOf(t, name); ok {
				return inst
			}
		}
	}
	return name
}

// ships reports whether the extension ships unit u (or its template),
// under its own name or an alias.
func (s *unitScope) ships(u string) bool {
	c := s.canonical(u)
	if _, ok := s.units[c]; ok {
		return true
	}
	if t, ok := unitTemplate(c); ok {
		_, ok = s.units[t]
		return ok
	}
	return false
}

// unitFacts is what checking a scope learned.
type unitFacts struct {
	problems   []string
	runsAsRoot bool
}

// execSections are the unit types an image may ship that run commands,
// with the section that holds their commands, user and timeout.
var execSections = map[string]string{"service": "Service", "socket": "Socket"}

// triggerKeys are the section and setting that name the unit a timer,
// path or socket starts; without one it starts the service of its name.
var triggerKeys = map[string][2]string{"timer": {"Timer", "Unit"}, "path": {"Path", "Unit"}, "socket": {"Socket", "Service"}}

// commandKeys are the settings that run a command line.
var commandKeys = map[string]bool{
	"ExecCondition": true, "ExecStartPre": true, "ExecStart": true, "ExecStartPost": true,
	"ExecReload": true, "ExecStop": true, "ExecStopPre": true, "ExecStopPost": true,
}

// check applies the unit rules: names that neither are nor reach into the
// base's units, drop-ins and dependency directories only for the
// extension's own units, aliases only to them, no setting (settingProblems),
// dependency entry or trigger that acts on a unit of the base or on the
// system's state, and every system unit that runs commands with a drop-in
// that bounds them (so a hanging extension cannot hold the boot).
func (s *unitScope) check(tree, base string) unitFacts {
	var f unitFacts
	bad := func(format string, a ...any) { f.problems = append(f.problems, fmt.Sprintf(format, a...)) }

	for _, name := range sortedKeys(s.aliases) {
		rel := s.aliases[name]
		target, err := os.Readlink(filepath.Join(tree, filepath.FromSlash(rel)))
		switch {
		case err != nil:
			bad("%s: %v", rel, err)
		case target == "/dev/null":
			bad("%s: masks a unit", rel)
		default:
			t := target
			if !strings.HasPrefix(t, "/") {
				t = path.Join("/"+s.dir, t)
			}
			if path.Dir(t) != "/"+s.dir || s.units[path.Base(t)] == "" {
				bad("%s: an alias must point at one of the extension's own units, not %s", rel, target)
			} else {
				s.target[name] = path.Base(t)
			}
		}
	}
	for _, rel := range s.linked {
		bad("%s: a drop-in must be a file, not a symlink", rel)
	}
	names := make(map[string]string, len(s.units)+len(s.aliases))
	for n, rel := range s.units {
		names[n] = rel
	}
	for n, rel := range s.aliases {
		names[n] = rel
	}
	for _, n := range sortedKeys(names) {
		switch {
		case existsIn(base, names[n]):
			// the same path: the collision check reports it
		case s.baseHas(base, n):
			bad("%s: the base has %s or its template; an extension never replaces a unit of the base", names[n], n)
		case isTemplate(n) && s.baseInstances(base, n):
			bad("%s: the base has instances of %s, which its drop-ins would change", names[n], n)
		}
	}
	for _, u := range sortedKeys(s.dropIns) {
		if !s.ships(u) {
			bad("%s: drop-in for %s, which the extension does not ship (extensions never change the base's units)", path.Dir(s.dropIns[u][0]), u)
		}
	}
	for _, u := range sortedKeys(s.depDirs) {
		if !s.ships(u) {
			bad("%s: adds dependencies to %s, which the extension does not ship", s.depDirs[u], u)
		}
	}
	for _, rel := range s.deps {
		name := path.Base(rel)
		if what := s.systemState(base, name); what != "" {
			bad("%s: a dependency on %s", rel, what)
		} else if what := s.baseUnit(base, name); what != "" && strings.HasSuffix(path.Dir(rel), ".upholds") {
			bad("%s: keeps restarting %s", rel, what)
		}
	}

	// Every unit file, and every instance with drop-ins of its own.
	todo := map[string]bool{}
	for u := range s.units {
		todo[u] = true
	}
	for k := range s.dropIns {
		c := s.canonical(k)
		if t, ok := unitTemplate(c); ok && s.units[t] != "" {
			todo[c] = true
		}
	}
	for _, u := range sortedKeys(todo) {
		file := s.units[u]
		if file == "" {
			t, _ := unitTemplate(u)
			file = s.units[t]
		}
		drops, p := s.dropInsOf(u)
		f.problems = append(f.problems, p...)
		var as []assignment
		for _, rel := range append([]string{file}, drops...) {
			b, err := readIn(tree, rel, 1<<20)
			if err != nil {
				bad("%s: %v", rel, err)
				continue
			}
			as = append(as, parseUnit(rel, b)...)
		}
		for _, a := range as {
			for _, p := range s.settingProblems(base, a) {
				bad("%s: %s", a.file, p)
			}
		}
		for _, t := range triggered(u, as) {
			if p := s.baseUnit(base, t); p != "" {
				bad("%s/%s: starts %s, %s", s.dir, u, t, p)
			}
		}
		typ := unitType(u)
		section, ok := execSections[typ]
		if !s.system || !ok || (typ == "socket" && len(commands(as, section)) == 0) {
			continue
		}
		if p := timeoutProblem(u, section, len(drops) > 0, as); p != "" {
			bad("%s/%s: %s", s.dir, u, p)
		}
		if runsAsRoot(typ, as) {
			f.runsAsRoot = true
		}
	}
	return f
}

// dropInsOf returns unit u's drop-ins in the order systemd applies them,
// by file name: those of its own directory, its template's and its
// aliases'. An instance's drop-in hides its template's of the same name;
// between other directories systemd leaves that open, which is a problem.
func (s *unitScope) dropInsOf(u string) (drops, problems []string) {
	tmpl, _ := unitTemplate(u)
	byName := map[string][]string{}
	for _, k := range sortedKeys(s.dropIns) {
		if c := s.canonical(k); c != u && (tmpl == "" || c != tmpl) {
			continue
		}
		for _, rel := range s.dropIns[k] {
			byName[path.Base(rel)] = append(byName[path.Base(rel)], rel)
		}
	}
	for _, name := range sortedKeys(byName) {
		rels := byName[name]
		if len(rels) == 2 {
			a, _ := dropInUnit(path.Base(path.Dir(rels[0])))
			b, _ := dropInUnit(path.Base(path.Dir(rels[1])))
			if t, ok := unitTemplate(a); ok && t == b {
				rels = rels[:1]
			} else if t, ok := unitTemplate(b); ok && t == a {
				rels = rels[1:]
			}
		}
		if len(rels) > 1 {
			problems = append(problems, fmt.Sprintf("%s: drop-ins of the same name for %s, in an order systemd leaves open", strings.Join(rels, ", "), u))
		}
		drops = append(drops, rels...)
	}
	return drops, problems
}

// baseHas reports whether the base has unit u (or its template) in this
// scope.
func (s *unitScope) baseHas(base, u string) bool {
	names := []string{u}
	if t, ok := unitTemplate(u); ok && t != u {
		names = append(names, t)
	}
	for _, d := range s.baseDirs {
		for _, n := range names {
			if strings.Contains(n, "/") {
				continue
			}
			if existsIn(base, d+"/"+n) {
				return true
			}
		}
	}
	return false
}

// baseUnit says what u is when an extension's unit may not act on it: a
// unit of the base in this scope (or an instance of one of its templates),
// one of a type systemd names at runtime, or a name with specifiers
// outside its instance, which this check does not resolve. "" otherwise.
func (s *unitScope) baseUnit(base, u string) string {
	stem := u
	if t, ok := unitTemplate(u); ok {
		stem = t
	}
	switch {
	case strings.Contains(stem, "%"):
		return "a unit named with specifiers this check does not resolve"
	case runtimeUnitTypes[unitType(u)]:
		return "a ." + unitType(u) + " unit, which systemd and generators name at runtime"
	case s.baseHas(base, u):
		return "a unit of the base"
	}
	return ""
}

// triggered returns the units a timer, path or socket may start: every one
// its Unit= or Service= lines name (systemd keeps a timer's or path's first,
// a socket's last, and ignores an empty one), else the service of its own
// name (a template's instances with Accept=yes).
func triggered(u string, as []assignment) []string {
	typ := unitType(u)
	tk, ok := triggerKeys[typ]
	if !ok {
		return nil
	}
	var names []string
	accept := false
	for _, a := range as {
		switch {
		case a.section != tk[0]:
		case a.key == tk[1]:
			if a.value != "" {
				names = append(names, a.value)
			}
		case a.key == "Accept":
			accept = parseBool(a.value)
		}
	}
	if len(names) > 0 {
		return names
	}
	stem := strings.TrimSuffix(u, "."+typ)
	if accept && !strings.Contains(stem, "@") {
		stem += "@"
	}
	return []string{stem + ".service"}
}

// baseInstances reports whether the base has a unit file that is an
// instance of template t in this scope: t's drop-ins would apply to it.
func (s *unitScope) baseInstances(base, t string) bool {
	for _, d := range s.baseDirs {
		ents, _, err := readDirIn(base, d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if tt, ok := unitTemplate(e.Name()); ok && tt == t {
				return true
			}
		}
	}
	return false
}

// timeoutProblem checks that the last timeout of a unit's commands comes
// from a drop-in and is finite: TimeoutStartSec= or TimeoutSec= of a
// service, TimeoutSec= of a socket.
func timeoutProblem(unit, section string, hasDropIn bool, as []assignment) string {
	key := "TimeoutSec"
	if section == "Service" {
		key = "TimeoutStartSec"
	}
	want := fmt.Sprintf("needs a drop-in (%s.d/*.conf) that sets a finite %s=", unit, key)
	if !hasDropIn {
		return want
	}
	var last *assignment
	for i, a := range as {
		if a.section == section && (a.key == key || a.key == "TimeoutSec") {
			last = &as[i]
		}
	}
	if last == nil || !strings.HasSuffix(path.Dir(last.file), ".d") {
		return want
	}
	d, inf, ok := parseTimespan(last.value)
	switch {
	case !ok:
		return fmt.Sprintf("%s=%q in %s is not a time span", last.key, last.value, last.file)
	case inf || d <= 0:
		return fmt.Sprintf("%s=%s in %s does not bound the start", last.key, last.value, last.file)
	}
	return ""
}

// commands returns a section's command lines by setting, after the resets
// (an empty value) that drop-ins may do.
func commands(as []assignment, section string) map[string][]string {
	out := map[string][]string{}
	for _, a := range as {
		switch {
		case a.section != section || !commandKeys[a.key]:
		case a.value == "":
			delete(out, a.key)
		default:
			out[a.key] = append(out[a.key], a.value)
		}
	}
	return out
}

// runsAsRoot reports whether a unit that runs commands runs one as root:
// a command prefixed '+' or '!' ('!!'), PermissionsStartOnly= with
// commands besides ExecStart=, or no User= other than root without
// DynamicUser=.
func runsAsRoot(typ string, as []assignment) bool {
	section := execSections[typ]
	cmds := commands(as, section)
	user, dynamic, startOnly := "", false, false
	for _, a := range as {
		if a.section != section {
			continue
		}
		switch a.key {
		case "User":
			user = a.value
		case "DynamicUser":
			dynamic = parseBool(a.value)
		case "PermissionsStartOnly":
			startOnly = parseBool(a.value)
		}
	}
	for key, lines := range cmds {
		if startOnly && key != "ExecStart" {
			return true
		}
		for _, l := range lines {
			if elevated(l) {
				return true
			}
		}
	}
	return !dynamic && (user == "" || user == "root" || user == "0")
}

// elevated reports whether a command line's prefixes run it with root's
// credentials whatever User= says: '+' (no sandbox at all), '!' and '!!'.
func elevated(cmd string) bool {
	for _, r := range cmd {
		switch r {
		case '+', '!':
			return true
		case '-', '@', ':', '|':
			continue
		}
		return false
	}
	return false
}

// assignment is one Key=value of a unit file or drop-in.
type assignment struct {
	file, section, key, value string
}

// parseUnit reads systemd's unit file syntax: sections, comments,
// backslash continuations (skipping comment lines inside them).
func parseUnit(file string, b []byte) []assignment {
	var out []assignment
	section := ""
	lines := strings.Split(string(b), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		for strings.HasSuffix(line, "\\") && i+1 < len(lines) {
			i++
			next := strings.TrimSpace(lines[i])
			if strings.HasPrefix(next, "#") || strings.HasPrefix(next, ";") {
				continue
			}
			line = strings.TrimSuffix(line, "\\") + " " + next
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		if line == "" {
			continue
		}
		if line[0] == '[' {
			if end := strings.IndexByte(line, ']'); end > 0 {
				section = line[1:end]
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out = append(out, assignment{file: file, section: section, key: strings.TrimSpace(k), value: strings.TrimSpace(v)})
	}
	return out
}

// spanUnits are systemd's time span units, in seconds.
var spanUnits = map[string]float64{
	"": 1, "s": 1, "sec": 1, "second": 1, "seconds": 1,
	"ns": 1e-9, "nsec": 1e-9, "us": 1e-6, "usec": 1e-6, "µs": 1e-6, "μs": 1e-6, "ms": 1e-3, "msec": 1e-3,
	"m": 60, "min": 60, "minute": 60, "minutes": 60,
	"h": 3600, "hr": 3600, "hour": 3600, "hours": 3600,
	"d": 86400, "day": 86400, "days": 86400,
	"w": 604800, "week": 604800, "weeks": 604800,
	"M": 2629800, "month": 2629800, "months": 2629800,
	"y": 31557600, "year": 31557600, "years": 31557600,
}

// parseTimespan parses a systemd time span ("90", "1min 30s", "infinity").
func parseTimespan(s string) (d time.Duration, infinite, ok bool) {
	s = strings.TrimSpace(s)
	if s == "infinity" {
		return 0, true, true
	}
	if s == "" {
		return 0, false, false
	}
	total := 0.0
	for s != "" {
		n := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
		if n < 0 {
			n = len(s)
		}
		if n == 0 {
			return 0, false, false
		}
		v, err := strconv.ParseFloat(s[:n], 64)
		if err != nil {
			return 0, false, false
		}
		s = strings.TrimLeft(s[n:], " ")
		u := strings.IndexFunc(s, func(r rune) bool { return (r >= '0' && r <= '9') || r == ' ' || r == '.' })
		if u < 0 {
			u = len(s)
		}
		mult, known := spanUnits[s[:u]]
		if !known {
			return 0, false, false
		}
		total += v * mult
		s = strings.TrimLeft(s[u:], " ")
	}
	return time.Duration(total * float64(time.Second)), false, true
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "yes", "y", "true", "t", "on":
		return true
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
