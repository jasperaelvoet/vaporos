package web

// Exports of the control center for the website. TestExportStrings writes
// the words each UI set shows, for the website's label check; TestExportDemo
// writes the live demo: the active pages with the demo's runtime in front
// of their scripts.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net"
	"os"
	"path"
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

// ---- the live demo (MASTER-PLAN §3.6 D1, spec-website §8)

// demoBase is where the website serves the demo: its pages, their static
// files and the demo's scripts all sit under it.
const demoBase = "/vaporos/demo/ui"

// demoStart is the preset a fresh demo shows (runtime.js START).
const demoStart = "idle"

// demoGroups are the fixture directories fixtures.js carries, as the
// engine reads them (createEngine's fixtures: {base, presets, scripts}).
var demoGroups = []string{"base", "presets", "scripts"}

// demoExport is the live demo as files, keyed by their slash path under
// the export directory.
type demoExport struct {
	Base     string
	Hash     string
	Files    map[string][]byte
	Pages    map[string]string // page path ("/system/updates") → its file
	Manifest demoManifest
}

// demoManifest is demo-manifest.json, what the website reads at build time:
// hash goes on the iframe's URL (?v=) and on the demo's scripts, and is the
// manifest the demo's ready message names; scenarios are the scripts the
// parent may run (spec-website §8.5), presets the states it may load.
type demoManifest struct {
	Generator int               `json:"generator"`
	Base      string            `json:"base"`
	Hash      string            `json:"hash"`
	Assets    string            `json:"assets"`
	Version   string            `json:"version"`
	Hostname  string            `json:"hostname"`
	Start     string            `json:"start"`
	Scenarios []string          `json:"scenarios"`
	Presets   []string          `json:"presets"`
	Pages     []string          `json:"pages"`
	OldURLs   map[string]string `json:"old_urls"`
	Files     map[string]string `json:"files"`
}

// demoFixtures reads fixtures/{base,presets,scripts} into fixtures.js, an
// ES module whose default export is what the engine takes. It returns the
// module, the script names (the scenarios), the presets the demo can show
// (runtime.js demoPreset: the installed system, signed in) and the base
// system document's hostname and version for the page shells.
func demoFixtures(t *testing.T) (js []byte, scripts, presets []string, host, version string) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("// fixtures.js: internal/web/fixtures (base, presets, scripts) for the live\n" +
		"// demo's engine. Written by TestExportDemo (internal/web/export_test.go).\n" +
		"export default {")
	for gi, group := range demoGroups {
		files, err := filepath.Glob(filepath.Join(fixturesDir, group, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no fixtures in %s/%s: %v", fixturesDir, group, err)
		}
		sort.Strings(files)
		if gi > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "\n%q: {", group)
		for i, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var doc bytes.Buffer
			if err := json.Compact(&doc, raw); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			name := strings.TrimSuffix(filepath.Base(f), ".json")
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "\n%q: %s", name, doc.Bytes())
			switch group {
			case "scripts":
				scripts = append(scripts, name)
			case "presets":
				var p struct{ Mode, Auth string }
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatalf("%s: %v", f, err)
				}
				if p.Mode != "installer" && p.Auth != "first-run" && p.Auth != "signed-out" {
					presets = append(presets, name)
				}
			case "base":
				if name == "system" {
					var sys struct{ Hostname, Version string }
					if err := json.Unmarshal(raw, &sys); err != nil {
						t.Fatalf("%s: %v", f, err)
					}
					host, version = sys.Hostname, sys.Version
				}
			}
		}
		b.WriteString("\n}")
	}
	b.WriteString("\n};\n")
	if host == "" || version == "" {
		t.Fatalf("%s/base/system.json names no hostname or version for the demo's pages", fixturesDir)
	}
	if !slicesContain(presets, demoStart) {
		t.Fatalf("the demo starts from preset %q, which is not one it can show", demoStart)
	}
	return b.Bytes(), scripts, presets, host, version
}

