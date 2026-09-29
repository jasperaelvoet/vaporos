package web

// The control center's stylesheet is built by Tailwind (tools/web/css.mjs)
// and committed as static/app.css. These tests keep it honest without Node:
// the input hash in its first line must match the files it was built from,
// and the class names in the markup and scripts must follow ARCH §2.5.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the repository root as seen from this package's directory,
// where go test runs.
const repoRoot = "../.."

// strictWeb reports whether VOS_WEB_STRICT=1 asks for the checks that may go
// red between our commits (the inactive UI set, the contract's gaps). web.yml
// sets it; the OS build's go test -race ./... does not.
func strictWeb() bool { return os.Getenv("VOS_WEB_STRICT") == "1" }

// cssInputRoots are the files static/app.css is built from. Keep them in the
// same order and meaning as INPUT_ROOTS in tools/web/lib/css-hash.mjs and the
// @source lines in styles/app.css. A nil match means the path is one file.
var cssInputRoots = []struct {
	path  string
	match func(name string) bool
}{
	{"internal/web/styles", func(n string) bool { return strings.HasSuffix(n, ".css") }},
	{"internal/web/templates/layout.html", nil},
	{"internal/web/templates/pages", func(string) bool { return true }},
	{"internal/web/templates/partials", func(string) bool { return true }},
	{"internal/web/static/js", func(n string) bool { return strings.HasSuffix(n, ".js") }},
	{"tools/web/package-lock.json", nil},
}

// cssInputFiles lists the hashed files of the repository at root, as
// slash-separated repo-relative paths sorted byte-wise. Nothing under a
// legacy/ or node_modules/ directory counts, nor any file or directory whose
// name starts with "." (git does not carry .DS_Store and friends).
func cssInputFiles(root string) ([]string, error) {
	var out []string
	for _, r := range cssInputRoots {
		abs := filepath.Join(root, filepath.FromSlash(r.path))
		st, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if r.match == nil {
			if st.Mode().IsRegular() {
				out = append(out, r.path)
			}
			continue
		}
		if !st.IsDir() {
			continue
		}
		err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if p != abs && strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if name == "legacy" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !r.match(name) {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out) // Go compares strings byte-wise
	return out, nil
}

// cssInputHash is the 16-hex input hash: sha256 over path + "\0" +
// hex(sha256(file)) + "\n" for each input file, in order.
func cssInputHash(root string) (string, error) {
	files, err := cssInputFiles(root)
	if err != nil {
		return "", err
	}
	all := sha256.New()
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(b)
		all.Write([]byte(rel + "\x00" + hex.EncodeToString(sum[:]) + "\n"))
	}
	return hex.EncodeToString(all.Sum(nil))[:16], nil
}

// cssHeader is static/app.css's first line, written by tools/web/css.mjs.
var cssHeader = regexp.MustCompile(`^/\*! vaporos-css inputs=([0-9a-f]{16}) tailwind=(\d+\.\d+\.\d+) \*/$`)

// The Go and Node hashes must agree. tools/web/test/testdata/css-hash is a
// miniature repository whose hash was worked out once with shasum;
// tools/web/test/css-hash.test.mjs checks the same vector.
func TestCSSInputHashVector(t *testing.T) {
	vector := filepath.Join(repoRoot, "tools", "web", "test", "testdata", "css-hash")
	want, err := os.ReadFile(filepath.Join(vector, "expected.txt"))
	if err != nil {
		t.Skipf("no shared vector: %v", err)
	}
	files, err := cssInputFiles(vector)
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := []string{
		"internal/web/static/js/fmt.js",
		"internal/web/static/js/pages/home.js",
		"internal/web/styles/app.css",
		"internal/web/styles/sub/b.css",
		"internal/web/templates/layout.html",
		"internal/web/templates/pages/home.html",
		"internal/web/templates/pages/notes.txt",
		"internal/web/templates/partials/logo.html",
		"tools/web/package-lock.json",
	}
	if strings.Join(files, "\n") != strings.Join(wantFiles, "\n") {
		t.Errorf("hashed files:\n%s\nwant:\n%s", strings.Join(files, "\n"), strings.Join(wantFiles, "\n"))
	}
	got, err := cssInputHash(vector)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSpace(string(want)) {
		t.Errorf("input hash = %s, want %s", got, strings.TrimSpace(string(want)))
	}
}

