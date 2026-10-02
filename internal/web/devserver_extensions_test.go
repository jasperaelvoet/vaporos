package web

import (
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/auth"
)

// The dev server's fake of the extensions (internal/extensions): the cards
// of GET /extensions and what a restart changes. Document:
// base/extensions.json. Installing one downloads it for a few seconds,
// publishing extensions.state as it goes, then waits for a restart, which
// mounts what is wanted and drops the rest (bootExtensions).

func init() {
	fakeResources["extensions"] = "/extensions"
	fakeTypes["extensions"] = func() any { return new(fakeExtensionsDoc) }
	eventTopics = appendMissing(eventTopics, "extensions.state")
	restartKinds = appendMissing(restartKinds, "extensions")
	appendixB["presets"] = appendMissing(appendixB["presets"], "extensions-installing", "extensions-restart", "extensions-attention")
}

func appendMissing(l []string, s ...string) []string {
	for _, x := range s {
		if !slices.Contains(l, x) {
			l = append(l, x)
		}
	}
	return l
}

// fakeExtensionsDoc is GET /extensions as docs/CONTRACTS.md has it, until
// the real handler's type can stand in for it.
type fakeExtensionsDoc struct {
	Extensions []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Summary  string `json:"summary"`
		Category string `json:"category"`
		Core     bool   `json:"core"`
		Upstream struct {
			Name    string `json:"name"`
			URL     string `json:"url"`
			License string `json:"license"`
		} `json:"upstream"`
		Caveats  []string `json:"caveats"`
		State    string   `json:"state"`
		Wanted   bool     `json:"wanted"`
		Mounted  bool     `json:"mounted"`
		Size     int64    `json:"size"`
		Progress *struct {
			Bytes int64 `json:"bytes"`
			Total int64 `json:"total"`
		} `json:"progress"`
		Reason      string   `json:"reason"`
		Permissions []string `json:"permissions"`
		RunsAsRoot  bool     `json:"runs_as_root"`
		Downloads   []struct {
			What     string `json:"what"`
			From     string `json:"from"`
			Checked  string `json:"checked"`
			RunsCode bool   `json:"runs_code"`
			When     string `json:"when"`
		} `json:"downloads"`
		Settings []struct {
			Key     string   `json:"key"`
			Type    string   `json:"type"`
			Label   string   `json:"label"`
			Help    string   `json:"help"`
			Restart bool     `json:"restart"`
			Choices []string `json:"choices"`
			Value   any      `json:"value"`
		} `json:"settings"`
		Actions []struct {
			Name    string `json:"name"`
			Label   string `json:"label"`
			Confirm *struct {
				Title  string `json:"title"`
				Body   string `json:"body"`
				Button string `json:"button"`
				Tone   string `json:"tone"`
			} `json:"confirm"`
		} `json:"actions"`
		Web *struct {
			Port  int    `json:"port"`
			Label string `json:"label"`
		} `json:"web"`
		Status []struct {
			Text string `json:"text"`
			Tone string `json:"tone"`
		} `json:"status"`
		Copy struct {
			Install string `json:"install"`
			Remove  string `json:"remove"`
		} `json:"copy"`
		Requires      []string `json:"requires"`
		RequiredBy    []string `json:"required_by"`
		NeedsPassword bool     `json:"needs_password"`
	} `json:"extensions"`
	Restart struct {
		Needed bool   `json:"needed"`
		Auto   bool   `json:"auto"`
		Reason string `json:"reason"`
	} `json:"restart"`
}

// fakeAheadOfContract are the fake's extension routes while
// docs/CONTRACTS.md has no row for them: the real handlers come with the
// backend's half of the control center. Each entry stops counting once the
// contract lists its route, so the contract tests then hold it to the row.
var fakeAheadOfContract = map[string]bool{
	"GET /extensions": true, "POST /extensions/{id}": true, "DELETE /extensions/{id}": true,
	"PUT /extensions/{id}/settings": true, "POST /extensions/{id}/actions/{name}": true,
	"POST /extensions/{id}/retry": true, "POST /extensions/skip-once": true,
}

// aheadOfContract reports whether key ("METHOD /path") is such a route.
func aheadOfContract(c *contractDoc, key string) bool {
	method, path, _ := strings.Cut(key, " ")
	_, listed := c.route(method, path)
	return fakeAheadOfContract[key] && !listed
}

// extTick is how often an install moves on; it takes eight steps.
const extTick = 400 * time.Millisecond

