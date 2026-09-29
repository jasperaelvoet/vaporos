package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// The dev server's data lives in internal/web/fixtures as JSON. It is read
// from disk by the Go fake (TestDevServer), the e2e harness, the unit tests
// and the website demo's JS engine; nothing embeds it, so it never reaches
// the binary.
//
//	base/<resource>.json    one document per GET resource, as the real API
//	                        answers it (fakeResources lists them)
//	presets/<name>.json     a starting state: base plus a JSON merge patch
//	scripts/<name>.json     timed steps that move a running server along
//
// A preset file holds:
//
//	description  what it shows
//	mode         "os" (default) or "installer"
//	auth         "signed-in" (default: sessions kept), "signed-out" (every
//	             session dropped) or "first-run" (no admin password; the
//	             setup code is ABCD-EFGH)
//	headless     installer only: no monitor, so the setup code is waived
//	patch        {resource: RFC 7386 merge patch}
//	errors       {"METHOD /path": [status, "message"]}: the route answers
//	             this error (the path as the route is registered)
//	latency      {"METHOD /path": ms}: the route always answers this late
//	events       [[topic, data], …] published once the server is up, so
//	             state topics are replayed to every new event stream
//	sim          knobs of the simulated services: web_activity (default
//	             true: the viewer's own requests keep the box busy, as on
//	             the real box), idle_seconds (idle time at start), stage
//	             ({phase, percent}: a download in progress), trial (a
//	             download stops with the on-trial refusal), install_fail
//	             (the install stops at the write step with this error)
//	expect       what the pages should derive: hero (SCN §3.2 state: ready,
//	             streaming, updating, fault-no-gpu, starting, fault-stopped,
//	             fault-not-answering, fault-unknown, restart-needed; null
//	             when the page goes elsewhere), redirect, attention ("pair"),
//	             restart (update, rollback, display) and cards (pair,
//	             update-progress, update-ready, update-available,
//	             update-failed, update-stopped, cant-check, low-space,
//	             cant-wake), in priority order
//
// A script file holds a description and steps; each step waits "after" ms,
// then applies "patch" (as above), publishes "events", and may go "down"
// (seconds; 0 wakes the box, a negative number keeps it off until woken)
// or "reset" to the running preset. A patch of "update" also publishes
// update.state, as every change of the update state does on the box.
//
// Strings "@now", "@-40m", "@+2h" and "@-3d" in a document are times
// relative to when the server loads the preset, written as RFC 3339.

const fixturesDir = "fixtures"

// fakeResources maps each document to the GET route that answers it.
var fakeResources = map[string]string{
	"system":            "/system",
	"ssh":               "/ssh",
	"update":            "/update",
	"sunshine":          "/sunshine",
	"sunshine-clients":  "/sunshine/clients",
	"sunshine-settings": "/sunshine/settings",
	"sunshine-logs":     "/sunshine/logs",
	"display":           "/display",
	"storage":           "/storage",
	"power":             "/power",
	"install-probe":     "/install/probe",
	"install-status":    "/install/status",
}

// defaultPreset is what TestDevServer serves without VOS_WEB_PRESET.
const defaultPreset = "idle"

// presetAliases are the dev server's older flags: each is the preset its
// variables (all set to 1) select. The first match wins.
var presetAliases = []struct {
	env    []string
	preset string
}{
	{[]string{"VOS_WEB_INSTALLER", "VOS_WEB_HEADLESS"}, "installer-waived"},
	{[]string{"VOS_WEB_INSTALLER"}, "installer-code"},
	{[]string{"VOS_WEB_SETUP"}, "first-run"},
	{[]string{"VOS_WEB_SIGNED_OUT"}, "signed-out"},
	{[]string{"VOS_WEB_STREAMING"}, "streaming"},
	{[]string{"VOS_WEB_PAIRING"}, "pairing-2"},
	{[]string{"VOS_WEB_HELD"}, "update-held"},
	{[]string{"VOS_WEB_TRIAL"}, "update-trial"},
	{[]string{"VOS_WEB_HEADLESS"}, "headless"},
}

// fakeError is an injected error, written as [status, "message"].
type fakeError struct {
	Status  int
	Message string
}

