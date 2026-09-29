package web

import (
	"fmt"
	"net/http"
)

// The dev server's fake of the virtual display: connectors, modes and HDR.
// TestDevServer and the fake's core are in devserver_test.go.

func (f *fakeAPI) seedDisplay() {
	f.display = map[string]any{
		"profile": "amd", "virtual_connector": "DP-1",
		"connectors": []map[string]any{
			{"name": "DP-1", "status": "connected", "physical": false},
			{"name": "DP-2", "status": "disconnected", "physical": false},
			{"name": "DP-3", "status": "disconnected", "physical": false},
			{"name": "HDMI-A-1", "status": "connected", "physical": true},
		},
		"available_connectors": []string{"DP-2", "DP-3"},
		"planes":               1,
		"modes": []string{"1280x720@60", "1280x720@120", "1920x1080@60", "1920x1080@120", "1920x1080@144",
			"2560x1440@60", "2560x1440@120", "2560x1600@60", "2560x1600@120", "3440x1440@100", "3840x2160@30", "3840x2160@60",
			"2796x1290@60", "2796x1290@120"},
		"current": "2560x1600@120", "hdr": true, "learned": []string{"2360x1640@120"}, "reboot_needed": false, "state": "welcome",
	}
}

func (f *fakeAPI) displayRoutes() {
	ok := struct{}{}
	f.handle("GET /display", true, func(w http.ResponseWriter, r *http.Request) any {
		if f.session != nil {
			f.display["state"], f.display["current"] = "streaming", f.session["mode"]
		}
		return f.display
	})
	f.handle("POST /display/modes", true, func(w http.ResponseWriter, r *http.Request) any {
		f.display["learned"] = append(f.display["learned"].([]string), fmt.Sprint(body(r)["mode"]))
		f.display["reboot_needed"] = true
		return map[string]bool{"reboot_needed": true}
	})
	f.handle("PUT /display/settings", true, func(w http.ResponseWriter, r *http.Request) any {
		b := body(r)
		f.display["hdr"] = b["hdr"]
		if c, ok := b["virtual_connector"].(string); ok {
			old := f.display["virtual_connector"].(string)
			f.display["virtual_connector"], f.display["reboot_needed"] = c, true
			var free []string
			for _, n := range append(f.display["available_connectors"].([]string), old) {
				if n != c {
					free = append(free, n)
				}
			}
			f.display["available_connectors"] = free
		}
		return ok
	})
}