// TestAppCSSFresh catches a template, script, style or token change that was
// committed without rebuilding static/app.css. It checks the Tailwind-built
// set's stylesheet, so it waits for the switch commit (S1) that makes that set
// active; until then web.yml's byte compare (css:check) covers it.
func TestAppCSSFresh(t *testing.T) {
	if !builtByTailwind(activeSet) {
		t.Skipf("the active UI set %q has a hand-written stylesheet; this runs from the switch to the new UI", activeSet.Name)
	}
	b, err := content.ReadFile(path.Join("static", activeSet.Static, "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(b), "\n")
	m := cssHeader.FindStringSubmatch(first)
	if m == nil {
		t.Fatalf("static/app.css does not start with the vaporos-css header (got %.80q): run npm --prefix tools/web run css", first)
	}
	want, err := cssInputHash(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if m[1] != want {
		t.Errorf("static/app.css was built from inputs %s, but they now hash to %s: run npm --prefix tools/web run css and commit static/app.css with its inputs", m[1], want)
	}
	var pkg struct {
		DevDependencies map[string]string `json:"devDependencies"`
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, "tools", "web", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if pin := pkg.DevDependencies["tailwindcss"]; m[2] != pin {
		t.Errorf("static/app.css was built with Tailwind %s, but tools/web pins %s: run npm ci --prefix tools/web, then npm --prefix tools/web run css", m[2], pin)
	}
}

// builtByTailwind reports whether the set's app.css is built from styles/ by
// Tailwind. The legacy set's stylesheet is hand-written and frozen, and the
// class rules below do not apply to it.
func builtByTailwind(s uiSet) bool { return s.Name != legacySet.Name }

// forEachCheckedSet runs fn per UI set, as a subtest named after the set. The
// active set is always checked; an inactive one only with VOS_WEB_STRICT=1,
// because it may be half-built between two commits. A set without pages is
// skipped, and so is a hand-written one when tailwindOnly is set.
func forEachCheckedSet(t *testing.T, tailwindOnly bool, fn func(t *testing.T, set uiSet)) {
	t.Helper()
	for _, set := range uiSets {
		t.Run(set.Name, func(t *testing.T) {
			switch {
			case len(set.Pages) == 0:
				t.Skipf("UI set %q has no pages yet", set.Name)
			case tailwindOnly && !builtByTailwind(set):
				t.Skipf("UI set %q has a hand-written stylesheet", set.Name)
			case set.Name != activeSet.Name && !strictWeb():
				t.Skipf("UI set %q is not active; VOS_WEB_STRICT=1 checks it", set.Name)
			}
			fn(t, set)
		})
	}
}

// ---- scanners: just enough JavaScript and CSS lexing for the class rules

// jsBlank returns src with comments replaced by spaces and, unless
// keepStrings, the contents of string, template and regex literals blanked
// too. Delimiters and newlines stay, so offsets and line numbers still match
// src, and code inside a template's ${…} stays code. It is a lexer, not a
// parser: a regex literal after a keyword such as return reads as division.
func jsBlank(src string, keepStrings bool) string {
	b := []byte(src)
	blank := func(from, to int) {
		for k := from; k < to && k < len(b); k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
	}
	lit := func(from, to int) {
		if !keepStrings {
			blank(from, to)
		}
	}
	var scanCode func(i int, inTemplate bool) int
	scanTemplate := func(i int) int { // b[i] == '`'
		j, start := i+1, i+1
		for j < len(b) {
			switch {
			case b[j] == '\\':
				j += 2
				continue
			case b[j] == '`':
				lit(start, j)
				return j + 1
			case b[j] == '$' && j+1 < len(b) && b[j+1] == '{':
				lit(start, j)
				j = scanCode(j+2, true)
				start = j
				continue
			}
			j++
		}
		lit(start, len(b))
		return len(b)
	}
	scanCode = func(i int, inTemplate bool) int {
		depth, prev := 0, byte(0)
		for i < len(b) {
			c := b[i]
			switch {
			case c == '/' && i+1 < len(b) && b[i+1] == '/':
				j := i
				for j < len(b) && b[j] != '\n' {
					j++
				}
				blank(i, j)
				i = j
				continue
			case c == '/' && i+1 < len(b) && b[i+1] == '*':
				end := len(b)
				if k := strings.Index(src[i+2:], "*/"); k >= 0 {
					end = i + 2 + k + 2
				}
				blank(i, end)
				i = end
				continue
			case c == '\'' || c == '"':
				j := i + 1
				for j < len(b) && b[j] != c && b[j] != '\n' {
					if b[j] == '\\' {
						j++
					}
					j++
				}
				lit(i+1, j)
				i, prev = min(j+1, len(b)), 'a'
				continue
			case c == '`':
				i, prev = scanTemplate(i), 'a'
				continue
			case c == '/' && (prev == 0 || strings.IndexByte("(,=:[!&|?{};+-*%<>~^", prev) >= 0):
				j, class := i+1, false
				for j < len(b) && b[j] != '\n' && (b[j] != '/' || class) {
					switch b[j] {
					case '\\':
						j++
					case '[':
						class = true
					case ']':
						class = false
					}
					j++
				}
				if j < len(b) && b[j] == '/' {
					lit(i+1, j)
					i, prev = j+1, 'a'
					continue
				}
			case c == '{':
				depth++
			case c == '}':
				if inTemplate && depth == 0 {
					return i + 1
				}
				depth--
			}
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				prev = c
			}
			i++
		}
		return i
	}
	scanCode(0, false)
	return string(b)
}

// lineOf is the 1-based line of offset off in s.
func lineOf(s string, off int) int { return strings.Count(s[:off], "\n") + 1 }

// jsQuoted returns the end of the '…', "…" or `…` literal at s[i] and, for a
// template literal, whether it has a ${…} part.
func jsQuoted(s string, i int) (end int, hasExpr bool) {
	q, j := s[i], i+1
	for j < len(s) {
		switch {
		case s[j] == '\\':
			j += 2
			continue
		case s[j] == q:
			return j + 1, hasExpr
		case q != '`' && s[j] == '\n':
			return j, false
		case q == '`' && s[j] == '$' && j+1 < len(s) && s[j+1] == '{':
			hasExpr = true
			j += 2
			for d := 1; j < len(s) && d > 0; {
				switch s[j] {
				case '\'', '"', '`':
					j, _ = jsQuoted(s, j)
					continue
				case '{':
					d++
				case '}':
					d--
				}
				j++
			}
			continue
		}
		j++
	}
	return len(s), hasExpr
}

// jsExpr reads one expression of comment-free source from s[i]: up to a
// ',' or ';' or an unmatched closing bracket. It returns the string literals
// at its top level (never keys or indexes inside brackets), why it builds a
// value at runtime ("" if it does not), and where it stopped.
func jsExpr(s string, i int) (lits []string, dynamic string, end int) {
	depth, start := 0, i
	for i < len(s) && i-start < 2000 {
		c := s[i]
		switch c {
		case '\'', '"', '`':
			j, hasExpr := jsQuoted(s, i)
			if depth == 0 {
				switch {
				case hasExpr:
					dynamic = "a template literal with ${…}"
				case j-1 > i:
					lits = append(lits, s[i+1:j-1])
				}
			}
			i = j
			continue
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return lits, dynamic, i
			}
			depth--
		case ',', ';':
			if depth == 0 {
				return lits, dynamic, i
			}
		case '+':
			if depth == 0 && !(i+1 < len(s) && s[i+1] == '+') && !(i > 0 && s[i-1] == '+') {
				dynamic = "concatenation with +"
			}
		}
		i++
	}
	return lits, dynamic, i
}

// jsClassUse is one place a script sets or tests class names.
type jsClassUse struct {
	line    int
	lits    []string // the class strings it names in full
	dynamic string   // why it builds a class name at runtime, or ""
}

var (
	jsClassContext = regexp.MustCompile(`(?:^|[{,\s(])(?:class|className|'class'|"class")\s*:|\bclassName\s*=|\.classList\.(add|remove|toggle|replace|contains)\(|\bsetAttribute\(\s*['"]class['"]\s*,`)
	jsClassesMark  = regexp.MustCompile(`/\*\s*classes\s*\*/\s*[{\[]`)
)

// jsClassUses finds every class context in a script: an h() or object key
// class:, className =, classList.add/remove/toggle/replace/contains(…),
// setAttribute('class', …), and lookup objects marked /* classes */.
func jsClassUses(src string) []jsClassUse {
	s := jsBlank(src, true)
	var out []jsClassUse
	for _, m := range jsClassContext.FindAllStringSubmatchIndex(s, -1) {
		at := m[1]
		if strings.HasSuffix(s[m[0]:m[1]], "=") && at < len(s) && s[at] == '=' {
			continue // className == …, a comparison
		}
		u := jsClassUse{line: lineOf(s, m[0])}
		method := ""
		if m[2] >= 0 {
			method = s[m[2]:m[3]]
		}
		for n := 0; ; n++ {
			lits, dyn, end := jsExpr(s, at)
			if !((method == "toggle" || method == "contains") && n > 0) {
				u.lits = append(u.lits, lits...)
				if u.dynamic == "" {
					u.dynamic = dyn
				}
			}
			if method == "" || end >= len(s) || s[end] != ',' {
				break
			}
			at = end + 1
		}
		out = append(out, u)
	}
	for _, m := range jsClassesMark.FindAllStringIndex(src, -1) {
		open := m[1] - 1
		u := jsClassUse{line: lineOf(src, m[0])}
		for i, depth := open, 0; i < len(s); i++ {
			switch s[i] {
			case '\'', '"', '`':
				j, _ := jsQuoted(s, i)
				rest := strings.TrimLeft(s[j:], " \t\r\n")
				if !strings.HasPrefix(rest, ":") && j-1 > i {
					u.lits = append(u.lits, s[i+1:j-1])
				}
				i = j - 1
				continue
			case '{', '[', '(':
				depth++
			case '}', ']', ')':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		out = append(out, u)
	}
	return out
}

// cssBlank returns css with comments and the contents of strings blanked,
// keeping offsets.
func cssBlank(css string) string {
	b := []byte(css)
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			end := len(b)
			if k := strings.Index(css[i+2:], "*/"); k >= 0 {
				end = i + 2 + k + 2
			}
			for k := i; k < end; k++ {
				if b[k] != '\n' {
					b[k] = ' '
				}
			}
			i = end - 1
		case b[i] == '"' || b[i] == '\'':
			q, j := b[i], i+1
			for j < len(b) && b[j] != q && b[j] != '\n' {
				if b[j] == '\\' {
					b[j] = ' '
					j++
				}
				if j < len(b) {
					b[j] = ' '
				}
				j++
			}
			i = j
		}
	}
	return string(b)
}

// cssRule is the text before one '{': a selector or an at-rule, and the
// top-level at-rule or selector it sits in ("" when it is top-level).
type cssRule struct{ prelude, outer string }

func cssRules(css string) []cssRule {
	b := cssBlank(css)
	var stack []string
	var out []cssRule
	start := 0
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '{':
			p := strings.TrimSpace(b[start:i])
			outer := ""
			if len(stack) > 0 {
				outer = stack[0]
			}
			out = append(out, cssRule{p, outer})
			stack = append(stack, p)
			start = i + 1
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			start = i + 1
		case ';':
			start = i + 1
		}
	}
	return out
}

