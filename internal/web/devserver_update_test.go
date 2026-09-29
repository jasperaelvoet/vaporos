package web

import (
	"net/http"
	"time"
)

// The dev server's fake of updates: the A/B slots, checking, staging with
// progress events, and activating. TestDevServer and the fake's core are in
// devserver_test.go.

func (f *fakeAPI) seedUpdate() {
	f.update = map[string]any{
		"booted": "20260929.101500", "staged": nil, "failed": []string{"20260921.083000"},
		"available":  map[string]any{"version": "20260929.143000", "size": 1_420_000_000, "checked": time.Now().Add(-40 * time.Minute).Format(time.RFC3339)},
		"last_error": "", "config": map[string]any{"source": "oci://ghcr.io/jasperaelvoet/vaporos", "channel": "main", "auto": "stage"},
		"booted_slot": "a", "other_slot": map[string]any{"version": "20260927.190000", "bootable": true},
		"held": nil, "busy": false, "progress": nil,
	}
}

func (f *fakeAPI) updateRoutes() {
	ok := struct{}{}
	f.handle("GET /update", true, func(w http.ResponseWriter, r *http.Request) any { return f.update })
	f.handle("POST /update/check", true, func(w http.ResponseWriter, r *http.Request) any {
		return map[string]any{"available": f.update["available"]}
	})
	f.handle("POST /update/stage", true, func(w http.ResponseWriter, r *http.Request) any {
		go f.fakeStage()
		return ok
	})
	f.handle("POST /update/activate", true, func(w http.ResponseWriter, r *http.Request) any { f.reboot(8 * time.Second); return ok })
	f.handle("POST /update/rollback", true, func(w http.ResponseWriter, r *http.Request) any { return ok })
	f.handle("PUT /update/settings", true, func(w http.ResponseWriter, r *http.Request) any {
		b := body(r)
		f.update["config"] = map[string]any{"source": "oci://ghcr.io/jasperaelvoet/vaporos", "channel": b["channel"], "auto": b["auto"]}
		return ok
	})
}

func (f *fakeAPI) fakeStage() {
	f.mu.Lock()
	var v any
	if a, ok := f.update["available"].(map[string]any); ok {
		v = a["version"]
	}
	f.mu.Unlock()
	if f.onTrial {
		// Refused for now: the server reports it only as this event, and
		// last_error stays as it was.
		time.Sleep(400 * time.Millisecond)
		f.hub.Publish("update.progress", map[string]any{"phase": "error", "version": v,
			"error": "the running version is still on trial; try again once it has fully started (20260929.101500, 2 tries left)"})
		return
	}
	const total = 1_420_000_000
	for p := 0; p <= 100; p += 4 {
		phase := "download"
		if p > 80 {
			phase = "verify"
		}
		f.hub.Publish("update.progress", map[string]any{"phase": phase, "percent": p, "bytes": total / 100 * p, "total": total, "version": v})
		time.Sleep(300 * time.Millisecond)
	}
	f.mu.Lock()
	f.update["staged"] = map[string]any{"version": v, "slot": "b", "at": time.Now().Format(time.RFC3339)}
	f.mu.Unlock()
	f.hub.Publish("update.progress", map[string]any{"phase": "done", "percent": 100, "version": v})
}