func (f *devFake) extensionRoutes(add fakeAdder) {
	add("GET", "/extensions", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.doc("extensions") })
	add("POST", "/extensions/skip-once", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return fakeOK })
	add("POST", "/extensions/{id}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Options  map[string]any `json:"options"`
			Password *string        `json:"password"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		x := f.extLocked(w, r.PathValue("id"))
		if x == nil {
			return nil
		}
		switch {
		case x["state"] == "not-in-this-version":
			return extRefuse(w, http.StatusConflict, "%s is not in this version of VaporOS", x)
		case x["core"] == true:
			return extRefuse(w, http.StatusConflict, "%s is part of VaporOS and always on", x)
		}
		if !checkExtSettings(w, x, req.Options) || (x["needs_password"] == true && !checkExtPassword(w, req.Password)) {
			return nil
		}
		applyExtSettings(x, req.Options)
		for _, id := range append(asStrings(x["requires"]), asStr(x["id"])) {
			if y := findExt(f.doc("extensions"), id); y != nil && y["wanted"] != true && y["core"] != true {
				f.wantExtLocked(y)
			}
		}
		return f.extChangedLocked()
	})
	add("DELETE", "/extensions/{id}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		x := f.extLocked(w, r.PathValue("id"))
		if x == nil {
			return nil
		}
		if x["core"] == true {
			return extRefuse(w, http.StatusConflict, "%s is part of VaporOS and always on", x)
		}
		var by []string
		for _, y := range asList(f.doc("extensions")["extensions"]) {
			if y := asObj(y); (y["wanted"] == true || y["mounted"] == true) && containsString(asList(y["requires"]), asStr(x["id"])) {
				by = append(by, asStr(y["name"]))
			}
		}
		if len(by) > 0 {
			api.Error(w, http.StatusConflict, "%s is needed by %s", asStr(x["name"]), strings.Join(by, ", "))
			return nil
		}
		x["wanted"], x["progress"], x["reason"] = false, nil, ""
		x["state"] = "not-installed"
		if x["mounted"] == true {
			x["state"] = "restart-needed"
		}
		if r.URL.Query().Get("purge") == "1" {
			for _, s := range asList(x["settings"]) {
				if s := asObj(s); s["type"] == "bool" {
					s["value"] = false
				}
			}
		}
		return f.extChangedLocked()
	})
	add("PUT", "/extensions/{id}/settings", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Settings map[string]any `json:"settings"`
			Password *string        `json:"password"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		x := f.extLocked(w, r.PathValue("id"))
		if x == nil || !checkExtSettings(w, x, req.Settings) {
			return nil
		}
		restart := false
		for _, s := range asList(x["settings"]) {
			if s := asObj(s); s["restart"] == true {
				if v, ok := req.Settings[asStr(s["key"])]; ok && v != s["value"] {
					restart = true
				}
			}
		}
		// Only a setting that feeds module_options asks for the password;
		// the fake takes every restart setting of such an extension for one.
		if restart && x["needs_password"] == true && x["runs_as_root"] != true && !checkExtPassword(w, req.Password) {
			return nil
		}
		applyExtSettings(x, req.Settings)
		if restart && x["mounted"] == true && x["wanted"] == true {
			x["state"] = "restart-needed"
		}
		return f.extChangedLocked()
	})
	add("POST", "/extensions/{id}/actions/{name}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Args map[string]any `json:"args"`
		}
		if r.ContentLength != 0 && !strictBody(w, r, &req) {
			return nil
		}
		x := f.extLocked(w, r.PathValue("id"))
		if x == nil {
			return nil
		}
		name := r.PathValue("name")
		if !slices.ContainsFunc(asList(x["actions"]), func(a any) bool { return asStr(asObj(a)["name"]) == name }) {
			api.Error(w, http.StatusNotFound, "%s has no action %q", asStr(x["name"]), name)
			return nil
		}
		if x["mounted"] != true {
			return extRefuse(w, http.StatusConflict, "%s is not installed yet", x)
		}
		return f.doc("extensions")
	})
	add("POST", "/extensions/{id}/retry", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		x := f.extLocked(w, r.PathValue("id"))
		if x == nil {
			return nil
		}
		if x["state"] == "needs-attention" && (x["wanted"] == true || x["core"] == true) {
			x["reason"] = ""
			f.wantExtLocked(x)
		}
		return f.extChangedLocked()
	})
}

// extLocked is the extension id, or nil after answering 404.
func (f *devFake) extLocked(w http.ResponseWriter, id string) map[string]any {
	if x := findExt(f.doc("extensions"), id); x != nil {
		return x
	}
	api.Error(w, http.StatusNotFound, "unknown extension %s", id)
	return nil
}

func findExt(doc map[string]any, id string) map[string]any {
	for _, x := range asList(doc["extensions"]) {
		if x := asObj(x); asStr(x["id"]) == id {
			return x
		}
	}
	return nil
}

func extRefuse(w http.ResponseWriter, code int, format string, x map[string]any) any {
	api.Error(w, code, format, asStr(x["name"]))
	return nil
}

func asStrings(v any) []string {
	var out []string
	for _, s := range asList(v) {
		out = append(out, asStr(s))
	}
	return out
}

