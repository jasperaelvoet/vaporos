package web

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	_ "time/tzdata" // validate the timezone list against a known database

	"github.com/jasperaelvoet/vaporos/internal/api"
)

// forEachSet runs fn once per UI set, as a subtest named after the set. A set
// without pages (the new UI before its first page lands) is skipped.
func forEachSet(t *testing.T, fn func(t *testing.T, set uiSet)) {
	t.Helper()
	for _, set := range uiSets {
		t.Run(set.Name, func(t *testing.T) {
			if len(set.Pages) == 0 {
				t.Skipf("UI set %q has no pages yet", set.Name)
			}
			fn(t, set)
		})
	}
}

// newHandler serves set the way Register serves the active one.
func newHandler(t *testing.T, set uiSet, installer bool, code string) (*api.Server, http.Handler) {
	t.Helper()
	srv := api.New(api.Options{Installer: installer})
	srv.SetSetupCode(code)
	register(srv, set, content)
	return srv, srv.Handler()
}

// get performs a request the way a browser on the LAN would reach vosd, so
// the tests keep passing behind the API's Host and source-IP checks.
func get(h http.Handler, target string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "localhost"
	req.RemoteAddr = "127.0.0.1:40000"
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func testUI(t *testing.T, set uiSet) *ui {
	t.Helper()
	u, err := newUI(api.New(api.Options{}), set, content)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Each page is its own subtest, named <page>/<set>, so
// -run 'TestPagesRender/devices' checks one page in whichever set has it.
func TestPagesRender(t *testing.T) {
	for _, set := range uiSets {
		if len(set.Pages) == 0 {
			t.Run(set.Name, func(t *testing.T) { t.Skipf("UI set %q has no pages yet", set.Name) })
			continue
		}
		_, h := newHandler(t, set, false, "")
		u := testUI(t, set)
		for i, p := range set.Pages {
			t.Run(p.Name+"/"+set.Name, func(t *testing.T) {
				rec := get(h, p.Path)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s = %d", p.Path, rec.Code)
				}
				if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
					t.Errorf("Content-Type = %q", ct)
				}
				if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
					t.Errorf("Cache-Control = %q", cc)
				}
				body := rec.Body.String()
				title := p.Title + " · VaporOS"
				if set.Current {
					title = p.DocTitle()
				}
				icon := "home"
				if set.Current {
					icon = "vapor"
				}
				for _, want := range []string{
					"<title>" + html.EscapeString(title) + "</title>",
					`data-page="` + p.Name + `"`,
					`<script type="module" src="` + u.static() + `/js/pages/` + p.Script + `.js"></script>`,
					`href="` + u.static() + `/app.css"`,
					`href="` + u.assets.prefix() + `/icon.svg"`,
					`<symbol id="i-` + icon + `"`,
				} {
					if !strings.Contains(body, want) {
						t.Errorf("page does not contain %q", want)
					}
				}
				if p.Bare == strings.Contains(body, `id="nav"`) {
					t.Errorf("bare=%v but navigation presence disagrees", p.Bare)
				}
				if set.Current {
					checkNextFrame(t, set, p, i, body)
				} else if p.Nav && !regexp.MustCompile(`<a href="`+regexp.QuoteMeta(p.Path)+`" aria-current="page">`).MatchString(body) {
					t.Errorf("navigation does not mark %s as current", p.Path)
				}
				// Every asset the page names must actually be served.
				for _, m := range regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`).FindAllStringSubmatch(body, -1) {
					if rec := get(h, m[1]); rec.Code != http.StatusOK {
						t.Errorf("GET %s = %d", m[1], rec.Code)
					}
				}
			})
		}
	}
}

// navLinks returns the <a> tags inside id="nav", as href → aria-current,
// whatever order their attributes come in.
func navLinks(body string) map[string]string {
	start := strings.Index(body, `id="nav"`)
	if start < 0 {
		return nil
	}
	end := strings.Index(body[start:], "</nav>")
	if end < 0 {
		return nil
	}
	out := map[string]string{}
	for _, tag := range regexp.MustCompile(`<a\b[^>]*>`).FindAllString(body[start:start+end], -1) {
		attrs := tagAttrs(tag)
		out[attrs["href"]] = attrs["aria-current"]
	}
	return out
}

// tagAttrs parses one start tag's attributes (values html-unescaped).
func tagAttrs(tag string) map[string]string {
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`\s([a-zA-Z_:][-a-zA-Z0-9_:.]*)(?:\s*=\s*"([^"]*)")?`).FindAllStringSubmatch(tag, -1) {
		out[strings.ToLower(m[1])] = html.UnescapeString(m[2])
	}
	return out
}

// checkNextFrame checks the next set's frame on one page: the four tabs
// with the right one current ("page" on a tab root, "true" on a
// sub-page), data-order, and head.js once, before the module script.
func checkNextFrame(t *testing.T, set uiSet, p page, order int, body string) {
	t.Helper()
	if !strings.Contains(body, `data-order="`+fmt.Sprint(order)+`"`) {
		t.Errorf("<html> lacks data-order=%q", fmt.Sprint(order))
	}
	head := regexp.MustCompile(`<script src="[^"]*/js/head\.js"></script>`).FindAllStringIndex(body, -1)
	module := strings.Index(body, `<script type="module"`)
	if len(head) != 1 || module < 0 || head[0][0] > module {
		t.Errorf("head.js appears %d times, want once before the module script", len(head))
	}
	if p.Bare {
		return
	}
	links := navLinks(body)
	if len(links) != 4 {
		t.Errorf("navigation has %d links, want the 4 tabs: %v", len(links), links)
	}
	want := map[string]string{}
	for _, q := range set.Pages {
		if q.Nav {
			want[q.Path] = ""
			if q.Name == p.Name {
				want[q.Path] = "page"
			} else if q.Name == p.Tab {
				want[q.Path] = "true"
			}
		}
	}
	for href, cur := range want {
		if got, ok := links[href]; !ok || got != cur {
			t.Errorf("tab %s: aria-current=%q, want %q", href, got, cur)
		}
	}
}

func TestUnknownPathIsNotAPage(t *testing.T) {
	forEachSet(t, func(t *testing.T, set uiSet) {
		_, h := newHandler(t, set, false, "")
		paths := []string{"/nope", "/index.html", "/pair/extra"}
		if set.Current {
			paths = append(paths, "/devices/", "/devices/x", "/system/", "/system/x", "/screen/stream", "/home", "/system/updates/x")
		}
		for _, p := range paths {
			if rec := get(h, p); rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", p, rec.Code)
			}
		}
	})
}

// recordingFS remembers every name opened through it. It implements only
// Open, so ReadFile, ReadDir, Glob, Stat and WalkDir all come through here.
type recordingFS struct {
	fsys   fs.FS
	opened []string
}

func (r *recordingFS) Open(name string) (fs.File, error) {
	r.opened = append(r.opened, name)
	return r.fsys.Open(name)
}