func (e *fakeError) UnmarshalJSON(b []byte) error {
	var pair []any
	if err := json.Unmarshal(b, &pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return fmt.Errorf("an error is [status, message], not %s", b)
	}
	st, ok1 := pair[0].(float64)
	msg, ok2 := pair[1].(string)
	if !ok1 || !ok2 || st < 400 || st > 599 || st != float64(int(st)) || msg == "" {
		return fmt.Errorf("an error is [status 400-599, message], not %s", b)
	}
	e.Status, e.Message = int(st), msg
	return nil
}

// fakeEvent is one event, written as [topic, data].
type fakeEvent struct {
	Topic string
	Data  json.RawMessage
}

func (e *fakeEvent) UnmarshalJSON(b []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(b, &pair); err != nil {
		return err
	}
	if len(pair) != 2 || json.Unmarshal(pair[0], &e.Topic) != nil || e.Topic == "" {
		return fmt.Errorf("an event is [topic, data], not %s", b)
	}
	e.Data = pair[1]
	return nil
}

type fakeSim struct {
	WebActivity *bool           `json:"web_activity"`
	IdleSeconds int             `json:"idle_seconds"`
	Stage       *fakeStagePoint `json:"stage"`
	Trial       bool            `json:"trial"`
	InstallFail string          `json:"install_fail"`
}

type fakeStagePoint struct {
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
}

type fakeExpect struct {
	Hero      *string  `json:"hero"`
	Redirect  string   `json:"redirect,omitempty"`
	Attention string   `json:"attention,omitempty"`
	Restart   []string `json:"restart"`
	Cards     []string `json:"cards"`
}

type fakePreset struct {
	Description string                     `json:"description"`
	Mode        string                     `json:"mode"`
	Auth        string                     `json:"auth"`
	Headless    bool                       `json:"headless"`
	Patch       map[string]json.RawMessage `json:"patch"`
	Errors      map[string]fakeError       `json:"errors"`
	Latency     map[string]int             `json:"latency"`
	Events      []fakeEvent                `json:"events"`
	Sim         fakeSim                    `json:"sim"`
	Expect      fakeExpect                 `json:"expect"`
}

func (p *fakePreset) installer() bool { return p.Mode == "installer" }

type fakeStep struct {
	After  int                        `json:"after"`
	Patch  map[string]json.RawMessage `json:"patch"`
	Events []fakeEvent                `json:"events"`
	Down   *int                       `json:"down"`
	Reset  bool                       `json:"reset"`
}

type fakeScript struct {
	Description string     `json:"description"`
	Steps       []fakeStep `json:"steps"`
}

// fixtureSet is everything under fixtures/, parsed.
type fixtureSet struct {
	base    map[string]any
	presets map[string]*fakePreset
	scripts map[string]*fakeScript
}

// loadFixtures reads dir strictly: unknown keys in a preset or script, an
// unknown resource or a malformed document is an error.
func loadFixtures(dir string) (*fixtureSet, error) {
	fx := &fixtureSet{base: map[string]any{}, presets: map[string]*fakePreset{}, scripts: map[string]*fakeScript{}}
	for name := range fakeResources {
		b, err := os.ReadFile(filepath.Join(dir, "base", name+".json"))
		if err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("base/%s.json: %w", name, err)
		}
		fx.base[name] = v
	}
	if err := readStrict(filepath.Join(dir, "presets"), func(name string, dec *json.Decoder) error {
		p := &fakePreset{}
		fx.presets[name] = p
		return dec.Decode(p)
	}); err != nil {
		return nil, err
	}
	if err := readStrict(filepath.Join(dir, "scripts"), func(name string, dec *json.Decoder) error {
		s := &fakeScript{}
		fx.scripts[name] = s
		return dec.Decode(s)
	}); err != nil {
		return nil, err
	}
	for name, p := range fx.presets {
		if err := p.check(); err != nil {
			return nil, fmt.Errorf("presets/%s.json: %w", name, err)
		}
	}
	for name, s := range fx.scripts {
		for _, st := range s.Steps {
			if err := checkPatch(st.Patch); err != nil {
				return nil, fmt.Errorf("scripts/%s.json: %w", name, err)
			}
		}
	}
	if fx.presets[defaultPreset] == nil {
		return nil, fmt.Errorf("presets/%s.json is missing", defaultPreset)
	}
	return fx, nil
}

func readStrict(dir string, decode func(name string, dec *json.Decoder) error) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := decode(strings.TrimSuffix(filepath.Base(f), ".json"), dec); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

