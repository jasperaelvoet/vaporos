package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

// The control center has two fakes of vosd over one set of fixtures: the
// dev server's (TestDevServer: the real api core with the fake services of
// devserver_*_test.go), which the e2e harness uses, and the website demo's
// (demo/engine.js), which runs in the browser. TestFakeEnginesAgree keeps
// them one fake: it plays the vectors in fixtures/vectors against both and
// wants the same transcript.
//
// A vector file is {"description", "vectors": [vector, …]}. A vector:
//
//	name          unique across the files
//	preset        what it starts from (fixtures/presets)
//	signed_in     whether the client starts with a session (default true;
//	              never on a signed-out preset)
//	slow          the Go side waits seconds: runs only with VOS_WEB_STRICT=1
//	ignore_topics topics left out of every step's events
//	steps         in order; each does one thing:
//	  req      "METHOD /path?query" (under /api/v1), with body (JSON) or raw
//	           (text), headers, csrf (default: the session's token on
//	           writes; "" sends none), setup (X-VOS-Setup), passive
//	  stream   {passive, setup}: opens GET /events and records the replay
//	  event    [topic, data]: published as POST /__dev/event does
//	  step     a script step (fixtures/scripts): patch, events, down, reset
//	  script   a script's steps, one after the other without the waits
//	  wait     {until: "down" | "up"} or {topic, data (a partial match)},
//	           timeout ms (default 30000): time passes until it happens
//	  sleep    ms of time passing
//	  down     seconds: restart (> 0), wake (0) or power off (< 0)
//	  wake     true: the box starts again (Wake-on-LAN)
//	  preset   a preset loaded mid-way (POST /__dev/preset)
//	and may add topics (only these events), collapse (one update.progress
//	per run of a phase), ignore (paths in the record not compared, such as
//	"body.progress.percent" while a stage runs on the Go side's clock) and
//	expect ({status, body, events, replay, matched, copy}, partial matches
//	checked on both sides; copy is the words messages.js shows, JS only).
//
// Each step records its status, content type, allow and retry-after
// headers and answer (req), the replay (stream), and the events it
// published. Times compare as offsets from the preset's load within 5 s,
// as do uptime_s, idle_seconds and shutdown_in; csrf tokens, job ids and
// new client uuids compare by their form. The first pairing.state is
// published at the start instead of a second later on both sides.
//
// A request that restarts the box (reboot, power off, activate, the
// installer's reboot) must be followed by {"wait": {"until": "down"}}: the
// Go side goes down a second later, whatever vector runs by then.

const engineVectorsDir = "fixtures/vectors"

type engineVector struct {
	Name     string       `json:"name"`
	Note     string       `json:"note,omitempty"`
	Preset   string       `json:"preset"`
	SignedIn *bool        `json:"signed_in,omitempty"`
	Slow     bool         `json:"slow,omitempty"`
	Ignore   []string     `json:"ignore_topics,omitempty"`
	Steps    []vectorStep `json:"steps"`
}

type vectorStep struct {
	Req     string            `json:"req,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	Raw     *string           `json:"raw,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	CSRF    *string           `json:"csrf,omitempty"`
	Setup   string            `json:"setup,omitempty"`
	Passive bool              `json:"passive,omitempty"`

	Stream *vectorStream `json:"stream,omitempty"`
	Event  *fakeEvent    `json:"event,omitempty"`
	Step   *fakeStep     `json:"step,omitempty"`
	Script string        `json:"script,omitempty"`
	Wait   *vectorWait   `json:"wait,omitempty"`
	Sleep  *int          `json:"sleep,omitempty"`
	Down   *float64      `json:"down,omitempty"`
	Wake   bool          `json:"wake,omitempty"`
	Preset string        `json:"preset,omitempty"`

	Topics   []string      `json:"topics,omitempty"`
	Collapse bool          `json:"collapse,omitempty"`
	Ignore   []string      `json:"ignore,omitempty"`
	Expect   *vectorExpect `json:"expect,omitempty"`
}

type vectorStream struct {
	Passive bool   `json:"passive,omitempty"`
	Setup   string `json:"setup,omitempty"`
}

type vectorWait struct {
	Until   string          `json:"until,omitempty"`
	Topic   string          `json:"topic,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Timeout int             `json:"timeout,omitempty"`
}

type vectorExpect struct {
	Status  *int            `json:"status,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
	Events  json.RawMessage `json:"events,omitempty"`
	Replay  json.RawMessage `json:"replay,omitempty"`
	Matched *bool           `json:"matched,omitempty"`
	Copy    string          `json:"copy,omitempty"`
}

