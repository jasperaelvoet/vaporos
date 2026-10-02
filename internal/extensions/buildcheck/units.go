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
	dropIns map[string][]string // unit name -> rels of its *.conf drop-ins
	depDirs map[string]string   // unit name -> rel of its .wants/.requires/.upholds
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
		if u, ok := dropInUnit(p[0]); ok {
			s.dropIns[u] = append(s.dropIns[u], rel)
		}
	}
}

// ships reports whether the extension ships unit u (or its template).
func (s *unitScope) ships(u string) bool {
	if _, ok := s.units[u]; ok {
		return true
	}
	if _, ok := s.aliases[u]; ok {
		return true
	}
	if t, ok := unitTemplate(u); ok {
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

// check applies the unit rules: drop-ins and dependency directories only
// for the extension's own units, aliases only to them, no Before= on a unit
// the base has, and every system service with a drop-in that bounds its
// start (so a hanging extension cannot hold the boot).
func (s *unitScope) check(tree, base string) unitFacts {
	var f unitFacts
	bad := func(format string, a ...any) { f.problems = append(f.problems, fmt.Sprintf(format, a...)) }

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
			}
		}
	}

	for _, u := range sortedKeys(s.units) {
		files := []string{s.units[u]}
		var drops []string
		if t, ok := unitTemplate(u); ok && t != u {
			drops = append(drops, s.dropIns[t]...)
		}
		drops = append(drops, s.dropIns[u]...)
		sort.SliceStable(drops, func(i, j int) bool { return path.Base(drops[i]) < path.Base(drops[j]) })
		files = append(files, drops...)

		var as []assignment
		for _, rel := range files {
			b, err := readLimited(filepath.Join(tree, filepath.FromSlash(rel)), 1<<20)
			if err != nil {
				bad("%s: %v", rel, err)
				continue
			}
			as = append(as, parseUnit(rel, b)...)
		}
		for _, a := range as {
			if a.section != "Unit" || a.key != "Before" {
				continue
			}
			for _, b := range strings.Fields(a.value) {
				if s.baseHas(base, b) {
					bad("%s: Before=%s orders it before a unit of the base", a.file, b)
				}
			}
		}
		if s.system && strings.HasSuffix(u, ".service") {
			if p := startTimeoutProblem(u, len(drops) > 0, as); p != "" {
				bad("%s: %s", s.units[u], p)
			}
			if runsAsRoot(as) {
				f.runsAsRoot = true
			}
		}
	}
	return f
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

// startTimeoutProblem checks that the last TimeoutStartSec= (or
// TimeoutSec=) of a service comes from a drop-in and is finite.
func startTimeoutProblem(unit string, hasDropIn bool, as []assignment) string {
	want := fmt.Sprintf("needs a drop-in (%s.d/*.conf) that sets a finite TimeoutStartSec=", unit)
	if !hasDropIn {
		return want
	}
	var last *assignment
	for i, a := range as {
		if a.section == "Service" && (a.key == "TimeoutStartSec" || a.key == "TimeoutSec") {
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

// runsAsRoot reports whether a service runs as root: no User= other than
// root, and no DynamicUser=.
func runsAsRoot(as []assignment) bool {
	user, dynamic := "", false
	for _, a := range as {
		if a.section != "Service" {
			continue
		}
		switch a.key {
		case "User":
			user = a.value
		case "DynamicUser":
			dynamic = parseBool(a.value)
		}
	}
	return !dynamic && (user == "" || user == "root" || user == "0")
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
