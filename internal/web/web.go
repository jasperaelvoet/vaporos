// Package web is the embedded web UI: server-rendered page shells plus
// vanilla JS that calls /api/v1. Templates, CSS, JS and icons are compiled
// into the binary with go:embed, so the UI works on a LAN without internet
// and can never drift from the daemon that serves it. There are no external
// assets: the API's CSP (default-src 'self') would block them anyway.
//
// Pages are Public routes. The shell carries nothing private; each page's
// script asks /api/v1/auth/me and sends the visitor to /login or /setup, so
// the auth decision stays in one place (the api package).
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/brand"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

//go:embed templates static
var content embed.FS

// page is one server-rendered route.
type page struct {
	Name     string // template file under templates/pages and <html data-page>
	Path     string // URL path
	Title    string // <title> and <h1>
	Heading  string // the <h1> when it differs from Title (next set)
	Label    string // navigation label, when shorter than Title
	Lead     string // one-line explanation under the heading
	Script   string // static/js/pages/<Script>.js
	Icon     string // icon name (icons.go)
	Nav      bool   // listed in the main navigation: a tab root
	Tab      string // a sub-page's tab root, by Name (next set)
	Bare     bool   // centred card without navigation (sign-in, setup)
	HideHead bool   // the <h1> is visually hidden (Home: the hero speaks first)
	// Styles are the page's own stylesheets, in cascade order: each is
	// styles/pages/<name>.css, which tools/web/css.mjs builds into
	// static/pages/<name>.css and the layout links after app.css (next set).
	Styles []string
}

// DocTitle is the next set's <title>: "Updates · VaporOS", or the title
// alone when it already names VaporOS ("Install VaporOS").
func (p page) DocTitle() string {
	if strings.Contains(p.Title, "VaporOS") {
		return p.Title
	}
	return p.Title + " · VaporOS"
}

// H1 is the page's main heading.
func (p page) H1() string {
	if p.Heading != "" {
		return p.Heading
	}
	return p.Title
}

// NavLabel is what the navigation shows for the page.
func (p page) NavLabel() string {
	if p.Label != "" {
		return p.Label
	}
	return p.Title
}

// pattern is the ServeMux pattern: "/" must match only itself, because a
// bare "/" would swallow every unknown path (including API typos).
func (p page) pattern() string {
	if p.Path == "/" {
		return "GET /{$}"
	}
	return "GET " + p.Path
}

// uiSet is one complete web UI: its page shells, its own static files and
// its routes. Two sets live side by side while a new UI is built at its final
// paths. Only activeSet is read, parsed and served, so a half-built set can
// never stop vosd (and with it the API) from starting.
type uiSet struct {
	Name      string   // "legacy" or "next"
	Templates string   // directory with layout.html and pages/<page>.html
	Static    string   // directory under static/ with the set's app.css and js/ ("" is static/ itself)
	Pages     []page   // every route, in navigation order (the order is <html data-order>)
	Installer page     // replaces the setup page on the live ISO
	Partials  string   // glob of extra {{define}} files parsed with the layout; may match nothing
	OldURLs   []oldURL // earlier paths that answer 303 to their page now
	Compress  bool     // pages are gzipped and carry an ETag
	Current   bool     // pageData.Version is the image version, and pages get Preloads and Fonts
}

// oldURL is a path an earlier UI served. Bookmarks, home-screen icons, the
// README and the welcome screen (/pair) still point at them.
type oldURL struct{ From, To string }

// legacySet is the eight-page UI. CONTRACTS.md "Pages".
var legacySet = uiSet{
	Name:      "legacy",
	Templates: "templates/legacy",
	Static:    "legacy",
	Pages:     legacyPages,
	Installer: legacyInstaller,
	Partials:  "templates/legacy/partials/*.html",
}

