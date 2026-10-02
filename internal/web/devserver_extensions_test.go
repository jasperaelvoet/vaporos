package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// The dev server's fake of the extensions (internal/extensions routes.go):
// one document, base/extensions.json, as GET /extensions answers it. It
// refuses what the real handlers refuse, in their words (TestValidationParity
// holds it to them), and demo/engine.js does the same (TestFakeEnginesAgree).
// Every change answers the document and publishes it as extensions.state;
// an added extension downloads in eight steps while the dev server runs (a
// preset's download too), then waits for a restart, which mounts what is
// wanted and drops the rest (bootExtensions); a restart with skip_once set
// mounts nothing, and the one after starts them again. The cards do not
// say what an extension conflicts with, so the fake refuses no conflict; a
// setting's needs_password says it feeds kernel module options, and a
// card's runs_as_root and module_options whether adding it takes the
// password (its needs_password, which the fake keeps up to date, adds its
// requirements not added yet); a required disk setting without a drive
// refuses the add. A card's web_running follows its mount: on while it is
// mounted and wanted.

// extTick is how often a download moves on.
const extTick = 400 * time.Millisecond

// maxActionArgs is extensions.maxActionArgs.
const maxActionArgs = 64 << 10

// startedOffText is a card's reason after a start without extensions
// (extensions.startedOffText).
const startedOffText = "VaporOS started without extensions this time. Restart to start them again."

func (f *devFake) extensionsRoutes(add fakeAdder) {
	add("GET", "/extensions", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.doc("extensions") })
	add("POST", "/extensions/skip-once", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.extSkipOnce(true) })
	add("DELETE", "/extensions/skip-once", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.extSkipOnce(false) })
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

// extSetting is x's setting key, or nil.
func extSetting(x map[string]any, key string) map[string]any {
	for _, s := range asList(x["settings"]) {
		if m := asObj(s); asStr(m["key"]) == key {
			return m
		}
	}
	return nil
}