var cssClassToken = regexp.MustCompile(`\.((?:\\[0-9a-fA-F]{1,6}[ \t\r\n\f]?|\\[^\r\n\f0-9a-fA-F]|[A-Za-z0-9_-]|[^\x00-\x7F])+)`)

// cssUnescape decodes CSS escapes: \31 0 is "1" then "0", \: is ":".
func cssUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i + 1
		for j < len(s) && j < i+7 && strings.IndexByte("0123456789abcdefABCDEF", s[j]) >= 0 {
			j++
		}
		if j == i+1 {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		var r rune
		for _, h := range s[i+1 : j] {
			r = r*16 + rune(strings.IndexRune("0123456789abcdef", h|0x20))
		}
		b.WriteRune(r)
		if j < len(s) && strings.IndexByte(" \t\r\n\f", s[j]) >= 0 {
			j++
		}
		i = j
	}
	return b.String()
}

// cssClasses returns the class names that the selectors of css define,
// counting only rules whose top-level block has the prelude outer, or every
// rule when outer is "*".
func cssClasses(css, outer string) map[string]bool {
	out := map[string]bool{}
	for _, r := range cssRules(css) {
		if strings.HasPrefix(r.prelude, "@") || (outer != "*" && r.outer != outer) {
			continue
		}
		for _, m := range cssClassToken.FindAllStringSubmatch(r.prelude, -1) {
			out[cssUnescape(m[1])] = true
		}
	}
	return out
}