func slicesContain(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// demoCSP is the policy vosd sends with every page, minus frame-ancestors,
// which a <meta> cannot carry (the website's iframe is the demo's frame).
// The demo's pages carry it as a <meta>, so the browser enforces the box's
// policy on the website too.
func demoCSP(t *testing.T) string {
	t.Helper()
	_, h := newHandler(t, nextSet, false, "")
	csp := get(h, "/login").Header().Get("Content-Security-Policy")
	var keep []string
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		name, _, _ := strings.Cut(d, " ")
		switch name {
		case "":
		case "frame-ancestors":
		case "sandbox", "report-uri", "report-to":
			t.Fatalf("vosd's policy has %s, which a <meta> policy cannot carry: teach TestExportDemo what the demo does instead", name)
		default:
			keep = append(keep, d)
		}
	}
	out := strings.Join(keep, "; ")
	if out == "" || strings.ContainsAny(out, "\"<>&") {
		t.Fatalf("vosd's policy %q does not fit a <meta>", csp)
	}
	return out
}

// demoRefresh is an old URL's page in the demo: vosd answers 303, a static
// site a meta refresh (the query is not kept) and a link.
func demoRefresh(csp, to string, p page) []byte {
	return []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="` + csp + `">
<meta name="robots" content="noindex">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<meta http-equiv="refresh" content="0; url=` + html.EscapeString(to) + `">
<title>` + html.EscapeString(p.DocTitle()) + `</title>
</head>
<body>
<p>This page is now <a href="` + html.EscapeString(to) + `">` + html.EscapeString(p.Title) + `</a>.</p>
</body>
</html>
`)
}

// demoHead is what goes in front of a page's first <meta> after the
// charset: the box's policy and noindex (the demo is not the product page).
func demoHead(csp string) string {
	return `<meta http-equiv="Content-Security-Policy" content="` + csp + "\">\n<meta name=\"robots\" content=\"noindex\">\n"
}

// demoScripts is what goes in front of the page's script: the engine and
// its fixtures preloaded beside the page's modules, and the runtime, which
// runs first (module scripts run in document order).
func demoScripts(base, hash string) string {
	v := "?v=" + hash
	return `<link rel="modulepreload" href="` + base + `/demo/engine.js` + v + "\">\n" +
		`<link rel="modulepreload" href="` + base + `/demo/fixtures.js` + v + "\">\n" +
		`<script type="module" src="` + base + `/demo/runtime.js` + v + "\"></script>\n"
}

