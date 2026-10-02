package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// The control center's tests run on an installed system whose image ships
// the v1 extensions' descriptors as the build writes them (with their
// build sections), plus LACT, which provides the same capability as
// CoolerControl, and HOTAS rules that require Star Citizen. Proton is core
// and mounted, as on a box after its first trial.
var shipped = map[string]string{
	"proton": `{"schema":1,"id":"proton","name":"CachyOS Proton","summary":"Runs Windows games in Steam, with CachyOS's own fixes and tuning.",
		"category":"runtime","core":true,"upstream":{"name":"CachyOS","url":"https://github.com/CachyOS/proton-cachyos","license":"LGPL-2.1-or-later and others"},
		"caveats":["Pick another Proton for a single game in that game's Steam properties."],
		"packages":["cachyos/proton-cachyos-slr"],"elf_exempt":["usr/share/steam/compatibilitytools.d"],
		"permissions":["modules","compat-tool"],"steam":{"default_compat_tool":"proton-cachyos-slr"},
		"build":{"size":5000,"packages":["proton-cachyos-slr 1:10.0.20260929-1"],"permissions":["modules","compat-tool"]}}`,
	"coolercontrol": `{"schema":1,"id":"coolercontrol","name":"CoolerControl","summary":"Fan curves for your graphics card, CPU and case fans, from a web page.",
		"category":"system","upstream":{"name":"CoolerControl","url":"https://gitlab.com/coolercontrol/coolercontrol","license":"GPL-3.0-or-later"},
		"caveats":["It controls fans directly. VaporOS puts them back to automatic when it stops."],
		"copy":{"install":"CoolerControl runs as root to reach your fans. It starts after the next restart.","remove":"Your fans go back to automatic."},
		"packages":["cachyos/coolercontrold"],"provides":["fan-control.amdgpu"],"permissions":["service","udev","modules"],
		"services":[{"unit":"coolercontrold.service","scope":"system"},{"unit":"cc-fans@.service","scope":"system"}],
		"module_options":[{"module":"amdgpu","param":"ppfeaturemask","setting":"overdrive"}],
		"network":{"ports":[{"proto":"tcp","port":11987,"mode":"proxied","upstream":"127.0.0.1:11986"}]},
		"web":{"port":11987,"label":"Open CoolerControl"},
		"actions":[{"name":"restore-fans","label":"Restore fans","run_as":"root"},{"name":"curves","label":"Reset curves"}],
		"data":[{"name":"config","where":"system"}],
		"settings":[{"key":"overdrive","type":"bool","label":"Graphics card overclocking","help":"Lets CoolerControl change the card's clocks.","restart":true},
			{"key":"poll","type":"choice","label":"Sensor updates","choices":["normal","slow"],"default":"slow"}],
		"build":{"size":2000,"permissions":["service","udev","modules"],"runs_as_root":true}}`,
	"lact": `{"schema":1,"id":"lact","name":"LACT","summary":"Graphics card fan and clock control.","category":"system",
		"upstream":{"name":"LACT","url":"https://github.com/ilya-zlobintsev/LACT","license":"MIT"},"provides":["fan-control.amdgpu"],
		"permissions":["service"],"services":[{"unit":"lactd.service","scope":"system"}],
		"build":{"size":1500,"permissions":["service"],"runs_as_root":true}}`,
	"truckersmp": `{"schema":1,"id":"truckersmp","name":"TruckersMP","summary":"Multiplayer for Euro Truck Simulator 2 and American Truck Simulator.",
		"category":"app","upstream":{"name":"TruckersMP","url":"https://truckersmp.com","license":"MIT"},
		"caveats":["Steam downloads ETS2 and ATS again as their Windows version."],"requires":["proton"],
		"downloads":[{"what":"The TruckersMP mod files","from":"update.ets2mp.com","checked":"publisher-hash","runs_code":true,"when":"update"}],
		"data":[{"name":"mods","where":"home"}],
		"actions":[{"name":"copy-profiles","label":"Copy profiles","confirm":{"title":"Copy your profiles?","body":"Profiles saved on this PC are copied to the Windows version.","button":"Copy"}}],
		"steam":{"compat_tool":"proton-cachyos-slr","force_compat_tool":[227300,270880],"hooks":[{"apps":[227300,270880]}]},
		"build":{"size":3000,"permissions":[]}}`,
	"star-citizen": `{"schema":1,"id":"star-citizen","name":"Star Citizen","summary":"The RSI Launcher, set up to install Star Citizen on a disk you pick.",
		"category":"app","upstream":{"name":"Cloud Imperium Games","url":"https://robertsspaceindustries.com","license":"Proprietary"},
		"caveats":["Easy Anti-Cheat isn't tested on VaporOS yet."],"requires":["proton"],
		"data":[{"name":"prefix","where":"library","min_free_gb":150,"fs":["ext4","btrfs","xfs","f2fs"]}],
		"settings":[{"key":"library","type":"disk","label":"Install on"}],
		"build":{"size":4000,"permissions":[]}}`,
	"sc-hotas": `{"schema":1,"id":"sc-hotas","name":"HOTAS for Star Citizen","summary":"Joystick and throttle rules for Star Citizen.","category":"app",
		"upstream":{"name":"VaporOS","url":"https://github.com/jasperaelvoet/vaporos","license":"MIT"},"requires":["star-citizen"],
		"permissions":["udev"],"build":{"size":1000,"permissions":["udev"]}}`,
}

