package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// The dev server's fake of the virtual display (internal/display): the
// connectors, modes and HDR. Document: base/display.json. A stream moves
// it with session.begin and session.end (devFake.followLocked).
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
		f.emitLocked("display.changed", struct{}{})
		return fakeOK
	})
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