var routeKeyRe = regexp.MustCompile(`^(GET|PUT|POST|DELETE) /[a-z/{}-]+$`)

func (p *fakePreset) check() error {
	switch p.Mode {
	case "", "os", "installer":
	default:
		return fmt.Errorf("mode %q", p.Mode)
	}
	switch p.Auth {
	case "", "signed-in", "signed-out", "first-run":
	default:
		return fmt.Errorf("auth %q", p.Auth)
	}
	if p.Headless && !p.installer() {
		return fmt.Errorf("headless is for installer presets (the waiver applies only there)")
	}
	for k := range p.Errors {
		if !routeKeyRe.MatchString(k) {
			return fmt.Errorf("error key %q is not \"METHOD /path\"", k)
		}
	}
	for k, ms := range p.Latency {
		if !routeKeyRe.MatchString(k) || ms < 0 {
			return fmt.Errorf("latency %q: %d", k, ms)
		}
	}
	if p.Sim.Stage != nil && !slices.Contains([]string{"download", "write", "verify", "install"}, p.Sim.Stage.Phase) {
		return fmt.Errorf("sim.stage.phase %q", p.Sim.Stage.Phase)
	}
	return checkPatch(p.Patch)
}

func checkPatch(patch map[string]json.RawMessage) error {
	for name := range patch {
		if _, ok := fakeResources[name]; !ok {
			return fmt.Errorf("patch of unknown resource %q", name)
		}
	}
	return nil
}

// presetNames is every preset, sorted.
func (fx *fixtureSet) presetNames() []string {
	var out []string
	for n := range fx.presets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// docs is base with the preset's patch applied, times resolved against now.
func (fx *fixtureSet) docs(p *fakePreset, now time.Time) (map[string]any, error) {
	docs := map[string]any{}
	for name, v := range fx.base {
		docs[name] = deepCopyJSON(v)
	}
	if err := applyPatch(docs, p.Patch, now); err != nil {
		return nil, err
	}
	for name, v := range docs {
		docs[name] = resolveTimes(v, now)
	}
	return docs, nil
}

// applyPatch merges each resource's patch into docs (RFC 7386).
func applyPatch(docs map[string]any, patch map[string]json.RawMessage, now time.Time) error {
	for name, raw := range patch {
		var p any
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("patch of %s: %w", name, err)
		}
		docs[name] = mergePatch(docs[name], resolveTimes(p, now))
	}
	return nil
}

// mergePatch is RFC 7386 JSON Merge Patch: objects merge key by key, null
// deletes a key, anything else (arrays included) replaces the target.
func mergePatch(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return deepCopyJSON(patch)
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = mergePatch(tm[k], v)
	}
	return tm
}

func deepCopyJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = deepCopyJSON(e)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = deepCopyJSON(e)
		}
		return s
	}
	return v
}

var relTimeRe = regexp.MustCompile(`^@(now|([+-])(\d+)([smhd]))$`)

// resolveTimes replaces "@now" and "@±<n><s|m|h|d>" strings with RFC 3339
// times relative to now.
func resolveTimes(v any, now time.Time) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = resolveTimes(e, now)
		}
	case []any:
		for i, e := range x {
			x[i] = resolveTimes(e, now)
		}
	case string:
		if t, ok := relTime(x, now); ok {
			return t.UTC().Truncate(time.Second).Format(time.RFC3339)
		}
	}
	return v
}

func relTime(s string, now time.Time) (time.Time, bool) {
	m := relTimeRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	if m[1] == "now" {
		return now, true
	}
	var n int
	fmt.Sscan(m[3], &n)
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[4]]
	d := time.Duration(n) * unit
	if m[2] == "-" {
		d = -d
	}
	return now.Add(d), true
}

// presetFromEnv picks the preset: VOS_WEB_PRESET, else an alias, else the
// default.
func presetFromEnv(fx *fixtureSet, getenv func(string) string) (string, error) {
	name := getenv("VOS_WEB_PRESET")
	if name == "" {
		name = defaultPreset
	alias:
		for _, a := range presetAliases {
			for _, v := range a.env {
				if getenv(v) != "1" {
					continue alias
				}
			}
			name = a.preset
			break
		}
	}
	if fx.presets[name] == nil {
		return "", fmt.Errorf("no preset %q (have %s)", name, strings.Join(fx.presetNames(), ", "))
	}
	return name, nil
}
