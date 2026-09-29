package web

import (
	"net/http"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
)

// The dev server's fake of the live ISO's installer: the disk probe, the
// install job and its progress events. TestDevServer and the fake's core are
// in devserver_test.go.

func (f *fakeAPI) seedInstall() {
	f.install = map[string]any{"state": "idle", "step": "", "percent": 0, "message": "", "error": ""}
}

func (f *fakeAPI) installRoutes() {
	ok := struct{}{}
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