// nextSet is the four-tab UI (MASTER-PLAN §2.2). It is unreachable in
// production until the switch commit makes it the active set.
var nextSet = uiSet{
	Name:      "next",
	Templates: "templates",
	Static:    "",
	Pages:     nextPages,
	Installer: nextInstaller,
	Partials:  "templates/partials/*.html",
	OldURLs:   oldURLs,
	Compress:  true,
	Current:   true,
}

// nextPages is every route of the four-tab UI, in order: Home is 0, the
// tabs 1-3 and the System sub-pages 4-9, so moving to a lower number is
// going back (head.js). Wave agents fill the pages; this list is the one
// place routes are added (spec-cc-screens §1.1).
var nextPages = []page{
	{Name: "home", Path: "/", Title: "Home", Script: "home", Icon: "vapor", Nav: true, HideHead: true, Styles: []string{"home"}},
	{Name: "devices", Path: "/devices", Title: "Devices", Script: "devices", Icon: "devices", Nav: true, Styles: []string{"devices"},
		Lead: "Phones, tablets, TVs and computers that play from this PC."},
	{Name: "screen", Path: "/screen", Title: "Screen", Script: "screen", Icon: "screen", Nav: true, Styles: []string{"screen"},
		Lead: "The invisible screen Moonlight streams, and how it's sent."},
	{Name: "system", Path: "/system", Title: "System", Script: "system", Icon: "system", Nav: true, Styles: []string{"system"}},
	{Name: "updates", Path: "/system/updates", Title: "Updates", Script: "updates", Icon: "update", Tab: "system", Styles: []string{"system", "updates"},
		Lead: "New versions install next to the running one and switch over on restart."},
	{Name: "power", Path: "/system/power", Title: "Power", Script: "power", Icon: "power", Tab: "system", Styles: []string{"system", "power"},
		Lead: "Sleep when nobody plays, wake from Moonlight."},
	{Name: "storage", Path: "/system/storage", Title: "Storage", Script: "storage", Icon: "drive", Tab: "system", Styles: []string{"system", "storage"},
		Lead: "Drives in this PC, and the game libraries VaporOS mounts for Steam."},
	{Name: "settings", Path: "/system/settings", Title: "Settings", Script: "settings", Icon: "sliders", Tab: "system", Styles: []string{"system", "settings"},
		Lead: "Name, password and remote access."},
	{Name: "logs", Path: "/system/logs", Title: "Logs", Script: "logs", Icon: "file", Tab: "system", Styles: []string{"system", "logs"},
		Lead: "What the stream server has been doing."},
	{Name: "about", Path: "/system/about", Title: "About", Script: "about", Icon: "info", Tab: "system", Styles: []string{"system", "about"}},
	{Name: "login", Path: "/login", Title: "Sign in", Script: "login", Bare: true, Styles: []string{"entry"}},
	{Name: "setup", Path: "/setup", Title: "Set up VaporOS", Heading: "Welcome to VaporOS", Script: "setup", Bare: true, Styles: []string{"entry"}},
}

// nextInstaller replaces the setup page on the live ISO.
var nextInstaller = page{Name: "setup", Path: "/setup", Title: "Install VaporOS", Script: "install", Bare: true, Styles: []string{"entry", "install"}}

// oldURLs are the eight-page UI's paths (spec-cc-screens §1.2). The
// welcome screen prints /pair (internal/display/status.go), and README.md
// names Pair and Updates. They answer 303 with the query kept, never a
// permanent redirect, which browsers would keep forever. /advanced#logs
// keeps its fragment across the redirect; settings.js sends it on to
// /system/logs.
var oldURLs = []oldURL{
	{"/pair", "/devices#pair"},
	{"/streaming", "/screen#stream"},
	{"/display", "/screen"},
	{"/storage", "/system/storage"},
	{"/updates", "/system/updates"},
	{"/power", "/system/power"},
	{"/advanced", "/system/settings"},
}

// activeSet is the UI vosd serves. There is deliberately no runtime knob.
var activeSet = legacySet

