package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// The dev server's fake of the extensions (internal/extensions routes.go):
// one document, base/extensions.json, as GET /extensions answers it. Every
// change answers the document and publishes it as extensions.state, as
// vosd does; an added extension downloads for a few seconds while the dev
// server runs, then waits for a restart, which starts it (bootExtensions).
// The fake knows the cards only: an extension's conflicts and its kernel
// module options are not in them, so it refuses no conflict and asks the
// password for a restart setting of an extension that needs one.

func (f *devFake) extensionsRoutes(add fakeAdder) {
	add("GET", "/extensions", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.doc("extensions") })
	add("POST", "/extensions/skip-once", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return fakeOK })
	add("POST", "/extensions/{id}", api.Authed, f.extInstall)
	add("DELETE", "/extensions/{id}", api.Authed, f.extRemove)
	add("PUT", "/extensions/{id}/settings", api.Authed, f.extSettings)
	add("POST", "/extensions/{id}/actions/{name}", api.Authed, f.extAction)
	add("POST", "/extensions/{id}/retry", api.Authed, f.extRetry)
}

// optionalBody decodes a body that may be empty, as the real handlers do.
func optionalBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := api.ReadJSON(r, v); err != nil && !errors.Is(err, io.EOF) {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return false
	}
	return true
}

func refused(w http.ResponseWriter, code int, msg string) any {
	api.Error(w, code, "%s", msg)
	return nil
}

// extLocked is id's card, or nil.
func (f *devFake) extLocked(id string) map[string]any {
	for _, x := range asList(f.doc("extensions")["extensions"]) {
		if m := asObj(x); asStr(m["id"]) == id {
			return m
		}
	}
	return nil
}

// checkExtSettings is the real check (extensions.CheckSetting) against a
// card's settings.
func checkExtSettings(x map[string]any, change map[string]any) (map[string]any, error) {
	out := map[string]any{}
	keys := make([]string, 0, len(change))
	for k := range change {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := change[k]
		var st *descriptor.Setting
		for _, s := range asList(x["settings"]) {
			if m := asObj(s); asStr(m["key"]) == k {
				st = &descriptor.Setting{Key: k, Type: asStr(m["type"]), Restart: m["restart"] == true}
				for _, c := range asList(m["choices"]) {
					st.Choices = append(st.Choices, asStr(c))
				}
			}
		}
		if st == nil {
			return nil, fmt.Errorf("unknown setting %q", k)
		}
		c, err := extensions.CheckSetting(*st, v)
		if err != nil {
			return nil, err
		}
		out[k] = c
	}
	return out, nil
}

// setExtValues stores values on x's settings and reports whether one that
// takes a restart changed.
func setExtValues(x map[string]any, values map[string]any) (restart bool) {
	for _, s := range asList(x["settings"]) {
		m := asObj(s)
		if v, ok := values[asStr(m["key"])]; ok {
			restart = restart || (m["restart"] == true && m["value"] != v)
			m["value"] = v
		}
	}
	return restart
}

// extPassword is the admin password check: none, or a wrong one, is 403.
func extPassword(w http.ResponseWriter, given, what string) bool {
	switch given {
	case devPassword:
		return true
	case "":
		api.Error(w, http.StatusForbidden, "%s needs the admin password", what)
	default:
		api.Error(w, http.StatusForbidden, "the password is wrong")
	}
	return false
}

func (f *devFake) extInstall(w http.ResponseWriter, r *http.Request) any {
	var req struct {
		Options  map[string]any `json:"options"`
		Password string         `json:"password"`
	}
	if !optionalBody(w, r, &req) {
		return nil
	}
	id := r.PathValue("id")
	x := f.extLocked(id)
	if x == nil {
		return refused(w, http.StatusNotFound, fmt.Sprintf("no extension %q", id))
	}
	name := asStr(x["name"])
	switch {
	case x["state"] == extensions.StateNotInThisVersion:
		return refused(w, http.StatusConflict, fmt.Sprintf("This version of VaporOS does not have %s.", name))
	case x["core"] == true:
		return refused(w, http.StatusConflict, name+" is part of VaporOS and always on.")
	}
	values, err := checkExtSettings(x, req.Options)
	if err != nil {
		return refused(w, http.StatusBadRequest, err.Error())
	}
	if x["needs_password"] == true && x["wanted"] != true && !extPassword(w, req.Password, "Adding "+name) {
		return nil
	}
	setExtValues(x, values)
	for _, r := range asList(x["requires"]) {
		if dep := f.extLocked(asStr(r)); dep != nil && dep["core"] != true && dep["wanted"] != true {
			f.addExtLocked(dep)
		}
	}
	if x["wanted"] != true {
		f.addExtLocked(x)
	}
	return f.extChangedLocked()
}

