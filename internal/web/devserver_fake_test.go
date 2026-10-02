package web

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// devFake is the dev server's stand-in for vosd's services. It keeps one
// JSON document per GET resource (fixtures/base, fakeResources), answers
// with the real status codes and messages, and registers its routes on
// the real api.Server with their real access levels, so the real guard
// (sessions, CSRF, setup code, JSON errors, security headers) applies.
// Each resource's routes are in devserver_<resource>_test.go.
type devFake struct {
	d         *devServer // nil in tests that only list routes
	hub       *events.Hub
	preset    *fakePreset
	installer bool

	mu         sync.Mutex
	docs       map[string]any
	errs       map[string]fakeError
	lat        map[string]time.Duration
	booted     time.Time
	lastTouch  time.Time // the viewer's last request (web activity)
	idleSince  time.Time
	powerSent  string          // what the last power.idle said: "" none yet, "idle", or the busy reason
	restMode   any             // display.current before a stream
	stage      *fakeStageRun   // the running update stage
	installing bool            // an install job runs
	installed  *devBoot        // what the ISO boots once the install is done
	ctx        context.Context // the world's lifetime
	routes     []fakeRoute
}

type fakeRoute struct {
	Method, Path string
	Access       api.Access
}

// fakeHandler runs with f.mu held. It returns the value to answer with
// (200, or fakeStatus), or nil when it wrote the answer itself.
type fakeHandler func(w http.ResponseWriter, r *http.Request) any

// fakeAdder registers one route.
type fakeAdder func(method, path string, access api.Access, h fakeHandler)

// fakeStatus answers v with a status other than 200.
type fakeStatus struct {
	code int
	v    any
}

var fakeOK = struct{}{}

func newDevFake(d *devServer, w *devWorld, p *fakePreset, docs map[string]any, installer bool) *devFake {
	now := time.Now()
	f := &devFake{
		d: d, preset: p, installer: installer, docs: docs, ctx: context.Background(),
		errs: map[string]fakeError{}, lat: map[string]time.Duration{},
		booted:    now.Add(-time.Duration(asNum(asObj(docs["system"])["uptime_s"])) * time.Second),
		idleSince: now.Add(-time.Duration(p.Sim.IdleSeconds) * time.Second),
		restMode:  asObj(docs["display"])["current"],
	}
	if w != nil {
		f.hub = w.hub
	} else {
		f.hub = events.NewHub()
	}
	for k, e := range p.Errors {
		f.errs[k] = e
	}
	for k, ms := range p.Latency {
		f.lat[k] = time.Duration(ms) * time.Millisecond
	}
	return f
}

// routeTable adds every route vosd serves in this mode: the display and
// the machine always, the installer on the live ISO, the rest installed
// (internal/daemon/daemon.go).
func (f *devFake) routeTable(add fakeAdder) {
	f.systemRoutes(add)
	f.displayRoutes(add)
	if f.installer {
		f.installRoutes(add)
		return
	}
	f.updateRoutes(add)
	f.sunshineRoutes(add)
	f.storageRoutes(add)
	f.powerRoutes(add)
	f.extensionsRoutes(add)
	f.statusRoutes(add)
}

// register puts the routes on srv behind injected latency and errors.
func (f *devFake) register(srv *api.Server) {
	f.routeTable(func(method, path string, access api.Access, h fakeHandler) {
		key := method + " " + path
		f.routes = append(f.routes, fakeRoute{method, path, access})
		srv.Handle(method, path, access, func(w http.ResponseWriter, r *http.Request) {
			if d := f.delay(key); d > 0 {
				select {
				case <-time.After(d):
				case <-r.Context().Done():
					return
				}
			}
			f.mu.Lock()
			if e, ok := f.errs[key]; ok {
				f.mu.Unlock()
				api.Error(w, e.Status, "%s", e.Message)
				return
			}
			v := h(w, r)
			code := http.StatusOK
			if s, ok := v.(fakeStatus); ok {
				code, v = s.code, s.v
			}
			var b []byte
			if v != nil {
				b, _ = json.Marshal(v) // under the lock: v may share maps with f.docs
			}
			f.mu.Unlock()
			if v != nil {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(code)
				w.Write(append(b, '\n'))
			}
		})
	})
}