// ownsTemplate reports whether name (a path in the embed) is one of the
// set's templates: its layout, pages or partials.
func (s uiSet) ownsTemplate(name string) bool {
	for _, dir := range []string{path.Join(s.Templates, "pages"), path.Dir(s.Partials)} {
		if name == dir || strings.HasPrefix(name, dir+"/") {
			return true
		}
	}
	return name == path.Join(s.Templates, "layout.html")
}

// A half-built new UI must never stop vosd from starting: Register reads the
// active set and the shared files only. Every other set is replaced here by
// files that would fail to parse or load, and none of them may be opened.
func TestRegisterReadsOnlyActiveSet(t *testing.T) {
	files := fstest.MapFS{}
	err := fs.WalkDir(content, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := content.ReadFile(p)
		files[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	broken := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	for _, set := range uiSets {
		if set.Name == activeSet.Name {
			continue
		}
		files[path.Join(set.Templates, "layout.html")] = broken(`{{define "layout"}}{{.Nope`)
		files[path.Join(set.Templates, "pages", "broken.html")] = broken(`{{template "missing"}}`)
		files[path.Join(path.Dir(set.Partials), "broken.html")] = broken(`{{define`)
		files[path.Join("static", set.Static, "app.css")] = broken(`@import url(//example.com/x.css);`)
		files[path.Join("static", set.Static, "js/pages/broken.js.map")] = broken(`{}`) // no content type: newAssetStore would fail
	}
	rec := &recordingFS{fsys: files}
	srv := api.New(api.Options{})
	register(srv, activeSet, rec) // panics if it parsed any of the above

	sawLayout := false
	for _, name := range rec.opened {
		sawLayout = sawLayout || name == path.Join(activeSet.Templates, "layout.html")
		for _, set := range uiSets {
			if set.Name == activeSet.Name {
				continue
			}
			static, isStatic := strings.CutPrefix(name, "static/")
			if set.ownsTemplate(name) || (isStatic && set.ownsStatic(static)) {
				t.Errorf("Register opened %s, which belongs to the inactive set %q", name, set.Name)
			}
		}
	}
	if !sawLayout {
		t.Errorf("Register never opened the active layout; opened %v", rec.opened)
	}
	if rec := get(srv.Handler(), activeSet.Pages[0].Path); rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d", activeSet.Pages[0].Path, rec.Code)
	}
}

func TestInstallerMode(t *testing.T) { forEachSet(t, testInstallerMode) }

func testInstallerMode(t *testing.T, set uiSet) {
	_, h := newHandler(t, set, true, "ABCD-EFGH")

	redirects := map[string]string{
		"/":                    "/setup",
		"/pair":                "/setup",
		"/login?next=/updates": "/setup?next=/updates",
		"/?code=ABCD-EFGH":     "/setup?code=ABCD-EFGH",
	}
	if set.Current {
		redirects["/devices"] = "/setup"
		redirects["/system/updates?x=1"] = "/setup?x=1"
		redirects["/updates"] = "/setup"
	}
	for target, want := range redirects {
		rec := get(h, target)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("GET %s = %d → %q, want 303 → %q", target, rec.Code, rec.Header().Get("Location"), want)
		}
	}

	rec := get(h, "/setup?code=ABCD-EFGH")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup = %d", rec.Code)
	}
	body := rec.Body.String()
	title := set.Installer.Title + " · VaporOS"
	if set.Current {
		title = set.Installer.DocTitle()
	}
	for _, want := range []string{
		`id="wizard"`,
		`/js/pages/` + set.Installer.Script + `.js"`,
		`<title>` + title + `</title>`,
		`value="ABCD-EFGH"`,
		`<option value="Europe/Brussels">Brussels</option>`,
		`<option value="America/Argentina/Buenos_Aires">Buenos Aires</option>`,
		`<optgroup label="Europe">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("installer page does not contain %q", want)
		}
	}
	if strings.Contains(body, `id="first-run"`) {
		t.Error("installer page shows the first-run form")
	}
	// The post-install wait polls http://<hostname>.local, another origin.
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' http://*.local") || !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("installer CSP = %q", csp)
	}
}

func TestFirstRunSetup(t *testing.T) { forEachSet(t, testFirstRunSetup) }

func testFirstRunSetup(t *testing.T, set uiSet) {
	_, h := newHandler(t, set, false, "ABCD-EFGH")
	body := get(h, "/setup?code=WXYZ-2345").Body.String()
	for _, want := range []string{`id="first-run"`, `/js/pages/setup.js"`, `id="setup-code"`, `value="WXYZ-2345"`} {
		if !strings.Contains(body, want) {
			t.Errorf("first-run page does not contain %q", want)
		}
	}
	if strings.Contains(body, `id="wizard"`) {
		t.Error("first-run page shows the installer")
	}

	// A hostile code is escaped, and an absurdly long one is dropped.
	body = get(h, `/setup?code=%22%3E%3Cscript%3Ealert(1)%3C/script%3E`).Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Error("setup code reflected unescaped")
	}
	if body = get(h, "/setup?code="+strings.Repeat("A", 40)).Body.String(); strings.Contains(body, strings.Repeat("A", 40)) {
		t.Error("overlong setup code echoed")
	}

	// Without a setup code the field is not shown at all.
	_, h = newHandler(t, set, false, "")
	if body := get(h, "/setup").Body.String(); strings.Contains(body, `id="setup-code"`) {
		t.Error("setup code field shown although no code is needed")
	}
}

// A page on another site can make a browser load /setup?code=… as often as
// it likes. The code is dropped before it is checked, so wrong ones cannot
// lock the browser out, and it never reaches the form or the page script.
func TestSetupCodeFromAnotherSiteIsDropped(t *testing.T) {
	forEachSet(t, testSetupCodeFromAnotherSiteIsDropped)
}

