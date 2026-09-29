package web

import (
	"fmt"
	"net/http"
	"time"
)

// The dev server's fake of power: idle shutdown, keep-awake, Wake-on-LAN and
// the idle countdown events. TestDevServer and the fake's core are in
// devserver_test.go.

func (f *fakeAPI) seedPower() {
	f.power = map[string]any{
		"idle_shutdown": true, "idle_minutes": 15, "keep_awake_until": nil,
		"wol": []map[string]any{{"iface": "enp6s0", "mac": "9c:6b:00:12:34:56", "enabled": true}}, "busy": nil,
	}
}

func (f *fakeAPI) powerRoutes() {
	ok := struct{}{}
	f.handle("GET /power", true, func(w http.ResponseWriter, r *http.Request) any {
		if f.session != nil {
			f.power["busy"] = map[string]string{"reason": "streaming to " + fmt.Sprint(f.session["client"])}
		}
		return f.power
	})
	f.handle("PUT /power", true, func(w http.ResponseWriter, r *http.Request) any {
		b := body(r)
		f.power["idle_shutdown"], f.power["idle_minutes"] = b["idle_shutdown"], b["idle_minutes"]
		return ok
	})
	f.handle("POST /power/keep-awake", true, func(w http.ResponseWriter, r *http.Request) any {
		m, _ := body(r)["minutes"].(float64)
		if m == 0 {
			f.power["keep_awake_until"] = nil
		} else {
			f.power["keep_awake_until"] = time.Now().Add(time.Duration(m) * time.Minute).Format(time.RFC3339)
		}
		return ok
	})
}

// ticker publishes the background events a real box would.
func (f *fakeAPI) ticker() {
	idle := 60
	for range time.Tick(5 * time.Second) {
		idle += 5
		f.hub.Publish("power.idle", map[string]any{"idle_seconds": idle, "shutdown_in": 15*60 - idle})
	}
}
