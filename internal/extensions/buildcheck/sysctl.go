package buildcheck

import (
	"path"
	"strings"
)

// sysctlSetting is one key a sysctl.d file sets.
type sysctlSetting struct {
	key, file string
}

// readSysctl returns the keys every *.conf in dirs (inside root) sets.
func readSysctl(root string, dirs ...string) ([]sysctlSetting, error) {
	var out []sysctlSetting
	for _, dir := range dirs {
		ents, _, err := readDirIn(root, dir)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			file := dir + "/" + e.Name()
			b, err := readIn(root, file, 1<<20)
			if err != nil {
				return nil, err
			}
			for _, k := range sysctlKeys(b) {
				out = append(out, sysctlSetting{key: k, file: file})
			}
		}
	}
	return out, nil
}

// sysctlKeys returns the keys a sysctl.d file sets, with dots as separators.
func sysctlKeys(b []byte) []string {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k = normalizeSysctl(strings.TrimSpace(k)); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// normalizeSysctl drops the "ignore failure" dash and turns a slash-separated
// key into a dotted one. The first separator decides which one a key uses;
// with slashes, dots are part of names (net/ipv4/conf/enp3s0.200/forwarding).
func normalizeSysctl(k string) string {
	k = strings.TrimSpace(strings.TrimPrefix(k, "-"))
	if i := strings.IndexAny(k, "./"); i >= 0 && k[i] == '/' {
		k = strings.Map(func(r rune) rune {
			switch r {
			case '/':
				return '.'
			case '.':
				return '/'
			}
			return r
		}, k)
	}
	return k
}

// sysctlOverlap reports whether two keys can name the same setting; keys
// may be globs.
func sysctlOverlap(a, b string) bool {
	if a == b {
		return true
	}
	pa, pb := sysctlPath(a), sysctlPath(b)
	if ok, _ := path.Match(pa, pb); ok {
		return true
	}
	ok, _ := path.Match(pb, pa)
	return ok
}

// isNetSysctl reports whether a key can set a network setting.
func isNetSysctl(k string) bool {
	return sysctlOverlap(strings.SplitN(k, ".", 2)[0], "net")
}

func sysctlPath(k string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '.':
			return '/'
		case '/':
			return '.'
		}
		return r
	}, k)
}