// realisticLatency is how long the slow calls take on a real box
// (/__dev/latency, VOS_WEB_LATENCY=1); other calls take 80 ms.
var realisticLatency = map[string]time.Duration{
	"GET /sunshine":           1200 * time.Millisecond, // Sunshine's API, up to 3 s
	"POST /sunshine/pair":     2500 * time.Millisecond, // Moonlight's handshake, up to 90 s
	"GET /sunshine/clients":   400 * time.Millisecond,
	"GET /storage":            900 * time.Millisecond, // lsblk and probing mounts
	"POST /storage/libraries": 1500 * time.Millisecond,
	"GET /power":              800 * time.Millisecond,  // ethtool, up to 3 s per adapter
	"POST /update/check":      4 * time.Second,         // the registry, up to 2 min
	"GET /install/probe":      2500 * time.Millisecond, // disks and the image, up to 20 s
}

func (f *devFake) delay(key string) time.Duration {
	f.mu.Lock()
	d, ok := f.lat[key]
	f.mu.Unlock()
	if ok {
		return d
	}
	if f.d != nil && f.d.slow.Load() {
		if d, ok := realisticLatency[key]; ok {
			return d
		}
		return 80 * time.Millisecond
	}
	return 0
}

// start runs what the services do on their own: the idle policy, and a
// stage or install the preset has in progress.
func (f *devFake) start(ctx context.Context) {
	f.ctx = ctx
	if f.installer {
		return
	}
	go f.powerLoop(ctx)
	f.mu.Lock()
	f.resumeExtInstallsLocked()
	f.mu.Unlock()
	if s := f.preset.Sim.Stage; s != nil {
		f.mu.Lock()
		f.startStageLocked("", *s)
		f.mu.Unlock()
	}
}

// touch is the api's activity hook: a signed-in request that is not
// passive keeps the box busy for five minutes (power.webActivityWindow).
func (f *devFake) touch() {
	if wa := f.preset.Sim.WebActivity; wa != nil && !*wa {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastTouch = time.Now()
	f.powerCheckLocked(time.Now(), false)
}

// publishRaw publishes an event from a preset, a script or /__dev/event,
// with "@now" times resolved, after the fake has followed it.
func (f *devFake) publishRaw(ev fakeEvent) {
	var data any
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		data = nil
	}
	data = resolveTimes(data, time.Now())
	f.mu.Lock()
	f.emitLocked(ev.Topic, data)
	f.mu.Unlock()
}

// emitLocked lets the fake services follow an event, as the real ones
// follow the hub, then publishes it.
func (f *devFake) emitLocked(topic string, data any) {
	f.followLocked(topic, data)
	f.hub.Publish(topic, data)
}

// followLocked is what the real services do when an event is published:
// Sunshine and the display follow a session (sunshine.go onEvent,
// display/modeswitch.go), Sunshine's pairings follow pairing.state and
// whether it runs sunshine.state, and the update state follows update.state.
func (f *devFake) followLocked(topic string, data any) {
	m := asObj(data)
	sun, disp := f.doc("sunshine"), f.doc("display")
	switch topic {
	case "session.begin":
		sess := asObj(deepCopyJSON(m))
		if asStr(sess["since"]) == "" {
			sess["since"] = time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
		}
		if disp["state"] != "streaming" {
			f.restMode = disp["current"]
		}
		sun["streaming"], sun["session"] = true, sess
		disp["state"] = "streaming"
		if mode := asStr(m["mode"]); mode != "" {
			disp["current"] = mode
		}
		f.rememberDeviceLocked(asStr(m["client"]), asStr(m["mode"]), m["hdr"] == true)
		f.powerCheckLocked(time.Now(), false)
	case "session.end":
		sun["streaming"], sun["session"] = false, nil
		disp["state"] = "none"
		for _, c := range asList(disp["connectors"]) {
			if asObj(c)["physical"] == true {
				disp["state"] = "welcome"
			}
		}
		disp["current"] = f.restMode
		f.powerCheckLocked(time.Now(), false)
	case "pairing.state":
		ps := asList(m["pairings"])
		if ps == nil {
			ps = []any{}
		}
		sun["pairings"], sun["pending_pairing"] = deepCopyJSON(ps), len(ps) > 0
	case "sunshine.state":
		sun["running"] = m["running"] == true
	case "update.state":
		up := f.doc("update")
		for _, k := range updateStateKeys {
			delete(up, k)
			if v, ok := m[k]; ok {
				up[k] = deepCopyJSON(v)
			}
		}
	}
}