// checkExtSettings is the real check (extensions.CheckSetting, keys in
// order) against a card's settings; it returns the values as stored.
func checkExtSettings(x map[string]any, change map[string]any) (map[string]any, error) {
	out := map[string]any{}
	keys := make([]string, 0, len(change))
	for k := range change {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		m := extSetting(x, k)
		if m == nil {
			return nil, fmt.Errorf("unknown setting %q", k)
		}
		st := descriptor.Setting{Key: k, Type: asStr(m["type"]), Restart: m["restart"] == true, Required: m["required"] == true}
		for _, c := range asList(m["choices"]) {
			st.Choices = append(st.Choices, asStr(c))
		}
		v, err := extensions.CheckSetting(st, change[k])
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// moduleSetting is "key feeds x's kernel module options": changing it
// takes the password.
func moduleSetting(x map[string]any, key string) bool {
	s := extSetting(x, key)
	return s != nil && s["needs_password"] == true
}

// ownPassword is whether adding x itself takes the password: it runs as
// root or sets kernel module options.
func ownPassword(x map[string]any) bool {
	return x["runs_as_root"] == true || x["module_options"] == true
}

// extSkipOnce sets or clears the flag that the next restart starts without
// extensions.
func (f *devFake) extSkipOnce(on bool) any {
	f.doc("extensions")["skip_once"] = on
	f.extChangedLocked()
	return fakeOK
}

// settingDefault is a setting's value without a settings file, as far as
// the card tells: false, the first choice, or no disk.
func settingDefault(s map[string]any) any {
	switch asStr(s["type"]) {
	case "bool":
		return false
	case "choice":
		if c := asList(s["choices"]); len(c) > 0 {
			return c[0]
		}
	}
	return ""
}

// extPassword is the admin password check the real handlers make through
// api.Server.Reauth, less the login limit.
func extPassword(w http.ResponseWriter, given, what string) bool {
	if given == "" {
		api.Error(w, http.StatusForbidden, "%s needs the admin password", what)
		return false
	}
	ok, err := auth.VerifyAdmin(given)
	switch {
	case errors.Is(err, auth.ErrNoAdmin):
		api.Error(w, http.StatusConflict, "no admin password is set yet; finish setup first")
	case err != nil:
		api.Error(w, http.StatusServiceUnavailable, "the password was not checked: %v", err)
	case !ok:
		api.Error(w, http.StatusForbidden, "the password is wrong")
	}
	return err == nil && ok
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
	adding := f.addingLocked(x)
	if msg := missingDrive(x, values, adding); msg != "" {
		return refused(w, http.StatusBadRequest, msg)
	}
	needs := false
	for k := range values {
		needs = needs || moduleSetting(x, k)
	}
	for _, a := range adding {
		needs = needs || ownPassword(a)
	}
	if needs && !extPassword(w, req.Password, "Adding "+name) {
		return nil
	}
	for k, v := range values {
		extSetting(x, k)["value"] = v
	}
	for _, a := range adding {
		f.addExtLocked(a)
	}
	return f.extChangedLocked()
}

// missingDrive is why adding x is refused for a drive (extensions'
// checkDrives): x, or a requirement it adds, has a required disk setting
// without one (in values for x, else the card's value); "" when none is.
func missingDrive(x, values map[string]any, adding []map[string]any) string {
	name := asStr(x["name"])
	for _, a := range append(slices.Clone(adding), x) {
		for _, s := range asList(a["settings"]) {
			m := asObj(s)
			if m["type"] != "disk" || m["required"] != true {
				continue
			}
			v := m["value"]
			if nv, ok := values[asStr(m["key"])]; ok && asStr(a["id"]) == asStr(x["id"]) {
				v = nv
			}
			if asStr(v) != "" {
				continue
			}
			if asStr(a["id"]) == asStr(x["id"]) {
				return name + " needs a game drive. Pick one to add it."
			}
			n := asStr(a["name"])
			return fmt.Sprintf("%s needs %s, which needs a game drive. Add %s first.", name, n, n)
		}
	}
	return ""
}

// addingLocked is what adding x adds: x and what it requires, directly or
// not, less what is wanted or core.
func (f *devFake) addingLocked(x map[string]any) []map[string]any {
	var out []map[string]any
	seen := map[string]bool{}
	var walk func(m map[string]any)
	walk = func(m map[string]any) {
		id := asStr(m["id"])
		if seen[id] {
			return
		}
		seen[id] = true
		for _, r := range asList(m["requires"]) {
			if dep := f.extLocked(asStr(r)); dep != nil {
				walk(dep)
			}
		}
		if m["wanted"] != true && m["core"] != true {
			out = append(out, m)
		}
	}
	walk(x)
	return out
}

// addExtLocked marks x wanted: one this boot mounted (removed until the
// restart) is back at once, any other downloads while the dev server runs
// (and without one it is downloaded already).
func (f *devFake) addExtLocked(x map[string]any) {
	x["wanted"], x["reason"], x["progress"] = true, "", nil
	switch {
	case x["mounted"] == true:
		x["state"], x["web_running"] = extensions.StateInstalled, x["web"] != nil
	case f.d != nil:
		x["state"] = extensions.StateInstalling
		x["progress"] = map[string]any{"bytes": 0.0, "total": extTotal(x)}
		go f.runExtInstall(asStr(x["id"]))
	default:
		x["state"] = extensions.StateRestartNeeded
	}
}

func extTotal(x map[string]any) float64 { return math.Max(asNum(x["size"]), 1) }

// runExtInstall moves a download on by an eighth every extTick until it
// waits for a restart.
func (f *devFake) runExtInstall(id string) {
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-time.After(extTick):
		}
		f.mu.Lock()
		x := f.extLocked(id)
		if x == nil || x["state"] != extensions.StateInstalling {
			f.mu.Unlock()
			return
		}
		p := asObj(x["progress"])
		total := asNum(p["total"])
		if total <= 0 {
			total = extTotal(x)
		}
		done := math.Min(total, asNum(p["bytes"])+math.Ceil(total/8))
		x["progress"] = map[string]any{"bytes": done, "total": total}
		if done >= total {
			x["state"], x["progress"] = extensions.StateRestartNeeded, nil
		}
		f.extChangedLocked()
		f.mu.Unlock()
		if done >= total {
			return
		}
	}
}

// resumeExtInstallsLocked moves the downloads of a preset or a new boot on.
func (f *devFake) resumeExtInstallsLocked() {
	if f.d == nil {
		return
	}
	for _, x := range asList(f.doc("extensions")["extensions"]) {
		if m := asObj(x); m["state"] == extensions.StateInstalling {
			go f.runExtInstall(asStr(m["id"]))
		}
	}
}

