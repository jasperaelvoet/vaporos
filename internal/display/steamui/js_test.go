package steamui

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// jsEnv is the window an expression runs in: Steam's settings (nil: no
// settingsStore), SteamClient ("api", "bare" without the setters, or
// "none"), VaporOS's marker, a view's size, and Steam's sleep as fakeUI
// has it.
type jsEnv struct {
	Settings       map[string]any `json:"settings"`
	Client         string         `json:"client"`
	Marker         any            `json:"marker,omitempty"`
	DPR            any            `json:"dpr,omitempty"`
	H              any            `json:"h,omitempty"`
	Store          *fakeStore     `json:"store,omitempty"`
	ClientSettings map[string]any `json:"clientSettings,omitempty"`
	SetSetting     string         `json:"setSetting,omitempty"`
}

func steamEnv(edit func(map[string]any)) jsEnv {
	s := readyUI().settings
	if edit != nil {
		edit(s)
	}
	return jsEnv{Settings: s, Client: "api"}
}

func (e jsEnv) with(edit func(*jsEnv)) jsEnv { edit(&e); return e }

func marker(gen, value any) map[string]any { return map[string]any{"gen": gen, "value": value} }

// jsCase is an expression in a window and what Go makes of its answer.
type jsCase struct {
	name    string
	env     jsEnv
	expr    string
	times   int     // evaluated this many times in the one window (1 when 0), answering a list when more
	kind    string  // read, apply, view, sleep, nosleep or resume
	state   State   // read's
	value   float64 // apply's
	view    View
	sleep   SleepState
	resumes []ResumeResult // one per evaluation
	err     error
	throws  bool     // the expression throws in Steam: no answer to read
	calls   []string // the setters called
	marker  any      // window.__vosScale afterwards
}

var readyEnv = steamEnv(nil)

const vaporOS = `External: VaporOS 27"|||Windowed`

var readyState = State{Name: vaporOS, External: true, Auto: true, Current: 1.71, AutoValue: 1.71, Min: 0.71, Max: 3.41}