func testSetupCodeFromAnotherSiteIsDropped(t *testing.T, set uiSet) {
	for _, installer := range []bool{true, false} {
		srv, h := newHandler(t, set, installer, "ABCD-EFGH")
		srv.Handle("GET", "/probe", api.Setup, func(w http.ResponseWriter, r *http.Request) { api.OK(w) })
		foreign := [][]string{
			{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Dest", "image"},
			{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Dest", "document"},
			{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Dest", "document"},
		}
		for i := range 12 {
			rec := get(h, "/setup?code=WRONG-000"+fmt.Sprint(i%10)+"&next=/pair", foreign[i%len(foreign)]...)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?next=%2Fpair" {
				t.Fatalf("installer=%v: foreign ?code= got %d → %q", installer, rec.Code, rec.Header().Get("Location"))
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatal("a foreign ?code= set a cookie")
			}
		}
		if installer {
			// The QR code's /?code= redirect drops it too.
			if rec := get(h, "/?code=ABCD-EFGH", foreign[0]...); rec.Header().Get("Location") != "/setup" {
				t.Fatalf("foreign /?code= → %q", rec.Header().Get("Location"))
			}
		}
		// The browser was not locked out: the right code still works.
		if rec := get(h, "/api/v1/probe", "X-VOS-Setup", "ABCD-EFGH"); rec.Code != http.StatusOK {
			t.Fatalf("installer=%v: locked out by foreign requests: %d", installer, rec.Code)
		}
		// A scanned QR code (Sec-Fetch-Site: none) is honoured.
		rec := get(h, "/setup?code=ABCD-EFGH", "Sec-Fetch-Site", "none", "Sec-Fetch-Dest", "document")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="ABCD-EFGH"`) || len(rec.Result().Cookies()) != 1 {
			t.Fatalf("installer=%v: QR navigation: %d, cookies %v", installer, rec.Code, rec.Result().Cookies())
		}
	}
}

func TestFirstRunIgnoresTheCodeWaiver(t *testing.T) {
	forEachSet(t, func(t *testing.T, set uiSet) {
		srv, h := newHandler(t, set, false, "ABCD-EFGH")
		srv.SetSetupWaiver(func() bool { return true }) // ignored outside installer mode
		if body := get(h, "/setup").Body.String(); !strings.Contains(body, `id="setup-code"`) {
			t.Error("first-run setup must always ask for the code")
		}
	})
}

type fakeCookieSetter struct{ got string }

func (f *fakeCookieSetter) SetSetupCookie(w http.ResponseWriter, r *http.Request, code string) bool {
	f.got = code
	return code == "GOOD"
}

func TestTrySetSetupCookie(t *testing.T) {
	f := &fakeCookieSetter{}
	rec, req := httptest.NewRecorder(), httptest.NewRequest("GET", "/setup?code=GOOD", nil)
	if !trySetSetupCookie(f, rec, req, "GOOD") || f.got != "GOOD" {
		t.Errorf("setter not called with the code (got %q)", f.got)
	}
	if trySetSetupCookie(struct{}{}, rec, req, "GOOD") {
		t.Error("value without the method reported a cookie")
	}
}

func TestStaticContentTypesAndCaching(t *testing.T) {
	forEachSet(t, testStaticContentTypesAndCaching)
}

func testStaticContentTypesAndCaching(t *testing.T, set uiSet) {
	_, h := newHandler(t, set, false, "")
	u := testUI(t, set)
	for name, ctype := range map[string]string{
		path.Join(set.Static, "app.css"):                              "text/css; charset=utf-8",
		path.Join(set.Static, "js/pages", set.Pages[0].Script+".js"):  "text/javascript; charset=utf-8",
		path.Join(set.Static, "js/pages", set.Installer.Script+".js"): "text/javascript; charset=utf-8",
		"icon.svg":             "image/svg+xml",
		"icon-192.png":         "image/png",
		"manifest.webmanifest": "application/manifest+json",
	} {
		rec := get(h, u.assets.prefix()+"/"+name)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", name, rec.Code)
			continue
		}
		hd := rec.Header()
		if hd.Get("Content-Type") != ctype {
			t.Errorf("%s: Content-Type %q, want %q", name, hd.Get("Content-Type"), ctype)
		}
		if hd.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: Cache-Control %q", name, hd.Get("Cache-Control"))
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("ETag") == "" {
			t.Errorf("%s: nosniff/ETag missing: %v", name, hd)
		}
		if !bytes.Equal(rec.Body.Bytes(), u.assets.files[name].body) {
			t.Errorf("%s: body differs from the embedded file", name)
		}
		// Revalidation works for every asset.
		if rec := get(h, u.assets.prefix()+"/"+name, "If-None-Match", hd.Get("ETag")); rec.Code != http.StatusNotModified {
			t.Errorf("%s: If-None-Match = %d, want 304", name, rec.Code)
		}
	}

	// A page from before an update asks for its old version: it gets the
	// current file, but must revalidate.
	rec := get(h, path.Join("/static/0123456789ab", set.Static, "app.css"))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("stale version: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	rec = get(h, "/favicon.ico")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("favicon: %d %v", rec.Code, rec.Header())
	}
}

func TestStaticGzip(t *testing.T) { forEachSet(t, testStaticGzip) }

func testStaticGzip(t *testing.T, set uiSet) {
	_, h := newHandler(t, set, false, "")
	u := testUI(t, set)
	css := path.Join(set.Static, "app.css")
	url := u.assets.prefix() + "/" + css
	rec := get(h, url, "Accept-Encoding", "br, gzip")
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("no gzip: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if !bytes.Equal(plain, u.assets.files[css].body) {
		t.Error("gzip body does not decompress to app.css")
	}
	gzTag := rec.Header().Get("ETag")
	if gzTag == u.assets.files[css].etag {
		t.Error("gzip variant shares the identity ETag")
	}
	if rec := get(h, url, "Accept-Encoding", "gzip;q=0"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzip sent despite q=0")
	}
	if rec := get(h, u.assets.prefix()+"/icon-192.png", "Accept-Encoding", "gzip"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("PNG was gzipped")
	}
}

// A WOFF2 font is served as font/woff2 and never gzipped: it is already
// Brotli-compressed inside.
func TestStaticWOFF2(t *testing.T) {
	s, err := newAssetStore(fstest.MapFS{"fonts/ui.woff2": {Data: bytes.Repeat([]byte("wOF2"), 4096)}},
		func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if a := s.files["fonts/ui.woff2"]; a == nil || a.gz != nil {
		t.Fatalf("fonts/ui.woff2 stored as %+v, want it without a gzip variant", a)
	}
	req := httptest.NewRequest("GET", s.prefix()+"/fonts/ui.woff2", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "font/woff2" || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("woff2: %d %v", rec.Code, rec.Header())
	}
}

func TestStaticNotFound(t *testing.T) { forEachSet(t, testStaticNotFound) }

func testStaticNotFound(t *testing.T, set uiSet) {
	_, h := newHandler(t, set, false, "")
	u := testUI(t, set)
	for _, p := range []string{
		"/static/",
		"/static/app.css", // no version segment
		u.assets.prefix() + "/",
		u.static() + "/js",
		u.static() + "/js/",
		u.assets.prefix() + "/nope.css",
		u.assets.prefix() + "/" + path.Join(set.Templates, "layout.html"),
		u.assets.prefix() + "/templates/layout.html",
	} {
		if rec := get(h, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	// Another set's files are not served.
	walkStatic(t, func(name string, b []byte) {
		if set.servesStatic(name) {
			return
		}
		if rec := get(h, u.assets.prefix()+"/"+name); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s from set %s = %d, want 404", name, set.Name, rec.Code)
		}
	})
	// Names that are not clean paths never reach the file table.
	for _, name := range []string{"../web.go", "js/../app.css", "/app.css", "js//lib.js", ""} {
		rec := httptest.NewRecorder()
		u.assets.serveFile(rec, httptest.NewRequest("GET", "/", nil), name, true)
		if rec.Code != http.StatusNotFound {
			t.Errorf("serveFile(%q) = %d, want 404", name, rec.Code)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"":                      false,
		"gzip":                  true,
		"GZIP":                  true,
		"deflate, gzip;q=1.0":   true,
		"gzip;q=0.5":            true,
		"gzip;q=0":              false,
		"gzip; q=0.000":         false,
		"br":                    false,
		"x-gzip":                false,
		"identity, gzip ; q=.1": true,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

// Static budgets. The appliance is mostly used from a phone, often over
// Wi-Fi. Raising one is a design decision: change it in the same commit and
// say why in its message.
const (
	budgetLegacyRaw = 136_208 // static/legacy/**: 135,208 bytes when it was set aside, plus 1 kB; frozen
	budgetSharedRaw = 45_000  // the files at the root of static/ that every set shares (icons, manifest)
	budgetIconRaw   = 12_000  // each PNG icon at the root of static/
	budgetTotalRaw  = 700_000 // every embedded static file of every set (fonts included), until the legacy UI is deleted
	budgetNextRaw   = 460_000 // the next set's own files (static/app.css, static/pages/, static/js/)

	// The next set (ARCH §3.3): what one page needs before it can paint and
	// work, as served. Re-baselined when the page stylesheets and the web
	// fonts' @font-face rules were wired in and the HTML began to be counted
	// as render() serves it (compressed once, cached per ETag), and the
	// import() calls a page makes as it starts began to count as critical:
	// the old numbers only passed because none of that was measured.
	budgetPageGz     = 58_000 // HTML + app.css + the page's stylesheets + head.js + the modules it loads at start, gzipped
	budgetBarePageGz = 40_000 // sign-in and first-run setup
	budgetInstallGz  = 52_000 // the installer wizard (its steps and the time zone list are in its HTML)
	budgetCSSGz      = 14_000 // app.css as served: the shell, the tokens and the font faces
	budgetPageCSSGz  = 4_000  // each static/pages/<page>.css as served
	budgetModuleRaw  = 24_000 // any single module, before gzip
	budgetLazyGz     = 30_000 // a page's on-demand import() modules together
)

func TestAssetBudget(t *testing.T) {
	var legacy, next, shared, total int
	walkStatic(t, func(name string, b []byte) {
		total += len(b)
		switch {
		case legacySet.ownsStatic(name):
			legacy += len(b)
		case nextSet.ownsStatic(name):
			next += len(b)
		case !strings.Contains(name, "/"):
			shared += len(b)
			if path.Ext(name) == ".png" && len(b) > budgetIconRaw {
				t.Errorf("%s is %d bytes, budget is %d", name, len(b), budgetIconRaw)
			}
		}
	})
	var wire int
	for _, a := range testUI(t, activeSet).assets.files {
		wire += a.wire()
	}
	t.Logf("static: legacy %d, next %d, shared %d, total %d bytes; the active set serves %d bytes gzipped", legacy, next, shared, total, wire)
	for _, b := range []struct {
		what        string
		size, limit int
	}{
		{"static/legacy", legacy, budgetLegacyRaw},
		{"the next set's own files", next, budgetNextRaw},
		{"the shared root files", shared, budgetSharedRaw},
		{"all static files", total, budgetTotalRaw},
	} {
		if b.size > b.limit {
			t.Errorf("%s: %d bytes, budget is %d", b.what, b.size, b.limit)
		}
	}
}

// wire is what the asset costs on the network: its gzip variant when it has one.
func (a *asset) wire() int {
	if a.gz != nil {
		return len(a.gz)
	}
	return len(a.body)
}

// The next set's budgets per page (ARCH §3.3), with a table in the log.
// The HTML is measured as render() serves it (gzipPage); a page's own
// stylesheets and every module it loads as it starts count, the modules it
// imports on demand are its lazy total.
func TestPageBudgets(t *testing.T) {
	set := nextSet
	u := testUI(t, set)
	css := u.assets.files[path.Join(set.Static, "app.css")]
	head := u.assets.files[path.Join(set.Static, "js/head.js")]
	if css.wire() > budgetCSSGz {
		t.Errorf("app.css: %d B gzipped, budget is %d", css.wire(), budgetCSSGz)
	}
	for name, a := range u.assets.files {
		if set.ownsStatic(name) && path.Ext(name) == ".js" && len(a.body) > budgetModuleRaw {
			t.Errorf("%s: %d B, budget is %d for one module", name, len(a.body), budgetModuleRaw)
		}
		if set.ownsStatic(name) && strings.HasPrefix(name, path.Join(set.Static, "pages")+"/") && a.wire() > budgetPageCSSGz {
			t.Errorf("%s: %d B gzipped, budget is %d for one page stylesheet", name, a.wire(), budgetPageCSSGz)
		}
	}
	kinds := map[string]page{set.Installer.Script: set.Installer}
	for _, p := range set.Pages {
		kinds[p.Script] = p
	}
	pages := renderAll(t, set)
	names := make([]string, 0, len(pages))
	for n := range pages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, script := range names {
		p := kinds[script]
		static, lazy, err := importGraph(u.assets.files, path.Join(set.Static, "js/pages", script+".js"))
		if err != nil {
			t.Fatal(err)
		}
		htmlGz := len(gzipPage([]byte(pages[script])))
		pageCSS := 0
		for _, rel := range u.styles[script] {
			pageCSS += u.assets.files[path.Join(set.Static, rel)].wire()
		}
		js, lazyGz := 0, 0
		var parts []string
		for _, m := range static {
			js += u.assets.files[m].wire()
			parts = append(parts, fmt.Sprintf("%s %d", strings.TrimPrefix(m, "js/"), u.assets.files[m].wire()))
		}
		for _, m := range lazy {
			lazyGz += u.assets.files[m].wire()
		}
		total := htmlGz + css.wire() + pageCSS + head.wire() + js
		limit := budgetPageGz
		switch {
		case script == set.Installer.Script:
			limit = budgetInstallGz
		case p.Bare:
			limit = budgetBarePageGz
		}
		t.Logf("%-8s critical %6d B (html %d + css %d + page css %d + head %d + js %d), lazy %d", script, total, htmlGz, css.wire(), pageCSS, head.wire(), js, lazyGz)
		if total > limit {
			t.Errorf("%s: critical %d B > %d B (html %d + css %d + page css %d + head %d + js %d: %s)", script, total, limit, htmlGz, css.wire(), pageCSS, head.wire(), js, strings.Join(parts, ", "))
		}
		if lazyGz > budgetLazyGz {
			t.Errorf("%s: its import() modules are %d B gzipped, budget is %d", script, lazyGz, budgetLazyGz)
		}
	}
}

// renderAll returns every page of set as HTML in both modes, keyed by script name.
func renderAll(t *testing.T, set uiSet) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, installer := range []bool{false, true} {
		_, h := newHandler(t, set, installer, "ABCD-EFGH")
		for _, p := range set.Pages {
			if installer && p.Name != "setup" {
				continue
			}
			rec := get(h, p.Path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s (installer=%v) = %d", p.Path, installer, rec.Code)
			}
			script := p.Script
			if installer {
				script = set.Installer.Script
			}
			out[script] = rec.Body.String()
		}
	}
	return out
}

// The API's CSP (default-src 'self') blocks inline script, inline event
// handlers and style attributes; any of them would silently break a page.
func TestCSPCompliance(t *testing.T) { forEachSet(t, testCSPCompliance) }

func testCSPCompliance(t *testing.T, set uiSet) {
	inlineScript := regexp.MustCompile(`<script(?:\s[^>]*)?>`)
	forbiddenAttr := regexp.MustCompile(`\s(style|on[a-z]+)\s*=`)
	for script, body := range renderAll(t, set) {
		for _, tag := range inlineScript.FindAllString(body, -1) {
			if !strings.Contains(tag, " src=") {
				t.Errorf("%s: inline script %s", script, tag)
			}
		}
		if m := forbiddenAttr.FindString(body); m != "" {
			t.Errorf("%s: forbidden attribute %q", script, m)
		}
		if strings.Contains(body, "http://") && !strings.Contains(script, "install") {
			for _, m := range regexp.MustCompile(`(?:src|href)="(https?://[^"]+)"`).FindAllStringSubmatch(body, -1) {
				if !strings.HasPrefix(m[1], "https://apps.apple.com/") && !strings.HasPrefix(m[1], "https://play.google.com/") && !strings.HasPrefix(m[1], "https://moonlight-stream.org/") {
					t.Errorf("%s: external reference %s", script, m[1])
				}
			}
		}
	}
	bad := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\(|new Function|setAttribute\(\s*['"]style|cssText|'unsafe-inline'`)
	// The next set also bans an h() style key (it would become a blocked
	// style attribute) and setting anything but a custom property.
	styleKey := regexp.MustCompile(`(?:^|[{,\s])style\s*:`)
	setProp := regexp.MustCompile(`\.style\.setProperty\(\s*['"]([^'"]*)['"]`)
	walkSet(t, set, func(name string, b []byte) {
		switch path.Ext(name) {
		case ".js":
			if m := bad.Find(b); m != nil {
				t.Errorf("%s uses %q", name, m)
			}
			if set.Current && set.ownsStatic(name) {
				code := jsBlank(string(b), false)
				if m := styleKey.FindString(code); m != "" {
					t.Errorf("%s passes a style key %q", name, m)
				}
				for _, m := range setProp.FindAllStringSubmatch(code, -1) {
					if !strings.HasPrefix(m[1], "--") {
						t.Errorf("%s sets style %q; only custom properties may be set", name, m[1])
					}
				}
			}
		case ".css":
			if regexp.MustCompile(`@import|url\((['"]?)(https?:)?//`).Match(b) {
				t.Errorf("%s loads something external", name)
			}
		}
	})
}

// walkStatic calls fn for every embedded static file, whichever set owns it.
func walkStatic(t *testing.T, fn func(name string, b []byte)) {
	t.Helper()
	err := fs.WalkDir(content, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := content.ReadFile(p)
		if err != nil {
			return err
		}
		fn(strings.TrimPrefix(p, "static/"), b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// walkSet calls fn for every static file set serves: its own and the shared ones.
func walkSet(t *testing.T, set uiSet, fn func(name string, b []byte)) {
	t.Helper()
	walkStatic(t, func(name string, b []byte) {
		if set.servesStatic(name) {
			fn(name, b)
		}
	})
}

// The scripts find their elements by id and their icons by name; a typo in
// either only shows up in a browser, so check them against the markup.
func TestScriptsMatchMarkup(t *testing.T) { forEachSet(t, testScriptsMatchMarkup) }

func testScriptsMatchMarkup(t *testing.T, set uiSet) {
	if set.Current {
		testScriptsMatchMarkupNext(t, set)
		return
	}
	u := testUI(t, set)
	html := renderAll(t, set)
	idAttr := regexp.MustCompile(`\sid="([^"]+)"`)
	byID := regexp.MustCompile(`byId\(\s*'([^']+)'\s*\)`)
	iconRef := regexp.MustCompile(`icon\(\s*'([^']+)'`)
	importRef := regexp.MustCompile(`(?m)^\s*(?:import|export)\s[^;]*?from\s+'([^']+)'`)

	ids := map[string]map[string]bool{}
	for script, body := range html {
		ids[script] = map[string]bool{}
		for _, m := range idAttr.FindAllStringSubmatch(body, -1) {
			if ids[script][m[1]] {
				t.Errorf("%s: duplicate id %q", script, m[1])
			}
			ids[script][m[1]] = true
		}
	}

	lib, pages := path.Join(set.Static, "js/lib.js"), path.Join(set.Static, "js/pages")+"/"
	walkSet(t, set, func(name string, b []byte) {
		if path.Ext(name) != ".js" {
			return
		}
		src := string(b)
		for _, m := range importRef.FindAllStringSubmatch(src, -1) {
			target := path.Join(path.Dir(name), m[1])
			if _, ok := u.assets.files[target]; !ok {
				t.Errorf("%s imports %s, which is not an asset", name, m[1])
			}
		}
		for _, m := range iconRef.FindAllStringSubmatch(src, -1) {
			if _, ok := iconPaths[m[1]]; !ok {
				t.Errorf("%s uses unknown icon %q", name, m[1])
			}
		}
		refs := byID.FindAllStringSubmatch(src, -1)
		if name == lib {
			// The shared runtime null-checks what it looks up, but every id
			// it names must exist on at least one page.
			for _, m := range refs {
				found := false
				for _, set := range ids {
					found = found || set[m[1]]
				}
				if !found {
					t.Errorf("%s: byId(%q) matches no page", name, m[1])
				}
			}
			return
		}
		if !strings.HasPrefix(name, pages) {
			return
		}
		page := strings.TrimSuffix(path.Base(name), ".js")
		if ids[page] == nil {
			t.Errorf("%s: no rendered page uses this script", name)
			return
		}
		for _, m := range refs {
			if !ids[page][m[1]] {
				t.Errorf("%s: byId(%q) has no element on the page", name, m[1])
			}
		}
	})
}

func TestIconsRender(t *testing.T) {
	for name := range iconPaths {
		h, err := iconHTML(name)
		if err != nil || !strings.Contains(string(h), `href="#i-`+name+`"`) {
			t.Errorf("icon %q: %v %s", name, err, h)
		}
	}
	if _, err := iconHTML("no-such-icon"); err == nil {
		t.Error("unknown icon accepted")
	}
	s := string(spriteHTML())
	if strings.Count(s, "<symbol ") != len(iconPaths) {
		t.Error("sprite does not contain every icon")
	}
}

func TestTimezones(t *testing.T) {
	seen := map[string]bool{}
	for _, tz := range timezones {
		if seen[tz] {
			t.Errorf("duplicate timezone %s", tz)
		}
		seen[tz] = true
		if _, err := time.LoadLocation(tz); err != nil {
			t.Errorf("%s: %v", tz, err)
		}
	}
	groups := timezoneGroups()
	n := 0
	for _, g := range groups {
		n += len(g.Zones)
		for _, z := range g.Zones {
			if strings.Contains(z.Label, "_") || strings.Contains(z.Label, "/") {
				t.Errorf("label %q not tidied", z.Label)
			}
		}
	}
	if n != len(timezones) {
		t.Errorf("groups hold %d zones, list has %d", n, len(timezones))
	}
	regions := map[string]bool{}
	for _, g := range groups {
		if regions[g.Region] {
			t.Errorf("region %s split into several groups; keep the list sorted by region", g.Region)
		}
		regions[g.Region] = true
	}
}

// TestJavaScript runs the JS checks when Node is installed (the dev Mac and
// CI have it; the appliance build does not need it): a module-mode syntax
// check of every script, and the unit tests in jstest/.
func TestJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	walkStatic(t, func(name string, b []byte) {
		if path.Ext(name) != ".js" {
			return
		}
		cmd := exec.Command(node, "--input-type=module", "--check")
		cmd.Stdin = bytes.NewReader(b)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	})
	tests, _ := filepath.Glob("jstest/*.test.mjs")
	if len(tests) == 0 {
		t.Fatal("no jstest/*.test.mjs files")
	}
	out, err := exec.Command(node, append([]string{"--test"}, tests...)...).CombinedOutput()
	if err != nil {
		t.Errorf("node --test: %v\n%s", err, out)
	}
}

// ---- the next set's shell (ARCH §3, §5, §6, §8)

// TestOldURLs: the eight-page UI's paths answer 303 to their new page with
// the query kept and the fragment added, are never cached, and land on a
// page that renders. A foreign ?code= is dropped; on the ISO they all go to
// /setup. /pair/extra stays a 404 (TestUnknownPathIsNotAPage).
func TestOldURLs(t *testing.T) {
	set := nextSet
	for _, installer := range []bool{false, true} {
		_, h := newHandler(t, set, installer, "ABCD-EFGH")
		for _, o := range set.OldURLs {
			to, frag, _ := strings.Cut(o.To, "#")
			cases := map[string]string{
				o.From:                         to + "#" + frag,
				o.From + "?next=%2Fx&a=1":      to + "?next=%2Fx&a=1#" + frag,
				o.From + "?code=ABCD-EFGH&b=2": to + "?b=2#" + frag,
			}
			for target, want := range cases {
				want = strings.TrimSuffix(want, "#")
				if installer {
					want, _, _ = strings.Cut(want, "#")
					_, q, _ := strings.Cut(want, "?")
					want = "/setup"
					if q != "" {
						want += "?" + q
					}
				}
				rec := get(h, target, "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Dest", "document")
				if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
					t.Errorf("installer=%v GET %s = %d → %q, want 303 → %q", installer, target, rec.Code, rec.Header().Get("Location"), want)
				}
				if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
					t.Errorf("GET %s: Cache-Control %q, want no-store", target, cc)
				}
				next, _, _ := strings.Cut(want, "#")
				if rec := get(h, next); rec.Code != http.StatusOK {
					t.Errorf("GET %s (after %s) = %d", next, target, rec.Code)
				}
			}
		}
	}
	// A first-party navigation keeps its setup code (the TV's QR code).
	_, h := newHandler(t, set, false, "")
	if rec := get(h, "/pair?code=ABCD-EFGH", "Sec-Fetch-Site", "none"); rec.Header().Get("Location") != "/devices?code=ABCD-EFGH#pair" {
		t.Errorf("/pair?code= from the QR code → %q", rec.Header().Get("Location"))
	}
}

// TestPageCompression: the next set's pages are gzipped with an ETag and
// answer If-None-Match with 304; the setup cookie is still set on a 304.
func TestPageCompression(t *testing.T) {
	_, h := newHandler(t, nextSet, false, "ABCD-EFGH")
	plain := get(h, "/devices")
	if plain.Header().Get("Content-Encoding") != "" || plain.Header().Get("ETag") == "" {
		t.Fatalf("identity: %v", plain.Header())
	}
	gz := get(h, "/devices", "Accept-Encoding", "gzip, br")
	hd := gz.Header()
	if hd.Get("Content-Encoding") != "gzip" || !strings.Contains(hd.Get("Vary"), "Accept-Encoding") || !strings.HasSuffix(hd.Get("ETag"), `-gz"`) {
		t.Fatalf("gzip: %v", hd)
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !bytes.Equal(body, plain.Body.Bytes()) {
		t.Error("the gzip body is not the page")
	}
	if rec := get(h, "/devices", "Accept-Encoding", "gzip", "If-None-Match", hd.Get("ETag")); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match = %d, want 304", rec.Code)
	}
	if rec := get(h, "/devices", "If-None-Match", plain.Header().Get("ETag")); rec.Code != http.StatusNotModified {
		t.Errorf("identity If-None-Match = %d, want 304", rec.Code)
	}
	if rec := get(h, "/devices", "Accept-Encoding", "gzip;q=0"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzip despite q=0")
	}
	// The QR code's first visit sets the setup cookie; so does a revisit
	// that the browser revalidates.
	_, h = newHandler(t, nextSet, false, "ABCD-EFGH")
	first := get(h, "/setup?code=ABCD-EFGH", "Sec-Fetch-Site", "none")
	again := get(h, "/setup?code=ABCD-EFGH", "Sec-Fetch-Site", "none", "If-None-Match", first.Header().Get("ETag"))
	if again.Code != http.StatusNotModified || len(again.Result().Cookies()) != 1 {
		t.Errorf("revalidated /setup?code=: %d, cookies %v", again.Code, again.Result().Cookies())
	}
	// The legacy set is left as it was.
	_, h = newHandler(t, legacySet, false, "")
	if rec := get(h, "/", "Accept-Encoding", "gzip"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("legacy pages are gzipped")
	}
}

// TestModulePreloads: every page preloads exactly the modules of its
// script's static import graph, each once.
func TestModulePreloads(t *testing.T) {
	set := nextSet
	u := testUI(t, set)
	link := regexp.MustCompile(`<link rel="modulepreload" href="([^"]+)">`)
	for script, body := range renderAll(t, set) {
		entry := path.Join(set.Static, "js/pages", script+".js")
		static, _, err := importGraph(u.assets.files, entry)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, m := range static {
			if m != entry {
				want[u.assets.prefix()+"/"+m] = true
			}
		}
		seen := map[string]int{}
		for _, m := range link.FindAllStringSubmatch(body, -1) {
			seen[m[1]]++
			if !want[m[1]] {
				t.Errorf("%s: preloads %s, which its script does not import", script, m[1])
			}
		}
		for m := range want {
			if seen[m] != 1 {
				t.Errorf("%s: preloads %s %d times, want once", script, m, seen[m])
			}
		}
	}
}

// TestHeadScript: head.js is a classic script (no import or export), at
// most 1 kB, and every page has it once before the module script
// (checkNextFrame).
func TestHeadScript(t *testing.T) {
	b, err := content.ReadFile("static/js/head.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 1024 {
		t.Errorf("head.js is %d bytes, budget is 1024", len(b))
	}
	if regexp.MustCompile(`(?m)^\s*(?:import|export)\b|\bimport\(`).MatchString(jsBlank(string(b), false)) {
		t.Error("head.js is a classic script: no import or export")
	}
}

// pureModules touch no DOM, storage, location or network, and import only
// each other, so jstest runs them under Node.
var pureModules = []string{"js/fmt.js", "js/validate.js", "js/state.js", "js/summary.js", "js/copy.js", "js/confirms.js", "js/messages.js"}

func TestPureModules(t *testing.T) {
	impure := regexp.MustCompile(`\b(?:document|window|localStorage|sessionStorage|location|navigator|EventSource)\b|\bfetch\(`)
	pure := map[string]bool{}
	for _, m := range pureModules {
		pure[m] = true
	}
	for _, m := range pureModules {
		b, err := content.ReadFile("static/" + m)
		if err != nil {
			t.Fatal(err)
		}
		src := jsBlank(string(b), false)
		if hit := impure.FindString(src); hit != "" {
			t.Errorf("%s uses %q; pure modules touch no DOM, storage or network", m, hit)
		}
		st, eager, lz := jsImports(b)
		for _, rel := range append(append(st, eager...), lz...) {
			if target := path.Join(path.Dir(m), rel); !pure[target] {
				t.Errorf("%s imports %s, which is not pure", m, rel)
			}
		}
	}
}

// TestTransportSeam: only core/api.js and core/live.js reach the network
// (plus the installer's cross-origin hand-off ping), so the website's live
// demo can supply globalThis.vosTransport.
func TestTransportSeam(t *testing.T) {
	net := regexp.MustCompile(`\bfetch\(|\bnew\s+EventSource\b|\bXMLHttpRequest\b|\bnavigator\.sendBeacon\b|\bnew\s+WebSocket\b`)
	allowed := map[string]bool{"js/core/api.js": true, "js/core/live.js": true, "js/pages/install.js": true}
	walkSet(t, nextSet, func(name string, b []byte) {
		if path.Ext(name) != ".js" || !nextSet.ownsStatic(name) || allowed[name] {
			return
		}
		if hit := net.FindString(jsBlank(string(b), false)); hit != "" {
			t.Errorf("%s uses %q; go through core/api.js", name, hit)
		}
	})
}

// TestCSSURLsResolve: every url() in the next set's stylesheet is a
// relative path to an embedded asset, or a reference to an element every
// page carries (the heat filters and gradient from {{filters}}).
func TestCSSURLsResolve(t *testing.T) {
	set := nextSet
	u := testUI(t, set)
	pages := renderAll(t, set)
	home := pages["home"]
	for name, a := range u.assets.files {
		if !set.ownsStatic(name) || path.Ext(name) != ".css" {
			continue
		}
		for _, m := range cssURL.FindAllStringSubmatch(string(a.body), -1) {
			ref := m[1] + m[2] + m[3]
			switch {
			case strings.HasPrefix(ref, "#"):
				if !strings.Contains(home, `id="`+ref[1:]+`"`) {
					t.Errorf("%s: url(%s) names no element on the pages", name, ref)
				}
			case strings.HasPrefix(ref, "/") || strings.Contains(ref, ":"):
				t.Errorf("%s: url(%s) must be relative to the stylesheet", name, ref)
			default:
				if _, ok := u.assets.files[path.Join(path.Dir(name), ref)]; !ok {
					t.Errorf("%s: url(%s) is not an embedded asset", name, ref)
				}
			}
		}
	}
}

// TestLinksHonourBase: rendered with a base (the website's demo path),
// every link and form of the next set stays under it.
func TestLinksHonourBase(t *testing.T) {
	set := nextSet
	u := testUI(t, set)
	u.base = "/x"
	attr := regexp.MustCompile(`\s(href|action|formaction)="(/[^"]*)"`)
	for _, installer := range []bool{false, true} {
		for _, p := range append([]page{set.Installer}, set.Pages...) {
			if installer != (p.Script == set.Installer.Script) {
				continue
			}
			d := u.data(p)
			d.Installer = installer
			if installer {
				d.Timezones = timezoneGroups()
			}
			var buf bytes.Buffer
			if err := u.tmpl[p.Name].ExecuteTemplate(&buf, "layout", d); err != nil {
				t.Fatal(err)
			}
			body := buf.String()
			if !strings.Contains(body, `data-base="/x"`) {
				t.Errorf("%s: <html> lacks data-base", p.Script)
			}
			for _, m := range attr.FindAllStringSubmatch(body, -1) {
				if !strings.HasPrefix(m[2], "/x/") && m[2] != "/x" && !strings.HasPrefix(m[2], "/static/") {
					t.Errorf("%s: %s=%q ignores the base", p.Script, m[1], m[2])
				}
			}
		}
	}
	// An old URL keeps the base too.
	srv := api.New(api.Options{})
	h := register(srv, set, content)
	h.load().base = "/x"
	if rec := get(srv.Handler(), "/pair"); rec.Header().Get("Location") != "/x/devices#pair" {
		t.Errorf("/pair → %q with a base", rec.Header().Get("Location"))
	}
}

// libraryModules are shell modules no page imports yet: the kit the tab
// pages build with. Remove a name once a page uses it (the test says so).
var libraryModules = map[string]string{
	"js/ui/controls.js": "switches, meters and steppers: C3, C4a, C4b",
}

var (
	jsRefStrict   = regexp.MustCompile(`\b(?:byId|setText|showError|cloneTpl|getElementById)\(\s*'([^']+)'|\$\$?\(\s*'#([\w-]+)'`)
	jsRefOptional = regexp.MustCompile(`\boptionalById\(\s*'([^']+)'`)
	jsTplRef      = regexp.MustCompile(`\bcloneTpl\(\s*'([^']+)'`)
	jsIconRef     = regexp.MustCompile(`\bicon\(\s*'([^']+)'`)
	templateBlock = regexp.MustCompile(`(?s)<template\b[^>]*>(.*?)</template>`)
	templateID    = regexp.MustCompile(`<template\b[^>]*\sid="([^"]+)"`)
)

// testScriptsMatchMarkupNext is ARCH §5.3's version for the next set: every
// id any module of a page's import graph (static and lazy) looks up must be
// on that page; an optionalById id must be on some page whose graph has the
// module; cloneTpl names a <template> on the page and no template holds an
// id; every module is some page's (or a listed library); imports and icons
// resolve; no page has an id twice.
func testScriptsMatchMarkupNext(t *testing.T, set uiSet) {
	u := testUI(t, set)
	pages := renderAll(t, set)
	idAttr := regexp.MustCompile(`\sid="([^"]+)"`)
	reached := map[string]bool{}
	optional := map[string]map[string]bool{} // module → ids it may look up
	optionalFound := map[string]bool{}       // module+id found on a page that has the module
	for script, body := range pages {
		ids := map[string]bool{}
		for _, m := range idAttr.FindAllStringSubmatch(body, -1) {
			if ids[m[1]] {
				t.Errorf("%s: duplicate id %q", script, m[1])
			}
			ids[m[1]] = true
		}
		tpls := map[string]bool{}
		for _, m := range templateID.FindAllStringSubmatch(body, -1) {
			tpls[m[1]] = true
		}
		for _, m := range templateBlock.FindAllStringSubmatch(body, -1) {
			if idAttr.MatchString(m[1]) {
				t.Errorf("%s: an id inside a <template> would repeat on every clone", script)
			}
		}
		static, lazy, err := importGraph(u.assets.files, path.Join(set.Static, "js/pages", script+".js"))
		if err != nil {
			t.Errorf("%s: %v", script, err)
			continue
		}
		for _, mod := range append(static, lazy...) {
			reached[mod] = true
			src := jsBlank(string(u.assets.files[mod].body), true)
			for _, m := range jsRefStrict.FindAllStringSubmatch(src, -1) {
				if id := m[1] + m[2]; !ids[id] {
					t.Errorf("%s: %s looks up #%s, which is not on the page (use optionalById if some pages lack it)", script, mod, id)
				}
			}
			for _, m := range jsTplRef.FindAllStringSubmatch(src, -1) {
				if !tpls[m[1]] {
					t.Errorf("%s: %s clones <template id=%q>, which is not on the page", script, mod, m[1])
				}
			}
			for _, m := range jsRefOptional.FindAllStringSubmatch(src, -1) {
				if optional[mod] == nil {
					optional[mod] = map[string]bool{}
				}
				optional[mod][m[1]] = true
				if ids[m[1]] {
					optionalFound[mod+"#"+m[1]] = true
				}
			}
		}
	}
	for mod, ids := range optional {
		for id := range ids {
			if !optionalFound[mod+"#"+id] {
				t.Errorf("%s: optionalById(%q) matches no page that uses the module", mod, id)
			}
		}
	}
	walkSet(t, set, func(name string, b []byte) {
		if path.Ext(name) != ".js" || !set.ownsStatic(name) {
			return
		}
		src := jsBlank(string(b), true)
		for _, m := range jsIconRef.FindAllStringSubmatch(src, -1) {
			if _, ok := iconPaths[m[1]]; !ok {
				t.Errorf("%s uses unknown icon %q", name, m[1])
			}
		}
		switch {
		case name == path.Join(set.Static, "js/head.js"):
		case libraryModules[name] != "" && reached[name]:
			t.Errorf("%s is used by a page now: drop it from libraryModules", name)
		case libraryModules[name] == "" && !reached[name]:
			t.Errorf("%s is in no page's import graph", name)
		}
	})
}

// TestA11yMarkup: what can be checked in the markup itself (ARCH §6), on
// every page of the next set in both modes.
func TestA11yMarkup(t *testing.T) {
	var (
		idAttr   = regexp.MustCompile(`\sid="([^"]+)"`)
		control  = regexp.MustCompile(`<(input|select|textarea)\b[^>]*>`)
		button   = regexp.MustCompile(`(?s)<button\b([^>]*)>(.*?)</button>`)
		dialog   = regexp.MustCompile(`<dialog\b[^>]*>`)
		svgOpen  = regexp.MustCompile(`<svg\b[^>]*>`)
		refs     = regexp.MustCompile(`\s(aria-labelledby|aria-describedby|aria-controls|for)="([^"]+)"`)
		tabIndex = regexp.MustCompile(`\stabindex="(\d+)"`)
		blank    = regexp.MustCompile(`<a\b[^>]*\starget="_blank"[^>]*>`)
		labelEl  = regexp.MustCompile(`(?s)<label\b[^>]*>.*?</label>`)
		tags     = regexp.MustCompile(`<[^>]*>`)
	)
	for script, body := range renderAll(t, nextSet) {
		ids := map[string]bool{}
		for _, m := range idAttr.FindAllStringSubmatch(body, -1) {
			ids[m[1]] = true
		}
		inLabel := labelEl.FindAllStringIndex(body, -1)
		wrapped := func(at int) bool {
			for _, r := range inLabel {
				if at > r[0] && at < r[1] {
					return true
				}
			}
			return false
		}
		for _, loc := range control.FindAllStringIndex(body, -1) {
			tag := body[loc[0]:loc[1]]
			a := tagAttrs(tag)
			if a["type"] == "hidden" || a["type"] == "submit" || a["type"] == "button" {
				continue
			}
			labelled := a["aria-label"] != "" || a["aria-labelledby"] != "" || wrapped(loc[0]) ||
				(a["id"] != "" && strings.Contains(body, `for="`+a["id"]+`"`))
			if !labelled {
				t.Errorf("%s: %s has no label", script, tag)
			}
			if a["role"] == "switch" && a["type"] != "checkbox" {
				t.Errorf("%s: role=switch belongs on a checkbox: %s", script, tag)
			}
		}
		for _, m := range button.FindAllStringSubmatch(body, -1) {
			a := tagAttrs("<button" + m[1] + ">")
			text := strings.TrimSpace(html.UnescapeString(tags.ReplaceAllString(m[2], "")))
			_, hidden := a["hidden"]
			if text == "" && a["aria-label"] == "" && a["aria-labelledby"] == "" && !hidden { // a hidden one gets its words when shown
				t.Errorf("%s: a button has no name: <button%s>", script, m[1])
			}
		}
		for _, tag := range dialog.FindAllString(body, -1) {
			if tagAttrs(tag)["aria-labelledby"] == "" {
				t.Errorf("%s: %s has no aria-labelledby", script, tag)
			}
		}
		for _, m := range refs.FindAllStringSubmatch(body, -1) {
			for _, id := range strings.Fields(m[2]) {
				if !ids[id] {
					t.Errorf("%s: %s=%q names no element", script, m[1], id)
				}
			}
		}
		for _, m := range tabIndex.FindAllStringSubmatch(body, -1) {
			if m[1] != "0" {
				t.Errorf("%s: tabindex=%s; never above 0", script, m[1])
			}
		}
		for _, tag := range svgOpen.FindAllString(body, -1) {
			a := tagAttrs(tag)
			if a["aria-hidden"] != "true" && !(a["role"] == "img" && (a["aria-label"] != "" || a["aria-labelledby"] != "")) {
				t.Errorf("%s: %s is neither aria-hidden nor a named image", script, tag)
			}
		}
		for _, tag := range blank.FindAllString(body, -1) {
			if a := tagAttrs(tag); a["rel"] != "noopener noreferrer" {
				t.Errorf("%s: %s needs rel=\"noopener noreferrer\"", script, tag)
			}
		}
		if n := strings.Count(body, "<h1"); n != 1 {
			t.Errorf("%s: %d <h1>, want exactly one", script, n)
		}
		if !regexp.MustCompile(`<main\b[^>]*\sid="main"`).MatchString(body) {
			t.Errorf("%s: no <main id=\"main\">", script)
		}
		after := body[strings.Index(body, "<body"):]
		if first := regexp.MustCompile(`<a\b[^>]*>`).FindString(after); !strings.Contains(first, `class="skip"`) {
			t.Errorf("%s: the first link is %s, want the skip link", script, first)
		}
	}
}

// {{handshake .Hostname}} draws the host's 5×5 mark in its two colours,
// the same for any spelling of the name, never from the setup code.
func TestHandshakeHTML(t *testing.T) {
	a, b := string(handshakeHTML("Vapor.local.")), string(handshakeHTML("vapor"))
	if a != b {
		t.Error("the mark depends on how the name is spelled")
	}
	if !strings.HasPrefix(a, `<svg class="handshake" viewBox="0 0 5 5"`) || !strings.Contains(a, `aria-hidden="true"`) {
		t.Errorf("handshake = %s", a)
	}
	if n := strings.Count(a, "h1v1h-1z"); n < 4 || n > 25 {
		t.Errorf("%d cells on, want 4 to 25", n)
	}
	if handshakeHTML("vapor") == handshakeHTML("den") {
		t.Error("two hosts share a mark")
	}
}