type rig struct {
	*env
	s    *Service
	b    *booted
	imgs map[string]image

	mu      sync.Mutex
	units   []string // systemctl calls
	gamer   []string // AsGamer calls
	events  []Document
	reauths int
	running map[bool][]string                                                      // the units listUnits finds, system (false) and user (true)
	asVapor func(ctx context.Context, name string, args ...string) (string, error) // runs an AsGamer call, after it is recorded
}

// rigPassword is the admin password the rig's re-authentication accepts.
const rigPassword = "vaporvapor"

func newRig(t *testing.T) *rig {
	t.Helper()
	e := newEnv(t)
	dir := t.TempDir()
	savedDesc, savedHome, savedUID := config.ExtDescriptorsDir, config.GamerHome, config.GamerUID
	t.Cleanup(func() { config.ExtDescriptorsDir, config.GamerHome, config.GamerUID = savedDesc, savedHome, savedUID })
	config.ExtDescriptorsDir = filepath.Join(dir, "descriptors")
	config.GamerHome = filepath.Join(dir, "home")
	config.GamerUID = -1 // tests cannot chown to vapor
	must(t, os.MkdirAll(config.GamerHome, 0o700))
	for id, d := range shipped {
		writeFile(t, filepath.Join(config.ExtDescriptorsDir, id+".json"), d)
	}
	r := &rig{env: e, imgs: map[string]image{}}
	order := []struct {
		id       string
		size     int
		requires []string
	}{{"proton", 5000, nil}, {"coolercontrol", 2000, nil}, {"lact", 1500, nil},
		{"truckersmp", 3000, []string{"proton"}}, {"star-citizen", 4000, []string{"proton"}},
		{"sc-hotas", 1000, []string{"star-citizen"}}}
	var imgs []image
	for _, o := range order {
		img := newImage(t, o.id, "", o.size, o.id == "proton", o.requires...)
		r.imgs[o.id] = img
		imgs = append(imgs, img)
		e.serve(img)
	}
	e.catalog(imgs...)
	e.seal(r.imgs["proton"])
	var enabled *store.Set
	locked(t, func() (err error) {
		if enabled, err = store.WriteEnabled([]string{"proton"}, nil); err != nil {
			return err
		}
		return store.AddProven([]store.Pair{{ID: "proton", FSVerity: r.imgs["proton"].entry.FSVerity}})
	})
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: enabled.Name, Mounted: mountedAs(r.imgs["proton"])})
	r.s, r.b = e.service()
	r.wire()
	r.pass()
	return r
}