func jsCases() []jsCase {
	notReady := State{Min: 0.5, Max: 2.5}
	withState := func(edit func(*State)) State { s := readyState; edit(&s); return s }
	return []jsCase{
		{name: "read", env: readyEnv, expr: readJS, kind: "read", state: readyState},
		{name: "read the marker", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(7, 1.25) }), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Gen, s.Value = 7, 1.25 }), marker: marker(7, 1.25)},
		{name: "read the automatic marker", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(8, 0) }), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Gen = 8 }), marker: marker(8, 0)},
		{name: "a fractional generation", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(1.5, 2) }), expr: readJS, kind: "read",
			state: readyState, marker: marker(1.5, 2)},
		{name: "a generation as text", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker("7", 2) }), expr: readJS, kind: "read",
			state: readyState, marker: marker("7", 2)},
		{name: "a negative generation", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(-3, 2) }), expr: readJS, kind: "read",
			state: readyState, marker: marker(-3, 2)},
		{name: "a marker of another kind", env: readyEnv.with(func(e *jsEnv) { e.Marker = "x" }), expr: readJS, kind: "read",
			state: readyState, marker: "x"},
		{name: "starting", env: readyEnv.with(func(e *jsEnv) { e.Settings = nil }), expr: readJS, kind: "read",
			state: notReady, err: ErrNotReady},
		{name: "settings not loaded", env: steamEnv(func(s map[string]any) { delete(s, "bDisplayIsUsingAutoScale") }), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Auto = false }), err: ErrNotReady},
		{name: "no display name", env: steamEnv(func(s map[string]any) { s["strDisplayName"] = "" }), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Name = "" }), err: ErrNotReady},
		{name: "only a prefix", env: steamEnv(func(s map[string]any) { s["strDisplayName"] = "External: " }), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Name = "External: " }), err: ErrNotReady},
		{name: "xwayland", env: steamEnv(func(s map[string]any) { s["strDisplayName"] = `External: xwayland-0 27"|||Windowed` }), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Name = `External: xwayland-0 27"|||Windowed` }), err: ErrNotReady},
		{name: "Xwayland", env: steamEnv(func(s map[string]any) { s["strDisplayName"] = "Xwayland" }), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Name = "Xwayland" }), err: ErrNotReady},
		{name: "an internal display", env: steamEnv(func(s map[string]any) {
			s["strDisplayName"] = "Internal: eDP-1|||Windowed"
			s["bDisplayIsExternal"] = false
		}), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Name, s.External = "Internal: eDP-1|||Windowed", false })},
		{name: "a long name", env: steamEnv(func(s map[string]any) { s["strDisplayName"] = "External: " + strings.Repeat("a", 300) }), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Name = ("External: " + strings.Repeat("a", 300))[:256] })},
		{name: "odd types", env: steamEnv(func(s map[string]any) {
			s["bDisplayIsUsingAutoScale"] = nil // defined: ready, but not on
			s["flCurrentDisplayScaleFactor"] = "2"
			s["flMinDisplayScaleFactor"] = nil
			s["flMaxDisplayScaleFactor"] = "x"
			s["bDisplayIsExternal"] = 1
		}), expr: readJS, kind: "read",
			state: State{Name: vaporOS, AutoValue: 1.71, Min: 0.5, Max: 2.5}},
		{name: "huge values", env: steamEnv(func(s map[string]any) {
			s["flCurrentDisplayScaleFactor"] = 1e300
			s["flAutoDisplayScaleFactor"] = -2
		}), expr: readJS, kind: "read",
			state: withState(func(s *State) { s.Current, s.AutoValue = 8, 0 })},
		{name: "no setters", env: readyEnv.with(func(e *jsEnv) { e.Client = "bare" }), expr: readJS, kind: "read",
			state: readyState, err: ErrUnsupported},
		{name: "no setters while starting", env: readyEnv.with(func(e *jsEnv) { e.Client, e.Settings = "bare", nil }), expr: readJS, kind: "read",
			state: notReady, err: ErrUnsupported},
		{name: "no SteamClient", env: readyEnv.with(func(e *jsEnv) { e.Client = "none" }), expr: readJS, kind: "read",
			state: readyState, err: ErrNotReady},

		{name: "set", env: readyEnv, expr: call(setJS, 3, 1.4), kind: "apply", value: 1.4,
			calls: []string{"auto(false)", "manual(1.4)"}, marker: marker(3, 1.4)},
		{name: "set above Steam's max", env: readyEnv, expr: call(setJS, 3, 5.0), kind: "apply", value: 3.41,
			calls: []string{"auto(false)", "manual(3.41)"}, marker: marker(3, 3.41)},
		{name: "set below Steam's min", env: readyEnv, expr: call(setJS, 3, 0.2), kind: "apply", value: 0.71,
			calls: []string{"auto(false)", "manual(0.71)"}, marker: marker(3, 0.71)},
		{name: "set without bounds", env: steamEnv(func(s map[string]any) {
			delete(s, "flMinDisplayScaleFactor")
			s["flMaxDisplayScaleFactor"] = "3"
		}), expr: call(setJS, 1, 3.0), kind: "apply", value: 2.5,
			calls: []string{"auto(false)", "manual(2.5)"}, marker: marker(1, 2.5)},
		{name: "set stale", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(5, 1.2) }), expr: call(setJS, 4, 1.4), kind: "apply",
			err: ErrStale, marker: marker(5, 1.2)},
		{name: "set the same generation again", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(5, 1.2) }), expr: call(setJS, 5, 1.4), kind: "apply",
			value: 1.4, calls: []string{"auto(false)", "manual(1.4)"}, marker: marker(5, 1.4)},
		{name: "set over an automatic marker", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(5, 0) }), expr: call(setJS, 6, 1.4), kind: "apply",
			value: 1.4, calls: []string{"auto(false)", "manual(1.4)"}, marker: marker(6, 1.4)},
		{name: "set over a bogus marker", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker("99", 2) }), expr: call(setJS, 1, 1.4), kind: "apply",
			value: 1.4, calls: []string{"auto(false)", "manual(1.4)"}, marker: marker(1, 1.4)},
		{name: "set while starting", env: readyEnv.with(func(e *jsEnv) { e.Settings = nil }), expr: call(setJS, 1, 1.4), kind: "apply",
			err: ErrNotReady},
		{name: "set on xwayland", env: steamEnv(func(s map[string]any) { s["strDisplayName"] = "External: xwayland-1" }), expr: call(setJS, 1, 1.4), kind: "apply",
			err: ErrNotReady},
		{name: "set without setters", env: readyEnv.with(func(e *jsEnv) { e.Client = "bare" }), expr: call(setJS, 1, 1.4), kind: "apply",
			err: ErrUnsupported},
		{name: "stale before not ready", env: readyEnv.with(func(e *jsEnv) { e.Settings, e.Marker = nil, marker(9, 1) }), expr: call(setJS, 2, 1.4), kind: "apply",
			err: ErrStale, marker: marker(9, 1)},

		{name: "auto", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(5, 1.2) }), expr: call(autoJS, 6), kind: "apply",
			calls: []string{"auto(true)"}, marker: marker(6, 0)},
		{name: "auto again", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(6, 0) }), expr: call(autoJS, 6), kind: "apply",
			calls: []string{"auto(true)"}, marker: marker(6, 0)},
		{name: "auto stale", env: readyEnv.with(func(e *jsEnv) { e.Marker = marker(7, 1.3) }), expr: call(autoJS, 6), kind: "apply",
			err: ErrStale, marker: marker(7, 1.3)},
		{name: "auto while starting", env: readyEnv.with(func(e *jsEnv) { e.Settings = nil }), expr: call(autoJS, 1), kind: "apply",
			err: ErrNotReady},
		{name: "auto without setters", env: readyEnv.with(func(e *jsEnv) { e.Client = "bare" }), expr: call(autoJS, 1), kind: "apply",
			err: ErrUnsupported},

		{name: "view", env: jsEnv{Client: "none", DPR: 1.5, H: 534}, expr: viewJS, kind: "view", view: View{DPR: 1.5, Height: 534}},
		{name: "a view not laid out", env: jsEnv{Client: "none", DPR: 1.71, H: 1}, expr: viewJS, kind: "view", view: View{DPR: 1.71, Height: 1}},
		{name: "a view without numbers", env: jsEnv{Client: "none", DPR: "2"}, expr: viewJS, kind: "view", view: View{}},
	}
}

