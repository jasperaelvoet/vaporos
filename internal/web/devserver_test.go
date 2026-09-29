package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// TestDevServer serves the real pages against an in-memory fake of the
// /api/v1 contract, for working on the UI without a VaporOS machine:
//
//	VOS_WEB_DEV=127.0.0.1:8080 go test ./internal/web -run TestDevServer -timeout 0
//
// VOS_WEB_INSTALLER=1 serves the installer; VOS_WEB_SIGNED_OUT=1 starts
// signed out (password "vaporvapor"); VOS_WEB_STREAMING=1 starts mid-stream;
// VOS_WEB_SETUP=1 starts without an admin password (first-run setup);
// VOS_WEB_PAIRING=1 starts with two devices waiting to pair;
// VOS_WEB_HEADLESS=1 waives the installer's setup code (no monitor);
// VOS_WEB_HELD=1 starts after a rollback from the newest version (held);
// VOS_WEB_TRIAL=1 makes a stage stop because the running version is on trial.
func TestDevServer(t *testing.T) {
	addr := os.Getenv("VOS_WEB_DEV")
	if addr == "" {
		t.Skip("set VOS_WEB_DEV=host:port to run the UI dev server")
	}
	installer := os.Getenv("VOS_WEB_INSTALLER") == "1"
	srv := api.New(api.Options{Installer: installer})
	if installer || os.Getenv("VOS_WEB_SETUP") == "1" {
		srv.SetSetupCode("ABCD-EFGH")
	}
	Register(srv)
	fake := newFakeAPI(installer)
	fake.authed = os.Getenv("VOS_WEB_SIGNED_OUT") != "1" && os.Getenv("VOS_WEB_SETUP") != "1"
	fake.needsSetup = os.Getenv("VOS_WEB_SETUP") == "1"
	if os.Getenv("VOS_WEB_STREAMING") == "1" {
		fake.session = map[string]any{"client": "Jasper's iPhone", "mode": "2796x1290@120", "hdr": true}
	}
	if os.Getenv("VOS_WEB_PAIRING") == "1" {
		fake.pairings = []map[string]string{
			{"id": "7f3a0c1e9b2d4a6f8e1c3b5d7a9f0e2c", "name": "Steam Deck", "address": "192.168.1.31"},
			{"id": "0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e", "name": "Pixel 9", "address": "192.168.1.44"},
		}
	}
	if os.Getenv("VOS_WEB_HEADLESS") == "1" {
		fake.headless = true
		srv.SetSetupWaiver(func() bool { return true })
	}
	if os.Getenv("VOS_WEB_HELD") == "1" {
		fake.update["held"] = map[string]any{"version": "20260929.143000", "rollback_index": 1790699400}
		fake.update["other_slot"] = map[string]any{"version": "20260929.143000", "bootable": false}
		fake.update["available"] = nil
	}
	fake.onTrial = os.Getenv("VOS_WEB_TRIAL") == "1"
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", fake)
	mux.Handle("/", srv.Handler())
	t.Logf("VaporOS UI on http://%s (installer=%v)", addr, installer)
	if err := http.ListenAndServe(addr, mux); err != nil {
		t.Fatal(err)
	}
}

// fakeAPI is a small, stateful stand-in for vosd that follows the request
// and response shapes in docs/CONTRACTS.md.
// Each API resource's routes and starting state are in its own file,
// devserver_<resource>_test.go.
type fakeAPI struct {
	mu         sync.Mutex
	mux        *http.ServeMux
	hub        *events.Hub
	installer  bool
	authed     bool
	needsSetup bool
	downUntil  time.Time
	booted     time.Time
	hostname   string
	session    map[string]any
	pairings   []map[string]string // devices waiting for a PIN
	headless   bool                // the installer waives its setup code
	onTrial    bool                // stages stop with the server's on-trial refusal
	clients    []map[string]string
	update     map[string]any
	display    map[string]any
	power      map[string]any
	ssh        map[string]any
	settings   map[string]any
	libraries  map[string]string // adopted uuid -> Steam: "registered", "pending" or ""
	install    map[string]any
}

