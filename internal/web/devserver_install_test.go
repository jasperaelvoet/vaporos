package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jasperaelvoet/vaporos/internal/api"
)

// The dev server's fake of the live ISO's installer (internal/install):
// the disk probe, the install job with the real steps and messages, and
// the restart into the installed system. Documents: base/install-probe.json
// and install-status.json. These routes need the setup code (Setup).

// The request checks of install/options.go.
var (
	fakeInstallHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	fakeTimezoneRe    = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+){0,3}$`)
	fakeLibraryRe     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

func (f *devFake) installRoutes(add fakeAdder) {
	add("GET", "/install/probe", api.Setup, func(w http.ResponseWriter, r *http.Request) any {
		p := cloneDoc(f.doc("install-probe"))
		q := r.URL.Query()
		p["source"] = q.Get("source")
		delete(p, "channel")
		if ch := q.Get("channel"); ch != "" {
			p["channel"] = ch
		}
		return p
	})
	add("POST", "/install", api.Setup, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Disk      string   `json:"disk"`
			Mode      string   `json:"mode"`
			Hostname  string   `json:"hostname"`
			Password  string   `json:"password"`
			Timezone  string   `json:"timezone"`
			Libraries []string `json:"libraries"`
			Source    string   `json:"source"`
			Channel   string   `json:"channel"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		if f.installing {
			api.Error(w, http.StatusConflict, "an installation is already running")
			return nil
		}
		disk, err := f.checkInstallLocked(&req.Disk, &req.Mode, &req.Hostname, req.Password, req.Timezone, req.Libraries)
		if err != nil {
			api.Error(w, http.StatusBadRequest, "%v", err)
			return nil
		}
		f.installing = true
		f.setInstallLocked("running", "probe", 0, "Starting the installation", "")
		go f.runInstall(disk, req.Mode, req.Hostname, req.Password)
		b := make([]byte, 8)
		rand.Read(b)
		return fakeStatus{http.StatusAccepted, map[string]string{"job": hex.EncodeToString(b)}}
	})
	add("GET", "/install/status", api.Setup, func(w http.ResponseWriter, r *http.Request) any {
		return f.doc("install-status")
	})
	add("POST", "/install/reboot", api.Setup, func(w http.ResponseWriter, r *http.Request) any {
		if f.installing {
			api.Error(w, http.StatusConflict, "an installation is running")
			return nil
		}
		// The ISO restarts into the installed system, or into itself.
		f.restartInto(20*time.Second, f.installed)
		return fakeOK
	})
}

// checkInstallLocked is the request check of install.validateRequest.
func (f *devFake) checkInstallLocked(disk, mode, hostname *string, password, timezone string, libraries []string) (map[string]any, error) {
	*disk = strings.TrimSpace(*disk)
	if *disk == "" {
		return nil, fmt.Errorf("no disk given")
	}
	*mode = strings.ToLower(strings.TrimSpace(*mode))
	switch *mode {
	case "":
		*mode = "erase"
	case "erase", "repair":
	default:
		return nil, fmt.Errorf("mode must be %q or %q, not %q", "erase", "repair", *mode)
	}
	*hostname = strings.ToLower(strings.TrimSpace(*hostname))
	if *hostname == "" && *mode == "erase" {
		*hostname = "vapor"
	}
	switch {
	case *hostname == "":
	case !fakeInstallHostRe.MatchString(*hostname):
		return nil, fmt.Errorf("invalid hostname %q: use 1-63 letters, digits and hyphens", *hostname)
	case *hostname == "localhost":
		return nil, fmt.Errorf(`invalid hostname: "localhost" is reserved`)
	}
	if password != "" {
		if utf8.RuneCountInString(password) < 8 {
			return nil, fmt.Errorf("the admin password needs at least %d characters", 8)
		}
		if len(password) > 1024 || !utf8.ValidString(password) || strings.ContainsRune(password, 0) {
			return nil, fmt.Errorf("the admin password is not valid")
		}
	}
	if tz := strings.TrimSpace(timezone); tz != "" && !fakeTimezoneRe.MatchString(tz) {
		return nil, fmt.Errorf("invalid timezone %q", tz)
	}
	for _, u := range libraries {
		if u = strings.TrimSpace(u); u != "" && !fakeLibraryRe.MatchString(u) {
			return nil, fmt.Errorf("invalid filesystem UUID %q", u)
		}
	}
	dev := "/dev/" + strings.TrimPrefix(*disk, "/dev/")
	for _, x := range asList(f.doc("install-probe")["disks"]) {
		d := asObj(x)
		if asStr(d["path"]) != dev {
			continue
		}
		switch {
		case d["is_live"] == true:
			return nil, fmt.Errorf("%s holds the VaporOS installer itself; choose another disk", dev)
		case *mode == "repair" && d["has_vaporos"] != true:
			return nil, fmt.Errorf("%s holds no VaporOS installation to repair", dev)
		}
		return d, nil
	}
	return nil, fmt.Errorf("%s is not a disk VaporOS can be installed on", dev)
}