// sleepEnv is a ready Steam, awake, its idle sleep at Steam's default
// (an hour), with an edit.
func sleepEnv(edit func(e *jsEnv)) jsEnv {
	e := steamEnv(nil)
	e.Store = &fakeStore{Suspending: false, Progress: 0.0}
	e.ClientSettings = map[string]any{"system_idle_suspend_ac_sec": 3600.0, "system_idle_suspend_battery_sec": 3600.0}
	e.SetSetting = "sync"
	if edit != nil {
		edit(&e)
	}
	return e
}

var awakeSleep = SleepState{Store: true, Progress: 0, CanResume: true, CanSet: true, AC: 3600, Battery: 3600}

func sleepCases() []jsCase {
	with := func(edit func(*SleepState)) SleepState { s := awakeSleep; edit(&s); return s }
	stuck := func(progress any) func(e *jsEnv) {
		return func(e *jsEnv) { e.Store.Suspending, e.Store.Progress = true, progress }
	}
	setCalls := []string{"setSetting(" + idleSuspendACNever + ")", "setSetting(" + idleSuspendBatteryNever + ")"}
	return []jsCase{
		{name: "sleep: awake", env: sleepEnv(nil), expr: sleepJS, kind: "sleep", sleep: awakeSleep},
		{name: "sleep: stuck", env: sleepEnv(stuck(1)), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.Suspending, s.Progress = true, SuspendComplete })},
		{name: "sleep: stuck before any progress", env: sleepEnv(stuck(0)), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.Suspending = true })},
		{name: "sleep: in flight", env: sleepEnv(stuck(2)), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.Suspending, s.Progress = true, 2 })},
		{name: "sleep: suspending not a boolean", env: sleepEnv(func(e *jsEnv) { e.Store.Suspending = 1 }), expr: sleepJS, kind: "sleep",
			sleep: awakeSleep},
		{name: "sleep: a progress of another kind", env: sleepEnv(stuck("1")), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.Suspending, s.Progress = true, -1 })},
		{name: "sleep: a fractional progress", env: sleepEnv(stuck(1.5)), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.Suspending, s.Progress = true, -1 })},
		{name: "sleep: no progress method", env: sleepEnv(func(e *jsEnv) { e.Store.Suspending, e.Store.NoState = true, true }), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.Suspending, s.Progress = true, -1 })},
		{name: "sleep: a store that throws", env: sleepEnv(func(e *jsEnv) { e.Store.Suspending, e.Store.Throws = true, true }), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.Progress = -1 })},
		{name: "sleep: no InitiateResume", env: sleepEnv(func(e *jsEnv) { e.Store.NoResume = true }), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.CanResume = false })},
		{name: "sleep: no store", env: sleepEnv(func(e *jsEnv) { e.Store = nil }), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.Store, s.Progress, s.CanResume = false, -1, false })},
		{name: "sleep: no SetSetting", env: sleepEnv(func(e *jsEnv) { e.SetSetting = "" }), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.CanSet = false })},
		{name: "sleep: SteamClient undefined", env: sleepEnv(func(e *jsEnv) { e.Client = "none" }), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.CanSet = false })},
		{name: "sleep: never", env: sleepEnv(func(e *jsEnv) {
			e.ClientSettings = map[string]any{"system_idle_suspend_ac_sec": 0, "system_idle_suspend_battery_sec": 0}
		}), expr: sleepJS, kind: "sleep", sleep: with(func(s *SleepState) { s.AC, s.Battery = 0, 0 })},
		{name: "sleep: garbage settings", env: sleepEnv(func(e *jsEnv) {
			e.ClientSettings = map[string]any{"system_idle_suspend_ac_sec": "3600", "system_idle_suspend_battery_sec": -5}
		}), expr: sleepJS, kind: "sleep", sleep: with(func(s *SleepState) { s.AC, s.Battery = -1, -1 })},
		{name: "sleep: settings out of range", env: sleepEnv(func(e *jsEnv) {
			e.ClientSettings = map[string]any{"system_idle_suspend_ac_sec": 3600.5, "system_idle_suspend_battery_sec": 1e10}
		}), expr: sleepJS, kind: "sleep", sleep: with(func(s *SleepState) { s.AC, s.Battery = -1, -1 })},
		{name: "sleep: one setting absent", env: sleepEnv(func(e *jsEnv) {
			e.ClientSettings = map[string]any{"system_idle_suspend_battery_sec": 0}
		}), expr: sleepJS, kind: "sleep", sleep: with(func(s *SleepState) { s.AC, s.Battery = -1, 0 })},
		{name: "sleep: no client settings", env: sleepEnv(func(e *jsEnv) { e.ClientSettings = nil }), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.AC, s.Battery = -1, -1 })},
		{name: "sleep: no settingsStore", env: sleepEnv(func(e *jsEnv) { e.Settings, e.ClientSettings = nil, nil }), expr: sleepJS, kind: "sleep",
			sleep: with(func(s *SleepState) { s.AC, s.Battery = -1, -1 })},

		{name: "never sleep", env: sleepEnv(nil), expr: neverSleepJS, kind: "nosleep", calls: setCalls},
		{name: "never sleep: SetSetting rejects", env: sleepEnv(func(e *jsEnv) { e.SetSetting = "reject" }), expr: neverSleepJS, kind: "nosleep",
			calls: setCalls},
		{name: "never sleep twice", env: sleepEnv(nil), expr: neverSleepJS, times: 2, kind: "nosleep",
			calls: append(slices.Clone(setCalls), setCalls...)},
		{name: "never sleep: no SetSetting", env: sleepEnv(func(e *jsEnv) { e.SetSetting = "" }), expr: neverSleepJS, kind: "nosleep",
			err: ErrNoSleepAPI},
		{name: "never sleep: SteamClient undefined", env: sleepEnv(func(e *jsEnv) { e.Client = "none" }), expr: neverSleepJS, kind: "nosleep",
			err: ErrNoSleepAPI},

		{name: "resume", env: sleepEnv(stuck(1)), expr: resumeJS, kind: "resume", resumes: []ResumeResult{Resumed}, calls: []string{"resume"}},
		{name: "resume twice", env: sleepEnv(stuck(1)), expr: resumeJS, times: 2, kind: "resume",
			resumes: []ResumeResult{Resumed, Awake}, calls: []string{"resume"}},
		{name: "resume while awake", env: sleepEnv(nil), expr: resumeJS, kind: "resume", resumes: []ResumeResult{Awake}},
		{name: "resume: suspending not a boolean", env: sleepEnv(func(e *jsEnv) { e.Store.Suspending = "yes" }), expr: resumeJS, kind: "resume",
			resumes: []ResumeResult{Awake}},
		{name: "resume: Steam stays suspending", env: sleepEnv(func(e *jsEnv) { stuck(1)(e); e.Store.Sticky = true }), expr: resumeJS, kind: "resume",
			resumes: []ResumeResult{StillSuspending}, calls: []string{"resume"}},
		{name: "resume: InitiateResume rejects", env: sleepEnv(func(e *jsEnv) { stuck(2)(e); e.Store.Reject = true }), expr: resumeJS, kind: "resume",
			resumes: []ResumeResult{Resumed}, calls: []string{"resume"}},
		{name: "resume: no InitiateResume", env: sleepEnv(func(e *jsEnv) { stuck(1)(e); e.Store.NoResume = true }), expr: resumeJS, kind: "resume",
			resumes: []ResumeResult{Awake}, err: ErrNoSleepAPI},
		{name: "resume: no store", env: sleepEnv(func(e *jsEnv) { e.Store = nil }), expr: resumeJS, kind: "resume",
			resumes: []ResumeResult{Awake}, err: ErrNoSleepAPI},
		{name: "resume: a store that throws", env: sleepEnv(func(e *jsEnv) { e.Store.Suspending, e.Store.Throws = true, true }),
			expr: resumeJS, kind: "resume", throws: true},
		{name: "resume: no InitiateResume on a store that throws", env: sleepEnv(func(e *jsEnv) {
			e.Store.Suspending, e.Store.Throws, e.Store.NoResume = true, true, true
		}), expr: resumeJS, kind: "resume", resumes: []ResumeResult{Awake}, err: ErrNoSleepAPI},
	}
}