// actions is how many things the step does (exactly one is allowed).
func (s vectorStep) actions() int {
	n := 0
	for _, b := range []bool{s.Req != "", s.Stream != nil, s.Event != nil, s.Step != nil, s.Script != "",
		s.Wait != nil, s.Sleep != nil, s.Down != nil, s.Wake, s.Preset != ""} {
		if b {
			n++
		}
	}
	return n
}

// engineTranscript is what a vector did on one side.
type engineTranscript struct {
	Start float64          `json:"start"` // when the preset loaded, ms since 1970
	Steps []map[string]any `json:"steps"`
}

// loadEngineVectors reads fixtures/vectors/*.json strictly: the parsed
// vectors, and each one's JSON as written (for node).
func loadEngineVectors(t *testing.T) ([]engineVector, map[string]json.RawMessage) {
	t.Helper()
	fx := testFixtures(t)
	files, err := filepath.Glob(filepath.Join(engineVectorsDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors in %s: %v", engineVectorsDir, err)
	}
	var out []engineVector
	raw := map[string]json.RawMessage{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var file struct {
			Description string            `json:"description"`
			Vectors     []json.RawMessage `json:"vectors"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&file); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if file.Description == "" || len(file.Vectors) == 0 {
			t.Fatalf("%s: wants a description and vectors", f)
		}
		for _, rv := range file.Vectors {
			var v engineVector
			dec := json.NewDecoder(bytes.NewReader(rv))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&v); err != nil {
				t.Fatalf("%s: %v in %s", f, err, rv)
			}
			switch {
			case v.Name == "" || raw[v.Name] != nil:
				t.Fatalf("%s: vector name %q is empty or taken", f, v.Name)
			case fx.presets[v.Preset] == nil:
				t.Fatalf("%s: %s: no preset %q", f, v.Name, v.Preset)
			case len(v.Steps) == 0:
				t.Fatalf("%s: %s has no steps", f, v.Name)
			}
			for i, s := range v.Steps {
				if s.actions() != 1 {
					t.Fatalf("%s: %s step %d does %d things, want one", f, v.Name, i, s.actions())
				}
				if s.Script != "" && fx.scripts[s.Script] == nil {
					t.Fatalf("%s: %s step %d: no script %q", f, v.Name, i, s.Script)
				}
				if s.Preset != "" && fx.presets[s.Preset] == nil {
					t.Fatalf("%s: %s step %d: no preset %q", f, v.Name, i, s.Preset)
				}
			}
			raw[v.Name] = rv
			out = append(out, v)
		}
	}
	return out, raw
}

// TestFakeEnginesAgree plays every vector against the Go fake (over HTTP)
// and the JS engine (in node) and compares the transcripts. It needs node,
// like TestJavaScript; slow vectors run only with VOS_WEB_STRICT=1.
func TestFakeEnginesAgree(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	vs, raw := loadEngineVectors(t)
	strict := os.Getenv("VOS_WEB_STRICT") == "1"
	var run []engineVector
	var send []json.RawMessage
	for _, v := range vs {
		if v.Slow && !strict {
			continue
		}
		run = append(run, v)
		send = append(send, raw[v.Name])
	}
	in, err := json.Marshal(map[string]any{"vectors": send})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join(engineVectorsDir, "run.mjs"))
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node run.mjs: %v\n%s", err, stderr.String())
	}
	js := map[string]engineTranscript{}
	if err := json.Unmarshal(out, &js); err != nil {
		t.Fatalf("node run.mjs printed no transcripts: %v\n%s", err, out)
	}

	g := newGoEngine(t)
	for _, v := range run {
		t.Run(v.Name, func(t *testing.T) {
			gt := g.play(t, v)
			jt, ok := js[v.Name]
			if !ok {
				t.Fatalf("the JS engine played no %s", v.Name)
			}
			for _, d := range compareTranscripts(v, gt, jt) {
				t.Error(d)
			}
			for side, tr := range map[string]engineTranscript{"go": gt, "js": jt} {
				for _, d := range checkExpectations(v, tr, side) {
					t.Error(d)
				}
			}
			if t.Failed() {
				gb, _ := json.MarshalIndent(gt.Steps, "", " ")
				jb, _ := json.MarshalIndent(jt.Steps, "", " ")
				t.Logf("go transcript:\n%s\njs transcript:\n%s", gb, jb)
			}
		})
	}
}

// TestDemoEngine runs fixtures/vectors/engine.test.mjs: what only the JS
// engine has (the transport the demo installs as globalThis.vosTransport,
// and save/restore across page loads).
func TestDemoEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "--test", filepath.Join(engineVectorsDir, "engine.test.mjs")).CombinedOutput()
	if err != nil {
		t.Errorf("node --test engine.test.mjs: %v\n%s", err, out)
	}
}

// ---- the Go side

// goEngine plays vectors against a dev server: the real api core with the
// fake services, served over HTTP as TestDevServer serves it.
type goEngine struct {
	d        *devServer
	srv      *httptest.Server
	authFile []byte // auth.json with the dev password
	sessions []byte // sessions.json with the signed-in client's session
	token    string // that session's cookie
	csrf     string
	sub      *goSub
	sentinel int
}

// goSub is the harness's subscription to the running boot's event hub.
type goSub struct {
	world  *devWorld
	ch     <-chan events.Event
	cancel func()
}

func newGoEngine(t *testing.T) *goEngine {
	t.Helper()
	dir := t.TempDir()
	redirectConfig(t, dir)
	if err := os.MkdirAll(config.RunDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hub := events.Default
	t.Cleanup(func() { events.Default = hub })
	g := &goEngine{d: &devServer{fx: testFixtures(t), set: activeSet, fsys: content}}
	// One argon2 hash and one sign-in for every vector: each world starts
	// from these files instead of hashing the password again.
	if err := auth.SetAdminPassword("", devPassword); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(config.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	g.authFile = b
	g.srv = httptest.NewServer(g.d)
	t.Cleanup(func() {
		g.d.mu.Lock()
		if g.d.timer != nil {
			g.d.timer.Stop()
		}
		g.d.cancelRunsLocked()
		w := g.d.world
		g.d.mu.Unlock()
		if w != nil {
			w.stop()
		}
		if g.sub != nil {
			g.sub.cancel()
		}
		g.srv.Close()
	})
	// Sign in in a boot of its own, so no vector's boot saw the activity.
	g.load(t, defaultPreset, true)
	resp, err := http.Post(g.srv.URL+api.Prefix+"/auth/login", "application/json", strings.NewReader(`{"password":"`+devPassword+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var me struct {
		CSRF string `json:"csrf"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil || resp.StatusCode != 200 || me.CSRF == "" {
		t.Fatalf("signing in: %d %v", resp.StatusCode, err)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "vos_session" {
			g.token = c.Value
		}
	}
	g.csrf = me.CSRF
	if g.sessions, err = os.ReadFile(config.SessionsPath()); err != nil || g.token == "" {
		t.Fatalf("no session after signing in: %v", err)
	}
	return g
}

// load is loadPreset without hashing the password again: the auth files
// come from the cache (and, at a vector's start, the client's session).
func (g *goEngine) load(t *testing.T, name string, start bool) time.Time {
	t.Helper()
	d := g.d
	p := d.fx.presets[name]
	write := func(path string, b []byte) {
		if b == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			return
		}
		if err := config.WriteFileAtomic(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(config.AuthPath(), g.authFile)
	if start {
		write(config.SessionsPath(), g.sessions)
	}
	switch p.Auth {
	case "first-run":
		write(config.AuthPath(), nil)
	case "signed-out":
		write(config.SessionsPath(), nil)
	}
	now := time.Now()
	docs, err := d.fx.docs(p, now)
	if err != nil {
		t.Fatal(err)
	}
	w, err := d.newWorld(p, docs, p.installer(), true)
	if err != nil {
		t.Fatal(err)
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
	g.resub()
	g.settle()
	g.drain()
	return now
}

// resub follows the running boot's hub (a boot starts with a new one).
func (g *goEngine) resub() {
	w, down := g.d.current()
	if down || (g.sub != nil && g.sub.world == w) {
		return
	}
	if g.sub != nil {
		g.sub.cancel()
	}
	ch, cancel := w.hub.Subscribe()
	g.sub = &goSub{world: w, ch: ch, cancel: cancel}
}

// settle publishes the first pairing.state now, as the JS side does.
func (g *goEngine) settle() {
	if w, down := g.d.current(); !down && !w.fake.installer {
		w.fake.publishFirstPairingState()
	}
}

// setDown is POST /__dev/down; a box that wakes publishes its first
// pairing.state at once, as at a vector's start.
func (g *goEngine) setDown(seconds float64) {
	g.d.setDown(seconds)
	g.resub()
	if seconds == 0 {
		g.settle()
	}
}

// drain takes the events published so far, [topic, data] each.
func (g *goEngine) drain() []any {
	var out []any
	for {
		select {
		case ev := <-g.sub.ch:
			out = append(out, eventPair(ev))
		default:
			return out
		}
	}
}

func eventPair(ev events.Event) []any {
	var data any
	json.Unmarshal(ev.Data, &data)
	return []any{ev.Topic, data}
}

// restartRoutes answer first and take the box down a second later.
var restartRoutes = map[string]bool{
	"POST /system/reboot": true, "POST /system/poweroff": true, "POST /update/activate": true, "POST /install/reboot": true,
}

func (g *goEngine) play(t *testing.T, v engineVector) engineTranscript {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	// A connection of its own per request: the box drops connections while
	// it is down, and no vector reuses another's.
	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Jar: jar, Transport: tr, Timeout: 30 * time.Second}
	start := g.load(t, v.Preset, true)
	csrf := ""
	if (v.SignedIn == nil || *v.SignedIn) && g.d.fx.presets[v.Preset].Auth != "signed-out" {
		u, _ := url.Parse(g.srv.URL)
		jar.SetCookies(u, []*http.Cookie{{Name: "vos_session", Value: g.token, Path: "/"}})
		csrf = g.csrf
	}
	restarting := ""
	var steps []map[string]any
	for i, st := range v.Steps {
		began := time.Now()
		g.resub()
		rec := map[string]any{}
		var evs []any
		switch {
		case st.Req != "":
			g.request(t, client, &csrf, st, rec)
			if restartRoutes[st.Req] && rec["status"] == 200 {
				restarting = st.Req
			}
		case st.Stream != nil:
			g.stream(t, client, st, rec)
		case st.Event != nil:
			rec["kind"] = "event"
			w, down := g.d.current()
			rec["ok"] = !down && st.Event.Topic != ""
			if rec["ok"] == true {
				w.fake.publishRaw(*st.Event)
			}
		case st.Step != nil || st.Script != "":
			rec["kind"] = "step"
			list := []fakeStep{}
			if st.Step != nil {
				list = append(list, *st.Step)
			} else {
				rec["kind"] = "script"
				list = g.d.fx.scripts[st.Script].Steps
			}
			g.d.mu.Lock()
			preset := g.d.preset
			g.d.mu.Unlock()
			for _, s := range list {
				if s.Reset {
					g.load(t, preset, false)
					break
				}
				if s.Down != nil {
					g.setDown(float64(*s.Down))
				}
				if w, down := g.d.current(); !down {
					if err := w.fake.applyStep(s); err != nil {
						t.Fatalf("step %d: %v", i, err)
					}
				}
			}
		case st.Wait != nil:
			rec["kind"] = "wait"
			var matched bool
			evs, matched = g.wait(t, *st.Wait)
			rec["matched"] = matched
			if matched && st.Wait.Until == "down" {
				restarting = ""
			}
		case st.Sleep != nil:
			rec["kind"] = "sleep"
			time.Sleep(time.Duration(*st.Sleep) * time.Millisecond)
		case st.Down != nil:
			rec["kind"] = "down"
			g.setDown(*st.Down)
		case st.Wake:
			rec["kind"] = "wake"
			g.setDown(0)
		case st.Preset != "":
			rec["kind"] = "preset"
			g.load(t, st.Preset, false)
		}
		if st.Preset != "" || (st.Script != "" || st.Step != nil) && hasReset(st, g.d.fx) {
			evs = nil
		} else {
			evs = append(evs, g.drain()...)
		}
		rec["events"] = filterEvents(evs, st, v)
		steps = append(steps, rec)
		if took := time.Since(began); took > 3*time.Second {
			t.Logf("step %d (%s) took %v on the Go side", i, stepLabel(rec), took.Round(time.Millisecond))
		}
	}
	if restarting != "" {
		t.Fatalf("%s restarts the box a second later: add {\"wait\": {\"until\": \"down\"}} after it", restarting)
	}
	// Numbers as the JS side has them (float64), for comparing.
	b, _ := json.Marshal(steps)
	var norm []map[string]any
	json.Unmarshal(b, &norm)
	return engineTranscript{Start: float64(start.UnixMilli()), Steps: norm}
}

func hasReset(st vectorStep, fx *fixtureSet) bool {
	if st.Step != nil {
		return st.Step.Reset
	}
	for _, s := range fx.scripts[st.Script].Steps {
		if s.Reset {
			return true
		}
	}
	return false
}

func (g *goEngine) request(t *testing.T, client *http.Client, csrf *string, st vectorStep, rec map[string]any) {
	t.Helper()
	method, path, _ := strings.Cut(st.Req, " ")
	var body io.Reader
	switch {
	case st.Raw != nil:
		body = strings.NewReader(*st.Raw)
	case len(st.Body) > 0:
		body = bytes.NewReader(st.Body)
	}
	req, err := http.NewRequest(method, g.srv.URL+api.Prefix+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range st.Headers {
		req.Header.Set(k, v)
	}
	if method != "GET" {
		tok := *csrf
		if st.CSRF != nil {
			tok = *st.CSRF
		}
		if tok != "" {
			req.Header.Set("X-VOS-CSRF", tok)
		}
	}
	if st.Setup != "" {
		req.Header.Set("X-VOS-Setup", st.Setup)
	}
	if st.Passive {
		req.Header.Set("X-VOS-Passive", "1")
	}
	rec["kind"], rec["req"] = "req", st.Req
	resp, err := client.Do(req)
	if err != nil {
		rec["status"] = 0 // the box is down: the connection dropped
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	ct := resp.Header.Get("Content-Type")
	mt, _, _ := strings.Cut(ct, ";")
	h := map[string]any{}
	for _, k := range []string{"Allow", "Retry-After"} {
		if v := resp.Header.Get(k); v != "" {
			h[strings.ToLower(k)] = v
		}
	}
	rec["status"], rec["type"], rec["headers"], rec["body"] = resp.StatusCode, mt, h, decodeAnswer(b, ct)
	if m, ok := rec["body"].(map[string]any); ok {
		if c, ok := m["csrf"].(string); ok && c != "" {
			*csrf = c
		}
	}
}

func decodeAnswer(b []byte, contentType string) any {
	if len(b) == 0 {
		return nil
	}
	if strings.Contains(contentType, "json") {
		var v any
		if err := json.Unmarshal(b, &v); err == nil {
			return v
		}
	}
	return string(b)
}

// stream opens GET /events and records its replay: everything the stream
// sends before a sentinel the harness publishes once it is open.
func (g *goEngine) stream(t *testing.T, client *http.Client, st vectorStep, rec map[string]any) {
	t.Helper()
	rec["kind"] = "stream"
	q := ""
	if st.Stream.Passive {
		q = "?passive=1"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", g.srv.URL+api.Prefix+"/events"+q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Stream.Setup != "" {
		req.Header.Set("X-VOS-Setup", st.Stream.Setup)
	}
	resp, err := client.Do(req)
	if err != nil {
		rec["status"] = 0
		return
	}
	defer resp.Body.Close()
	rec["status"] = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		rec["body"] = decodeAnswer(b, resp.Header.Get("Content-Type"))
		return
	}
	g.sentinel++
	n := g.sentinel
	type sse struct {
		topic, data string
	}
	got := make(chan sse, 64)
	go func() {
		defer close(got)
		sc := bufio.NewScanner(resp.Body)
		var ev sse
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.topic != "" {
					got <- ev
				}
				ev = sse{}
			case strings.HasPrefix(line, "event: "):
				ev.topic = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data += strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	w, _ := g.d.current()
	w.hub.Publish("system.message", map[string]any{"sentinel": n})
	replay := []any{}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-got:
			if !ok {
				t.Fatalf("the event stream ended before the sentinel")
			}
			var data any
			json.Unmarshal([]byte(ev.data), &data)
			if isSentinel(ev.topic, data) {
				rec["replay"] = replay
				return
			}
			replay = append(replay, []any{ev.topic, data})
		case <-deadline:
			t.Fatalf("no sentinel on the event stream within 5 s")
		}
	}
}

func isSentinel(topic string, data any) bool {
	m, ok := data.(map[string]any)
	return ok && topic == "system.message" && m["sentinel"] != nil
}

// wait lets time pass until the box is down or up, or a matching event is
// published; then 50 ms more for what the same moment publishes.
func (g *goEngine) wait(t *testing.T, w vectorWait) ([]any, bool) {
	t.Helper()
	timeout := 30 * time.Second
	if w.Timeout > 0 {
		timeout = time.Duration(w.Timeout) * time.Millisecond
	}
	var want any
	if len(w.Data) > 0 {
		json.Unmarshal(w.Data, &want)
	}
	var evs []any
	done := func() bool {
		_, down := g.d.current()
		switch w.Until {
		case "down":
			return down
		case "up":
			return !down
		}
		for _, e := range evs {
			p := e.([]any)
			if p[0] == w.Topic && (want == nil || partialMatch(want, p[1])) {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(timeout)
	for !done() && time.Now().Before(deadline) {
		g.resub()
		select {
		case ev := <-g.sub.ch:
			evs = append(evs, eventPair(ev))
		case <-time.After(5 * time.Millisecond):
		}
	}
	ok := done()
	if ok {
		time.Sleep(50 * time.Millisecond)
	}
	return append(evs, g.drain()...), ok
}

// filterEvents drops the harness's sentinels, keeps a step's topics, drops
// the vector's ignored topics and collapses update.progress when asked.
func filterEvents(evs []any, st vectorStep, v engineVector) []any {
	out := []any{}
	for _, e := range evs {
		p := e.([]any)
		topic := p[0].(string)
		switch {
		case isSentinel(topic, p[1]):
		case len(st.Topics) > 0 && !containsStr(st.Topics, topic):
		case containsStr(v.Ignore, topic):
		default:
			if st.Collapse && topic == "update.progress" && len(out) > 0 {
				prev := out[len(out)-1].([]any)
				if prev[0] == topic && asObj(prev[1])["phase"] == asObj(p[1])["phase"] {
					out[len(out)-1] = p
					continue
				}
			}
			out = append(out, p)
		}
	}
	return out
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---- comparing

// engineTolerance: clock-derived numbers that may differ by a few seconds
// (the Go side runs on the wall clock, the JS side on a virtual one).
var engineTolerance = map[string]bool{"uptime_s": true, "idle_seconds": true, "shutdown_in": true}

// engineOpaque: random values, compared by their form only.
var engineOpaque = map[string]*regexp.Regexp{
	"csrf": regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`),
	"job":  regexp.MustCompile(`^[0-9a-f]{16}$`),
	"uuid": regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`),
}

var rfc3339Re = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$`)

// decodePrefix starts api.ReadJSON's errors.
const decodePrefix = "bad request body: "

// engineSlack is how far times and clock-derived counters may differ.
const engineSlack = 5.0 // seconds

func compareTranscripts(v engineVector, g, j engineTranscript) []string {
	c := &comparer{gStart: g.Start / 1000, jStart: j.Start / 1000}
	if len(g.Steps) != len(j.Steps) {
		return []string{fmt.Sprintf("go played %d steps, js %d", len(g.Steps), len(j.Steps))}
	}
	for i := range g.Steps {
		gs, js := g.Steps[i], map[string]any{}
		for k, v := range j.Steps[i] {
			if k != "copy" { // the JS side's words for an error (T5)
				js[k] = v
			}
		}
		gs = asObj(deepCopyJSON(gs))
		for _, p := range v.Steps[i].Ignore {
			dropPath(gs, p)
			dropPath(js, p)
		}
		c.same(fmt.Sprintf("step %d (%v)", i, stepLabel(gs)), "", gs, js)
	}
	return c.diffs
}

// dropPath deletes a dotted path ("body.progress.percent") from m.
func dropPath(m map[string]any, path string) {
	head, rest, more := strings.Cut(path, ".")
	if !more {
		delete(m, head)
		return
	}
	if sub, ok := m[head].(map[string]any); ok {
		dropPath(sub, rest)
	}
}

func stepLabel(s map[string]any) string {
	if r, ok := s["req"].(string); ok {
		return r
	}
	return fmt.Sprint(s["kind"])
}

type comparer struct {
	gStart, jStart float64
	diffs          []string
}

func (c *comparer) fail(path string, g, j any) {
	gb, _ := json.Marshal(g)
	jb, _ := json.Marshal(j)
	c.diffs = append(c.diffs, fmt.Sprintf("%s: go %s, js %s", path, gb, jb))
}

func (c *comparer) same(path, key string, g, j any) {
	switch gv := g.(type) {
	case map[string]any:
		jv, ok := j.(map[string]any)
		if !ok {
			c.fail(path, g, j)
			return
		}
		keys := map[string]bool{}
		for k := range gv {
			keys[k] = true
		}
		for k := range jv {
			keys[k] = true
		}
		var sorted []string
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			gx, gok := gv[k]
			jx, jok := jv[k]
			if gok != jok {
				c.fail(path+"."+k, gx, jx)
				continue
			}
			c.same(path+"."+k, k, gx, jx)
		}
	case []any:
		jv, ok := j.([]any)
		if !ok || len(gv) != len(jv) {
			c.fail(path, g, j)
			return
		}
		for i := range gv {
			c.same(fmt.Sprintf("%s[%d]", path, i), key, gv[i], jv[i])
		}
	case string:
		jv, ok := j.(string)
		if !ok {
			c.fail(path, g, j)
			return
		}
		if re := engineOpaque[key]; re != nil && re.MatchString(gv) && re.MatchString(jv) {
			return
		}
		// Go's JSON decoder's words after this prefix are not ported.
		if key == "error" && strings.HasPrefix(gv, decodePrefix) && strings.HasPrefix(jv, decodePrefix) {
			return
		}
		if rfc3339Re.MatchString(gv) && rfc3339Re.MatchString(jv) {
			gt, err1 := time.Parse(time.RFC3339, gv)
			jt, err2 := time.Parse(time.RFC3339, jv)
			if err1 == nil && err2 == nil {
				dg := float64(gt.UnixMilli())/1000 - c.gStart
				dj := float64(jt.UnixMilli())/1000 - c.jStart
				if math.Abs(dg-dj) > engineSlack {
					c.diffs = append(c.diffs, fmt.Sprintf("%s: go %s (%+.0f s from the start), js %s (%+.0f s)", path, gv, dg, jv, dj))
				}
				return
			}
		}
		if gv != jv {
			c.fail(path, g, j)
		}
	case float64:
		jv, ok := j.(float64)
		if !ok || (engineTolerance[key] && math.Abs(gv-jv) > engineSlack) || (!engineTolerance[key] && gv != jv) {
			c.fail(path, g, j)
		}
	default:
		if g != j {
			c.fail(path, g, j)
		}
	}
}

// partialMatch reports whether got has everything want has: objects key by
// key, arrays element by element, anything else equal (clock-derived
// counters within engineSlack).
func partialMatch(want, got any) bool { return partialMatchKey("", want, got) }

func partialMatchKey(key string, want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			if !partialMatchKey(k, v, g[k]) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !partialMatchKey(key, w[i], g[i]) {
				return false
			}
		}
		return true
	case float64:
		g, ok := got.(float64)
		return ok && (g == w || engineTolerance[key] && math.Abs(g-w) <= engineSlack)
	}
	return want == got
}

