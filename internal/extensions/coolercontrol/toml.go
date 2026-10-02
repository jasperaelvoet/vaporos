package coolercontrol

import (
	"errors"
	"regexp"
	"strings"
)

// setting is a key of config.toml's [settings] table with its TOML value.
type setting struct{ key, value string }

var (
	settingsHeaderRe = regexp.MustCompile(`^\s*\[\s*settings\s*\]\s*(#.*)?$`)
	keyRe            = regexp.MustCompile(`^\s*(?:([A-Za-z0-9_-]+)|"([A-Za-z0-9_-]+)")\s*=`)
	topSettingsRe    = regexp.MustCompile(`^\s*settings\s*[.=]`)
)

var errTOML = errors.New("config.toml is not TOML VaporOS can change; delete CoolerControl's settings to start over")

// patchSettings returns doc, a TOML document, with its [settings] table
// holding force's values and defaults' where the table lacks the key.
// Everything else stays byte for byte: this is just enough TOML to change
// a few keys of a file coolercontrold writes (one value per key, values
// that may span lines, strings, comments), not a parser.
func patchSettings(doc string, force, defaults []setting) (string, error) {
	lines := strings.SplitAfter(doc, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	type extent struct{ first, last int }
	var (
		depth      int
		ml         string // the closing delimiter of a multi-line string we are in
		inSettings bool
		header     = -1
		insertAt   = -1 // the last line of [settings]' content
		seenHeader bool
		entries    = map[string]extent{}
		curKey     string
		curFirst   int
	)
	for i, l := range lines {
		if depth == 0 && ml == "" {
			trimmed := strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(trimmed, "["):
				seenHeader = true
				inSettings = settingsHeaderRe.MatchString(trimmed)
				if inSettings {
					if header >= 0 {
						return "", errTOML
					}
					header, insertAt = i, i
				}
			case inSettings:
				if m := keyRe.FindStringSubmatch(l); m != nil {
					curKey, curFirst = m[1]+m[2], i
				}
				if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
					insertAt = i
				}
			case !seenHeader && topSettingsRe.MatchString(l):
				return "", errTOML // settings as dotted keys or an inline table
			}
		} else if inSettings {
			insertAt = i
		}
		depth, ml = scanTOML(l, depth, ml)
		if curKey != "" && depth == 0 && ml == "" {
			if _, dup := entries[curKey]; dup {
				return "", errTOML
			}
			entries[curKey] = extent{curFirst, i}
			curKey = ""
		}
	}
	if depth != 0 || ml != "" {
		return "", errTOML
	}

	replace := map[int]setting{}
	var inserts []string
	for _, s := range force {
		if e, ok := entries[s.key]; ok {
			replace[e.first] = s
		} else {
			inserts = append(inserts, s.key+" = "+s.value+"\n")
		}
	}
	for _, s := range defaults {
		if _, ok := entries[s.key]; !ok {
			inserts = append(inserts, s.key+" = "+s.value+"\n")
		}
	}
	if header < 0 {
		if len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
			lines[len(lines)-1] += "\n"
		}
		if len(lines) > 0 {
			lines = append(lines, "\n")
		}
		return strings.Join(append(append(lines, "[settings]\n"), inserts...), ""), nil
	}
	var out []string
	insert := func() {
		if n := len(out); n > 0 && !strings.HasSuffix(out[n-1], "\n") {
			out[n-1] += "\n"
		}
		out = append(out, inserts...)
	}
	for i := 0; i < len(lines); {
		if s, ok := replace[i]; ok {
			out = append(out, s.key+" = "+s.value+"\n")
			next := entries[s.key].last + 1
			if insertAt >= i && insertAt < next {
				insert()
			}
			i = next
			continue
		}
		out = append(out, lines[i])
		if i == insertAt {
			insert()
		}
		i++
	}
	return strings.Join(out, ""), nil
}

// scanTOML follows one line of a TOML document: how deep it leaves us in
// arrays and inline tables, and whether in a multi-line string (its
// closing delimiter).
func scanTOML(line string, depth int, ml string) (int, string) {
	for i := 0; i < len(line); {
		if ml != "" {
			j := strings.Index(line[i:], ml)
			if j < 0 {
				return depth, ml
			}
			i, ml = i+j+len(ml), ""
			continue
		}
		switch c := line[i]; {
		case c == '#':
			return depth, ml
		case strings.HasPrefix(line[i:], `"""`), strings.HasPrefix(line[i:], `'''`):
			ml = line[i : i+3]
			i += 3
		case c == '"':
			for i++; i < len(line) && line[i] != '"' && line[i] != '\n'; i++ {
				if line[i] == '\\' {
					i++
				}
			}
			i++
		case c == '\'':
			for i++; i < len(line) && line[i] != '\'' && line[i] != '\n'; i++ {
			}
			i++
		case c == '[' || c == '{':
			depth++
			i++
		case c == ']' || c == '}':
			depth = max(depth-1, 0)
			i++
		default:
			i++
		}
	}
	return depth, ml
}
