package web

// Exports of the control center for the website. TestExportStrings writes
// the words each UI set shows, for the website's label check; the live demo's
// exporter (TestExportDemo) joins it here later.

import (
	"bytes"
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

var (
	// hiddenHTML is markup whose text nobody reads: scripts, styles, the
	// icon sprite and comments.
	hiddenHTML = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<svg\b[^>]*>.*?</svg>|<!--.*?-->`)
	// inlineTag is an inline element's tag; dropping it keeps a sentence
	// with a link or <strong> in it whole.
	inlineTag = regexp.MustCompile(`(?i)</?(?:a|abbr|b|code|em|i|kbd|mark|q|s|small|span|strong|sub|sup|time|u|var)\b[^>]*>`)
	anyTag    = regexp.MustCompile(`<[^>]*>`)
	textAttr  = regexp.MustCompile(`\s(?:aria-label|title|placeholder|alt)="([^"]*)"`)
)

// tidyString collapses whitespace and keeps s only if it has a letter.
func tidyString(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	return s, strings.IndexFunc(s, unicode.IsLetter) >= 0 && len(s) <= 500
}

// htmlStrings returns the text a reader of body sees: its text nodes (each
// alone, and each run of text with its inline elements dropped) and its
// aria-label, title, placeholder and alt values.
func htmlStrings(body string) []string {
	body = hiddenHTML.ReplaceAllString(body, " ")
	var out []string
	for _, m := range textAttr.FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	for _, src := range []string{body, inlineTag.ReplaceAllString(body, "")} {
		for _, chunk := range anyTag.Split(src, -1) {
			out = append(out, html.UnescapeString(chunk))
		}
	}
	return out
}

// jsUnescape decodes the escapes a string literal may hold.
func jsUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case 'n', 'r', 't':
			b.WriteByte(' ')
		case 'u', 'x':
			n := 2
			if c == 'u' {
				n = 4
			}
			if i+n < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += n
					continue
				}
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// jsStrings returns the string literals of a script and the static parts of
// its template literals (WEB §8.2 step 5).
func jsStrings(src string) []string {
	var out []string
	jsScan(src, func(kind byte, from, to int) {
		if kind != '/' {
			out = append(out, jsUnescape(src[from:to]))
		}
	})
	return out
}

// uiStrings is the sorted, unique set of words set shows: the text of every
// page in both modes, the navigation labels, titles and leads, and the
// strings in its scripts. fromHTML is the part that came from the rendered
// pages alone.
func uiStrings(t *testing.T, set uiSet) (all []string, fromHTML map[string]bool) {
	t.Helper()
	seen := map[string]bool{}
	fromHTML = map[string]bool{}
	add := func(s string, html bool) {
		if s, ok := tidyString(s); ok {
			seen[s] = true
			if html {
				fromHTML[s] = true
			}
		}
	}
	for _, body := range renderAll(t, set) {
		for _, s := range htmlStrings(body) {
			add(s, true)
		}
	}
	for _, p := range append([]page{set.Installer}, set.Pages...) {
		add(p.Title, false)
		add(p.NavLabel(), false)
		add(p.Lead, false)
	}
	for _, src := range setScripts(t, set) {
		for _, s := range jsStrings(src) {
			add(s, false)
		}
	}
	for s := range seen {
		all = append(all, s)
	}
	sort.Strings(all)
	return all, fromHTML
}

// TestExportStrings writes ui-strings.<set>.json, a sorted JSON array of the
// words each UI set shows, for the website's check that every [[Label]] in
// its copy names something the control center really says:
//
//	VOS_WEB_EXPORT_STRINGS="$PWD/website/tests/fixtures" go test ./internal/web -run TestExportStrings
//
// pages.yml runs this before the website's tests. The path must be absolute:
// go test runs in internal/web. Without the variable it checks the
// extraction on the active set and writes nothing.
func TestExportStrings(t *testing.T) {
	dir := os.Getenv("VOS_WEB_EXPORT_STRINGS")
	if dir != "" && !filepath.IsAbs(dir) {
		t.Fatalf("VOS_WEB_EXPORT_STRINGS=%q must be an absolute path: go test runs in internal/web", dir)
	}
	for _, set := range uiSets {
		t.Run(set.Name, func(t *testing.T) {
			switch {
			case len(set.Pages) == 0:
				t.Skipf("UI set %q has no pages yet", set.Name)
			case dir == "" && set.Name != activeSet.Name && !strictWeb():
				t.Skipf("UI set %q is not active; VOS_WEB_STRICT=1 checks it", set.Name)
			}
			all, fromHTML := uiStrings(t, set)
			for _, p := range set.Pages {
				if p.Nav && !fromHTML[p.NavLabel()] {
					t.Errorf("the rendered pages never show the navigation label %q", p.NavLabel())
				}
			}
			// Text may say /var/mnt/<name>; a tag with attributes or a
			// template action in it means the extraction broke.
			leak := regexp.MustCompile(`\{\{|<[a-zA-Z][\w-]*\s[^>]*=`)
			for s := range fromHTML {
				if leak.MatchString(s) {
					t.Errorf("markup leaked into the strings: %q", s)
				}
			}
			if dir == "" {
				return
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(all); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "ui-strings."+set.Name+".json")
			if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %d strings to %s", len(all), out)
		})
	}
}
