package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/system"
)

// The dev server's fake of the machine (internal/system): its name,
// versions, restart, power off and SSH. Documents: base/system.json and
// base/ssh.json. The fake's core is in devserver_fake_test.go.

func (f *devFake) systemRoutes(add fakeAdder) {
	add("GET", "/system", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.systemAnswerLocked() })
	add("PUT", "/system/hostname", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Hostname string `json:"hostname"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		name := strings.ToLower(strings.TrimSpace(req.Hostname))
		if err := system.ValidateHostname(name); err != nil {
			api.Error(w, http.StatusBadRequest, "%v", err)
			return nil
		}
		// Written where the real core reads it: within hostCacheTTL (2 s)
		// the old .local name answers 421, as on the box.
		if err := config.WriteFileAtomic(config.HostnamePath, []byte(name+"\n"), 0o644); err != nil {
			api.Error(w, http.StatusInternalServerError, "writing %s: %v", config.HostnamePath, err)
			return nil
		}
		sys := f.doc("system")
		sys["hostname"], sys["mdns"] = name, name+".local"
		return fakeOK
	})
	power := func(message string, dur time.Duration) fakeHandler {
		return func(w http.ResponseWriter, r *http.Request) any {
			f.emitLocked("system.message", map[string]string{"level": "info", "text": message})
			f.restartLocked(dur, nil)
			return fakeOK
		}
	}
	add("POST", "/system/reboot", api.Authed, power("Restarting…", 8*time.Second))
	add("POST", "/system/poweroff", api.Authed, power("Shutting down…", -1))

	add("GET", "/ssh", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.doc("ssh") })
	add("PUT", "/ssh", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req system.SSHState
		if !strictBody(w, r, &req) {
			return nil
		}
		keys, err := system.NormalizeKeys(req.Keys)
		if err != nil {
			api.Error(w, http.StatusBadRequest, "%v", err)
			return nil
		}
		if req.Enabled && len(keys) == 0 {
			api.Error(w, http.StatusBadRequest, "add at least one public key before enabling SSH: the vapor account has no password")
			return nil
		}
		ks := []any{}
		for _, k := range keys {
			ks = append(ks, k)
		}
		f.docs["ssh"] = map[string]any{"enabled": req.Enabled, "keys": ks}
		return f.docs["ssh"]
	})
}

// systemAnswerLocked is GET /system: the name from the file the Host check
// reads too, and the uptime since this boot.
func (f *devFake) systemAnswerLocked() map[string]any {
	s := cloneDoc(f.doc("system"))
	hn := config.Hostname()
	s["hostname"], s["mdns"] = hn, strings.SplitN(hn, ".", 2)[0]+".local"
	s["uptime_s"] = int(time.Since(f.booted).Seconds())
	return s
}