func newFakeAPI(installer bool) *fakeAPI {
	f := &fakeAPI{
		mux: http.NewServeMux(), hub: events.NewHub(), installer: installer,
		booted: time.Now().Add(-26 * time.Hour),
	}
	f.seedSunshine()
	f.seedDisplay()
	f.seedUpdate()
	f.seedPower()
	f.seedStorage()
	f.seedSystem()
	f.seedInstall()
	f.routes()
	go f.ticker()
	return f
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down := time.Now().Before(f.downUntil)
	f.mu.Unlock()
	if down {
		// A restarting machine drops connections rather than answering.
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				c.Close()
				return
			}
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	f.mux.ServeHTTP(w, r)
}

// handle registers a route; authed routes answer 401 while signed out.
func (f *fakeAPI) handle(pattern string, authed bool, h func(w http.ResponseWriter, r *http.Request) any) {
	method, path, _ := strings.Cut(pattern, " ")
	f.mux.HandleFunc(method+" "+api.Prefix+path, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if authed && !f.authed {
			api.Error(w, http.StatusUnauthorized, "not signed in")
			return
		}
		time.Sleep(120 * time.Millisecond) // make spinners visible
		if v := h(w, r); v != nil {
			api.WriteJSON(w, http.StatusOK, v)
		}
	})
}

func body(r *http.Request) map[string]any {
	m := map[string]any{}
	json.NewDecoder(r.Body).Decode(&m)
	return m
}

func (f *fakeAPI) reboot(d time.Duration) {
	f.downUntil = time.Now().Add(d)
	f.booted = f.downUntil
}

func (f *fakeAPI) routes() {
	ok := struct{}{}
	f.handle("GET /ping", false, func(w http.ResponseWriter, r *http.Request) any {
		mode := "os"
		if f.installer {
			mode = "installer"
		}
		return map[string]any{"ok": true, "mode": mode, "version": "20260929.101500"}
	})
	f.handle("GET /auth/me", false, func(w http.ResponseWriter, r *http.Request) any {
		return map[string]any{"authenticated": f.authed, "csrf": "dev-csrf", "needs_setup": f.needsSetup, "installer": f.installer}
	})
	f.handle("POST /auth/login", false, func(w http.ResponseWriter, r *http.Request) any {
		if body(r)["password"] != "vaporvapor" {
			api.Error(w, http.StatusUnauthorized, "wrong password")
			return nil
		}
		f.authed = true
		return map[string]string{"csrf": "dev-csrf"}
	})
	f.handle("POST /auth/logout", true, func(w http.ResponseWriter, r *http.Request) any { f.authed = false; return ok })
	f.handle("POST /auth/setup", false, func(w http.ResponseWriter, r *http.Request) any {
		if r.Header.Get("X-VOS-Setup") != "ABCD-EFGH" {
			api.Error(w, http.StatusForbidden, "wrong setup code")
			return nil
		}
		f.authed, f.needsSetup = true, false
		return map[string]string{"csrf": "dev-csrf"}
	})
	f.handle("POST /auth/password", true, func(w http.ResponseWriter, r *http.Request) any {
		if body(r)["current"] != "vaporvapor" {
			api.Error(w, http.StatusForbidden, "current password is wrong")
			return nil
		}
		return ok
	})

	f.systemRoutes()
	f.updateRoutes()
	f.sunshineRoutes()
	f.displayRoutes()
	f.storageRoutes()
	f.powerRoutes()
	f.installRoutes()

	f.mux.HandleFunc("GET "+api.Prefix+"/events", f.serveEvents)
}

func (f *fakeAPI) serveEvents(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	authed := f.authed || f.installer
	f.mu.Unlock()
	if !authed {
		api.Error(w, http.StatusUnauthorized, "not signed in")
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	ch, cancel := f.hub.Subscribe()
	defer cancel()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Topic, ev.Data)
			fl.Flush()
		}
	}
}