// addExtLocked marks x wanted: a removed one that still runs is back, and
// any other downloads while the dev server runs (else it is downloaded).
func (f *devFake) addExtLocked(x map[string]any) {
	x["wanted"], x["reason"], x["progress"] = true, "", nil
	switch {
	case x["mounted"] == true:
		x["state"] = extensions.StateInstalled
	case f.d != nil:
		x["state"] = extensions.StateInstalling
		x["progress"] = map[string]any{"bytes": 0, "total": x["size"]}
		go f.downloadExt(asStr(x["id"]))
	default:
		x["state"] = extensions.StateRestartNeeded
	}
}

// downloadExt walks an added extension's download in eight steps, then it
// waits for a restart.
func (f *devFake) downloadExt(id string) {
	for step := 1; step <= 8; step++ {
		select {
		case <-f.ctx.Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
		f.mu.Lock()
		x := f.extLocked(id)
		if x == nil || x["state"] != extensions.StateInstalling {
			f.mu.Unlock()
			return
		}
		total := int64(asNum(x["size"]))
		x["progress"] = map[string]any{"bytes": total * int64(step) / 8, "total": total}
		if step == 8 {
			x["state"], x["progress"] = extensions.StateRestartNeeded, nil
		}
		f.extChangedLocked()
		f.mu.Unlock()
	}
}

func (f *devFake) extRemove(w http.ResponseWriter, r *http.Request) any {
	switch r.URL.Query().Get("purge") {
	case "", "0", "1":
	default:
		return refused(w, http.StatusBadRequest, "purge must be 0 or 1")
	}
	id := r.PathValue("id")
	x := f.extLocked(id)
	if x == nil {
		return refused(w, http.StatusNotFound, fmt.Sprintf("no extension %q", id))
	}
	name := asStr(x["name"])
	if x["core"] == true {
		return refused(w, http.StatusConflict, name+" is part of VaporOS and cannot be removed.")
	}
	if deps := f.requiredByLocked(id); len(deps) > 0 {
		var names []string
		for _, d := range deps {
			names = append(names, asStr(f.extLocked(d)["name"]))
		}
		n := joinNames(names)
		return refused(w, http.StatusConflict, fmt.Sprintf("%s needs %s. Remove %s first.", n, name, n))
	}
	x["wanted"], x["reason"], x["progress"] = false, "", nil
	x["state"] = extensions.StateNotInstalled
	if x["mounted"] == true {
		x["state"] = extensions.StateRestartNeeded
	}
	return f.extChangedLocked()
}

func (f *devFake) extSettings(w http.ResponseWriter, r *http.Request) any {
	var req struct {
		Settings map[string]any `json:"settings"`
		Password string         `json:"password"`
	}
	if !optionalBody(w, r, &req) {
		return nil
	}
	id := r.PathValue("id")
	x := f.extLocked(id)
	if x == nil {
		return refused(w, http.StatusNotFound, fmt.Sprintf("no extension %q", id))
	}
	values, err := checkExtSettings(x, req.Settings)
	if err != nil {
		return refused(w, http.StatusBadRequest, err.Error())
	}
	before := deepCopyJSON(x["settings"])
	if setExtValues(x, values) {
		if x["needs_password"] == true && !extPassword(w, req.Password, "Changing this setting") {
			x["settings"] = before
			return nil
		}
		if x["mounted"] == true && x["wanted"] == true {
			x["state"] = extensions.StateRestartNeeded
		}
	}
	return f.extChangedLocked()
}

func (f *devFake) extAction(w http.ResponseWriter, r *http.Request) any {
	var req struct {
		Args json.RawMessage `json:"args"`
	}
	if !optionalBody(w, r, &req) {
		return nil
	}
	if len(req.Args) > 0 && req.Args[0] != '{' {
		return refused(w, http.StatusBadRequest, "args must be an object")
	}
	id, action := r.PathValue("id"), r.PathValue("name")
	x := f.extLocked(id)
	if x == nil {
		return refused(w, http.StatusNotFound, fmt.Sprintf("no extension %q", id))
	}
	name := asStr(x["name"])
	if !slices.ContainsFunc(asList(x["actions"]), func(a any) bool { return asStr(asObj(a)["name"]) == action }) {
		return refused(w, http.StatusNotFound, fmt.Sprintf("%s has no action %q", name, action))
	}
	if x["mounted"] != true {
		return refused(w, http.StatusConflict, fmt.Sprintf("%s is not running yet. Restart VaporOS first.", name))
	}
	return f.doc("extensions")
}

func (f *devFake) extRetry(w http.ResponseWriter, r *http.Request) any {
	id := r.PathValue("id")
	x := f.extLocked(id)
	if x == nil {
		return refused(w, http.StatusNotFound, fmt.Sprintf("no extension %q", id))
	}
	if x["state"] == extensions.StateNeedsAttention {
		x["reason"] = ""
		x["state"] = extensions.StateRestartNeeded
		if x["mounted"] == true {
			x["state"] = extensions.StateInstalled
		}
	}
	return f.extChangedLocked()
}

// requiredByLocked is the wanted cards that require id.
func (f *devFake) requiredByLocked(id string) []string {
	var out []string
	for _, c := range asList(f.doc("extensions")["extensions"]) {
		m := asObj(c)
		if (m["wanted"] == true || m["core"] == true) && containsString(asList(m["requires"]), id) {
			out = append(out, asStr(m["id"]))
		}
	}
	return out
}

// extChangedLocked brings required_by and restart up to date, publishes the
// document and returns it.
func (f *devFake) extChangedLocked() any {
	doc := f.doc("extensions")
	refreshExtensions(doc)
	f.emitLocked("extensions.state", doc)
	return doc
}

// refreshExtensions recomputes what follows from the cards: who requires
// whom, and the restart a restart-needed card waits for.
func refreshExtensions(doc map[string]any) {
	cards := asList(doc["extensions"])
	var adding, removing, changing []string
	for _, c := range cards {
		m := asObj(c)
		req := []any{}
		for _, o := range cards {
			om := asObj(o)
			if (om["wanted"] == true || om["core"] == true) && containsString(asList(om["requires"]), asStr(m["id"])) {
				req = append(req, om["id"])
			}
		}
		m["required_by"] = req
		if m["state"] != extensions.StateRestartNeeded {
			continue
		}
		switch name := asStr(m["name"]); {
		case m["mounted"] != true:
			adding = append(adding, name)
		case m["wanted"] != true:
			removing = append(removing, name)
		default:
			changing = append(changing, name)
		}
	}
	rs := asObj(doc["restart"])
	needed := len(adding)+len(removing)+len(changing) > 0
	if !needed {
		doc["restart"] = map[string]any{"needed": false, "auto": false, "reason": ""}
		return
	}
	var parts []string
	if len(adding) > 0 {
		parts = append(parts, "adding "+joinNames(adding))
	}
	if len(removing) > 0 {
		parts = append(parts, "removing "+joinNames(removing))
	}
	if len(changing) > 0 {
		parts = append(parts, "changing the settings of "+joinNames(changing))
	}
	auto := rs["auto"] == true || rs["needed"] != true
	doc["restart"] = map[string]any{"needed": true, "auto": auto, "reason": "Restart to finish " + joinNames(parts) + "."}
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	out := names[0]
	for _, n := range names[1 : len(names)-1] {
		out += ", " + n
	}
	return out + " and " + names[len(names)-1]
}

// bootExtensions is what a restart starts: the extensions a restart-needed
// card waited for are added or gone, and nothing waits any more.
func bootExtensions(docs map[string]any) {
	doc := asObj(docs["extensions"])
	for _, c := range asList(doc["extensions"]) {
		m := asObj(c)
		if m["state"] != extensions.StateRestartNeeded {
			continue
		}
		if m["wanted"] == true {
			m["state"], m["mounted"] = extensions.StateInstalled, true
		} else {
			m["state"], m["mounted"] = extensions.StateNotInstalled, false
		}
	}
	refreshExtensions(doc)
}

// extensionsRestart is GET /status's restart kind "extensions".
func extensionsRestart(doc map[string]any) bool {
	return asObj(doc["restart"])["needed"] == true
}

// The fake adds, removes and changes extensions as vosd answers (CONTRACTS
// "Extensions", Control center), publishes each change, puts the restart
// kind into GET /status, and a restart applies what waited for it.
func TestFakeExtensions(t *testing.T) {
	f, _ := fakeWorld(t, "idle")
	mux := http.NewServeMux()
	for _, routes := range []func(fakeAdder){f.extensionsRoutes, f.statusRoutes} {
		routes(func(method, path string, _ api.Access, h fakeHandler) {
			mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
				f.mu.Lock()
				v := h(w, r)
				f.mu.Unlock()
				if v != nil {
					json.NewEncoder(w).Encode(v)
				}
			})
		})
	}
	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	card := func(doc map[string]any, id string) map[string]any {
		for _, c := range asList(doc["extensions"]) {
			if asStr(asObj(c)["id"]) == id {
				return asObj(c)
			}
		}
		t.Fatalf("no card %s", id)
		return nil
	}
	ch, cancel := f.hub.Subscribe()
	defer cancel()

	if code, ans := do("POST", "/extensions/coolercontrol", `{}`); code != 403 || ans["error"] != "Adding CoolerControl needs the admin password" {
		t.Fatalf("without the password: %d %v", code, ans)
	}
	if code, ans := do("POST", "/extensions/coolercontrol", `{"password":"`+devPassword+`","options":{"overdrive":1}}`); code != 400 || ans["error"] != "overdrive must be true or false" {
		t.Fatalf("bad option: %d %v", code, ans)
	}
	code, doc := do("POST", "/extensions/coolercontrol", `{"password":"`+devPassword+`","options":{"overdrive":true}}`)
	if c := card(doc, "coolercontrol"); code != 200 || c["state"] != "restart-needed" || c["wanted"] != true {
		t.Fatalf("added: %d %v", code, c)
	}
	if r := asObj(doc["restart"]); r["needed"] != true || r["auto"] != true || r["reason"] != "Restart to finish adding CoolerControl." {
		t.Fatalf("restart = %v", r)
	}
	waitEvent(t, ch, "extensions.state", time.Second, func(m map[string]any) bool { return asObj(m["restart"])["needed"] == true })
	_, st := do("GET", "/status", "")
	if rs := asList(asObj(st["restart"])["reasons"]); len(rs) != 1 || asObj(rs[0])["kind"] != "extensions" {
		t.Fatalf("GET /status restart = %v", st["restart"])
	}

	if code, ans := do("DELETE", "/extensions/proton", ""); code != 409 || ans["error"] != "CachyOS Proton is part of VaporOS and cannot be removed." {
		t.Fatalf("removing core: %d %v", code, ans)
	}
	if code, _ := do("POST", "/extensions/truckersmp", `{}`); code != 200 {
		t.Fatal("adding truckersmp")
	}
	_, doc = do("GET", "/extensions", "")
	if rb := card(doc, "proton")["required_by"]; !reflect.DeepEqual(rb, []any{"truckersmp"}) {
		t.Fatalf("proton required_by = %v", rb)
	}

	f.mu.Lock()
	bootExtensions(f.docs)
	f.mu.Unlock()
	_, doc = do("GET", "/extensions", "")
	if c := card(doc, "coolercontrol"); c["state"] != "installed" || c["mounted"] != true || asObj(doc["restart"])["needed"] != false {
		t.Fatalf("after the restart: %v, restart %v", c, doc["restart"])
	}
	if code, ans := do("POST", "/extensions/truckersmp/actions/copy-profiles", `{"args":{}}`); code != 200 {
		t.Fatalf("action: %d %v", code, ans)
	}
	if code, ans := do("POST", "/extensions/coolercontrol/actions/copy-profiles", `{}`); code != 404 || ans["error"] != `CoolerControl has no action "copy-profiles"` {
		t.Fatalf("unknown action: %d %v", code, ans)
	}
	if code, ans := do("PUT", "/extensions/coolercontrol/settings", `{"settings":{"overdrive":false}}`); code != 403 {
		t.Fatalf("restart setting without the password: %d %v", code, ans)
	}
	_, doc = do("PUT", "/extensions/coolercontrol/settings", `{"settings":{"poll":"slow"}}`)
	if c := card(doc, "coolercontrol"); c["state"] != "installed" || asObj(asList(c["settings"])[1])["value"] != "slow" {
		t.Fatalf("plain setting: %v", c)
	}
	_, doc = do("DELETE", "/extensions/coolercontrol?purge=1", "")
	if c := card(doc, "coolercontrol"); c["state"] != "restart-needed" || c["wanted"] != false ||
		asObj(doc["restart"])["reason"] != "Restart to finish removing CoolerControl." {
		t.Fatalf("removed: %v, restart %v", c, doc["restart"])
	}
	if code, _ := do("POST", "/extensions/skip-once", ""); code != 200 {
		t.Fatal("skip-once")
	}
}
