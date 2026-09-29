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
		booted: time.Now().Add(-26 * time.Hour), hostname: "vapor",
		clients: []map[string]string{
			{"uuid": "0f3c", "name": "Jasper's iPhone"},
			{"uuid": "7a91", "name": "Living room Apple TV"},
			{"uuid": "c2d8", "name": "MacBook Pro"},
		},
		update: map[string]any{
			"booted": "20260929.101500", "staged": nil, "failed": []string{"20260921.083000"},
			"available":  map[string]any{"version": "20260929.143000", "size": 1_420_000_000, "checked": time.Now().Add(-40 * time.Minute).Format(time.RFC3339)},
			"last_error": "", "config": map[string]any{"source": "oci://ghcr.io/jasperaelvoet/vaporos", "channel": "main", "auto": "stage"},
			"booted_slot": "a", "other_slot": map[string]any{"version": "20260927.190000", "bootable": true},
			"held": nil, "busy": false, "progress": nil,
		},
		display: map[string]any{
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
		},
		power: map[string]any{
			"idle_shutdown": true, "idle_minutes": 15, "keep_awake_until": nil,
			"wol": []map[string]any{{"iface": "enp6s0", "mac": "9c:6b:00:12:34:56", "enabled": true}}, "busy": nil,
		},
		ssh:       map[string]any{"enabled": false, "keys": []string{}},
		settings:  map[string]any{"encoder": "vulkan", "bitrate_kbps_max": 150000, "gamepad": "xone", "audio_sink": ""},
		libraries: map[string]string{"5e1d-aa01": "registered", "3c4d-5e6f": ""},
		install:   map[string]any{"state": "idle", "step": "", "percent": 0, "message": "", "error": ""},
	}
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

	f.handle("GET /system", true, func(w http.ResponseWriter, r *http.Request) any {
		return map[string]any{
			"hostname": f.hostname, "version": "20260929.101500", "channel": "main", "booted_slot": "a",
			"uptime_s": int(time.Since(f.booted).Seconds()), "cpu": "AMD Ryzen 7 9800X3D",
			"gpu": map[string]any{"vendor": "amd", "name": "AMD Radeon RX 9070 XT", "driver": "amdgpu", "supported": true},
			"ips": []string{"192.168.1.167", "fe80::9e6b:ff:fe12:3456"}, "mdns": f.hostname + ".local",
			"disk":  map[string]any{"data_total": 960_000_000_000, "data_free": 612_000_000_000},
			"temps": []map[string]any{{"name": "CPU", "c": 48.5}, {"name": "GPU", "c": 41}},
		}
	})
	f.handle("PUT /system/hostname", true, func(w http.ResponseWriter, r *http.Request) any {
		f.hostname, _ = body(r)["hostname"].(string)
		return ok
	})
	f.handle("POST /system/reboot", true, func(w http.ResponseWriter, r *http.Request) any { f.reboot(6 * time.Second); return ok })
	f.handle("POST /system/poweroff", true, func(w http.ResponseWriter, r *http.Request) any { f.reboot(20 * time.Second); return ok })

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

	f.handle("GET /storage", true, func(w http.ResponseWriter, r *http.Request) any {
		disks := []map[string]any{
			{"path": "/dev/nvme0n1p4", "model": "Samsung SSD 990 PRO 1TB", "size": 960_000_000_000, "uuid": "sys-0001", "label": "vos_data", "fstype": "ext4", "mounted_at": "/state", "is_system": true, "free": 612_000_000_000},
			{"path": "/dev/sda1", "model": "Samsung SSD 870 EVO 1TB", "size": 1_000_000_000_000, "uuid": "5e1d-aa01", "label": "SATA1TB", "fstype": "ext4", "steam_library": true, "library_dir": "."},
			{"path": "/dev/sdb1", "model": "WDC WD20EZAZ", "size": 2_000_000_000_000, "uuid": "77b2-c9d0", "label": "Games2", "fstype": "btrfs", "steam_library": true, "library_dir": "SteamLibrary"},
			{"path": "/dev/sdc1", "model": "Crucial X9", "size": 500_000_000_000, "uuid": "E0A1-33F2", "label": "WINDATA", "fstype": "ntfs", "steam_library": false},
			{"path": "/dev/sdd1", "model": "SanDisk Extreme", "size": 256_000_000_000, "uuid": "6A1B-2C3D", "label": "CAMERA", "fstype": "exfat", "steam_library": false},
		}
		attached := map[string]bool{}
		for _, d := range disks {
			uuid := d["uuid"].(string)
			attached[uuid] = true
			st, adopted := f.libraries[uuid]
			d["registered"] = adopted && st == "registered"
			if !adopted {
				continue
			}
			d["adopted"], d["mounted_at"], d["free"] = true, "/var/mnt/"+d["label"].(string), 420_000_000_000
			d["registration_pending"] = st == "pending"
			if d["steam_library"] != true {
				d["steam_library"], d["library_dir"] = true, "SteamLibrary" // made on adoption
			}
		}
		for uuid, st := range f.libraries {
			if !attached[uuid] {
				disks = append(disks, map[string]any{"uuid": uuid, "label": "OldSSD", "fstype": "ext4", "is_system": false, "steam_library": false,
					"adopted": true, "missing": true, "registered": st == "registered", "registration_pending": st == "pending"})
			}
		}
		return map[string]any{"disks": disks}
	})
	f.handle("POST /storage/libraries", true, func(w http.ResponseWriter, r *http.Request) any {
		uuid := fmt.Sprint(body(r)["uuid"])
		label, ok := map[string]string{"5e1d-aa01": "SATA1TB", "77b2-c9d0": "Games2", "E0A1-33F2": "WINDATA"}[uuid]
		if !ok {
			api.Error(w, http.StatusBadRequest, "exfat filesystems cannot hold a Steam library (use ext4, btrfs, xfs, f2fs or NTFS)")
			return nil
		}
		// Games2 waits for Steam to stop, like a disk adopted mid-stream.
		st, hadGames := "registered", uuid != "E0A1-33F2"
		if uuid == "77b2-c9d0" {
			st = "pending"
		}
		f.libraries[uuid] = st
		mp, lib := "/var/mnt/"+label, "/var/mnt/"+label
		if uuid != "5e1d-aa01" {
			lib += "/SteamLibrary"
		}
		games := ""
		if hadGames {
			games = " Its installed games appear in Steam without downloading."
		}
		hint := lib + " is one of Steam's game libraries." + games
		if st == "pending" {
			hint = "VaporOS adds " + lib + " to Steam's game libraries the next time Steam is not running (at the latest after a restart)." + games +
				" To use it right away, open Steam > Settings > Storage > Add Drive and choose " + lib + "."
		}
		return map[string]any{"mountpoint": mp, "library": lib, "registered": st == "registered", "registration_pending": st == "pending", "hint": hint}
	})
	f.handle("DELETE /storage/libraries/{uuid}", true, func(w http.ResponseWriter, r *http.Request) any {
		delete(f.libraries, r.PathValue("uuid"))
		return ok
	})

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
	f.handle("GET /ssh", true, func(w http.ResponseWriter, r *http.Request) any { return f.ssh })
	f.handle("PUT /ssh", true, func(w http.ResponseWriter, r *http.Request) any { f.ssh = body(r); return ok })

	setup := func(h func(w http.ResponseWriter, r *http.Request) any) func(w http.ResponseWriter, r *http.Request) any {
		return func(w http.ResponseWriter, r *http.Request) any {
			if !f.headless && r.Header.Get("X-VOS-Setup") != "ABCD-EFGH" {
				if c, err := r.Cookie("vos_setup"); err != nil || c.Value != "ABCD-EFGH" {
					api.Error(w, http.StatusForbidden, "setup code required")
					return nil
				}
			}
			return h(w, r)
		}
	}
	f.handle("GET /install/probe", false, setup(func(w http.ResponseWriter, r *http.Request) any {
		return map[string]any{
			"disks": []map[string]any{
				{"path": "/dev/nvme0n1", "model": "Samsung SSD 990 PRO 1TB", "size": 1_000_204_886_016, "transport": "nvme", "removable": false, "is_live": false, "has_vaporos": true, "steam_libraries": []any{}},
				{"path": "/dev/sda", "model": "Samsung SSD 870 EVO 1TB", "size": 1_000_204_886_016, "transport": "sata", "removable": false, "is_live": false, "has_vaporos": false,
					"steam_libraries": []map[string]string{{"uuid": "5e1d-aa01", "label": "SATA1TB", "path": "/SteamLibrary"}}},
				{"path": "/dev/sdb", "model": "SanDisk Ultra Fit", "size": 32_000_000_000, "transport": "usb", "removable": true, "is_live": true, "has_vaporos": false},
				{"path": "/dev/sdc", "model": "Kingston DataTraveler", "size": 16_000_000_000, "transport": "usb", "removable": true, "is_live": false, "has_vaporos": false},
			},
			"ips": []string{"192.168.1.167"}, "timezone": "UTC", "min_size": 26_306_674_688, "source": "",
			"gpu": map[string]any{"vendor": "amd", "name": "AMD Radeon RX 9070 XT", "driver": "amdgpu", "supported": true},
		}
	}))
	f.handle("POST /install", false, setup(func(w http.ResponseWriter, r *http.Request) any {
		go f.fakeInstall()
		return map[string]string{"job": "1"}
	}))
	f.handle("GET /install/status", false, setup(func(w http.ResponseWriter, r *http.Request) any { return f.install }))
	f.handle("POST /install/reboot", false, setup(func(w http.ResponseWriter, r *http.Request) any {
		f.installer = false
		f.reboot(8 * time.Second)
		return ok
	}))

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