// allJSCases is every case: the scale's and the sleep's.
func allJSCases() []jsCase { return append(jsCases(), sleepCases()...) }

// check holds Go's reading of an answer to the case.
func (tc jsCase) check(t *testing.T, result json.RawMessage) {
	t.Helper()
	switch tc.kind {
	case "read":
		var r readResult
		if err := json.Unmarshal(result, &r); err != nil {
			t.Fatalf("%s: %v", result, err)
		}
		st, err := r.decode()
		if st != tc.state || !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
			t.Errorf("read %s:\n got %+v, %v\nwant %+v, %v", result, st, err, tc.state, tc.err)
		}
	case "apply":
		var r applyResult
		if err := json.Unmarshal(result, &r); err != nil {
			t.Fatalf("%s: %v", result, err)
		}
		v, err := r.result()
		if v != tc.value || !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
			t.Errorf("apply %s: got %v, %v; want %v, %v", result, v, err, tc.value, tc.err)
		}
	case "view":
		var r viewResult
		if err := json.Unmarshal(result, &r); err != nil {
			t.Fatalf("%s: %v", result, err)
		}
		if v := r.view(Target{}); v != tc.view {
			t.Errorf("view %s: got %+v, want %+v", result, v, tc.view)
		}
	case "sleep":
		var r sleepResult
		if err := json.Unmarshal(result, &r); err != nil {
			t.Fatalf("%s: %v", result, err)
		}
		if st := r.decode(); st != tc.sleep {
			t.Errorf("sleep %s:\n got %+v\nwant %+v", result, st, tc.sleep)
		}
	case "nosleep":
		for _, r := range tc.statuses(t, result) {
			if err := r.neverSleep(); !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
				t.Errorf("never sleep %s: %v, want %v", result, err, tc.err)
			}
		}
	case "resume":
		rs := tc.statuses(t, result)
		if len(rs) != len(tc.resumes) {
			t.Fatalf("resume %s: %d answers, want %d", result, len(rs), len(tc.resumes))
		}
		for i, r := range rs {
			v, err := r.resume()
			if v != tc.resumes[i] || !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
				t.Errorf("resume %d of %s: %v, %v; want %v, %v", i+1, result, v, err, tc.resumes[i], tc.err)
			}
		}
	default:
		t.Fatalf("kind %q", tc.kind)
	}
}

