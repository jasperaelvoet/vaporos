package web

// The control center's stylesheet is built by Tailwind (tools/web/css.mjs)
// and committed as static/app.css. These tests keep it honest without Node:
// the input hash in its first line must match the files it was built from,
// and the class names in the markup and scripts must follow ARCH §2.5.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