// buildDemo renders the active set's pages for base, as vosd would serve
// them there, and gathers everything else the demo needs.
func buildDemo(t *testing.T, base string) *demoExport {
	t.Helper()
	set := nextSet // the runtime drives the next UI's pages (core/api.js vosTransport)
	u := testUI(t, set)
	u.base = base
	fixturesJS, scripts, presets, host, version := demoFixtures(t)
	csp := demoCSP(t)
	d := &demoExport{Base: base, Files: map[string][]byte{}, Pages: map[string]string{}}
	put := func(name string, b []byte) {
		if !fs.ValidPath(name) || path.Clean(name) != name {
			t.Fatalf("export path %q is not clean", name)
		}
		if _, dup := d.Files[name]; dup {
			t.Fatalf("two files at %s in the export", name)
		}
		d.Files[name] = b
	}

	// Static files, at the versioned prefix the pages name. The manifest's
	// app is the demo: "/" would be the website's host.
	for name, a := range u.assets.files {
		body := a.body
		if name == "manifest.webmanifest" {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatalf("static/%s: %v", name, err)
			}
			for _, k := range []string{"id", "start_url", "scope"} {
				if _, ok := m[k]; ok {
					m[k] = base + "/"
				}
			}
			b, err := json.MarshalIndent(m, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			body = append(b, '\n')
		}
		put(path.Join("static", u.assets.version, name), body)
	}
	for _, name := range []string{"runtime.js", "engine.js"} {
		b, err := os.ReadFile(filepath.Join("demo", name))
		if err != nil {
			t.Fatal(err)
		}
		put("demo/"+name, b)
	}
	put("demo/fixtures.js", fixturesJS)

	// Pages, rendered with the demo's data, then the old URLs.
	type rendered struct {
		p    page
		body string
	}
	var pages []rendered
	byPath := map[string]page{}
	for _, p := range set.Pages {
		pd := u.data(p)
		pd.Static = base + pd.Static
		pd.Shared = base + pd.Shared
		pd.Hostname, pd.Version = host, version
		pd.Demo = true
		var buf bytes.Buffer
		if err := u.tmpl[p.Name].ExecuteTemplate(&buf, "layout", pd); err != nil {
			t.Fatalf("render %s: %v", p.Name, err)
		}
		pages = append(pages, rendered{p, buf.String()})
		byPath[p.Path] = p
	}
	oldURLs := map[string]string{}
	for _, o := range set.OldURLs {
		to, _, _ := strings.Cut(o.To, "#")
		target, ok := byPath[to]
		if !ok {
			t.Fatalf("old URL %s goes to %s, which is no page", o.From, o.To)
		}
		oldURLs[o.From] = o.To
		b := demoRefresh(csp, base+o.To, target)
		rel := strings.TrimPrefix(o.From, "/")
		put(rel+".html", b)
		put(rel+"/index.html", b)
	}

	// The hash covers every input of the export: the pages as rendered, the
	// static files, the demo's scripts and the policy. The pages then name
	// it on the demo's scripts, so a new export is never mixed with a cached
	// runtime, and the runtime keeps each export's state apart.
	h := sha256.New()
	fmt.Fprintf(h, "demo\x00%s\x00%s\x00", base, csp)
	var names []string
	for name := range d.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sum := sha256.Sum256(d.Files[name])
		fmt.Fprintf(h, "%s\x00%x\x00", name, sum)
	}
	for _, r := range pages {
		fmt.Fprintf(h, "%s\x00%s\x00", r.p.Path, r.body)
	}
	d.Hash = hex.EncodeToString(h.Sum(nil))[:12]

	charset := "<meta charset=\"utf-8\">\n"
	for _, r := range pages {
		script := fmt.Sprintf(`<script type="module" src="%s%s/js/pages/%s.js"></script>`, base, u.static(), r.p.Script)
		if strings.Count(r.body, charset) != 1 || strings.Count(r.body, script) != 1 {
			t.Fatalf("%s: the layout no longer has one %q and one %q: update TestExportDemo's hooks", r.p.Name, strings.TrimSpace(charset), script)
		}
		body := strings.Replace(r.body, charset, charset+demoHead(csp), 1)
		body = strings.Replace(body, script, demoScripts(base, d.Hash)+script, 1)
		if r.p.Path == "/" {
			put("index.html", []byte(body))
			d.Pages["/"] = "index.html"
			continue
		}
		rel := strings.TrimPrefix(r.p.Path, "/")
		put(rel+".html", []byte(body))
		put(rel+"/index.html", []byte(body))
		d.Pages[r.p.Path] = rel + ".html"
	}

	m := demoManifest{
		Generator: 1, Base: base, Hash: d.Hash, Assets: u.assets.version, Version: version, Hostname: host,
		Start: demoStart, Scenarios: scripts, Presets: presets, OldURLs: oldURLs, Files: map[string]string{},
	}
	for p := range d.Pages {
		m.Pages = append(m.Pages, p)
	}
	sort.Strings(m.Pages)
	for name, b := range d.Files {
		sum := sha256.Sum256(b)
		m.Files[name] = hex.EncodeToString(sum[:])
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		t.Fatal(err)
	}
	put("demo-manifest.json", buf.Bytes())
	d.Manifest = m
	return d
}

