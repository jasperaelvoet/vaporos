package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// The dev server's fake of the virtual display (internal/display): the
// connectors, modes and HDR. Document: base/display.json. A stream moves
// it with session.begin and session.end (devFake.followLocked).

func (f *devFake) displayRoutes(add fakeAdder) {
	add("GET", "/display", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.doc("display") })
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
		d := f.doc("display")
		if !inCatalogue(md) {
			d["added"] = sortedModes(append(asList(d["added"]), md.String()))
			d["learned"] = sortedModes(append(asList(d["learned"]), md.String()))
		}
		d["reboot_needed"] = true
		f.emitLocked("display.changed", struct{}{})
		return map[string]bool{"reboot_needed": true}
	})
	add("DELETE", "/display/modes/{mode}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		md, err := edid.ParseMode(r.PathValue("mode"))
		if err != nil {
			api.Error(w, http.StatusBadRequest, "%v", err)
			return nil
		}
		d := f.doc("display")
		if !containsString(asList(d["learned"]), md.String()) {
			api.Error(w, http.StatusNotFound, "%s is neither an added nor a learned mode", md)
			return nil
		}
		drop := func(l []any) []any {
			out := []any{}
			for _, m := range l {
				if m != md.String() {
					out = append(out, m)
				}
			}
			return out
		}
		d["added"], d["learned"], d["reboot_needed"] = drop(asList(d["added"])), drop(asList(d["learned"])), true
		f.emitLocked("display.changed", struct{}{})
		return map[string]bool{"reboot_needed": true}
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
	for _, c := range edid.Catalogue {
		if c == md {
			return true
		}
	}
	return false
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