// uiSets is every set, active or not.
var uiSets = []uiSet{legacySet, nextSet}

// ownsStatic reports whether name (a path under static/) is one of the set's
// own files. Everything no set owns (icons, manifest, fonts) is shared.
func (s uiSet) ownsStatic(name string) bool {
	if s.Static != "" {
		return name == s.Static || strings.HasPrefix(name, s.Static+"/")
	}
	return name == "app.css" || name == "js" || strings.HasPrefix(name, "js/") || name == "pages" || strings.HasPrefix(name, "pages/")
}

// servesStatic reports whether the set's asset store holds name: its own
// files and the shared ones, never another set's.
func (s uiSet) servesStatic(name string) bool {
	for _, o := range uiSets {
		if o.Name != s.Name && o.ownsStatic(name) {
			return false
		}
	}
	return true
}

// legacyPages lists every legacy page in navigation order.
var legacyPages = []page{
	{Name: "dashboard", Path: "/", Title: "Home", Script: "dashboard", Icon: "home", Nav: true},
	{Name: "pair", Path: "/pair", Title: "Pair a device", Label: "Pair",
		Lead:   "Connect Moonlight on a phone, tablet, TV or computer to stream your Steam games.",
		Script: "pair", Icon: "phone", Nav: true},
	{Name: "streaming", Path: "/streaming", Title: "Streaming",
		Lead:   "Sunshine is the streaming server Moonlight connects to.",
		Script: "streaming", Icon: "gamepad", Nav: true},
	{Name: "display", Path: "/display", Title: "Display",
		Lead:   "The virtual screen that follows each Moonlight device's resolution, refresh rate and HDR.",
		Script: "display", Icon: "monitor", Nav: true},
	{Name: "storage", Path: "/storage", Title: "Storage",
		Lead:   "Drives in this PC, and the game libraries VaporOS mounts for Steam.",
		Script: "storage", Icon: "drive", Nav: true},
	{Name: "updates", Path: "/updates", Title: "Updates",
		Lead:   "VaporOS installs updates next to the running system and switches over on restart.",
		Script: "updates", Icon: "update", Nav: true},
	{Name: "power", Path: "/power", Title: "Power",
		Lead:   "Sleep when nobody plays, wake from Moonlight.",
		Script: "power", Icon: "power", Nav: true},
	{Name: "advanced", Path: "/advanced", Title: "Advanced",
		Lead:   "Name, password, remote access, logs and restarts.",
		Script: "advanced", Icon: "sliders", Nav: true},
	{Name: "login", Path: "/login", Title: "Sign in", Script: "login", Bare: true},
	{Name: "setup", Path: "/setup", Title: "Welcome to VaporOS", Script: "setup", Bare: true},
}

// legacyInstaller replaces the setup page on the live ISO: same route, the
// full installer wizard instead of the first-run password form.
var legacyInstaller = page{Name: "setup", Path: "/setup", Title: "Install VaporOS", Script: "install", Bare: true}

// pageData is what every template sees.
type pageData struct {
	Page      page
	Nav       []page // the tab roots, in order (the legacy layout's name)
	Tabs      []page // the same list, as the next layout calls it
	Tab       string // the highlighted tab's Name; "" on bare pages
	Order     int    // the page's index in the registry (<html data-order>)
	Static    string // versioned prefix of the set's own files, "/static/<hash>/legacy"
	Shared    string // versioned prefix of the files every UI shares (icons, manifest, fonts), "/static/<hash>"
	Base      string // prefix of every link: "" on the box, the demo's path on the website
	Preloads  []string
	Styles    []string // the page's own built stylesheets, relative to Static ("pages/home.css")
	Fonts     []string // fonts to preload, relative to Shared
	Theme     struct{ Dark, Light string }
	Hostname  string // this machine's name, without .local
	Version   string
	Installer bool
	NeedCode  bool   // a setup code guards /setup's API calls
	Code      string // setup code from ?code=, to prefill the field
	Timezones []tzGroup
	Demo      bool // rendered for the website's live demo (TestExportDemo)
}