// wire gives the service the rig's seams: events, systemctl, AsGamer and
// the password check are recorded or faked.
func (r *rig) wire() {
	r.s.cc.publish = func(topic string, data any) {
		var d Document
		if b, ok := data.(json.RawMessage); topic == "extensions.state" && ok && json.Unmarshal(b, &d) == nil {
			r.mu.Lock()
			r.events = append(r.events, d)
			r.mu.Unlock()
		}
	}
	r.s.cc.systemctl = func(_ context.Context, user bool, args ...string) error {
		line := strings.Join(args, " ")
		if user {
			line = "--user " + line
		}
		r.mu.Lock()
		r.units = append(r.units, line)
		r.mu.Unlock()
		return nil
	}
	r.s.cc.listUnits = func(_ context.Context, user bool, patterns ...string) ([]string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		var out []string
		for _, u := range r.running[user] {
			if slices.ContainsFunc(patterns, func(p string) bool { ok, _ := path.Match(p, u); return ok }) {
				out = append(out, u)
			}
		}
		return out, nil
	}
	r.s.cc.asGamer = func(ctx context.Context, name string, args ...string) (string, error) {
		r.mu.Lock()
		r.gamer = append(r.gamer, strings.Join(append([]string{name}, args...), " "))
		run := r.asVapor
		r.mu.Unlock()
		switch {
		case run != nil:
			return run(ctx, name, args...)
		case name == vosBinary:
			return vaporCLI(ctx, args...)
		}
		return "", nil
	}
	r.s.cc.reauth = func(w http.ResponseWriter, _ *http.Request, password string) bool {
		r.mu.Lock()
		r.reauths++
		r.mu.Unlock()
		if password != rigPassword {
			http.Error(w, `{"error":"the password is wrong"}`, http.StatusForbidden)
			return false
		}
		return true
	}
}

// vaporCLI runs `vos ext action` in this process, as AsGamer runs it as
// vapor: its output, and an error with its exit status.
func vaporCLI(_ context.Context, args ...string) (string, error) {
	if len(args) < 2 || args[0] != "ext" || args[1] != "action" {
		return "", fmt.Errorf("vos %v is not vos ext action", args)
	}
	var out bytes.Buffer
	if code := actionCmd(args[2:], &out); code != 0 {
		return out.String(), exitStatus(code)
	}
	return out.String(), nil
}

// exitStatus is a command's exit status, as *exec.ExitError tells it.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

// pass runs one reconcile, as Run does after a change, and waits for the
// helpers' installs it started.
func (r *rig) pass() {
	r.t.Helper()
	r.s.pass(r.t.Context(), r.b)
	r.s.waitInstalls()
}

// boot restarts into the set a restart tries: pending, mounted whole, as
// a trial `vos health` passes, then vosd's first pass.
func (r *rig) boot() {
	r.t.Helper()
	p, err := store.Pending()
	must(r.t, err)
	if p == nil {
		r.t.Fatal("nothing pending to boot")
	}
	var imgs []image
	for _, id := range p.IDs {
		imgs = append(imgs, r.imgs[id])
	}
	rep := store.BootReport{Mode: store.ModePending, Set: p.Name, TriesLeft: p.Tries - 1, Mounted: mountedAs(imgs...)}
	r.report(rep)
	writeFile(r.t, config.ExtTrialOKPath(), p.Name+"\n")
	r.s, r.b = r.service()
	r.wire()
	makeDataAreas(&rep) // as Run does first
	r.pass()
}

func (r *rig) doc() Document {
	r.t.Helper()
	return r.s.Document(r.t.Context())
}