// checkExpectations checks a vector's expect entries on one side.
func checkExpectations(v engineVector, tr engineTranscript, side string) []string {
	var out []string
	for i, st := range v.Steps {
		e := st.Expect
		if e == nil || i >= len(tr.Steps) {
			continue
		}
		rec := tr.Steps[i]
		where := fmt.Sprintf("%s step %d (%s)", side, i, stepLabel(rec))
		check := func(what string, raw json.RawMessage, got any) {
			if len(raw) == 0 {
				return
			}
			var want any
			if err := json.Unmarshal(raw, &want); err != nil {
				out = append(out, fmt.Sprintf("%s: expect.%s: %v", where, what, err))
				return
			}
			if !partialMatch(want, got) {
				gb, _ := json.Marshal(got)
				out = append(out, fmt.Sprintf("%s: %s %s, want %s", where, what, gb, raw))
			}
		}
		if e.Status != nil && rec["status"] != float64(*e.Status) {
			out = append(out, fmt.Sprintf("%s: status %v, want %d", where, rec["status"], *e.Status))
		}
		check("body", e.Body, rec["body"])
		check("events", e.Events, rec["events"])
		check("replay", e.Replay, rec["replay"])
		if e.Matched != nil && rec["matched"] != *e.Matched {
			out = append(out, fmt.Sprintf("%s: matched %v, want %v", where, rec["matched"], *e.Matched))
		}
		if e.Copy != "" && side == "js" && rec["copy"] != e.Copy {
			out = append(out, fmt.Sprintf("%s: messages.js shows %q, want %q", where, rec["copy"], e.Copy))
		}
	}
	return out
}