type ui struct {
	srv      *api.Server
	set      uiSet
	assets   *assetStore
	tmpl     map[string]*template.Template
	nav      []page
	preloads map[string][]string // page script → its static import graph, under the set's static dir
	lazy     map[string][]string // page script → modules it imports with import()
	styles   map[string][]string // page script → its built page stylesheets, under the set's static dir
	fonts    []string
	base     string       // see pageData.Base
	gz       sync.Map     // ETag → the page's gzipped body (gzipped)
	gzN      atomic.Int32 // entries in gz since it last started over
}

// uiHolder hands the handlers the current UI. Production stores one and
// keeps it; the dev server can swap in a rebuilt one when a file changes.
type uiHolder struct{ cur atomic.Pointer[ui] }

func (h *uiHolder) load() *ui   { return h.cur.Load() }
func (h *uiHolder) store(u *ui) { h.cur.Store(u) }

// Register adds the active UI's pages and the static assets to srv.
func Register(srv *api.Server) {
	register(srv, activeSet, content)
}

// register serves set, read from fsys (the embedded files in production),
// and returns the holder the handlers read the UI from.
func register(srv *api.Server, set uiSet, fsys fs.FS) *uiHolder {
	u, err := newUI(srv, set, fsys)
	if err != nil {
		// Everything here is embedded at build time, so this is a
		// programming error the package tests catch before a release.
		panic("web: " + err.Error())
	}
	h := &uiHolder{}
	h.store(u)
	for _, p := range set.Pages {
		srv.HandleRaw(p.pattern(), api.Public, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.load().servePage(w, r, p)
		}))
	}
	for _, o := range set.OldURLs {
		srv.HandleRaw("GET "+o.From, api.Public, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.load().serveOldURL(w, r, o)
		}))
	}
	srv.HandleRaw("GET /static/", api.Public, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.load().assets.ServeHTTP(w, r)
	}))
	// Browsers and tools ask for /favicon.ico whatever the page says.
	srv.HandleRaw("GET /favicon.ico", api.Public, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.load().assets.serveFile(w, r, "icon-192.png", false)
	}))
	return h
}

// newUI reads one set from fsys, which holds templates/ and static/ as the
// embed does. It opens nothing that belongs to another set.
func newUI(srv *api.Server, set uiSet, fsys fs.FS) (*ui, error) {
	if len(set.Pages) == 0 {
		return nil, fmt.Errorf("UI set %q has no pages", set.Name)
	}
	static, err := fs.Sub(fsys, "static")
	if err != nil {
		return nil, err
	}
	assets, err := newAssetStore(static, set.servesStatic)
	if err != nil {
		return nil, err
	}
	u := &ui{srv: srv, set: set, assets: assets, tmpl: map[string]*template.Template{}}
	for _, p := range set.Pages {
		if p.Nav {
			u.nav = append(u.nav, p)
		}
	}
	sprite := spriteHTML()
	if set.Current {
		icons, err := iconsUsed(fsys, set, assets)
		if err != nil {
			return nil, err
		}
		sprite = spriteHTML(icons...)
	}
	base, err := template.New("").Funcs(template.FuncMap{
		"icon":      iconHTML,
		"sprite":    func() template.HTML { return sprite },
		"filters":   filtersHTML,
		"handshake": handshakeHTML,
	}).ParseFS(fsys, path.Join(set.Templates, "layout.html"))
	if err != nil {
		return nil, err
	}
	partials, err := fs.Glob(fsys, set.Partials)
	if err != nil {
		return nil, err
	}
	if len(partials) > 0 {
		if _, err := base.ParseFS(fsys, set.Partials); err != nil {
			return nil, err
		}
	}
	for _, p := range set.Pages {
		t, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := t.ParseFS(fsys, path.Join(set.Templates, "pages", p.Name+".html")); err != nil {
			return nil, err
		}
		u.tmpl[p.Name] = t
	}
	for _, p := range append([]page{set.Installer}, set.Pages...) {
		if p.Script == "" {
			continue
		}
		script := path.Join(set.Static, "js/pages", p.Script+".js")
		if _, ok := assets.files[script]; !ok {
			return nil, fmt.Errorf("page %s: missing script static/%s", p.Name, script)
		}
	}
	if set.Current {
		u.preloads, u.lazy = map[string][]string{}, map[string][]string{}
		for _, p := range append([]page{set.Installer}, set.Pages...) {
			entry := path.Join(set.Static, "js/pages", p.Script+".js")
			static, lazy, err := importGraph(assets.files, entry)
			if err != nil {
				return nil, fmt.Errorf("page %s: %v", p.Name, err)
			}
			var pre []string
			for _, m := range static {
				if m != entry {
					pre = append(pre, strings.TrimPrefix(strings.TrimPrefix(m, set.Static), "/"))
				}
			}
			u.preloads[p.Script], u.lazy[p.Script] = pre, lazy
		}
		u.styles = pageStyles(assets, set)
		u.fonts = preloadFonts(assets, set)
	}
	return u, nil
}