func (f *devFake) setInstallLocked(state, step string, pct int, msg, errText string) {
	st := map[string]any{"state": state, "step": step, "percent": pct, "message": msg, "error": errText}
	f.docs["install-status"] = st
	f.emitLocked("install.progress", map[string]any{"step": step, "percent": pct, "message": msg, "state": state})
}

// runInstall walks the real steps and messages (install/install.go,
// image.go, target.go), then waits for POST /install/reboot, which boots
// the installed system with the name and password the wizard chose.
func (f *devFake) runInstall(disk map[string]any, mode, hostname, password string) {
	dev := asStr(disk["path"])
	part := dev + "2"
	if strings.Contains(dev, "nvme") {
		part = dev + "p2"
	}
	version := asStr(f.doc("install-probe")["version"])
	type step struct {
		step string
		pct  int
		msg  string
	}
	steps := []step{
		{"probe", 0, "Checking the system"},
		{"probe", 1, "Reading the VaporOS image from /run/vos/medium/vos"},
		{"probe", 2, fmt.Sprintf("Ready to install VaporOS %s on %s", version, dev)},
	}
	if mode == "repair" {
		steps = append(steps, step{"partition", 3, "Checking the data partition"},
			step{"partition", 6, "Formatting the boot partition"}, step{"partition", 10, "Disk prepared"})
	} else {
		steps = append(steps, step{"partition", 3, "Erasing " + dev}, step{"partition", 5, "Creating partitions"},
			step{"partition", 8, "Creating filesystems"}, step{"partition", 10, "Disk prepared"})
	}
	for pct := 10; pct < 75; pct += 8 {
		steps = append(steps, step{"write", pct, fmt.Sprintf("Writing VaporOS %s to %s", version, part)})
	}
	steps = append(steps, step{"verify", 75, "Verifying " + part}, step{"verify", 90, "Image verified"},
		step{"bootloader", 90, "Installing the bootloader"}, step{"bootloader", 92, "Writing the boot entry"},
		step{"bootloader", 95, "Bootloader installed"},
		step{"configure", 95, "Configuring " + map[bool]string{true: hostname, false: "the system"}[hostname != ""]},
		step{"configure", 99, "Finishing"})
	for _, s := range steps {
		select {
		case <-f.ctx.Done():
			return
		case <-time.After(700 * time.Millisecond):
		}
		f.mu.Lock()
		if fail := f.preset.Sim.InstallFail; fail != "" && s.step == "write" {
			f.installing = false
			f.setInstallLocked("failed", s.step, s.pct, "Installation failed: "+fail, fail)
			f.mu.Unlock()
			return
		}
		f.setInstallLocked("running", s.step, s.pct, s.msg, "")
		f.mu.Unlock()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installing = false
	f.setInstallLocked("done", "done", 100, fmt.Sprintf("VaporOS %s is installed on %s", version, dev), "")
	if f.d == nil {
		return
	}
	// What the restart boots: a fresh system (the empty preset) with the
	// name and password from the wizard. A repair keeps the disk's name.
	docs, err := f.d.fx.docs(f.d.fx.presets["empty"], time.Now())
	if err != nil {
		return
	}
	if hostname == "" {
		hostname = asStr(disk["hostname"])
	}
	if hostname == "" {
		hostname = "vapor"
	}
	sys := asObj(docs["system"])
	sys["hostname"], sys["mdns"], sys["uptime_s"] = hostname, hostname+".local", 0
	f.installed = &devBoot{docs: docs, errs: map[string]fakeError{}, preset: "empty", password: password}
}