// statuses decodes the case's answer, one per evaluation.
func (tc jsCase) statuses(t *testing.T, result json.RawMessage) []statusResult {
	t.Helper()
	if tc.times > 1 {
		var rs []statusResult
		if err := json.Unmarshal(result, &rs); err != nil || len(rs) != tc.times {
			t.Fatalf("%s: %v", result, err)
		}
		return rs
	}
	var r statusResult
	if err := json.Unmarshal(result, &r); err != nil {
		t.Fatalf("%s: %v", result, err)
	}
	return []statusResult{r}
}

// jsOutcome is what an expression did: its value, the setters it called
// and the marker it left, or what it threw.
type jsOutcome struct {
	Result json.RawMessage `json:"result"`
	Calls  []string        `json:"calls"`
	Marker json.RawMessage `json:"marker"`
	Error  string          `json:"error"`
}

// model runs the case in fakeUI. An expression the fake does not answer
// throws in Steam when the case says so, and fails the test otherwise.
func (tc jsCase) model(t *testing.T) jsOutcome {
	t.Helper()
	b, _ := json.Marshal(tc.env)
	var env jsEnv // a copy: the model changes its settings
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	u := &fakeUI{settings: env.Settings, client: env.Client, marker: env.Marker, dpr: env.DPR, h: env.H,
		store: env.Store, clientSettings: env.ClientSettings, setSetting: env.SetSetting}
	var vs []any
	for range max(tc.times, 1) {
		v, ok := u.eval(tc.expr)
		if !ok && tc.throws {
			return jsOutcome{Error: "threw"}
		}
		if !ok {
			t.Fatalf("%s: the fake does not know the expression", tc.name)
		}
		vs = append(vs, v)
	}
	var res []byte
	if tc.times > 1 {
		res, _ = json.Marshal(vs)
	} else {
		res, _ = json.Marshal(vs[0])
	}
	mk, _ := json.Marshal(u.marker)
	return jsOutcome{Result: res, Calls: u.calls, Marker: mk}
}