// pageStyles maps each page script to its built stylesheets that the asset
// store holds. A page whose stylesheet is not built yet (between a commit
// that adds one and the regenerated CSS) links none rather than a 404;
// TestAppCSSFresh and the class tests catch it for the active set.
func pageStyles(assets *assetStore, set uiSet) map[string][]string {
	out := map[string][]string{}
	for _, p := range append([]page{set.Installer}, set.Pages...) {
		var have []string
		for _, name := range p.Styles {
			rel := path.Join("pages", name+".css")
			if assets.files[path.Join(set.Static, rel)] != nil {
				have = append(have, rel)
			}
		}
		out[p.Script] = have
	}
	return out
}

// fontPreloads are the faces design/fonts/fonts.json marks "preload": the
// state words' warm cut and the body text. TestFonts holds this list to
// fonts.json. A page preloads one only once the set's stylesheet uses it,
// so a preload is never wasted.
var fontPreloads = []string{"fonts/anybody-warm.woff2", "fonts/monasans-regular.woff2"}

func preloadFonts(assets *assetStore, set uiSet) []string {
	css := assets.files[path.Join(set.Static, "app.css")]
	var out []string
	for _, f := range fontPreloads {
		if css != nil && assets.files[f] != nil && bytes.Contains(css.body, []byte(f)) {
			out = append(out, f)
		}
	}
	return out
}

// filtersHTML inlines REDLINE's heat and cold filters (generated by
// internal/brand from the one heat ramp) and the grey heat gradient the
// signature art fills its sources with. It is 0×0 rather than
// display:none, which would disable the filters in some engines.
func filtersHTML() template.HTML {
	return template.HTML(`<svg class="defs" width="0" height="0" aria-hidden="true" focusable="false"><defs>` +
		`<radialGradient id="g-heat"><stop offset="0" stop-color="#fff"/><stop offset=".18" stop-color="#e8e8e8"/>` +
		`<stop offset=".38" stop-color="#a6a6a6"/><stop offset=".58" stop-color="#5c5c5c"/><stop offset=".78" stop-color="#262626"/>` +
		`<stop offset="1" stop-color="#000"/></radialGradient>` + brand.FilterSVG + `</defs></svg>`)
}