// checkExtSettings answers 400 unless every key is one of x's settings
// with a value of its type.
func checkExtSettings(w http.ResponseWriter, x map[string]any, set map[string]any) bool {
	for k, v := range set {
		var s map[string]any
		for _, y := range asList(x["settings"]) {
			if asStr(asObj(y)["key"]) == k {
				s = asObj(y)
			}
		}
		if s == nil {
			api.Error(w, http.StatusBadRequest, "unknown setting %q", k)
			return false
		}
		str, isStr := v.(string)
		ok := false
		switch s["type"] {
		case "bool":
			_, ok = v.(bool)
		case "choice":
			ok = isStr && containsString(asList(s["choices"]), str)
		case "disk":
			ok = isStr && (str == "" || fakeFSUUIDRe.MatchString(str))
		}
		if !ok {
			api.Error(w, http.StatusBadRequest, "bad value for %s", k)
			return false
		}
	}
	return true
}

func applyExtSettings(x map[string]any, set map[string]any) {
	for _, s := range asList(x["settings"]) {
		if s := asObj(s); s != nil {
			if v, ok := set[asStr(s["key"])]; ok {
				s["value"] = v
			}
		}
	}
}

// checkExtPassword is the admin password check (as POST /auth/password
// makes it, less the login limit): 403 when it is missing or wrong.
func checkExtPassword(w http.ResponseWriter, pw *string) bool {
	if pw == nil || *pw == "" {
		api.Error(w, http.StatusForbidden, "the VaporOS password is needed")
		return false
	}
	ok, err := auth.VerifyAdmin(*pw)
	if err != nil {
		api.Error(w, http.StatusConflict, "no admin password is set yet; finish setup first")
		return false
	}
	if !ok {
		api.Error(w, http.StatusForbidden, "wrong password")
		return false
	}
	return true
}

// wantExtLocked adds x to the wanted set: it downloads, then waits for a
// restart (one already mounted waits for nothing).
func (f *devFake) wantExtLocked(x map[string]any) {
	x["wanted"], x["reason"] = true, ""
	if x["mounted"] == true {
		x["state"] = "installed"
		return
	}
	x["state"] = "installing"
	x["progress"] = map[string]any{"bytes": 0, "total": math.Max(asNum(x["size"]), 1)}
	if f.d != nil {
		go f.runExtInstall(asStr(x["id"]))
	}
}

// runExtInstall moves an install along until it waits for a restart.
func (f *devFake) runExtInstall(id string) {
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-time.After(extTick):
		}
		f.mu.Lock()
		x := findExt(f.doc("extensions"), id)
		if x == nil || x["state"] != "installing" {
			f.mu.Unlock()
			return
		}
		p := asObj(x["progress"])
		total := asNum(p["total"])
		done := math.Min(total, asNum(p["bytes"])+math.Ceil(total/8))
		x["progress"] = map[string]any{"bytes": done, "total": total}
		if done >= total {
			x["state"], x["progress"] = "restart-needed", nil
		}
		f.extChangedLocked()
		f.mu.Unlock()
		if done >= total {
			return
		}
	}
}

// extChangedLocked brings restart.needed up to date, publishes the
// document and returns it.
func (f *devFake) extChangedLocked() any {
	doc := f.doc("extensions")
	rs := asObj(doc["restart"])
	needed := false
	for _, x := range asList(doc["extensions"]) {
		needed = needed || asObj(x)["state"] == "restart-needed"
	}
	rs["needed"] = needed
	doc["restart"] = rs
	f.emitLocked("extensions.state", doc)
	return doc
}

// extensionsRestart adds the extensions to GET /status's restart reasons.
func extensionsRestart(restart map[string]any, ext map[string]any) map[string]any {
	if asObj(ext["restart"])["needed"] != true {
		return restart
	}
	restart["reasons"] = append(asList(restart["reasons"]), map[string]any{"kind": "extensions"})
	restart["needed"] = true
	return restart
}

// bootExtensions is a restart of the extensions: what is wanted and ready
// is mounted, and what is not wanted any more is gone. A download under way
// starts over (resumeExtInstallsLocked).
func bootExtensions(docs map[string]any) {
	doc := asObj(docs["extensions"])
	for _, x := range asList(doc["extensions"]) {
		x := asObj(x)
		switch x["state"] {
		case "installing":
			x["progress"] = map[string]any{"bytes": 0, "total": math.Max(asNum(x["size"]), 1)}
		case "restart-needed":
			wanted := x["wanted"] == true || x["core"] == true
			x["mounted"], x["progress"] = wanted, nil
			x["state"] = map[bool]string{true: "installed", false: "not-installed"}[wanted]
		}
	}
	if rs, ok := doc["restart"].(map[string]any); ok {
		rs["needed"] = false
	}
}

// resumeExtInstallsLocked moves the downloads of a preset or a new boot on.
func (f *devFake) resumeExtInstallsLocked() {
	if f.d == nil {
		return
	}
	for _, x := range asList(f.doc("extensions")["extensions"]) {
		if x := asObj(x); x["state"] == "installing" {
			go f.runExtInstall(asStr(x["id"]))
		}
	}
}
