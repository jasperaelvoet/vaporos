package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// TestDevServer serves a web UI set on the real api core (sessions, CSRF,
// Host and Origin checks, rate limits, the event stream and its replay)
// with fake services behind it, so the UI can be worked on without a
// VaporOS machine:
//
//	VOS_WEB_DEV=127.0.0.1:8081 go test ./internal/web -run '^TestDevServer$' -count=1 -timeout 0
//
// Sign in with the password vaporvapor. Sessions, the admin password and
// the hostname live in $VOS_WEB_STATE (default tools/web/.state), so a
// browser stays signed in across restarts.
//
//	VOS_WEB_PRESET=<name>  the starting state, from fixtures/presets (default idle);
//	                       VOS_WEB_STREAMING, _PAIRING, _SIGNED_OUT, _SETUP, _HELD,
//	                       _TRIAL, _INSTALLER and _HEADLESS=1 are aliases (presetAliases)
//	VOS_WEB_UI=legacy|next the UI set to serve (default: the one vosd serves)
//	VOS_WEB_LIVE=1         read templates/ and static/ from disk and reload on change
//	VOS_WEB_LATENCY=1      start with realistic slow calls on
//	VOS_WEB_STATE=<dir>    where the real core keeps its files
//
// Controls, answered only to loopback clients (see serveDev):
//
//	POST /__dev/preset  {"name"}            reset to base plus that preset
//	POST /__dev/event   {"topic","data"}    publish an event
//	POST /__dev/down    {"seconds"}         restart (n > 0), wake (0) or power off (n < 0)
//	POST /__dev/latency {"enabled"}         realistic slow calls on or off
//	POST /__dev/script  {"name"}            run fixtures/scripts/<name>.json
//	GET  /__dev/state                       the preset, the documents and the knobs
func TestDevServer(t *testing.T) {
	addr := os.Getenv("VOS_WEB_DEV")
	if addr == "" {
		t.Skip("set VOS_WEB_DEV=host:port to run the UI dev server")
	}
	fx, err := loadFixtures(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	preset, err := presetFromEnv(fx, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	set, err := uiSetFromEnv(os.Getenv("VOS_WEB_UI"))
	if err != nil {
		t.Fatal(err)
	}
	live := os.Getenv("VOS_WEB_LIVE") == "1"
	var fsys fs.FS = content
	if live {
		fsys = os.DirFS(".") // go test runs in internal/web, beside templates/ and static/
	}
	state, err := devStateDir(os.Getenv("VOS_WEB_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	redirectConfig(t, state)

	d := &devServer{fx: fx, set: set, fsys: fsys}
	d.slow.Store(os.Getenv("VOS_WEB_LATENCY") == "1")
	if err := d.loadPreset(preset); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if live {
		go d.watch(ctx, 250*time.Millisecond)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("VaporOS UI (%s set, preset %s) on http://%s; password vaporvapor; state in %s", set.Name, preset, ln.Addr(), state)
	hs := &http.Server{Handler: d, ReadHeaderTimeout: 10 * time.Second}
	if err := hs.Serve(ln); err != nil {
		t.Fatal(err)
	}
}

func uiSetFromEnv(name string) (uiSet, error) {
	if name == "" {
		return activeSet, nil
	}
	for _, s := range uiSets {
		if s.Name == name {
			return s, nil
		}
	}
	return uiSet{}, fmt.Errorf("VOS_WEB_UI=%q: want legacy or next", name)
}

// devStateDir is dir, else tools/web/.state at the repository root.
func devStateDir(dir string) (string, error) {
	if dir == "" {
		dir = filepath.Join("..", "..", "tools", "web", ".state")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return dir, os.MkdirAll(filepath.Join(dir, "run"), 0o700)
}

// redirectConfig points the real core's files (sessions, auth.json, the
// hostname) into dir for the rest of the test.
func redirectConfig(t *testing.T, dir string) {
	oldState, oldRun, oldHost := config.StateDir, config.RunDir, config.HostnamePath
	config.StateDir, config.RunDir, config.HostnamePath = dir, filepath.Join(dir, "run"), filepath.Join(dir, "hostname")
	t.Cleanup(func() { config.StateDir, config.RunDir, config.HostnamePath = oldState, oldRun, oldHost })
}

// devPassword is the admin password of every preset but first-run.
const devPassword = "vaporvapor"

// devSetupCode is the installer's and first-run's setup code.
const devSetupCode = "ABCD-EFGH"

// devServer is the outer handler: /__dev/* controls, the "down" state of a
// restarting box, and the current world (one real api.Server with its fake
// services), which a preset change or a boot replaces.
type devServer struct {
	fx   *fixtureSet
	set  uiSet
	fsys fs.FS
	slow atomic.Bool // realistic latency
	// built is the UI as last read from fsys, which every new world serves
	// (watch rebuilds it).
	built atomic.Pointer[ui]

	mu     sync.Mutex
	preset string
	world  *devWorld
	down   bool
	boot   *devBoot    // what comes up when the box is woken
	timer  *time.Timer // the end of a restart
	runs   []func()    // cancels running scripts
}

// devWorld is one boot of the fake box.
type devWorld struct {
	srv   *api.Server
	fake  *devFake
	ui    *uiHolder
	h     http.Handler
	ctx   context.Context // ends with the world: event streams, tickers
	stop  context.CancelFunc
	hub   *events.Hub
	start time.Time
}

// devBoot is the state a box boots with after a restart or power-off.
type devBoot struct {
	docs      map[string]any
	errs      map[string]fakeError
	installer bool
	preset    string // the preset the box runs from now on ("" keeps it)
	password  string // the admin password the installer set ("" keeps it)
}

// loadPreset resets everything to base plus the preset.
func (d *devServer) loadPreset(name string) error {
	p := d.fx.presets[name]
	if p == nil {
		return fmt.Errorf("no preset %q", name)
	}
	docs, err := d.fx.docs(p, time.Now())
	if err != nil {
		return err
	}
	switch p.Auth {
	case "first-run":
		if err := os.Remove(config.AuthPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	default:
		if err := auth.SetAdminPassword("", devPassword); err != nil {
			return err
		}
	}
	if p.Auth == "signed-out" {
		if err := os.Remove(config.SessionsPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	w, err := d.newWorld(p, docs, p.installer(), true)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.cancelRunsLocked()
	if d.timer != nil {
		d.timer.Stop()
	}
	old := d.world
	d.preset, d.world, d.down, d.boot = name, w, false, nil
	d.mu.Unlock()
	if old != nil {
		old.stop()
	}
	return nil
}

// newWorld builds a real api.Server with the UI and the fake services on
// docs. fresh publishes the preset's events (history before this boot).
func (d *devServer) newWorld(p *fakePreset, docs map[string]any, installer, fresh bool) (*devWorld, error) {
	if err := config.WriteFileAtomic(config.HostnamePath, []byte(asStr(asObj(docs["system"])["hostname"])+"\n"), 0o644); err != nil {
		return nil, err
	}
	// A new vosd starts with an empty hub: nothing to replay yet.
	hub := events.NewHub()
	events.Default = hub
	version := asStr(asObj(docs["update"])["booted"])
	srv := api.New(api.Options{Installer: installer, Version: version})
	if installer || !auth.HasAdmin() {
		srv.SetSetupCode(devSetupCode)
	}
	if installer && p.Headless {
		srv.SetSetupWaiver(func() bool { return true })
	}
	holder, err := d.mountUI(srv)
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	w := &devWorld{srv: srv, ui: holder, h: srv.Handler(), ctx: ctx, stop: stop, hub: hub, start: time.Now()}
	w.fake = newDevFake(d, w, p, docs, installer)
	w.fake.register(srv)
	if !installer {
		srv.OnActivity(w.fake.touch)
	}
	w.fake.start(ctx)
	if fresh {
		for _, ev := range p.Events {
			w.fake.publishRaw(ev)
		}
	}
	return w, nil
}

// mountUI is register with the UI the last world served, read again only
// when there is none yet, and a broken UI as an error instead of a panic.
// Reading it takes seconds under the race detector, which a world's clock
// would count against the fixtures' times.
func (d *devServer) mountUI(srv *api.Server) (*uiHolder, error) {
	if u := d.built.Load(); u != nil {
		return mount(srv, d.set, u.withServer(srv)), nil
	}
	u, err := newUI(srv, d.set, d.fsys)
	if err != nil {
		return nil, err
	}
	d.built.Store(u)
	return mount(srv, d.set, u), nil
}

func (d *devServer) current() (*devWorld, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.world, d.down
}

func (d *devServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/__dev/") {
		d.serveDev(w, r)
		return
	}
	world, down := d.current()
	if down {
		dropConnection(w)
		return
	}
	if r.URL.Path == api.Prefix+"/events" {
		// The stream ends with its world, as it does when vosd restarts.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		defer context.AfterFunc(world.ctx, cancel)()
		r = r.WithContext(ctx)
	}
	world.h.ServeHTTP(w, r)
}

// dropConnection closes the connection without an answer, as a machine
// that is restarting does.
func dropConnection(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if c, _, err := hj.Hijack(); err == nil {
			c.Close()
			return
		}
	}
	http.Error(w, "down", http.StatusServiceUnavailable)
}

// goDown takes the box down with what it boots with next: for dur, or
// until woken when dur < 0.
func (d *devServer) goDown(dur time.Duration, boot *devBoot) {
	d.mu.Lock()
	if d.down {
		d.mu.Unlock()
		return
	}
	old := d.world
	d.down, d.boot = true, boot
	if d.timer != nil {
		d.timer.Stop()
	}
	if dur >= 0 {
		d.timer = time.AfterFunc(dur, d.wake)
	}
	d.mu.Unlock()
	old.stop()
}

// wake boots the box again with the state it went down with.
func (d *devServer) wake() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.down {
		return
	}
	b := d.boot
	if b.preset != "" {
		d.preset = b.preset
	}
	p := d.fx.presets[d.preset]
	if b.password != "" {
		if err := auth.SetAdminPassword("", b.password); err != nil {
			log.Printf("dev server: %v", err)
		}
	}
	w, err := d.newWorld(p, b.docs, b.installer, false)
	if err != nil {
		log.Printf("dev server: booting: %v", err)
		return
	}
	w.fake.errs = b.errs
	d.world, d.down, d.boot = w, false, nil
}

func (d *devServer) cancelRunsLocked() {
	for _, c := range d.runs {
		c()
	}
	d.runs = nil
}

// devRequest decodes a control's JSON body.
func devRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return false
	}
	return true
}

// serveDev answers /__dev/*: only to loopback clients, and never to a page
// of another origin (a web page could otherwise post to localhost).
func (d *devServer) serveDev(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		api.Error(w, http.StatusForbidden, "dev controls answer only on this machine")
		return
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
		api.Error(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /__dev/preset":
		var req struct {
			Name string `json:"name"`
		}
		if !devRequest(w, r, &req) {
			return
		}
		if err := d.loadPreset(req.Name); err != nil {
			api.Error(w, http.StatusBadRequest, "%v", err)
			return
		}
		api.WriteJSON(w, http.StatusOK, map[string]string{"preset": req.Name})
	case "POST /__dev/event":
		var req struct {
			Topic string          `json:"topic"`
			Data  json.RawMessage `json:"data"`
		}
		if !devRequest(w, r, &req) {
			return
		}
		world, down := d.current()
		if down || req.Topic == "" {
			api.Error(w, http.StatusConflict, "the box is down, or no topic")
			return
		}
		if req.Data == nil {
			req.Data = json.RawMessage("{}")
		}
		world.fake.publishRaw(fakeEvent{Topic: req.Topic, Data: req.Data})
		api.OK(w)
	case "POST /__dev/down":
		var req struct {
			Seconds *float64 `json:"seconds"`
		}
		if !devRequest(w, r, &req) {
			return
		}
		if req.Seconds == nil {
			api.Error(w, http.StatusBadRequest, "seconds: > 0 restarts, 0 wakes, < 0 powers off until woken")
			return
		}
		d.setDown(*req.Seconds)
		api.OK(w)
	case "POST /__dev/latency":
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if !devRequest(w, r, &req) {
			return
		}
		d.slow.Store(req.Enabled)
		api.WriteJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
	case "POST /__dev/script":
		var req struct {
			Name string `json:"name"`
		}
		if !devRequest(w, r, &req) {
			return
		}
		s := d.fx.scripts[req.Name]
		if s == nil {
			api.Error(w, http.StatusBadRequest, "no script %q", req.Name)
			return
		}
		d.runScript(s)
		api.OK(w)
	case "GET /__dev/state":
		api.WriteJSON(w, http.StatusOK, d.state())
	default:
		api.Error(w, http.StatusNotFound, "no such dev control")
	}
}

// setDown restarts (n > 0 seconds), wakes (0) or powers off (n < 0).
func (d *devServer) setDown(seconds float64) {
	if seconds == 0 {
		d.wake()
		return
	}
	world, down := d.current()
	if down {
		return
	}
	dur := time.Duration(seconds * float64(time.Second))
	if seconds < 0 {
		dur = -1
	}
	d.goDown(dur, world.fake.bootState(nil))
}

// runScript runs a script's steps in the background.
func (d *devServer) runScript(s *fakeScript) {
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.runs = append(d.runs, cancel)
	preset := d.preset
	d.mu.Unlock()
	go func() {
		for _, st := range s.Steps {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(st.After) * time.Millisecond):
			}
			if st.Reset {
				if err := d.loadPreset(preset); err != nil {
					log.Printf("dev server: %v", err)
				}
				return
			}
			if st.Down != nil {
				d.setDown(float64(*st.Down))
			}
			world, down := d.current()
			if down {
				continue
			}
			if err := world.fake.applyStep(st); err != nil {
				log.Printf("dev server: script: %v", err)
			}
		}
	}()
}

func (d *devServer) state() map[string]any {
	world, down := d.current()
	d.mu.Lock()
	preset := d.preset
	d.mu.Unlock()
	var scripts []string
	for n := range d.fx.scripts {
		scripts = append(scripts, n)
	}
	sort.Strings(scripts)
	st := map[string]any{
		"preset": preset, "presets": d.fx.presetNames(), "scripts": scripts,
		"ui": d.set.Name, "down": down, "latency": d.slow.Load(),
	}
	if !down {
		st["installer"] = world.fake.installer
		st["docs"] = world.fake.snapshot()
	}
	return st
}

// watch rebuilds the UI whenever a file under templates/ or static/
// changes (name, size or time), and keeps the old one on an error.
func (d *devServer) watch(ctx context.Context, every time.Duration) {
	last := fingerprint(d.fsys)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		fp := fingerprint(d.fsys)
		if fp == last {
			continue
		}
		last = fp
		world, down := d.current()
		if down {
			d.built.Store(nil) // the next boot reads it again
			continue
		}
		u, err := newUI(world.srv, d.set, d.fsys)
		if err != nil {
			log.Printf("dev server: keeping the old UI: %v", err)
			continue
		}
		d.built.Store(u)
		world.ui.store(u)
		log.Printf("dev server: reloaded the UI")
	}
}

func fingerprint(fsys fs.FS) string {
	var b strings.Builder
	for _, root := range []string{"templates", "static"} {
		fs.WalkDir(fsys, root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			if info, err := e.Info(); err == nil {
				fmt.Fprintf(&b, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
			}
			return nil
		})
	}
	return b.String()
}
