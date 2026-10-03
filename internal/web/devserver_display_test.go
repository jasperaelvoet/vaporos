package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// The dev server's fake of the virtual display (internal/display): the
// connectors, modes, HDR and interface scaling (ui_scaling, steam_ui and
// the screens). Document: base/display.json. A stream moves it with
// session.begin and session.end (devFake.followLocked).
//
// As in display.Info, added lists the valid modes beyond the catalogue in
// display.extra_modes, devices is clients.json (every device's last mode,
// newest first), and learned is added plus the device modes beyond the
// catalogue: displayLocked keeps learned so, and a change to it rewrites
// the EDID, which asks for a restart (reboot_needed).

func (f *devFake) displayRoutes(add fakeAdder) {
	add("GET", "/display", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.displayLocked() })
	add("POST", "/display/modes", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Mode string `json:"mode"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		md, err := edid.ParseMode(req.Mode)
		if err == nil {
			err = edid.Check(md)
		}
		if err != nil {
			api.Error(w, http.StatusBadRequest, "%v", err)
			return nil
		}
		d := f.displayLocked()
		// A catalogue mode goes into extra_modes too, but added (like the
		// EDID) never shows it.
		if !inCatalogue(md) && !hasMode(asList(d["added"]), md) {
			d["added"] = sortedModes(append(asList(d["added"]), md.String()))
		}
		f.displayLocked()
		f.emitLocked("display.changed", struct{}{})
		return map[string]bool{"reboot_needed": d["reboot_needed"] == true}
	})
	add("DELETE", "/display/modes/{mode}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		md, err := edid.ParseMode(r.PathValue("mode"))
		if err != nil {
			api.Error(w, http.StatusBadRequest, "%v", err)
			return nil
		}
		d := f.displayLocked()
		added := asList(d["added"])
		devs := []any{}
		for _, dev := range asList(d["devices"]) {
			if m, err := edid.ParseMode(asStr(asObj(dev)["mode"])); err != nil || m != md {
				devs = append(devs, dev)
			}
		}
		if !hasMode(added, md) && len(devs) == len(asList(d["devices"])) {
			api.Error(w, http.StatusNotFound, "%s is neither an added nor a learned mode", md)
			return nil
		}
		d["added"] = slices.DeleteFunc(slices.Clone(added), func(v any) bool { return sameMode(v, md) })
		d["devices"] = devs
		f.displayLocked()
		f.emitLocked("display.changed", struct{}{})
		return map[string]bool{"reboot_needed": d["reboot_needed"] == true}
	})
	add("PUT", "/display/settings", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			HDR              *bool   `json:"hdr"`
			VirtualConnector *string `json:"virtual_connector"`
			UIScaling        *bool   `json:"ui_scaling"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		d := f.doc("display")
		if c := req.VirtualConnector; c != nil && *c != asStr(d["virtual_connector"]) {
			if d["profile"] == "none" {
				api.Error(w, http.StatusConflict, "no supported GPU for a virtual display")
				return nil
			}
			if !candidateConnector(d, *c) {
				api.Error(w, http.StatusBadRequest, "%q is not a DP or HDMI connector of %s", *c, asStr(asObj(f.doc("system")["gpu"])["name"]))
				return nil
			}
			f.moveVirtualLocked(*c)
		}
		if req.HDR != nil {
			d["hdr"] = *req.HDR
		}
		if req.UIScaling != nil {
			f.scalingLocked(*req.UIScaling)
		}
		f.emitLocked("display.changed", struct{}{})
		return fakeOK
	})
	add("PUT", "/display/screens/{id}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var e display.ScreenEdit
		if !strictBody(w, r, &e) {
			return nil
		}
		pick := ""
		if e.Kind != nil && *e.Kind != "auto" {
			if _, ok := display.UserKind(*e.Kind); !ok {
				api.Error(w, http.StatusBadRequest, "%v", display.ErrBadKind)
				return nil
			}
			pick = *e.Kind
		}
		if e.Size != nil && !(*e.Size >= display.SizeMin && *e.Size <= display.SizeMax) {
			api.Error(w, http.StatusBadRequest, "%v", display.ErrBadSize)
			return nil
		}
		id := r.PathValue("id")
		sc := screenByID(f.doc("display"), id)
		if sc == nil {
			api.Error(w, http.StatusNotFound, "%v", display.ErrNoScreen)
			return nil
		}
		f.editScreenLocked(id, sc, e.Kind != nil, pick, e.Size, e.SteamAuto)
		f.emitLocked("display.changed", struct{}{})
		return sc
	})
	add("DELETE", "/display/screens/{id}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		id := r.PathValue("id")
		d := f.doc("display")
		sc := screenByID(d, id)
		if sc == nil {
			api.Error(w, http.StatusNotFound, "%v", display.ErrNoScreen)
			return nil
		}
		if f.streamingScreenLocked() == id {
			// The device streaming now starts afresh at once, as its next
			// session would, and stays listed.
			if sc["kind_from"] == "you" {
				f.autoScreenLocked(id, sc)
			}
			sc["size"], sc["steam_auto"] = 1.0, false
		} else {
			d["screens"] = slices.DeleteFunc(slices.Clone(asList(d["screens"])), func(v any) bool { return asStr(asObj(v)["id"]) == id })
			delete(f.guessFrom, id)
		}
		f.emitLocked("display.changed", struct{}{})
		return fakeOK
	})
	// The fake keeps no hint: it checks the body as the real route does.
	add("POST", "/display/hint", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			W     int     `json:"w"`
			H     int     `json:"h"`
			DPR   float64 `json:"dpr"`
			Touch int     `json:"touch"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		switch {
		case req.W < 1 || req.W > 20000 || req.H < 1 || req.H > 20000:
			api.Error(w, http.StatusBadRequest, "w and h must be between 1 and 20000")
		case !(req.DPR >= 0.5 && req.DPR <= 8):
			api.Error(w, http.StatusBadRequest, "dpr must be between 0.5 and 8")
		case req.Touch < 0 || req.Touch > 20:
			api.Error(w, http.StatusBadRequest, "touch must be between 0 and 20")
		default:
			return fakeOK
		}
		return nil
	})
}

// ---- screens (display/scale.go, screens.go)
//
// GET /display screens is the document's list, kept as the real one lists
// it. The fake never infers and never derives an id: a session.begin's
// screen (its id, kind, kind_from, ui_scale and game_dpi) is taken as it
// is, as if the scaler had applied it at once, and a screen's kind is the
// user's pick when its kind_from is "you", else automatic. Nor does it work
// out a streaming screen's size_min and size_max: they are the preset's
// sim.size_ranges for that screen, whatever its kind.
//
// steam_ui "resumed" comes only from a patch (the fake has no resume): it
// stays through a PUT of the screen, goes off and comes back with
// display.ui_scaling, and the next session.begin or session.end ends it.

// scalingLocked follows display.ui_scaling: steam_ui is off while it is
// false, and once it is true again Steam is sized (ok, or resumed when it
// was) while a session runs, else idle.
func (f *devFake) scalingLocked(on bool) {
	d := f.doc("display")
	d["ui_scaling"] = on
	switch {
	case !on:
		if d["steam_ui"] == "resumed" {
			f.resumed = true
		}
		d["steam_ui"] = "off"
	case d["steam_ui"] == "off":
		d["steam_ui"] = "idle"
		if f.doc("sunshine")["streaming"] == true {
			d["steam_ui"] = "ok"
			if f.resumed {
				d["steam_ui"] = "resumed"
			}
		}
	}
}

// screenBeginLocked is the display's side of a session.begin: the screen
// the payload names comes first, keeping the user's size and steam_auto;
// one that cannot be told apart (id "") is listed only while it streams.
// Its guess is its kind unless the user picked that. A begin ends a resume.
func (f *devFake) screenBeginLocked(stream map[string]any) {
	disp := f.doc("display")
	f.resumed = false
	if disp["ui_scaling"] != false && (disp["steam_ui"] == "idle" || disp["steam_ui"] == "resumed") {
		disp["steam_ui"] = "ok"
	}
	if l, ok := disp["screens"].([]any); ok {
		disp["screens"] = screensAtRest(l)
	}
	ref, ok := stream["screen"].(map[string]any)
	if !ok {
		return
	}
	id, kind, from := asStr(ref["id"]), asStr(ref["kind"]), asStr(ref["kind_from"])
	var prev map[string]any
	rest := []any{}
	for _, v := range asList(disp["screens"]) {
		if id != "" && asStr(asObj(v)["id"]) == id {
			prev = asObj(v)
			continue
		}
		rest = append(rest, v)
	}
	sc := map[string]any{
		"id": id, "name": asStr(ref["name"]), "kind": kind, "kind_from": from, "guess": kind,
		"size": 1.0, "steam_auto": false, "mode": asStr(stream["mode"]), "last_seen": stream["since"],
		"ui_scale": asNum(ref["ui_scale"]), "game_dpi": asNum(ref["game_dpi"]), "savable": id != "",
	}
	if size, ok := prev["size"].(float64); ok {
		sc["size"], sc["steam_auto"] = size, prev["steam_auto"] == true
	}
	if r, ok := f.preset.Sim.SizeRanges[id]; ok {
		sc["size_min"], sc["size_max"] = r[0], r[1]
	}
	switch {
	case from == "you":
		sc["guess"] = "unknown"
		if g := asStr(prev["guess"]); g != "" {
			sc["guess"] = g
		}
	case id != "":
		f.guessFrom[id] = from
	}
	disp["screens"] = append([]any{sc}, rest...)
}

// screensStreamEnded is the display once no session runs: a screen that
// could not be told apart is gone, none has size bounds, and sizing Steam
// is idle (or off).
func screensStreamEnded(disp map[string]any) {
	if l, ok := disp["screens"].([]any); ok {
		disp["screens"] = screensAtRest(l)
	}
	if s, ok := disp["steam_ui"].(string); ok && s != "off" {
		disp["steam_ui"] = "idle"
	}
}

// screensAtRest is the list without the session's screen: no screen that
// cannot be told apart, and no size_min or size_max.
func screensAtRest(l []any) []any {
	out := []any{}
	for _, v := range l {
		sc := asObj(v)
		if asStr(sc["id"]) != "" {
			delete(sc, "size_min")
			delete(sc, "size_max")
			out = append(out, v)
		}
	}
	return out
}

// screenByID is the listed screen with id, nil when there is none.
func screenByID(d map[string]any, id string) map[string]any {
	for _, v := range asList(d["screens"]) {
		if sc, ok := v.(map[string]any); ok && id != "" && asStr(sc["id"]) == id {
			return sc
		}
	}
	return nil
}

// streamingScreenLocked is the id of the screen streaming now ("" none).
func (f *devFake) streamingScreenLocked() string {
	if f.doc("sunshine")["streaming"] != true {
		return ""
	}
	return asStr(asObj(f.stream["screen"])["id"])
}

// editScreenLocked is display.Screens.Edit on a listed screen: a kind
// (pick "" is automatic) resets the size to 1.0 when it changes it, a size
// alone pins the kind in effect as the user's unless it is the session's
// veto (kind_from stream with a kind other than the guess), and a kind or
// size without steam_auto turns steam_auto off.
func (f *devFake) editScreenLocked(id string, sc map[string]any, kindSet bool, pick string, size *float64, steamAuto *bool) {
	stored := ""
	if sc["kind_from"] == "you" {
		stored = asStr(sc["kind"])
	} else {
		f.guessFrom[id] = asStr(sc["kind_from"])
	}
	if kindSet {
		if pick != stored {
			sc["size"] = 1.0
		}
		stored = pick
	}
	if size != nil {
		sc["size"] = *size
		vetoed := sc["kind_from"] == "stream" && asStr(sc["guess"]) != "" && sc["kind"] != sc["guess"]
		if !kindSet && stored == "" && !vetoed {
			stored = asStr(sc["kind"])
		}
	}
	switch {
	case steamAuto != nil:
		sc["steam_auto"] = *steamAuto
	case kindSet || size != nil:
		sc["steam_auto"] = false
	}
	switch {
	case stored != "":
		sc["kind"], sc["kind_from"] = stored, "you"
	case sc["kind_from"] == "you":
		f.autoScreenLocked(id, sc)
	}
}

// autoScreenLocked makes a picked screen automatic again: its guess, for
// the reason the fake last saw for its kind. A screen the fake has only
// ever seen picked says "resolution" ("default" when the guess is unknown).
func (f *devFake) autoScreenLocked(id string, sc map[string]any) {
	guess := asStr(sc["guess"])
	if guess == "" {
		guess = "unknown"
	}
	from := f.guessFrom[id]
	if from == "" {
		from = "resolution"
		if guess == "unknown" {
			from = "default"
		}
	}
	sc["kind"], sc["kind_from"] = guess, from
}

// displayLocked is the display document with learned rebuilt from added
// and devices, as display.Info builds it. When learned changes (a device
// asked for a new mode, or a mode was added or removed), the EDID the next
// boot loads changes: reboot_needed.
func (f *devFake) displayLocked() map[string]any {
	d := f.doc("display")
	learned := learnedModes(d)
	if !slices.Equal(learned, asList(d["learned"])) {
		d["learned"] = learned
		d["reboot_needed"] = true
	}
	return d
}

// learnedModes is display.Manager.learnedModes over the document: added,
// then every device's valid mode beyond the catalogue, once each, sorted.
func learnedModes(d map[string]any) []any {
	extra := slices.Clone(asList(d["added"]))
	for _, dev := range asList(d["devices"]) {
		extra = append(extra, asObj(dev)["mode"])
	}
	var beyond []any
	for _, v := range extra {
		if m, err := edid.ParseMode(asStr(v)); err == nil && !inCatalogue(m) && edid.Check(m) == nil {
			beyond = append(beyond, m.String())
		}
	}
	return sortedModes(beyond)
}

func sameMode(v any, md edid.Mode) bool {
	m, err := edid.ParseMode(asStr(v))
	return err == nil && m == md
}

func hasMode(l []any, md edid.Mode) bool {
	return slices.ContainsFunc(l, func(v any) bool { return sameMode(v, md) })
}

// moveVirtualLocked makes c the virtual connector from the next boot: the
// old one becomes free, as setVirtualLocked's cmdline change does.
func (f *devFake) moveVirtualLocked(c string) {
	d := f.doc("display")
	old := asStr(d["virtual_connector"])
	free := []any{}
	for _, n := range append(asList(d["available_connectors"]), old) {
		if n != c && n != "" {
			free = append(free, n)
		}
	}
	sort.Slice(free, func(i, j int) bool { return asStr(free[i]) < asStr(free[j]) })
	d["virtual_connector"], d["available_connectors"], d["reboot_needed"] = c, free, true
}

// candidateConnector reports whether c is a DP or HDMI connector of the GPU
// (display.isCandidateType).
func candidateConnector(d map[string]any, c string) bool {
	for _, conn := range asList(d["connectors"]) {
		if n := asStr(asObj(conn)["name"]); n == c {
			return strings.HasPrefix(n, "DP-") || strings.HasPrefix(n, "HDMI-")
		}
	}
	return false
}

func inCatalogue(md edid.Mode) bool {
	return slices.Contains(edid.Catalogue, md)
}

// sortedModes orders modes as GET /display lists them: by area, then
// width, then refresh rate, largest first; each once.
func sortedModes(l []any) []any {
	var ms []edid.Mode
	seen := map[edid.Mode]bool{}
	for _, v := range l {
		if m, err := edid.ParseMode(asStr(v)); err == nil && !seen[m] {
			seen[m] = true
			ms = append(ms, m)
		}
	}
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Area() != ms[j].Area() {
			return ms[i].Area() > ms[j].Area()
		}
		if ms[i].W != ms[j].W {
			return ms[i].W > ms[j].W
		}
		return ms[i].Refresh > ms[j].Refresh
	})
	out := []any{}
	for _, m := range ms {
		out = append(out, m.String())
	}
	return out
}

// ---- tests

// displayFake is a fake in preset name with its display routes by
// "METHOD path", for calling handlers directly.
func displayFake(t *testing.T, name string) (*devFake, map[string]fakeHandler) {
	t.Helper()
	fx := testFixtures(t)
	p := fx.presets[name]
	docs, err := fx.docs(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := newDevFake(nil, nil, p, docs, false)
	hs := map[string]fakeHandler{}
	f.displayRoutes(func(method, path string, _ api.Access, h fakeHandler) { hs[method+" "+path] = h })
	return f, hs
}

// call runs one display route: the status and the JSON answer.
func (f *devFake) call(t *testing.T, hs map[string]fakeHandler, method, path, mode, body string) (int, map[string]any) {
	t.Helper()
	route := method + " " + path
	r := httptest.NewRequest(method, api.Prefix+strings.Replace(path, "{mode}", mode, 1), strings.NewReader(body))
	if mode != "" {
		r.SetPathValue("mode", mode)
	}
	w := httptest.NewRecorder()
	f.mu.Lock()
	v := hs[route](w, r)
	f.mu.Unlock()
	if v == nil {
		var e map[string]any
		json.Unmarshal(w.Body.Bytes(), &e)
		return w.Code, e
	}
	b, _ := json.Marshal(v)
	var out map[string]any
	json.Unmarshal(b, &out)
	return http.StatusOK, out
}

// displayFixtureGaps are presets whose display document the real service
// could not answer, each with what to change. Owners fix them; the test
// says when an entry can go.
var displayFixtureGaps = map[string]string{
	"reboot-needed": "added 3000x2000@120 needs a 720 MHz pixel clock, which edid.Check refuses: use 3000x2000@60 in added and learned",
}

// Every preset's display document is one the real service could answer:
// added holds valid modes beyond the catalogue, and learned is exactly
// added plus the devices' modes beyond the catalogue (display.Info).
func TestFakeDisplayFixturesConsistent(t *testing.T) {
	fx := testFixtures(t)
	for _, name := range fx.presetNames() {
		p := fx.presets[name]
		if p.installer() {
			continue
		}
		docs, err := fx.docs(p, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		d := asObj(docs["display"])
		var bad []string
		if want := learnedModes(d); !slices.Equal(asList(d["learned"]), want) {
			bad = append(bad, fmt.Sprintf("display.learned = %v, want added plus the devices' modes beyond the catalogue: %v", d["learned"], want))
		}
		for _, a := range asList(d["added"]) {
			if m, err := edid.ParseMode(asStr(a)); err != nil || inCatalogue(m) || edid.Check(m) != nil {
				bad = append(bad, fmt.Sprintf("display.added holds %v, not a valid mode beyond the catalogue", a))
			}
		}
		switch gap := displayFixtureGaps[name]; {
		case gap != "" && len(bad) == 0:
			t.Errorf("presets/%s.json: its display document is consistent now: drop it from displayFixtureGaps", name)
		case gap != "":
			t.Logf("presets/%s.json: known gap (%s): %s", name, gap, strings.Join(bad, "; "))
		default:
			for _, b := range bad {
				t.Errorf("presets/%s.json: %s", name, b)
			}
		}
	}
}

var (
	screenIDRe   = regexp.MustCompile(`^[0-9a-f]{12}$`)
	screenSource = []display.Source{display.FromYou, display.FromName, display.FromBrowser, display.FromResolution,
		display.FromHistory, display.FromStream, display.FromDefault}
)

// Every preset's screens are a list the real display could answer
// (display.Screens.Views): unique ids of 12 hex digits, a non-generic
// name's being its own (display.ScreenID), kinds and sources from the
// contract, a size within 0.4-2.5, savable exactly when there is an id, a
// game DPI of 96 times a quarter step, most recently seen first. Every
// session.begin screen in a preset or script names a kind and a source
// and, for a non-generic name, that name's id.
func TestFakeScreensFixturesConsistent(t *testing.T) {
	fx := testFixtures(t)
	checkRef := func(where string, sc map[string]any, view bool) []string {
		var bad []string
		id, name := asStr(sc["id"]), asStr(sc["name"])
		switch {
		case id != "" && !screenIDRe.MatchString(id):
			bad = append(bad, fmt.Sprintf("%s: id %q is not 12 hex digits", where, id))
		case id != "" && !display.GenericName(name) && id != display.ScreenID(display.ScreenKey(name, "", "")):
			bad = append(bad, fmt.Sprintf("%s: id %q is not %q's (%s)", where, id, name, display.ScreenID(name)))
		}
		if !display.Kind(asStr(sc["kind"])).Valid() {
			bad = append(bad, fmt.Sprintf("%s: kind %v", where, sc["kind"]))
		}
		if !slices.Contains(screenSource, display.Source(asStr(sc["kind_from"]))) {
			bad = append(bad, fmt.Sprintf("%s: kind_from %v", where, sc["kind_from"]))
		}
		if dpi := asNum(sc["game_dpi"]); dpi != 0 && (dpi < 96 || int(dpi)%24 != 0) {
			bad = append(bad, fmt.Sprintf("%s: game_dpi %v is not 96 times a multiple of 0.25", where, dpi))
		}
		if !view {
			return bad
		}
		if !display.Kind(asStr(sc["guess"])).Valid() {
			bad = append(bad, fmt.Sprintf("%s: guess %v", where, sc["guess"]))
		}
		if size := asNum(sc["size"]); size < display.SizeMin || size > display.SizeMax {
			bad = append(bad, fmt.Sprintf("%s: size %v outside 0.4-2.5", where, sc["size"]))
		}
		if sc["savable"] != (id != "") {
			bad = append(bad, fmt.Sprintf("%s: savable %v with id %q", where, sc["savable"], id))
		}
		return bad
	}
	checkEvents := func(where string, evs []fakeEvent) {
		for _, ev := range evs {
			var m map[string]any
			if ev.Topic != "session.begin" || json.Unmarshal(ev.Data, &m) != nil {
				continue
			}
			if ref, ok := m["screen"].(map[string]any); ok {
				for _, b := range checkRef(where+": session.begin screen", ref, false) {
					t.Error(b)
				}
			}
		}
	}
	now := time.Now()
	for _, name := range fx.presetNames() {
		p := fx.presets[name]
		where := "presets/" + name + ".json"
		docs, err := fx.docs(p, now)
		if err != nil {
			t.Fatal(err)
		}
		checkEvents(where, p.Events)
		seen := map[string]bool{}
		var last time.Time
		for i, v := range asList(asObj(docs["display"])["screens"]) {
			sc := asObj(v)
			w := fmt.Sprintf("%s: display.screens[%d]", where, i)
			for _, b := range checkRef(w, sc, true) {
				t.Error(b)
			}
			id := asStr(sc["id"])
			if seen[id] || (id == "" && i > 0) {
				t.Errorf("%s: id %q twice, or a screen without one that is not first", w, id)
			}
			seen[id] = true
			at, err := time.Parse(time.RFC3339, asStr(sc["last_seen"]))
			if err != nil || (i > 0 && at.After(last)) {
				t.Errorf("%s: last_seen %v is not a time at or before the screen above's", w, sc["last_seen"])
			}
			last = at
		}
	}
	for name, s := range fx.scripts {
		for i, st := range s.Steps {
			checkEvents(fmt.Sprintf("scripts/%s.json step %d", name, i), st.Events)
		}
	}
}

// The fake adds and removes modes as display/routes.go does (CONTRACTS.md
// POST and DELETE /display/modes), and learns a device's new mode.
func TestFakeDisplayModes(t *testing.T) {
	events := func(f *devFake) func() []string {
		ch, cancel := f.hub.Subscribe()
		t.Cleanup(cancel)
		return func() (out []string) {
			for {
				select {
				case ev := <-ch:
					out = append(out, ev.Topic)
				default:
					return out
				}
			}
		}
	}
	doc := func(f *devFake) map[string]any { return asObj(deepCopyJSON(f.doc("display"))) }

	t.Run("add", func(t *testing.T) {
		f, hs := displayFake(t, "idle")
		seen := events(f)
		code, ans := f.call(t, hs, "POST", "/display/modes", "", `{"mode":"2560x1080@144"}`)
		d := doc(f)
		if code != 200 || ans["reboot_needed"] != true || !hasMode(asList(d["added"]), edid.Mode{W: 2560, H: 1080, Refresh: 144}) ||
			!hasMode(asList(d["learned"]), edid.Mode{W: 2560, H: 1080, Refresh: 144}) || d["reboot_needed"] != true {
			t.Errorf("POST 2560x1080@144: %d %v, display %v", code, ans, d)
		}
		if got := seen(); !slices.Equal(got, []string{"display.changed"}) {
			t.Errorf("events = %v, want display.changed", got)
		}
	})
	t.Run("add a catalogue mode", func(t *testing.T) {
		f, hs := displayFake(t, "idle")
		code, ans := f.call(t, hs, "POST", "/display/modes", "", `{"mode":"1920x1080@60"}`)
		if d := doc(f); code != 200 || ans["reboot_needed"] != false || len(asList(d["added"])) != 0 {
			t.Errorf("POST 1920x1080@60 (in the catalogue): %d %v, added %v", code, ans, d["added"])
		}
	})
	t.Run("remove a device's mode", func(t *testing.T) {
		f, hs := displayFake(t, "idle")
		seen := events(f)
		code, ans := f.call(t, hs, "DELETE", "/display/modes/{mode}", "2360x1640@120", "")
		d := doc(f)
		if code != 200 || ans["reboot_needed"] != true || len(asList(d["learned"])) != 0 {
			t.Errorf("DELETE 2360x1640@120: %d %v, learned %v", code, ans, d["learned"])
		}
		for _, dev := range asList(d["devices"]) {
			if asStr(asObj(dev)["mode"]) == "2360x1640@120" {
				t.Errorf("DELETE 2360x1640@120 kept the device that asked for it: %v", dev)
			}
		}
		if got := seen(); !slices.Equal(got, []string{"display.changed"}) {
			t.Errorf("events = %v, want display.changed", got)
		}
	})
	t.Run("remove an added mode", func(t *testing.T) {
		f, hs := displayFake(t, "reboot-needed")
		code, _ := f.call(t, hs, "DELETE", "/display/modes/{mode}", "3000x2000@120", "")
		if d := doc(f); code != 200 || len(asList(d["added"])) != 0 || hasMode(asList(d["learned"]), edid.Mode{W: 3000, H: 2000, Refresh: 120}) {
			t.Errorf("DELETE 3000x2000@120: %d, display %v", code, d)
		}
	})
	t.Run("remove a catalogue mode a device used", func(t *testing.T) {
		f, hs := displayFake(t, "idle")
		code, ans := f.call(t, hs, "DELETE", "/display/modes/{mode}", "3840x2160@60", "")
		if d := doc(f); code != 200 || ans["reboot_needed"] != false || len(asList(d["devices"])) != 3 {
			t.Errorf("DELETE 3840x2160@60: %d %v, devices %v", code, ans, d["devices"])
		}
	})
	t.Run("remove an unknown mode", func(t *testing.T) {
		f, hs := displayFake(t, "idle")
		code, ans := f.call(t, hs, "DELETE", "/display/modes/{mode}", "1234x567@60", "")
		if code != 404 || ans["error"] != "1234x567@60 is neither an added nor a learned mode" {
			t.Errorf("DELETE 1234x567@60: %d %v", code, ans)
		}
	})
	t.Run("a device teaches its mode", func(t *testing.T) {
		f, hs := displayFake(t, "idle")
		f.mu.Lock()
		f.emitLocked("session.begin", map[string]any{"client": "Desk", "mode": "3024x1964@60", "hdr": false})
		f.mu.Unlock()
		code, ans := f.call(t, hs, "GET", "/display", "", "")
		if code != 200 || !hasMode(asList(ans["learned"]), edid.Mode{W: 3024, H: 1964, Refresh: 60}) || ans["reboot_needed"] != true {
			t.Errorf("after a session in 3024x1964@60: learned %v, reboot_needed %v", ans["learned"], ans["reboot_needed"])
		}
	})
}
