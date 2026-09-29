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
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // validate the timezone list against a known database

	"github.com/jasperaelvoet/vaporos/internal/api"
)

func newHandler(t *testing.T, installer bool, code string) (*api.Server, http.Handler) {
	t.Helper()
	srv := api.New(api.Options{Installer: installer})
	srv.SetSetupCode(code)
	Register(srv)
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

func testUI(t *testing.T) *ui {
	t.Helper()
	u, err := newUI(api.New(api.Options{}), activeSet, content)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestPagesRender(t *testing.T) {
	_, h := newHandler(t, false, "")
	u := testUI(t)
	for _, p := range activeSet.Pages {
		t.Run(p.Name, func(t *testing.T) {
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
			for _, want := range []string{
				"<title>" + html.EscapeString(p.Title) + " · VaporOS</title>",
				`data-page="` + p.Name + `"`,
				`<script type="module" src="` + u.assets.prefix() + `/legacy/js/pages/` + p.Script + `.js"></script>`,
				`href="` + u.assets.prefix() + `/legacy/app.css"`,
				`href="` + u.assets.prefix() + `/icon.svg"`,
				`<symbol id="i-home"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("page does not contain %q", want)
				}
			}
			if p.Bare == strings.Contains(body, `id="nav"`) {
				t.Errorf("bare=%v but navigation presence disagrees", p.Bare)
			}
			if p.Nav && !regexp.MustCompile(`<a href="`+regexp.QuoteMeta(p.Path)+`" aria-current="page">`).MatchString(body) {
				t.Errorf("navigation does not mark %s as current", p.Path)
			}
			// The page's script and stylesheet must actually be served.
			for _, ref := range []string{"/legacy/js/pages/" + p.Script + ".js", "/legacy/app.css", "/legacy/js/lib.js", "/icon.svg"} {
				if rec := get(h, u.assets.prefix()+ref); rec.Code != http.StatusOK {
					t.Errorf("GET %s = %d", ref, rec.Code)
				}
			}
		})
	}
}

func TestUnknownPathIsNotAPage(t *testing.T) {
	_, h := newHandler(t, false, "")
	for _, p := range []string{"/nope", "/index.html", "/pair/extra"} {
		if rec := get(h, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}

func TestInstallerMode(t *testing.T) {
	_, h := newHandler(t, true, "ABCD-EFGH")

	for target, want := range map[string]string{
		"/":                    "/setup",
		"/pair":                "/setup",
		"/login?next=/updates": "/setup?next=/updates",
		"/?code=ABCD-EFGH":     "/setup?code=ABCD-EFGH",
	} {
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
	for _, want := range []string{
		`id="wizard"`,
		`/js/pages/install.js"`,
		`<title>Install VaporOS · VaporOS</title>`,
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

func TestFirstRunSetup(t *testing.T) {
	_, h := newHandler(t, false, "ABCD-EFGH")
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
	_, h = newHandler(t, false, "")
	if body := get(h, "/setup").Body.String(); strings.Contains(body, `id="setup-code"`) {
		t.Error("setup code field shown although no code is needed")
	}
}

// A page on another site can make a browser load /setup?code=… as often as
// it likes. The code is dropped before it is checked, so wrong ones cannot
// lock the browser out, and it never reaches the form or the page script.
func TestSetupCodeFromAnotherSiteIsDropped(t *testing.T) {
	for _, installer := range []bool{true, false} {
		srv, h := newHandler(t, installer, "ABCD-EFGH")
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
	srv, h := newHandler(t, false, "ABCD-EFGH")
	srv.SetSetupWaiver(func() bool { return true }) // ignored outside installer mode
	if body := get(h, "/setup").Body.String(); !strings.Contains(body, `id="setup-code"`) {
		t.Error("first-run setup must always ask for the code")
	}
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
	_, h := newHandler(t, false, "")
	u := testUI(t)
	for name, ctype := range map[string]string{
		"legacy/app.css":             "text/css; charset=utf-8",
		"legacy/js/lib.js":           "text/javascript; charset=utf-8",
		"legacy/js/pages/install.js": "text/javascript; charset=utf-8",
		"icon.svg":                   "image/svg+xml",
		"icon-192.png":               "image/png",
		"manifest.webmanifest":       "application/manifest+json",
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
	rec := get(h, "/static/0123456789ab/legacy/app.css")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("stale version: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	rec = get(h, "/favicon.ico")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("favicon: %d %v", rec.Code, rec.Header())
	}
}

func TestStaticGzip(t *testing.T) {
	_, h := newHandler(t, false, "")
	u := testUI(t)
	url := u.assets.prefix() + "/legacy/app.css"
	rec := get(h, url, "Accept-Encoding", "br, gzip")
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("no gzip: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if !bytes.Equal(plain, u.assets.files["legacy/app.css"].body) {
		t.Error("gzip body does not decompress to app.css")
	}
	gzTag := rec.Header().Get("ETag")
	if gzTag == u.assets.files["legacy/app.css"].etag {
		t.Error("gzip variant shares the identity ETag")
	}
	if rec := get(h, url, "Accept-Encoding", "gzip;q=0"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzip sent despite q=0")
	}
	if rec := get(h, u.assets.prefix()+"/icon-192.png", "Accept-Encoding", "gzip"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("PNG was gzipped")
	}
}

func TestStaticNotFound(t *testing.T) {
	_, h := newHandler(t, false, "")
	u := testUI(t)
	for _, p := range []string{
		"/static/",
		"/static/app.css", // no version segment
		u.assets.prefix() + "/",
		u.assets.prefix() + "/js",
		u.assets.prefix() + "/js/",
		u.assets.prefix() + "/nope.css",
		u.assets.prefix() + "/templates/layout.html",
	} {
		if rec := get(h, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
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

// The appliance is mostly used from a phone, often over Wi-Fi: keep the
// whole UI small.
func TestAssetBudget(t *testing.T) {
	u := testUI(t)
	var total, wire int
	for _, a := range u.assets.files {
		total += len(a.body)
		if a.gz != nil {
			wire += len(a.gz)
		} else {
			wire += len(a.body)
		}
	}
	t.Logf("static assets: %d bytes (%d gzipped)", total, wire)
	if total >= 150_000 {
		t.Errorf("static assets are %d bytes, budget is 150 kB", total)
	}
}

// renderAll returns every page's HTML in both modes, keyed by script name.
func renderAll(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, installer := range []bool{false, true} {
		_, h := newHandler(t, installer, "ABCD-EFGH")
		for _, p := range activeSet.Pages {
			if installer && p.Name != "setup" {
				continue
			}
			rec := get(h, p.Path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s (installer=%v) = %d", p.Path, installer, rec.Code)
			}
			script := p.Script
			if installer {
				script = activeSet.Installer.Script
			}
			out[script] = rec.Body.String()
		}
	}
	return out
}

// The API's CSP (default-src 'self') blocks inline script, inline event
// handlers and style attributes; any of them would silently break a page.
func TestCSPCompliance(t *testing.T) {
	inlineScript := regexp.MustCompile(`<script(?:\s[^>]*)?>`)
	forbiddenAttr := regexp.MustCompile(`\s(style|on[a-z]+)\s*=`)
	for script, body := range renderAll(t) {
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
	walkStatic(t, func(name string, b []byte) {
		switch path.Ext(name) {
		case ".js":
			if m := bad.Find(b); m != nil {
				t.Errorf("%s uses %q", name, m)
			}
		case ".css":
			if regexp.MustCompile(`@import|url\((['"]?)(https?:)?//`).Match(b) {
				t.Errorf("%s loads something external", name)
			}
		}
	})
}

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

// The scripts find their elements by id and their icons by name; a typo in
// either only shows up in a browser, so check them against the markup.
func TestScriptsMatchMarkup(t *testing.T) {
	u := testUI(t)
	html := renderAll(t)
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

	walkStatic(t, func(name string, b []byte) {
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
		if name == "legacy/js/lib.js" {
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
		if !strings.HasPrefix(name, "legacy/js/pages/") {
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

// The inline logo in the page header and icon.svg are the same drawing.
func TestLogoMatchesIcon(t *testing.T) {
	d := regexp.MustCompile(`<path d="([^"]+)"`)
	layout, _ := content.ReadFile("templates/legacy/layout.html")
	icon, _ := content.ReadFile("static/icon.svg")
	logo := layout[bytes.Index(layout, []byte(`{{define "logo"}}`)):]
	a, b := d.FindAllSubmatch(logo, -1), d.FindAllSubmatch(icon, -1)
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("logo has %d paths, icon.svg %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i][1], b[i][1]) {
			t.Errorf("path %d differs: %s vs %s", i, a[i][1], b[i][1])
		}
	}
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