// handshakeHTML is {{handshake .Hostname}}: the 5×5 mark the TV draws next
// to the setup code, so the phone and the screen can be matched at a
// glance (MASTER-PLAN §4.4). It depends on the hostname only.
func handshakeHTML(host string) template.HTML {
	hs := brand.HandshakeFor(host)
	hex := func(c interface{ RGBA() (r, g, b, a uint32) }) string {
		r, g, b, _ := c.RGBA()
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="handshake" viewBox="0 0 5 5" width="40" height="40" aria-hidden="true" focusable="false"><rect width="5" height="5" fill="%s"/><path fill="%s" d="`, hex(hs.Ground), hex(hs.On))
	for r := range 5 {
		for c := range 5 {
			if hs.Cells[r][c] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", c, r)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String())
}

// static is the versioned URL prefix of the set's own files.
func (u *ui) static() string { return path.Join(u.assets.prefix(), u.set.Static) }

// cleanQuery is the request's query with a setup code from another site
// dropped. Another site can put ?code= in a URL (an <img>, or a popup it
// keeps re-navigating); honouring it would let that site lock this browser
// out of setup with wrong codes, so it is dropped before the cookie, the
// form or the page script can use it. Someone who followed such a link
// types the code.
func cleanQuery(r *http.Request) (rawQuery string, foreign bool) {
	rawQuery = r.URL.RawQuery
	if q := r.URL.Query(); q.Has("code") && !api.FirstPartyNavigation(r) {
		q.Del("code")
		rawQuery, foreign = q.Encode(), true
	}
	return rawQuery, foreign
}

// serveOldURL answers an earlier UI's path with 303 to its page now, the
// query kept and a fragment added (/pair → /devices#pair). On the ISO every
// path goes to /setup, as every page does.
func (u *ui) serveOldURL(w http.ResponseWriter, r *http.Request, o oldURL) {
	rawQuery, _ := cleanQuery(r)
	to, frag, _ := strings.Cut(o.To, "#")
	if u.srv.Options().Installer {
		to, frag = "/setup", ""
	}
	loc := u.base + to
	if rawQuery != "" {
		loc += "?" + rawQuery
	}
	if frag != "" {
		loc += "#" + frag
	}
	h := w.Header()
	h.Set("Location", loc)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusSeeOther)
}

func (u *ui) servePage(w http.ResponseWriter, r *http.Request, p page) {
	installer := u.srv.Options().Installer
	rawQuery, foreignCode := cleanQuery(r)
	withQuery := func(path string) string {
		if rawQuery != "" {
			return path + "?" + rawQuery
		}
		return path
	}
	// The live ISO has one job; every other page would only fail
	// against the missing OS routes. ?code= from a QR code is kept.
	if installer && p.Name != "setup" {
		http.Redirect(w, r, withQuery("/setup"), http.StatusSeeOther)
		return
	}
	if foreignCode && p.Name == "setup" {
		http.Redirect(w, r, withQuery(p.Path), http.StatusSeeOther)
		return
	}
	d := u.data(p)
	d.Installer = installer
	if p.Name == "setup" {
		u.prepareSetup(w, r, &d)
	}
	u.render(w, r, d)
}

// data is the page's pageData before the request's own parts.
func (u *ui) data(p page) pageData {
	d := pageData{
		Page:    p,
		Nav:     u.nav,
		Tabs:    u.nav,
		Static:  u.static(),
		Shared:  u.assets.prefix(),
		Base:    u.base,
		Version: config.BinaryVersion,
	}
	for i, q := range u.set.Pages {
		if q.Path == p.Path {
			d.Order = i
		}
	}
	switch {
	case p.Nav:
		d.Tab = p.Name
	case p.Tab != "":
		d.Tab = p.Tab
	}
	if u.set.Current {
		d.Preloads = u.preloads[p.Script]
		d.Styles = u.styles[p.Script]
		d.Fonts = u.fonts
		d.Theme.Dark, d.Theme.Light = brand.ThemeColorDark, brand.ThemeColorLight
		d.Hostname, _, _ = strings.Cut(config.Hostname(), ".")
		d.Version = imageVersion()
	}
	return d
}

// imageVersion is the version the TV shows: the OS image's, else vosd's.
func imageVersion() string {
	if ii, err := config.LoadImageInfo(); err == nil && ii.Version != "" {
		return ii.Version
	}
	return config.BinaryVersion
}

// prepareSetup handles the setup code for both setup flavours. The code
// arrives from the QR code as ?code=; the page prefills its field with it
// and sends it as X-VOS-Setup, and when the api package offers it we also
// set the vos_setup cookie, which EventSource needs (it cannot send headers).
func (u *ui) prepareSetup(w http.ResponseWriter, r *http.Request, d *pageData) {
	d.NeedCode = u.srv.SetupCode() != "" && !u.srv.SetupWaived()
	if code := cleanCode(r.URL.Query().Get("code")); code != "" {
		d.Code = code
		trySetSetupCookie(u.srv, w, r, code)
	}
	if d.Installer {
		d.Page = u.set.Installer
		if u.set.Current {
			d.Preloads = u.preloads[d.Page.Script]
			d.Styles = u.styles[d.Page.Script]
		}
		d.Timezones = timezoneGroups()
		// After the install reboots, the page waits for the new system at
		// http://<hostname>.local, a different origin. Widen connect-src for
		// this page only; everything else keeps the contract's policy.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; connect-src 'self' http://*.local; frame-ancestors 'none'")
	}
}

// setupCookieSetter is the optional api.Server method that turns a setup
// code into the vos_setup cookie. Asserting it keeps this package building
// against api versions that do not have it yet.
type setupCookieSetter interface {
	SetSetupCookie(w http.ResponseWriter, r *http.Request, code string) bool
}

func trySetSetupCookie(target any, w http.ResponseWriter, r *http.Request, code string) bool {
	if s, ok := target.(setupCookieSetter); ok {
		return s.SetSetupCookie(w, r, code)
	}
	return false
}

// cleanCode trims a setup code from the URL and bounds its length; it is
// only ever echoed back into an escaped form field.
func cleanCode(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 32 {
		return ""
	}
	return s
}

// render executes the page into a buffer first, so a template error becomes
// a clean 500 instead of half a page.
func (u *ui) render(w http.ResponseWriter, r *http.Request, d pageData) {
	var buf bytes.Buffer
	if err := u.tmpl[d.Page.Name].ExecuteTemplate(&buf, "layout", d); err != nil {
		log.Printf("web: render %s: %v", d.Page.Name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// Pages name versioned assets, so they must be revalidated after an
	// update; the assets themselves are immutable. no-cache (not no-store)
	// keeps pages in the back/forward cache.
	h.Set("Cache-Control", "no-cache")
	if !u.set.Compress {
		w.Write(buf.Bytes())
		return
	}
	// The page carries no secret (the CSRF token comes from /auth/me) and
	// the only reflected input, ?code=, is dropped on cross-site requests,
	// so compressing it leaks nothing.
	body := buf.Bytes()
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h.Add("Vary", "Accept-Encoding")
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		body, etag = u.gzipped(etag, body), strings.TrimSuffix(etag, `"`)+`-gz"`
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("ETag", etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// gzipped returns the page compressed, from a small cache keyed by its
// ETag: a page's HTML is the same on every request (only /setup?code= and
// the installer differ), so each is compressed once, at the best level, and
// served from memory after that. The cache starts over past 64 bodies, so a
// flood of distinct setup codes cannot grow it.
func (u *ui) gzipped(etag string, body []byte) []byte {
	if b, ok := u.gz.Load(etag); ok {
		return b.([]byte)
	}
	if u.gzN.Add(1) > 64 {
		u.gz.Clear()
		u.gzN.Store(1)
	}
	b := gzipPage(body)
	u.gz.Store(etag, b)
	return b
}

// gzipPage compresses a page as render serves it (TestPageBudgets measures
// the same bytes).
func gzipPage(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}
