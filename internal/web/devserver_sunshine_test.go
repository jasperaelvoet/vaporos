package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
)

// The dev server's fake of Sunshine: paired devices, pairing with a PIN,
// stream settings and logs. TestDevServer and the fake's core are in
// devserver_test.go.

func (f *fakeAPI) seedSunshine() {
	f.clients = []map[string]string{
		{"uuid": "0f3c", "name": "Jasper's iPhone"},
		{"uuid": "7a91", "name": "Living room Apple TV"},
		{"uuid": "c2d8", "name": "MacBook Pro"},
	}
	f.settings = map[string]any{"encoder": "vulkan", "bitrate_kbps_max": 150000, "gamepad": "xone", "audio_sink": ""}
}

func (f *fakeAPI) sunshineRoutes() {
	ok := struct{}{}
	f.handle("GET /sunshine", true, func(w http.ResponseWriter, r *http.Request) any {
		var s any
		if f.session != nil {
			s = f.session
		}
		return map[string]any{"running": true, "version": "2026.928.143000", "streaming": f.session != nil, "session": s,
			"pending_pairing": len(f.pairings) > 0, "pairings": append([]map[string]string{}, f.pairings...)}
	})
	f.handle("POST /sunshine/pair", true, func(w http.ResponseWriter, r *http.Request) any {
		b := body(r)
		id, _ := b["pairing_id"].(string)
		switch {
		case id == "" && len(f.pairings) > 1:
			api.Error(w, http.StatusConflict, "%d devices are waiting to pair; choose which one this PIN is for", len(f.pairings))
			return nil
		case b["pin"] != "1234":
			api.Error(w, http.StatusBadRequest, "sunshine rejected the PIN")
			return nil
		}
		for i, p := range f.pairings {
			if id == "" || p["id"] == id {
				f.pairings = append(f.pairings[:i:i], f.pairings[i+1:]...)
				break
			}
		}
		f.clients = append(f.clients, map[string]string{"uuid": fmt.Sprint(time.Now().UnixNano()), "name": fmt.Sprint(b["name"])})
		return ok
	})
	f.handle("GET /sunshine/clients", true, func(w http.ResponseWriter, r *http.Request) any {
		return map[string]any{"clients": f.clients}
	})
	f.handle("DELETE /sunshine/clients/{uuid}", true, func(w http.ResponseWriter, r *http.Request) any {
		out := f.clients[:0]
		for _, c := range f.clients {
			if c["uuid"] != r.PathValue("uuid") {
				out = append(out, c)
			}
		}
		f.clients = out
		return ok
	})
	f.handle("GET /sunshine/settings", true, func(w http.ResponseWriter, r *http.Request) any { return f.settings })
	f.handle("PUT /sunshine/settings", true, func(w http.ResponseWriter, r *http.Request) any { f.settings = body(r); return ok })
	f.handle("GET /sunshine/logs", true, func(w http.ResponseWriter, r *http.Request) any {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for i := 0; i < 40; i++ {
			fmt.Fprintf(w, "[2026-09-29 14:%02d:07.123]: Info: CLIENT CONNECTED\n[2026-09-29 14:%02d:07.456]: Info: Found display [DP-1]\n", i, i)
		}
		return nil
	})
	f.handle("POST /sunshine/restart", true, func(w http.ResponseWriter, r *http.Request) any { return ok })
}
