package sunshine

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strings"
)

// Sunshine's version comes from the running Sunshine (GET /api/config) or,
// while that cannot answer, from the image's package database. Never from
// `sunshine --version`: every Sunshine invocation initializes logging,
// which rotates sunshine.log out from under the running server, and that
// log is what the web UI shows.

// installedVersion is the Sunshine package's version in the image, "" if
// unknown. The image is read-only, so it is looked up once.
func (s *Service) installedVersion() string {
	s.pkgVersionOnce.Do(func() {
		s.pkgVersion = packageVersion(s.pacmanDB, "sunshine")
	})
	return s.pkgVersion
}

// packageVersion reads a package's version from a pacman database
// (<db>/local/<name>-<version>/desc), the same answer `pacman -Q` gives,
// without the epoch and release ("1:2026.928.101500-2" → "2026.928.101500").
func packageVersion(db, name string) string {
	matches, _ := filepath.Glob(filepath.Join(db, "local", name+"-*", "desc"))
	for _, m := range matches {
		data, err := readRegular(m)
		if err != nil {
			continue
		}
		desc := parseDesc(data)
		if desc["NAME"] != name || desc["VERSION"] == "" {
			continue // e.g. sunshine-foo-1.0-1 matched the glob too
		}
		v := desc["VERSION"]
		if i := strings.LastIndexByte(v, '-'); i > 0 {
			v = v[:i]
		}
		if i := strings.IndexByte(v, ':'); i >= 0 {
			v = v[i+1:]
		}
		return v
	}
	return ""
}

// parseDesc reads the first value of each %FIELD% section of a pacman
// desc file.
func parseDesc(data []byte) map[string]string {
	out := map[string]string{}
	field := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case len(line) > 2 && strings.HasPrefix(line, "%") && strings.HasSuffix(line, "%"):
			field = line[1 : len(line)-1]
		case line == "":
			field = ""
		case field != "":
			if _, ok := out[field]; !ok {
				out[field] = line
			}
		}
	}
	return out
}