// ---- the set's sources

// setTemplates returns the set's template sources by embed path: its
// layout, pages and partials.
func setTemplates(t *testing.T, set uiSet) map[string]string {
	t.Helper()
	names := []string{path.Join(set.Templates, "layout.html")}
	pages, _ := fs.Glob(content, path.Join(set.Templates, "pages", "*.html"))
	partials, _ := fs.Glob(content, set.Partials)
	out := map[string]string{}
	for _, name := range append(append(names, pages...), partials...) {
		b, err := content.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

// setScripts returns the set's own JavaScript by path under static/.
func setScripts(t *testing.T, set uiSet) map[string]string {
	t.Helper()
	out := map[string]string{}
	walkStatic(t, func(name string, b []byte) {
		if path.Ext(name) == ".js" && set.ownsStatic(name) {
			out[name] = string(b)
		}
	})
	return out
}

var classAttr = regexp.MustCompile(`\sclass="([^"]*)"`)

// usedClasses maps every class the set's pages, templates and scripts use to
// one place that uses it.
func usedClasses(t *testing.T, set uiSet) map[string]string {
	t.Helper()
	used := map[string]string{}
	add := func(classes, where string) {
		for _, c := range strings.Fields(classes) {
			if _, ok := used[c]; !ok {
				used[c] = where
			}
		}
	}
	for script, body := range renderAll(t, set) {
		for _, m := range classAttr.FindAllStringSubmatch(body, -1) {
			add(html.UnescapeString(m[1]), "the rendered "+script+" page")
		}
	}
	actions := regexp.MustCompile(`\{\{.*?\}\}`)
	for name, src := range setTemplates(t, set) {
		for _, m := range classAttr.FindAllStringSubmatchIndex(src, -1) {
			add(html.UnescapeString(actions.ReplaceAllString(src[m[2]:m[3]], " ")), fmt.Sprintf("%s:%d", name, lineOf(src, m[0])))
		}
	}
	for name, src := range setScripts(t, set) {
		for _, u := range jsClassUses(src) {
			add(strings.Join(u.lits, " "), fmt.Sprintf("static/%s:%d", name, u.line))
		}
	}
	return used
}

// setCSS is the set's built stylesheet.
func setCSS(t *testing.T, set uiSet) string {
	t.Helper()
	b, err := content.ReadFile(path.Join("static", set.Static, "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// unstyled lists classes that may appear without a rule. Keep it empty: a
// class without a rule is a typo or a stale stylesheet.
var unstyled = map[string]bool{}

// R-C1, R-C7, R-C9: every class the markup, a script or Go assigns has a rule
// in the built stylesheet. This catches a class added without rebuilding,
// a typo, and a stock palette name (the tokens reset the palette, so
// bg-red-500 generates nothing).
func TestClassesAreStyled(t *testing.T) {
	forEachCheckedSet(t, true, func(t *testing.T, set uiSet) {
		defined := cssClasses(setCSS(t, set), "*")
		used := usedClasses(t, set)
		names := make([]string, 0, len(used))
		for c := range used {
			names = append(names, c)
		}
		sort.Strings(names)
		for _, c := range names {
			if !defined[c] && !unstyled[c] {
				t.Errorf("class %q (%s) has no rule in static/%s: run npm --prefix tools/web run css, or fix the name",
					c, used[c], path.Join(set.Static, "app.css"))
			}
		}
	})
}

// R-C6: no component class may share its name with a Tailwind utility. The
// utilities layer comes later in the cascade, so the utility would win:
// today's list-item row would become display:list-item.
func TestNoUtilityCollisions(t *testing.T) {
	forEachCheckedSet(t, true, func(t *testing.T, set uiSet) {
		css := setCSS(t, set)
		utilities := cssClasses(css, "@layer utilities")
		var both []string
		for c := range cssClasses(css, "@layer components") {
			if utilities[c] {
				both = append(both, c)
			}
		}
		sort.Strings(both)
		for _, c := range both {
			t.Errorf("%q is both a component class and a Tailwind utility; rename the component (ARCH §2.5 R-C6)", c)
		}
	})
}

// The scanners behind the class rules, on inputs small enough to read.
func TestClassScanners(t *testing.T) {
	js := strings.Join([]string{
		`// class: 'in-a-comment'`,
		`const SIZE = /* classes */ { sm: 'btn btn-sm', 'md': "btn" };`,
		`h('span', { class: 'badge', dataset: { tone } }, text);`,
		"h('p', { class: open ? 'sheet is-open' : `sheet`, text: 'not a class' });",
		`el.classList.add('rail:flex', "p-4"); el.classList.toggle('open', 'x' === y);`,
		`el.className = SIZE[size]; if (el.className == 'nope') {}`,
		"el.setAttribute('class', `badge badge-${tone}`);",
		`h('i', { class: 'icon ' + name });`,
		`const re = /class: 'regex'/g;`,
	}, "\n")
	uses := jsClassUses(js)
	var lits, dyn []string
	for _, u := range uses {
		lits = append(lits, u.lits...)
		if u.dynamic != "" {
			dyn = append(dyn, fmt.Sprintf("%d:%s", u.line, u.dynamic))
		}
	}
	sort.Strings(lits)
	if got, want := strings.Join(lits, "|"), "btn|btn btn-sm|icon |open|p-4|rail:flex|sheet|sheet is-open|badge"; got != sortedJoin(want) {
		t.Errorf("class literals = %s\nwant %s", got, sortedJoin(want))
	}
	if got, want := strings.Join(dyn, "|"), "7:a template literal with ${…}|8:concatenation with +"; got != want {
		t.Errorf("dynamic class names = %s, want %s", got, want)
	}

	blank := jsBlank("a = 'x.toSorted('; /* .toSorted( */ b = `t${c.toSorted()}`; // .toSorted(\nd = /[/]x/.test(e)", false)
	if n := strings.Count(blank, ".toSorted("); n != 1 {
		t.Errorf("jsBlank left %d .toSorted( outside the ${…} code:\n%s", n, blank)
	}
	if len(blank) != len("a = 'x.toSorted('; /* .toSorted( */ b = `t${c.toSorted()}`; // .toSorted(\nd = /[/]x/.test(e)") {
		t.Error("jsBlank changed the length")
	}

	css := `/*! .comment */@layer theme{:root{--x:1}}@layer components{.btn{color:red}.btn:hover{content:".dot"}.\31 0x{}}` +
		`@layer utilities{.p-4{padding:1rem}.rail\:grid{@media (width>=56.25rem){display:grid}}.btn{}` +
		`.group-hover\:x:is(:where(.group):hover *){}}.plain{background:url(fonts/ui.woff2)}`
	if got := keys(cssClasses(css, "*")); got != "10x btn group group-hover:x p-4 plain rail:grid" {
		t.Errorf("defined classes = %s", got)
	}
	if got := keys(cssClasses(css, "@layer components")); got != "10x btn" {
		t.Errorf("component classes = %s", got)
	}
	if got := keys(cssClasses(css, "@layer utilities")); got != "btn group group-hover:x p-4 rail:grid" {
		t.Errorf("utility classes = %s", got)
	}
}

func keys(m map[string]bool) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func sortedJoin(s string) string {
	parts := strings.Split(s, "|")
	sort.Strings(parts)
	return strings.Join(parts, "|")
}
