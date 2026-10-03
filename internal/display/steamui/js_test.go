package steamui

import (
	"bytes"
	"encoding/json"
	"errors"
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
// "none"), VaporOS's marker and a view's size.
type jsEnv struct {
	Settings map[string]any `json:"settings"`
	Client   string         `json:"client"`
	Marker   any            `json:"marker,omitempty"`
	DPR      any            `json:"dpr,omitempty"`
	H        any            `json:"h,omitempty"`
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
	name   string
	env    jsEnv
	expr   string
	kind   string  // read, apply or view
	state  State   // read's
	value  float64 // apply's
	view   View
	err    error
	calls  []string // the setters called
	marker any      // window.__vosScale afterwards
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
	default:
		t.Fatalf("kind %q", tc.kind)
	}
}

// jsOutcome is what an expression did: its value, the setters it called
// and the marker it left.
type jsOutcome struct {
	Result json.RawMessage `json:"result"`
	Calls  []string        `json:"calls"`
	Marker json.RawMessage `json:"marker"`
	Error  string          `json:"error"`
}

// model runs the case in fakeUI.
func (tc jsCase) model(t *testing.T) jsOutcome {
	t.Helper()
	b, _ := json.Marshal(tc.env)
	var env jsEnv // a copy: the model changes its settings
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	u := &fakeUI{settings: env.Settings, client: env.Client, marker: env.Marker, dpr: env.DPR, h: env.H}
	v, ok := u.eval(tc.expr)
	if !ok {
		t.Fatalf("%s: the fake does not know the expression", tc.name)
	}
	res, _ := json.Marshal(v)
	mk, _ := json.Marshal(u.marker)
	return jsOutcome{Result: res, Calls: u.calls, Marker: mk}
}

func (tc jsCase) checkOutcome(t *testing.T, o jsOutcome) {
	t.Helper()
	if o.Error != "" {
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
	for _, tc := range jsCases() {
		t.Run(tc.name, func(t *testing.T) { tc.checkOutcome(t, tc.model(t)) })
	}
}

// nodeRunner evaluates each {env, expr} in a fresh window of its own.
const nodeRunner = `
const vm = require("node:vm");
let input = "";
process.stdin.on("data", (d) => (input += d));
process.stdin.on("end", () => {
  const out = JSON.parse(input).map(({env, expr}) => {
    const calls = [];
    const g = {};
    g.window = g;
    if (env.settings !== null) g.settingsStore = {settings: env.settings};
    if (env.client !== "none") {
      g.SteamClient = {Window: {}};
      if (env.client === "api") {
        g.SteamClient.Window.SetGamepadUIAutoDisplayScale = (b) => { calls.push("auto(" + b + ")"); };
        g.SteamClient.Window.SetGamepadUIManualDisplayScaleFactor = (v) => { calls.push("manual(" + v + ")"); };
      }
    }
    if ("marker" in env) g.__vosScale = env.marker;
    if ("dpr" in env) g.devicePixelRatio = env.dpr;
    if ("h" in env) g.innerHeight = env.h;
    vm.createContext(g);
    try {
      const result = vm.runInContext(expr, g);
      return {result: JSON.parse(JSON.stringify(result ?? null)), calls, marker: g.__vosScale ?? null};
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
	cases := jsCases()
	type input struct {
		Env  jsEnv  `json:"env"`
		Expr string `json:"expr"`
	}
	var in []input
	for _, tc := range cases {
		in = append(in, input{tc.env, tc.expr})
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
			if !jsonEqual(got.Result, m.Result) || !slices.Equal(got.Calls, m.Calls) || !jsonEqual(got.Marker, m.Marker) {
				t.Errorf("the fake differs from the JavaScript:\n js   %s %q %s\n fake %s %q %s",
					got.Result, got.Calls, got.Marker, m.Result, m.Calls, m.Marker)
			}
		})
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