// rememberDeviceLocked records a client's last mode, newest first, as
// clients.json does for GET /display devices.
func (f *devFake) rememberDeviceLocked(name, mode string, hdr bool) {
	if name == "" || mode == "" {
		return
	}
	disp := f.doc("display")
	devs := []any{map[string]any{"name": name, "mode": mode, "hdr": hdr,
		"last_seen": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)}}
	for _, d := range asList(disp["devices"]) {
		if asStr(asObj(d)["name"]) != name {
			devs = append(devs, d)
		}
	}
	disp["devices"] = devs
}

// applyStep applies a script step's patch and events.
func (f *devFake) applyStep(st fakeStep) error {
	f.mu.Lock()
	err := applyPatch(f.docs, st.Patch, time.Now())
	if _, ok := st.Patch["update"]; ok && err == nil {
		f.emitLocked("update.state", f.updateStateLocked())
	}
	f.mu.Unlock()
	for _, ev := range st.Events {
		f.publishRaw(ev)
	}
	return err
}

// bootState is what the box boots with next: the documents as they are,
// after change (a restart into a staged update), with a fresh uptime.
func (f *devFake) bootState(change func(docs map[string]any)) *devBoot {
	f.mu.Lock()
	defer f.mu.Unlock()
	docs := deepCopyJSON(f.docs).(map[string]any)
	asObj(docs["system"])["uptime_s"] = 0
	up := asObj(docs["update"])
	up["busy"], up["progress"] = false, nil
	sun, disp := asObj(docs["sunshine"]), asObj(docs["display"])
	if sun["streaming"] == true {
		sun["streaming"], sun["session"] = false, nil
		disp["state"], disp["current"] = "welcome", f.restMode
	}
	disp["reboot_needed"] = false
	sun["pairings"], sun["pending_pairing"] = []any{}, false
	errs := map[string]fakeError{}
	for k, e := range f.errs {
		errs[k] = e
	}
	if !f.installer {
		bootNext(docs)
		bootExtensions(docs)
	}
	if change != nil {
		change(docs)
	}
	return &devBoot{docs: docs, errs: errs, installer: f.installer}
}

// restartLocked answers first, then takes the box down a second later for
// dur (or until woken when dur < 0), as system.handlePower does.
func (f *devFake) restartLocked(dur time.Duration, change func(docs map[string]any)) {
	if f.d == nil {
		return
	}
	go func() {
		time.Sleep(time.Second)
		f.d.goDown(dur, f.bootState(change))
	}()
}

// restartInto is restartLocked into another system (nil: this one).
func (f *devFake) restartInto(dur time.Duration, boot *devBoot) {
	if boot == nil {
		f.restartLocked(dur, nil)
		return
	}
	if f.d == nil {
		return
	}
	go func() {
		time.Sleep(time.Second)
		f.d.goDown(dur, boot)
	}()
}

// snapshot is a copy of the documents.
func (f *devFake) snapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return deepCopyJSON(f.docs).(map[string]any)
}

// doc is one document as an object (created when missing).
func (f *devFake) doc(name string) map[string]any {
	m, ok := f.docs[name].(map[string]any)
	if !ok {
		m = map[string]any{}
		f.docs[name] = m
	}
	return m
}

// clone is a shallow copy of a document for an answer that adds fields.
func cloneDoc(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func asObj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asStr(v any) string {
	s, _ := v.(string)
	return s
}

func asNum(v any) float64 {
	n, _ := v.(float64)
	return n
}

// strictBody decodes a request body the way the real handlers do, into v.
func strictBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := api.ReadJSON(r, v); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return false
	}
	return true
}

// containsString reports whether l holds s.
func containsString(l []any, s string) bool {
	return slices.ContainsFunc(l, func(v any) bool { return v == s })
}