func (f *fakeAPI) fakeInstall() {
	steps := []struct {
		step, msg string
		pct       int
	}{
		{"partition", "Creating partitions on /dev/nvme0n1", 5},
		{"format", "Formatting the data partition", 12},
		{"write", "Writing the system image (1.4 GB)", 20},
		{"write", "Writing the system image (1.4 GB)", 45},
		{"write", "Writing the system image (1.4 GB)", 70},
		{"verify", "Checking what was written", 85},
		{"boot", "Installing the boot loader", 93},
		{"config", "Saving your settings", 98},
	}
	for _, s := range steps {
		st := map[string]any{"state": "running", "step": s.step, "percent": s.pct, "message": s.msg, "error": ""}
		f.mu.Lock()
		f.install = st
		f.mu.Unlock()
		f.hub.Publish("install.progress", st)
		time.Sleep(900 * time.Millisecond)
	}
	done := map[string]any{"state": "done", "step": "done", "percent": 100, "message": "VaporOS is installed", "error": ""}
	f.mu.Lock()
	f.install = done
	f.mu.Unlock()
	f.hub.Publish("install.progress", done)
}

// ticker publishes the background events a real box would.
func (f *fakeAPI) ticker() {
	idle := 60
	for range time.Tick(5 * time.Second) {
		idle += 5
		f.hub.Publish("power.idle", map[string]any{"idle_seconds": idle, "shutdown_in": 15*60 - idle})
	}
}
