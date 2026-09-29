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
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

//go:embed templates static
var content embed.FS

// page is one server-rendered route.
type page struct {
	Name   string // template file under templates/pages and <html data-page>
	Path   string // URL path
	Title  string // <title> and <h1>
	Label  string // navigation label, when shorter than Title
	Lead   string // one-line explanation under the heading
	Script string // static/js/pages/<Script>.js
	Icon   string // icon name (icons.go)
	Nav    bool   // listed in the main navigation
	Bare   bool   // centred card without navigation (sign-in, setup)
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

// pages lists every page in navigation order. CONTRACTS.md "Pages".
var pages = []page{
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

// installerSetup replaces the setup page on the live ISO: same route, the
// full installer wizard instead of the first-run password form.
var installerSetup = page{Name: "setup", Path: "/setup", Title: "Install VaporOS", Script: "install", Bare: true}

// pageData is what every template sees.
type pageData struct {
	Page      page
	Nav       []page
	Static    string // versioned prefix of this UI's own files, "/static/<hash>/legacy"
	Shared    string // versioned prefix of the files every UI shares (icons, manifest), "/static/<hash>"
	Version   string
	Installer bool
	NeedCode  bool   // a setup code guards /setup's API calls
	Code      string // setup code from ?code=, to prefill the field
	Timezones []tzGroup
}

type ui struct {
	srv    *api.Server
	assets *assetStore
	tmpl   map[string]*template.Template
	nav    []page
}

// Register adds the pages and static assets to srv.
func Register(srv *api.Server) {
	u, err := newUI(srv)
	if err != nil {
		// Everything here is embedded at build time, so this is a
		// programming error the package tests catch before a release.
		panic("web: " + err.Error())
	}
	for _, p := range pages {
		srv.HandleRaw(p.pattern(), api.Public, u.pageHandler(p))
	}
	srv.HandleRaw("GET /static/", api.Public, u.assets)
	// Browsers and tools ask for /favicon.ico whatever the page says.
	srv.HandleRaw("GET /favicon.ico", api.Public, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.assets.serveFile(w, r, "icon-192.png", false)
	}))
}

func newUI(srv *api.Server) (*ui, error) {
	static, err := fs.Sub(content, "static")
	if err != nil {
		return nil, err
	}
	assets, err := newAssetStore(static)
	if err != nil {
		return nil, err
	}
	u := &ui{srv: srv, assets: assets, tmpl: map[string]*template.Template{}}
	for _, p := range pages {
		if p.Nav {
			u.nav = append(u.nav, p)
		}
	}
	base, err := template.New("").Funcs(template.FuncMap{
		"icon":   iconHTML,
		"sprite": spriteHTML,
	}).ParseFS(content, "templates/legacy/layout.html")
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		t, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := t.ParseFS(content, "templates/legacy/pages/"+p.Name+".html"); err != nil {
			return nil, err
		}
		u.tmpl[p.Name] = t
	}
	for _, p := range append(pages, installerSetup) {
		if _, ok := assets.files["legacy/js/pages/"+p.Script+".js"]; !ok {
			return nil, fmt.Errorf("page %s: missing script legacy/js/pages/%s.js", p.Name, p.Script)
		}
	}
	return u, nil
}

func (u *ui) pageHandler(p page) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installer := u.srv.Options().Installer
		rawQuery := r.URL.RawQuery
		foreignCode := false
		if q := r.URL.Query(); q.Has("code") && !api.FirstPartyNavigation(r) {
			// Another site put a setup code in this URL (an <img>, or a
			// popup it keeps re-navigating). Honouring it would let that
			// site lock this browser out of setup with wrong codes, so it
			// is dropped before the cookie, the form or the page script
			// can use it. Someone who followed such a link types the code.
			q.Del("code")
			rawQuery, foreignCode = q.Encode(), true
		}
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
		d := pageData{
			Page:      p,
			Nav:       u.nav,
			Static:    u.assets.prefix() + "/legacy",
			Shared:    u.assets.prefix(),
			Version:   config.BinaryVersion,
			Installer: installer,
		}
		if p.Name == "setup" {
			u.prepareSetup(w, r, &d)
		}
		u.render(w, d)
	}
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
		d.Page = installerSetup
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
func (u *ui) render(w http.ResponseWriter, d pageData) {
	var buf bytes.Buffer
	if err := u.tmpl[d.Page.Name].ExecuteTemplate(&buf, "layout", d); err != nil {
		log.Printf("web: render %s: %v", d.Page.Name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// Pages name versioned assets, so they must be revalidated after an
	// update; the assets themselves are immutable.
	h.Set("Cache-Control", "no-cache")
	w.Write(buf.Bytes())
}