func (r *rig) card(id string) ExtensionDoc {
	r.t.Helper()
	for _, x := range r.doc().Extensions {
		if x.ID == id {
			return x
		}
	}
	r.t.Fatalf("no card for %s", id)
	return ExtensionDoc{}
}

// do sends a request through the routes Routes registers, behind a mux
// that stands in for the api server's (sessions and CSRF are its tests').
func (r *rig) do(method, path, body string) (int, string) {
	r.t.Helper()
	mux := http.NewServeMux()
	for _, rt := range []struct {
		pattern string
		h       http.HandlerFunc
	}{
		{"GET /extensions", r.s.handleGet},
		{"POST /extensions/skip-once", r.s.handleSkipOnce},
		{"DELETE /extensions/skip-once", r.s.handleKeepOnce},
		{"POST /extensions/{id}", r.s.handleInstall},
		{"DELETE /extensions/{id}", r.s.handleRemove},
		{"PUT /extensions/{id}/settings", r.s.handleSettings},
		{"POST /extensions/{id}/actions/{name}", r.s.handleAction},
		{"POST /extensions/{id}/retry", r.s.handleRetry},
	} {
		mux.HandleFunc(rt.pattern, rt.h)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code, strings.TrimSpace(w.Body.String())
}

// recHelper is a helper that records its calls, and how many of them ran
// at once.
type recHelper struct {
	NopHelper
	mu         sync.Mutex
	calls      []string
	installErr error
	removeErr  error
	actionErr  error
	status     []StatusLine
	options    func(x *Ext) []string

	// With hold set, Install tells started and waits for hold to close
	// (or for its context, unless stubborn).
	hold     chan struct{}
	started  chan struct{}
	stubborn bool

	inside, most int
}

func (h *recHelper) record(s string) {
	h.mu.Lock()
	h.calls = append(h.calls, s)
	h.mu.Unlock()
}

func (h *recHelper) Calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// enter and leave count the calls under way.
func (h *recHelper) enter() {
	h.mu.Lock()
	h.inside++
	h.most = max(h.most, h.inside)
	h.mu.Unlock()
}

func (h *recHelper) leave() {
	h.mu.Lock()
	h.inside--
	h.mu.Unlock()
}

func (h *recHelper) Status(context.Context, *Ext) []StatusLine { return h.status }

func (h *recHelper) Install(ctx context.Context, x *Ext) error {
	h.enter()
	defer h.leave()
	h.record("install " + x.ID)
	h.mu.Lock()
	hold, started, stubborn := h.hold, h.started, h.stubborn
	h.mu.Unlock()
	if hold != nil {
		started <- struct{}{}
		if stubborn {
			<-hold
		} else {
			select {
			case <-hold:
			case <-ctx.Done():
				h.record("install " + x.ID + " stopped")
				return ctx.Err()
			}
		}
	}
	return h.installErr
}

func (h *recHelper) Remove(_ context.Context, x *Ext, purge bool) error {
	h.enter()
	defer h.leave()
	if purge {
		h.record("remove " + x.ID + " purge")
	} else {
		h.record("remove " + x.ID)
	}
	return h.removeErr
}

func (h *recHelper) Action(_ context.Context, x *Ext, name string, args json.RawMessage) error {
	h.enter()
	defer h.leave()
	if name != "copy-profiles" && name != "restore-fans" {
		return ErrNoAction
	}
	h.record("action " + x.ID + " " + name + " " + string(args))
	return h.actionErr
}

func (h *recHelper) ModuleOptions(x *Ext) []string {
	if h.options != nil {
		return h.options(x)
	}
	return nil
}

// useHelper registers h for id for the test.
func useHelper(t *testing.T, id string, h Helper) {
	t.Helper()
	old, had := helpers[id]
	helpers[id] = h
	t.Cleanup(func() {
		if had {
			helpers[id] = old
		} else {
			delete(helpers, id)
		}
	})
}