func (tc jsCase) checkOutcome(t *testing.T, o jsOutcome) {
	t.Helper()
	switch {
	case tc.throws && o.Error == "":
		t.Fatalf("did not throw: %s", o.Result)
	case tc.throws:
		return
	case o.Error != "":
		t.Fatalf("threw %s", o.Error)
	}
	tc.check(t, o.Result)
	if !slices.Equal(o.Calls, tc.calls) {
		t.Errorf("calls %q, want %q", o.Calls, tc.calls)
	}
	want, _ := json.Marshal(tc.marker)
	if !jsonEqual(o.Marker, want) {
		t.Errorf("marker %s, want %s", o.Marker, want)
	}
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// TestJSContract: Go's reading of every answer, through the fake Steam.
func TestJSContract(t *testing.T) {
	for _, tc := range allJSCases() {
		t.Run(tc.name, func(t *testing.T) { tc.checkOutcome(t, tc.model(t)) })
	}
}

// nodeRunner evaluates each {env, expr} in a fresh window of its own,
// times times. Steam's methods and getters check what they are called
// on, as its native bindings and its store's do; an unhandled rejection
// fails the run, as it would leave one in Steam.
const nodeRunner = `
const vm = require("node:vm");
let input = "";
process.stdin.on("data", (d) => (input += d));
process.stdin.on("end", () => {
  const out = JSON.parse(input).map(({env, expr, times}) => {
    const calls = [];
    const g = {};
    g.window = g;
    if (env.settings !== null) g.settingsStore = {settings: env.settings};
    if (env.clientSettings) (g.settingsStore ??= {}).clientSettings = env.clientSettings;
    if (env.client !== "none") {
      g.SteamClient = {Window: {}};
      if (env.client === "api") {
        g.SteamClient.Window.SetGamepadUIAutoDisplayScale = (b) => { calls.push("auto(" + b + ")"); };
        g.SteamClient.Window.SetGamepadUIManualDisplayScaleFactor = (v) => { calls.push("manual(" + v + ")"); };
      }
      if (env.setSetting) {
        const settings = {
          SetSetting(m) {
            if (this !== settings) throw new TypeError("Illegal invocation");
            calls.push("setSetting(" + m + ")");
            if (env.setSetting === "reject") return Promise.reject(new Error("refused"));
          },
        };
        g.SteamClient.Settings = settings;
      }
    }
    if (env.store) {
      const s = env.store;
      const store = {
        get suspending() {
          if (this !== store) throw new TypeError("Illegal invocation");
          if (s.throws) throw new Error("not yet");
          return s.suspending;
        },
      };
      if (!s.noState) {
        store.GetSuspendResumeState = function () {
          if (this !== store) throw new TypeError("Illegal invocation");
          if (s.throws) throw new Error("not yet");
          return s.progress;
        };
      }
      if (!s.noResume) {
        store.InitiateResume = function () {
          if (this !== store) throw new TypeError("Illegal invocation");
          calls.push("resume");
          if (!s.sticky) s.suspending = false;
          if (s.reject) return Promise.reject(new Error("refused"));
        };
      }
      g.SuspendResumeStore = store;
    }
    if ("marker" in env) g.__vosScale = env.marker;
    if ("dpr" in env) g.devicePixelRatio = env.dpr;
    if ("h" in env) g.innerHeight = env.h;
    vm.createContext(g);
    try {
      const results = [];
      for (let i = 0; i < (times || 1); i++) {
        results.push(JSON.parse(JSON.stringify(vm.runInContext(expr, g) ?? null)));
      }
      return {result: times > 1 ? results : results[0], calls, marker: g.__vosScale ?? null};
    } catch (e) {
      return {error: String(e && e.stack || e)};
    }
  });
  process.stdout.write(JSON.stringify(out));
});
`

// TestFakeMatchesJS runs the real expressions in Node (when installed):
// Go reads their answers as it reads the fake's, and the fake does what
// they do.
func TestFakeMatchesJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	cases := allJSCases()
	type input struct {
		Env   jsEnv  `json:"env"`
		Expr  string `json:"expr"`
		Times int    `json:"times,omitempty"`
	}
	var in []input
	for _, tc := range cases {
		in = append(in, input{tc.env, tc.expr, tc.times})
	}
	b, _ := json.Marshal(in)
	script := filepath.Join(t.TempDir(), "run.cjs")
	if err := os.WriteFile(script, []byte(nodeRunner), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script)
	cmd.Stdin = bytes.NewReader(b)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.Bytes())
	}
	var outcomes []jsOutcome
	if err := json.Unmarshal(out, &outcomes); err != nil || len(outcomes) != len(cases) {
		t.Fatalf("node said %s: %v", out, err)
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := outcomes[i]
			tc.checkOutcome(t, got)
			m := tc.model(t)
			if tc.throws {
				tc.checkOutcome(t, m) // both threw
				return
			}
			if !jsonEqual(got.Result, m.Result) || !slices.Equal(got.Calls, m.Calls) || !jsonEqual(got.Marker, m.Marker) {
				t.Errorf("the fake differs from the JavaScript:\n js   %s %q %s\n fake %s %q %s",
					got.Result, got.Calls, got.Marker, m.Result, m.Calls, m.Marker)
			}
		})
	}
}

