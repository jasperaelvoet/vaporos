package web

import (
	"net/http"
	"time"
)

// The dev server's fake of the machine: its name, versions, restart, power off
// and SSH. TestDevServer and the fake's core are in devserver_test.go.

func (f *fakeAPI) seedSystem() {
	f.hostname = "vapor"
	f.ssh = map[string]any{"enabled": false, "keys": []string{}}
}

func (f *fakeAPI) systemRoutes() {
	ok := struct{}{}
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

	f.handle("GET /ssh", true, func(w http.ResponseWriter, r *http.Request) any { return f.ssh })
	f.handle("PUT /ssh", true, func(w http.ResponseWriter, r *http.Request) any { f.ssh = body(r); return ok })
}