func (f *devFake) extRemove(w http.ResponseWriter, r *http.Request) any {
	purge := false
	switch r.URL.Query().Get("purge") {
	case "", "0":
	case "1":
		purge = true
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
	x["wanted"], x["reason"], x["progress"], x["web_running"] = false, "", nil, false
	x["state"] = extensions.StateNotInstalled
	if x["mounted"] == true {
		x["state"] = extensions.StateRestartNeeded
	}
	// Purging deletes settings/<id>.json: the settings read as defaults.
	if purge {
		for _, s := range asList(x["settings"]) {
			m := asObj(s)
			m["value"] = settingDefault(m)
		}
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
	modules := false
	for k, v := range values {
		modules = modules || (moduleSetting(x, k) && !reflect.DeepEqual(extSetting(x, k)["value"], v))
	}
	if modules && !extPassword(w, req.Password, "Changing this setting") {
		return nil
	}
	for k, v := range values {
		extSetting(x, k)["value"] = v
	}
	// New module options are a new set, which the next restart tries.
	if modules && x["mounted"] == true && x["wanted"] == true {
		x["state"] = extensions.StateRestartNeeded
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
	if len(req.Args) > maxActionArgs {
		return refused(w, http.StatusBadRequest, "args is too large")
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

// extRetry is "Try again": the failure is forgotten, so the extension
// downloads again and waits for the restart that tries it (one that runs
// is set up again).
func (f *devFake) extRetry(w http.ResponseWriter, r *http.Request) any {
	id := r.PathValue("id")
	x := f.extLocked(id)
	if x == nil {
		return refused(w, http.StatusNotFound, fmt.Sprintf("no extension %q", id))
	}
	if x["state"] == extensions.StateNeedsAttention && (x["wanted"] == true || x["core"] == true) {
		f.addExtLocked(x)
	}
	return f.extChangedLocked()
}

// requiredByLocked is the wanted or core cards that require id.
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
// whom, whether adding one takes the password, and the restart a
// restart-needed card waits for. A restart that is needed may happen by
// itself (restart.auto), as the circuit breaker of a fresh set allows;
// not while the next start leaves the extensions out, which takes the
// restart after it, as the reason says.
func refreshExtensions(doc map[string]any) {
	cards := asList(doc["extensions"])
	byID := map[string]map[string]any{}
	for _, c := range cards {
		byID[asStr(asObj(c)["id"])] = asObj(c)
	}
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
		m["needs_password"] = needsPassword(byID, m)
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
	if len(adding)+len(removing)+len(changing) == 0 {
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
	// With skip_once the next start mounts nothing: the restart after it does.
	if doc["skip_once"] == true {
		doc["restart"] = map[string]any{"needed": true, "auto": false, "reason": "The next start is without extensions. Restart again after it to finish " + joinNames(parts) + "."}
		return
	}
	doc["restart"] = map[string]any{"needed": true, "auto": true, "reason": "Restart to finish " + joinNames(parts) + "."}
}

// needsPassword is whether adding x takes the password: it, or a
// requirement not added yet (directly or not), runs as root or sets kernel
// module options.
func needsPassword(byID map[string]map[string]any, x map[string]any) bool {
	seen := map[string]bool{}
	var walk func(m map[string]any, own bool) bool
	walk = func(m map[string]any, own bool) bool {
		id := asStr(m["id"])
		if seen[id] {
			return false
		}
		seen[id] = true
		if ownPassword(m) && (own || (m["wanted"] != true && m["core"] != true)) {
			return true
		}
		for _, r := range asList(m["requires"]) {
			if dep := byID[asStr(r)]; dep != nil && walk(dep, false) {
				return true
			}
		}
		return false
	}
	return walk(x, true)
}

// joinNames is "A", "A and B" or "A, B and C" (extensions.joinNames).
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// bootExtensions is what a restart starts: what is wanted and was waiting
// (or was left out by the restart before) is mounted, what is not wanted
// any more is gone, and a download under way starts over
// (resumeExtInstallsLocked). With skip_once nothing is mounted, and what
// is wanted needs attention until the next restart.
func bootExtensions(docs map[string]any) {
	doc, ok := docs["extensions"].(map[string]any)
	if !ok {
		return
	}
	skip := doc["skip_once"] == true
	for _, c := range asList(doc["extensions"]) {
		m := asObj(c)
		wanted := m["wanted"] == true || m["core"] == true
		switch {
		case m["state"] == extensions.StateInstalling:
			m["progress"] = map[string]any{"bytes": 0.0, "total": extTotal(m)}
		case m["state"] == extensions.StateRestartNeeded,
			m["state"] == extensions.StateNeedsAttention && m["reason"] == startedOffText:
			m["mounted"], m["progress"], m["reason"] = wanted, nil, ""
			m["state"] = extensions.StateNotInstalled
			if wanted {
				m["state"] = extensions.StateInstalled
			}
		}
		if skip && m["mounted"] == true {
			m["mounted"], m["state"] = false, extensions.StateNotInstalled
			if wanted {
				m["state"], m["reason"] = extensions.StateNeedsAttention, startedOffText
			}
		}
		m["web_running"] = m["mounted"] == true && wanted && m["web"] != nil
	}
	doc["skip_once"] = false
	refreshExtensions(doc)
}

// extensionsRestart is GET /status's restart kind "extensions": none while
// the next start leaves the extensions out.
func extensionsRestart(doc map[string]any) bool {
	return asObj(doc["restart"])["needed"] == true && doc["skip_once"] != true
}

// The fake adds, removes and changes extensions as vosd answers (CONTRACTS
// "Extensions", Control center), publishes each change, puts the restart
// kind into GET /status, and a restart applies what waited for it.
func TestFakeExtensions(t *testing.T) {
	redirectConfig(t, t.TempDir())
	if err := auth.SetAdminPassword("", devPassword); err != nil {
		t.Fatal(err)
	}
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
	if code, ans := do("POST", "/extensions/coolercontrol", `{"password":"nope-nope"}`); code != 403 || ans["error"] != "the password is wrong" {
		t.Fatalf("a wrong password: %d %v", code, ans)
	}
	if code, ans := do("POST", "/extensions/coolercontrol", `{"password":"`+devPassword+`","options":{"gpu_fan_curves":1}}`); code != 400 || ans["error"] != "gpu_fan_curves must be true or false" {
		t.Fatalf("bad option: %d %v", code, ans)
	}
	for _, v := range []string{"-x", "", "/state"} {
		if code, ans := do("PUT", "/extensions/star-citizen/settings", `{"settings":{"disk":"`+v+`"}}`); code != 400 ||
			ans["error"] != "disk must be the system drive (/var) or a game drive (a folder in /var/mnt)" {
			t.Fatalf("a drive %q: %d %v", v, code, ans)
		}
	}
	if code, ans := do("POST", "/extensions/star-citizen", `{}`); code != 400 || ans["error"] != "Star Citizen needs a game drive. Pick one to add it." {
		t.Fatalf("no drive picked: %d %v", code, ans)
	}
	if code, _ := do("PUT", "/extensions/star-citizen/settings", `{"settings":{"disk":"/var/mnt/Games"}}`); code != 200 {
		t.Fatalf("a disk: %d", code)
	}
	code, doc := do("POST", "/extensions/coolercontrol", `{"password":"`+devPassword+`","options":{"gpu_fan_curves":true}}`)
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
	if code, _ := do("POST", "/extensions/truckersmp", ``); code != 200 {
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
	if c := card(doc, "coolercontrol"); c["state"] != "installed" || c["mounted"] != true || c["web_running"] != true || asObj(doc["restart"])["needed"] != false {
		t.Fatalf("after the restart: %v, restart %v", c, doc["restart"])
	}
	if code, ans := do("POST", "/extensions/truckersmp/actions/copy-profiles", `{"args":{}}`); code != 200 {
		t.Fatalf("action: %d %v", code, ans)
	}
	if code, ans := do("POST", "/extensions/coolercontrol/actions/copy-profiles", `{}`); code != 404 || ans["error"] != `CoolerControl has no action "copy-profiles"` {
		t.Fatalf("unknown action: %d %v", code, ans)
	}
	if code, ans := do("PUT", "/extensions/coolercontrol/settings", `{"settings":{"gpu_fan_curves":false}}`); code != 403 {
		t.Fatalf("a module setting without the password: %d %v", code, ans)
	}
	// A module setting saved as it is changes nothing: no password.
	_, doc = do("PUT", "/extensions/coolercontrol/settings", `{"settings":{"gpu_fan_curves":true}}`)
	if c := card(doc, "coolercontrol"); c["state"] != "installed" || extSetting(c, "gpu_fan_curves")["value"] != true {
		t.Fatalf("an unchanged module setting: %v", c)
	}
	_, doc = do("PUT", "/extensions/coolercontrol/settings", `{"password":"`+devPassword+`","settings":{"it87_conflicts":true}}`)
	if c := card(doc, "coolercontrol"); c["state"] != "restart-needed" ||
		asObj(doc["restart"])["reason"] != "Restart to finish changing the settings of CoolerControl." {
		t.Fatalf("module setting: %v, restart %v", c, doc["restart"])
	}
	_, doc = do("DELETE", "/extensions/coolercontrol?purge=1", "")
	if c := card(doc, "coolercontrol"); c["state"] != "restart-needed" || c["wanted"] != false || c["web_running"] != false ||
		extSetting(c, "gpu_fan_curves")["value"] != false || extSetting(c, "it87_conflicts")["value"] != false || asObj(doc["restart"])["reason"] != "Restart to finish removing CoolerControl." {
		t.Fatalf("removed: %v, restart %v", c, doc["restart"])
	}
	if code, _ := do("POST", "/extensions/skip-once", ""); code != 200 {
		t.Fatal("skip-once")
	}
	_, doc = do("GET", "/extensions", "")
	if r := asObj(doc["restart"]); doc["skip_once"] != true || r["needed"] != true || r["auto"] != false ||
		r["reason"] != "The next start is without extensions. Restart again after it to finish removing CoolerControl." {
		t.Fatalf("after skip-once: skip_once %v, restart %v", doc["skip_once"], doc["restart"])
	}
	if _, st := do("GET", "/status", ""); len(asList(asObj(st["restart"])["reasons"])) != 0 {
		t.Fatalf("GET /status restart with skip-once = %v", st["restart"])
	}
	if code, _ := do("DELETE", "/extensions/skip-once", ""); code != 200 {
		t.Fatal("taking skip-once back")
	}
	_, doc = do("GET", "/extensions", "")
	if doc["skip_once"] != false || asObj(doc["restart"])["needed"] != true || asObj(doc["restart"])["auto"] != true {
		t.Fatalf("after taking it back: skip_once %v, restart %v", doc["skip_once"], doc["restart"])
	}

	// A restart with skip_once mounts nothing; the one after starts them again.
	do("POST", "/extensions/skip-once", "")
	f.mu.Lock()
	bootExtensions(f.docs)
	f.mu.Unlock()
	_, doc = do("GET", "/extensions", "")
	if c := card(doc, "proton"); c["state"] != "needs-attention" || c["mounted"] != false || doc["skip_once"] != false {
		t.Fatalf("after a start without extensions: %v, skip_once %v", c, doc["skip_once"])
	}
	f.mu.Lock()
	bootExtensions(f.docs)
	f.mu.Unlock()
	_, doc = do("GET", "/extensions", "")
	if c := card(doc, "proton"); c["state"] != "installed" || c["mounted"] != true || c["reason"] != "" {
		t.Fatalf("after the next start: %v", c)
	}
	if code, ans := do("POST", "/extensions/truckersmp/actions/copy-profiles", `{"args":{"x":"`+strings.Repeat("a", maxActionArgs)+`"}}`); code != 400 || ans["error"] != "args is too large" {
		t.Fatalf("large args: %d %v", code, ans["error"])
	}
}

// Adding an extension takes the password when it, or a requirement not
// added yet, runs as root or sets kernel module options, as the real
// document says.
func TestFakeNeedsPassword(t *testing.T) {
	doc := map[string]any{"extensions": []any{
		map[string]any{"id": "coolercontrol", "runs_as_root": true, "requires": []any{}},
		map[string]any{"id": "fan-profiles", "requires": []any{"coolercontrol"}},
		map[string]any{"id": "proton", "core": true, "module_options": true, "requires": []any{}},
		map[string]any{"id": "truckersmp", "requires": []any{"proton"}},
	}, "restart": map[string]any{}}
	refreshExtensions(doc)
	want := map[string]bool{"coolercontrol": true, "fan-profiles": true, "proton": true, "truckersmp": false}
	for _, c := range asList(doc["extensions"]) {
		if m := asObj(c); m["needs_password"] != want[asStr(m["id"])] {
			t.Errorf("%s needs_password = %v", m["id"], m["needs_password"])
		}
	}
	asObj(asList(doc["extensions"])[0])["wanted"] = true
	refreshExtensions(doc)
	if m := asObj(asList(doc["extensions"])[1]); m["needs_password"] != false {
		t.Errorf("fan-profiles once CoolerControl is wanted: %v", m["needs_password"])
	}
}