// The messages neverSleepJS sends are a CMsgClientSettings with the one
// field, a varint 0: the key (field number << 3, wire type 0) and the
// value, each a protobuf varint.
func TestNeverSleepMessages(t *testing.T) {
	msg := func(field uint64) string {
		b := binary.AppendUvarint(nil, field<<3)
		b = binary.AppendUvarint(b, 0)
		return base64.StdEncoding.EncodeToString(b)
	}
	for _, tc := range []struct {
		field uint64 // CMsgClientSettings
		lit   string
	}{
		{24004, idleSuspendACNever},      // system_idle_suspend_ac_sec
		{24003, idleSuspendBatteryNever}, // system_idle_suspend_battery_sec
	} {
		if got := msg(tc.field); got != tc.lit {
			t.Errorf("field %d: %s, js.go has %s", tc.field, got, tc.lit)
		}
		if !strings.Contains(neverSleepJS, `s.SetSetting("`+tc.lit+`")`) {
			t.Errorf("neverSleepJS does not send %s", tc.lit)
		}
	}
}

func TestCount(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		want int
	}{{0, 0}, {3600, 3600}, {math.MaxInt32, math.MaxInt32}, {math.MaxInt32 + 1, -1}, {-1, -1}, {1.5, -1}, {math.NaN(), -1}, {math.Inf(1), -1}} {
		if got := count(&tc.v); got != tc.want {
			t.Errorf("count(%v) = %d, want %d", tc.v, got, tc.want)
		}
	}
	if count(nil) != -1 {
		t.Error("count(nil) is not -1")
	}
}

// The numbers in an expression are JSON.
func TestCall(t *testing.T) {
	if got := call("f", uint64(9007199254740991), 1.5); got != "(f)(9007199254740991,1.5)" {
		t.Errorf("got %s", got)
	}
	if got := call("f", uint64(3), 2.0, 1e-7); got != "(f)(3,2,1e-7)" {
		t.Errorf("got %s", got)
	}
}