var (
	demoRef        = regexp.MustCompile(`<(\w+)\b[^>]*?\s(href|src)="([^"]*)"`)
	demoPageScript = regexp.MustCompile(`<script type="module" src="[^"]*/js/pages/`)
	demoIPv4       = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	demoMAC        = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}:){5}[0-9a-f]{2}\b`)
)

// demoTextFile reports whether name is text a privacy scan reads.
func demoTextFile(name string) bool {
	switch path.Ext(name) {
	case ".html", ".js", ".css", ".json", ".webmanifest", ".svg", ".txt":
		return true
	}
	return false
}

// publicIPv4 reports whether s is an IPv4 address that is not private,
// loopback, link-local, broadcast or for documentation.
func publicIPv4(s string) bool {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return false // a version number, not an address
	}
	doc := []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "100.64.0.0/10", "0.0.0.0/8"}
	for _, c := range doc {
		if _, n, _ := net.ParseCIDR(c); n.Contains(ip) {
			return false
		}
	}
	return !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Equal(net.IPv4bcast))
}

// checkDemo holds the export to its contract: each page has the box's
// policy before anything it loads, noindex and the runtime before the page
// script; every link stays under the base and names a file of the export
// (the log's download is the runtime's); and nothing names a real place or
// person (spec-website §8.3: generic names, private addresses, locally
// administered MACs, not the machine that ran the export).
func checkDemo(t *testing.T, d *demoExport) {
	t.Helper()
	base := d.Base
	resolves := func(p string) bool {
		rel := strings.TrimPrefix(strings.TrimPrefix(p, base), "/")
		if rel == "" {
			rel = "index.html"
		}
		for _, c := range []string{rel, strings.TrimSuffix(rel, "/") + ".html", path.Join(rel, "index.html")} {
			if _, ok := d.Files[c]; ok {
				return true
			}
		}
		return false
	}
	var names []string
	for name := range d.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if path.Ext(name) != ".html" {
			continue
		}
		body := string(d.Files[name])
		head, _, ok := strings.Cut(body, "</head>")
		if !ok {
			t.Errorf("%s: no </head>", name)
			continue
		}
		cspAt := strings.Index(head, `<meta http-equiv="Content-Security-Policy"`)
		first := len(head)
		for _, tag := range []string{"<script", "<link", "<style"} {
			if i := strings.Index(head, tag); i >= 0 && i < first {
				first = i
			}
		}
		if cspAt < 0 || cspAt > first || strings.Count(body, "Content-Security-Policy") != 1 {
			t.Errorf("%s: the policy must be one <meta> before anything the page loads", name)
		}
		if !strings.Contains(head, `<meta name="robots" content="noindex">`) {
			t.Errorf("%s: no noindex", name)
		}
		if strings.Contains(body, `http-equiv="refresh"`) {
			if strings.Contains(body, "<script") {
				t.Errorf("%s: an old URL's page runs no script", name)
			}
		} else {
			runtime := strings.Index(body, `<script type="module" src="`+base+`/demo/runtime.js?v=`+d.Hash+`"`)
			page := demoPageScript.FindStringIndex(body)
			if page == nil {
				page = []int{-1}
			}
			if runtime < 0 || page[0] < 0 || runtime > page[0] || strings.Count(body, "/demo/runtime.js") != 1 {
				t.Errorf("%s: the runtime must load once, before the page's script", name)
			}
		}
		for _, m := range demoRef.FindAllStringSubmatch(body, -1) {
			tag, attr, ref := strings.ToLower(m[1]), m[2], html.UnescapeString(m[3])
			switch {
			case ref == "" || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "data:"):
			case strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://"):
				if tag != "a" {
					t.Errorf("%s: <%s %s=%q> loads from another site", name, tag, attr, ref)
				}
			case !strings.HasPrefix(ref, base+"/"):
				t.Errorf("%s: <%s %s=%q> leaves the demo's base %s", name, tag, attr, ref, base)
			default:
				p := ref
				if i := strings.IndexAny(p, "?#"); i >= 0 {
					p = p[:i]
				}
				if strings.HasPrefix(p, base+"/api/v1/") {
					if tag != "a" {
						t.Errorf("%s: <%s %s=%q> would load the API from the website", name, tag, attr, ref)
					}
					continue // the runtime answers these clicks (the log's download)
				}
				if !resolves(p) {
					t.Errorf("%s: <%s %s=%q> is not in the export", name, tag, attr, ref)
				}
			}
		}
	}
	for _, s := range d.Manifest.Scenarios {
		if !slicesContain(appendixB["scripts"], s) {
			t.Errorf("scenario %q is not a script of MASTER-PLAN Appendix B", s)
		}
	}

	// Privacy. The hostname of the machine that ran the export stands in
	// for whatever of it a page might pick up (config.Hostname falls back
	// to it); the fixtures' own hostname is generic.
	var private []string
	if h, err := os.Hostname(); err == nil {
		if short, _, _ := strings.Cut(strings.ToLower(h), "."); len(short) >= 4 && short != "localhost" && short != d.Manifest.Hostname {
			private = append(private, short)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		private = append(private, strings.ToLower(home))
	}
	for _, name := range names {
		if !demoTextFile(name) {
			continue
		}
		text := string(d.Files[name])
		lower := strings.ToLower(text)
		for _, s := range private {
			if strings.Contains(lower, s) {
				t.Errorf("%s names %q, from the machine that ran the export", name, s)
			}
		}
		for _, ip := range demoIPv4.FindAllString(text, -1) {
			if publicIPv4(ip) {
				t.Errorf("%s has the public address %s; the demo uses private ones", name, ip)
			}
		}
		for _, mac := range demoMAC.FindAllString(text, -1) {
			b, _ := strconv.ParseUint(mac[:2], 16, 8)
			if b&2 == 0 && !strings.EqualFold(mac, "ff:ff:ff:ff:ff:ff") && mac != "00:00:00:00:00:00" {
				t.Errorf("%s has the MAC %s, which is not locally administered", name, mac)
			}
		}
	}
}

// writeDemo writes the export into dir and deletes what an earlier export
// left there. It writes only into an empty directory or an earlier export.
func writeDemo(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		if _, err := os.Stat(filepath.Join(dir, "demo-manifest.json")); err != nil {
			t.Fatalf("%s is not empty and holds no demo-manifest.json: not writing a demo over it", dir)
		}
	}
	for name, b := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if old, err := os.ReadFile(full); err == nil && bytes.Equal(old, b) {
			continue
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if p != dir {
				dirs = append(dirs, p)
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if _, ok := files[filepath.ToSlash(rel)]; !ok {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // only empty ones go
	}
}

// TestExportDemo writes the website's live demo (MASTER-PLAN §3.6 D1,
// spec-website §8): the active set's pages rendered for /vaporos/demo/ui,
// each at x.html and x/index.html (GitHub Pages serves either), with the
// box's policy as a <meta>, noindex, and demo/runtime.js before the page's
// script; the old URLs as meta refreshes; the static files; the demo's
// scripts (runtime.js, engine.js and fixtures.js from fixtures/); and
// demo-manifest.json:
//
//	VOS_WEB_EXPORT="$PWD/website/public/demo/ui" go test ./internal/web -run TestExportDemo
//
// pages.yml runs it when the website's demo is on; nothing it writes is
// committed. The path must be absolute: go test runs in internal/web.
// Without the variable it builds the export in memory and checks it.
func TestExportDemo(t *testing.T) {
	dir := os.Getenv("VOS_WEB_EXPORT")
	if dir != "" && !filepath.IsAbs(dir) {
		t.Fatalf("VOS_WEB_EXPORT=%q must be an absolute path: go test runs in internal/web", dir)
	}
	d := buildDemo(t, demoBase)
	checkDemo(t, d)
	if dir == "" || t.Failed() {
		return
	}
	writeDemo(t, dir, d.Files)
	t.Logf("wrote the demo (%d files, hash %s) to %s", len(d.Files), d.Hash, dir)
}
