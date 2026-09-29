package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/install"
	"github.com/jasperaelvoet/vaporos/internal/power"
	"github.com/jasperaelvoet/vaporos/internal/storage"
	"github.com/jasperaelvoet/vaporos/internal/sunshine"
	"github.com/jasperaelvoet/vaporos/internal/system"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// The contract tests keep the dev server's fake from drifting away from
// the real API (docs/CONTRACTS.md and the real handlers' types). Two run
// in every go test and hold only for our own files: TestFixtureKeysKnown
// and TestFakeRoutesInContract. Three compare against what the backend
// session may change at any time, so they run only with VOS_WEB_STRICT=1
// (web.yml and our gates): TestFixturesCoverContract, TestFakeRoutesComplete
// and TestValidationParity.

// notInContractYetRoutes are real routes the fake serves that the contract
// does not list yet.
var notInContractYetRoutes = []string{}

// ---- the fixtures' own consistency and the real types (always on)

// fakeTypes is the real type each document decodes into. Unexported
// answer wrappers are mirrored here from the handlers named.
var fakeTypes = map[string]func() any{
	"system": func() any { return new(system.Info) },
	"ssh":    func() any { return new(system.SSHState) },
	"update": func() any { return new(update.View) },
	"sunshine": func() any { // sunshine.statusResponse and its sessionInfo
		return new(struct {
			sunshine.Summary
			Session *struct {
				Client string `json:"client"`
				Mode   string `json:"mode"`
				HDR    bool   `json:"hdr"`
				App    string `json:"app,omitempty"`
				Since  string `json:"since,omitempty"`
			} `json:"session"`
		})
	},
	"sunshine-clients": func() any {
		return new(struct {
			Clients []sunshine.PairedClient `json:"clients"`
		})
	},
	"sunshine-settings": func() any { // sunshine.settingsView
		return new(struct {
			sunshine.Settings
			Choices struct {
				Encoder        []string `json:"encoder"`
				Gamepad        []string `json:"gamepad"`
				BitrateKbpsMax struct {
					Min int `json:"min"`
					Max int `json:"max"`
				} `json:"bitrate_kbps_max"`
			} `json:"choices"`
		})
	},
	"sunshine-logs": func() any { return new([]string) },
	"display":       func() any { return new(display.Info) },
	"storage": func() any {
		return new(struct {
			Disks []storage.Disk `json:"disks"`
		})
	},
	"power": func() any { // power.powerState
		return new(struct {
			power.Summary
			WoL []power.WoLIface `json:"wol"`
		})
	},
	"status": func() any { // daemon.statusView
		return new(struct {
			System   *system.Info      `json:"system"`
			Sunshine *sunshine.Summary `json:"sunshine"`
			Stream   *display.Session  `json:"stream"`
			Display  *display.Info     `json:"display"`
			Update   *update.View      `json:"update"`
			Power    *power.Summary    `json:"power"`
			Restart  struct {
				Needed  bool `json:"needed"`
				Reasons []struct {
					Kind    string `json:"kind"`
					Version string `json:"version,omitempty"`
				} `json:"reasons"`
			} `json:"restart"`
		})
	},
	"install-probe":  func() any { return new(install.ProbeResult) },
	"install-status": func() any { return new(install.Status) },
}

// eventTopics are the topics vosd publishes (grep Publish in internal/).
var eventTopics = []string{"update.progress", "update.state", "install.progress", "session.begin", "session.end",
	"pairing.pending", "pairing.state", "display.changed", "power.idle", "system.message"}

func testFixtures(t *testing.T) *fixtureSet {
	t.Helper()
	fx, err := loadFixtures(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	return fx
}

// appendixB is every preset of MASTER-PLAN Appendix B and every script.
var appendixB = map[string][]string{
	"presets": {"idle", "headless", "streaming", "pairing-1", "pairing-2", "keep-awake", "busy-web", "idle-countdown",
		"no-wol", "empty", "ssh-on", "signed-out", "first-run",
		"update-available", "update-staging", "update-staged", "update-stale-check", "update-error",
		"update-check-failed", "update-failed-newer", "update-held", "update-trial", "rollback-pending",
		"no-gpu", "sunshine-starting", "sunshine-stopped", "sunshine-unreachable", "reboot-needed",
		"disk-low", "storage-missing", "storage-pending", "logs-empty", "logs-error",
		"installer-code", "installer-waived", "installer-one-disk", "installer-no-disk",
		"installer-source-error", "installer-two-vaporos", "installer-failed"},
	"scripts": {"stream", "stream-end", "pair", "update", "power-off", "wake", "reset"},
}

var (
	heroStates   = []string{"ready", "streaming", "updating", "fault-no-gpu", "starting", "fault-stopped", "fault-not-answering", "fault-unknown", "restart-needed"}
	restartKinds = []string{"update", "rollback", "display"}
	cardIDs      = []string{"pair", "update-progress", "update-ready", "update-available", "update-failed", "update-stopped", "cant-check", "low-space", "cant-wake"}
)

// TestFixtureKeysKnown decodes every document, as base and after every
// preset and script patch, into the real handlers' types with unknown
// fields refused, and checks the presets and scripts themselves: the
// Appendix B names, error and latency routes the fake serves, known event
// topics, and the expect vocabulary.
func TestFixtureKeysKnown(t *testing.T) {
	fx := testFixtures(t)
	now := time.Now()
	decode := func(where, name string, doc any) {
		t.Helper()
		b, _ := json.Marshal(doc)
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(fakeTypes[name]()); err != nil {
			t.Errorf("%s: %s: %v", where, name, err)
		}
	}
	for name := range fakeResources {
		if fakeTypes[name] == nil {
			t.Fatalf("no real type for resource %s", name)
		}
		decode("base", name, resolveTimes(deepCopyJSON(fx.base[name]), now))
	}

	for kind, want := range appendixB {
		var have []string
		if kind == "presets" {
			have = fx.presetNames()
		} else {
			for n := range fx.scripts {
				have = append(have, n)
			}
		}
		sort.Strings(have)
		w := slices.Clone(want)
		sort.Strings(w)
		if !slices.Equal(have, w) {
			t.Errorf("%s: have %v, MASTER-PLAN Appendix B lists %v", kind, have, w)
		}
	}
	for _, a := range presetAliases {
		if fx.presets[a.preset] == nil {
			t.Errorf("alias %v names no preset %q", a.env, a.preset)
		}
	}

	osRoutes, isoRoutes := fakeRouteKeys(false), fakeRouteKeys(true)
	checkEvents := func(where string, evs []fakeEvent) {
		for _, ev := range evs {
			if !slices.Contains(eventTopics, ev.Topic) {
				t.Errorf("%s: unknown topic %q", where, ev.Topic)
			}
			if !json.Valid(ev.Data) {
				t.Errorf("%s: %s data is not JSON", where, ev.Topic)
			}
		}
	}
	for _, name := range fx.presetNames() {
		p := fx.presets[name]
		where := "presets/" + name + ".json"
		docs, err := fx.docs(p, now)
		if err != nil {
			t.Errorf("%s: %v", where, err)
			continue
		}
		for res := range p.Patch {
			decode(where, res, docs[res])
		}
		answers, err := fakeAnswers(fx, p)
		if err != nil {
			t.Fatal(err)
		}
		for res, v := range answers {
			if !strings.HasPrefix(res, "event ") {
				decode(where+" (as served)", res, v)
			}
		}
		routes := osRoutes
		if p.installer() {
			routes = isoRoutes
		}
		for k := range p.Errors {
			if !routes[k] {
				t.Errorf("%s: errors: the fake serves no %s in this mode", where, k)
			}
		}
		for k := range p.Latency {
			if !routes[k] {
				t.Errorf("%s: latency: the fake serves no %s in this mode", where, k)
			}
		}
		checkEvents(where, p.Events)
		e := p.Expect
		switch {
		case e.Hero == nil && e.Redirect == "":
			t.Errorf("%s: expect needs a hero, or a redirect with hero null", where)
		case e.Hero != nil && !slices.Contains(heroStates, *e.Hero):
			t.Errorf("%s: expect.hero %q is not one of %v", where, *e.Hero, heroStates)
		}
		for _, r := range e.Restart {
			if !slices.Contains(restartKinds, r) {
				t.Errorf("%s: expect.restart %q is not one of %v", where, r, restartKinds)
			}
		}
		last := -1
		for _, c := range e.Cards {
			i := slices.Index(cardIDs, c)
			if i < 0 {
				t.Errorf("%s: expect.cards %q is not one of %v", where, c, cardIDs)
			} else if i < last {
				t.Errorf("%s: expect.cards are not in priority order %v", where, cardIDs)
			}
			last = i
		}
		if e.Attention != "" && e.Attention != "pair" {
			t.Errorf("%s: expect.attention %q", where, e.Attention)
		}
	}
	for name, s := range fx.scripts {
		where := "scripts/" + name + ".json"
		docs, _ := fx.docs(fx.presets[defaultPreset], now)
		for i, st := range s.Steps {
			if err := applyPatch(docs, st.Patch, now); err != nil {
				t.Errorf("%s step %d: %v", where, i, err)
			}
			for res := range st.Patch {
				decode(fmt.Sprintf("%s step %d", where, i), res, docs[res])
			}
			checkEvents(where, st.Events)
		}
	}
}

// fakeRouteKeys lists the fake's routes in one mode as "METHOD /path".
func fakeRouteKeys(installer bool) map[string]bool {
	out := map[string]bool{}
	for _, r := range fakeRouteList(installer) {
		out[r.Method+" "+r.Path] = true
	}
	return out
}

func fakeRouteList(installer bool) []fakeRoute {
	p := &fakePreset{}
	f := newDevFake(nil, nil, p, map[string]any{}, installer)
	var out []fakeRoute
	f.routeTable(func(method, path string, access api.Access, h fakeHandler) {
		out = append(out, fakeRoute{method, path, access})
	})
	return out
}

var accessNames = map[api.Access]string{api.Public: "Public", api.Authed: "Authed", api.Setup: "Setup", api.Local: "Local"}

// ---- docs/CONTRACTS.md

type contractRoute struct {
	Method, Path, Access, Response string
}

type contractDoc struct {
	routes      []contractRoute
	topics      map[string]string   // topic → its data cell
	updateState []string            // the keys of the "Update state" document
	config      map[string][]string // config.json section → its keys
}

// splitRow splits a Markdown table row on | that are not escaped.
func splitRow(line string) []string {
	var cells []string
	var b strings.Builder
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			b.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(line[i])
		}
	}
	return append(cells, strings.TrimSpace(b.String()))
}

var backtickRe = regexp.MustCompile("`([^`]+)`")

// loadContract reads the HTTP API tables of docs/CONTRACTS.md.
func loadContract(t *testing.T) *contractDoc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "CONTRACTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	c := &contractDoc{topics: map[string]string{}, config: map[string][]string{}}
	table := ""
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "| Method + path |"):
			table = "routes"
			continue
		case strings.HasPrefix(line, "| Topic |"):
			table = "topics"
			continue
		case strings.HasPrefix(line, "## config.json"):
			for _, l := range lines[i+1:] {
				if strings.HasPrefix(l, "}") {
					break
				}
				if m := regexp.MustCompile(`^\s*"([a-z_]+)":\s*\{(.*)\}`).FindStringSubmatch(l); m != nil {
					for _, k := range regexp.MustCompile(`"([a-z_]+)":`).FindAllStringSubmatch(m[2], -1) {
						c.config[m[1]] = append(c.config[m[1]], k[1])
					}
				}
			}
		case strings.HasPrefix(line, "**Update state**"):
			for _, l := range lines[i+1:] {
				if strings.HasPrefix(l, "```") && len(c.updateState) > 0 {
					break
				}
				for _, m := range regexp.MustCompile(`"([a-z_]+)":`).FindAllStringSubmatch(l, -1) {
					c.updateState = append(c.updateState, m[1])
				}
			}
		case !strings.HasPrefix(line, "|"):
			table = ""
		}
		if table == "" || strings.HasPrefix(line, "| ---") {
			continue
		}
		cells := splitRow(line)
		switch table {
		case "routes":
			if len(cells) < 3 {
				continue
			}
			methods := strings.Split(strings.Fields(cells[0])[0], "/")
			for _, p := range backtickRe.FindAllStringSubmatch(cells[0], -1) {
				for _, m := range methods {
					c.routes = append(c.routes, contractRoute{m, p[1], cells[1], cells[2]})
				}
			}
		case "topics":
			if m := backtickRe.FindStringSubmatch(cells[0]); m != nil && len(cells) > 1 {
				c.topics[m[1]] = cells[1]
			}
		}
	}
	if len(c.routes) < 30 || len(c.topics) < 8 || len(c.updateState) < 5 {
		t.Fatalf("docs/CONTRACTS.md: read %d routes, %d topics and %d update-state keys; has its layout changed?",
			len(c.routes), len(c.topics), len(c.updateState))
	}
	return c
}

func (c *contractDoc) route(method, path string) (contractRoute, bool) {
	for _, r := range c.routes {
		if r.Method == method && r.Path == path {
			return r, true
		}
	}
	return contractRoute{}, false
}

// fakeAnswers serves every GET document the fake answers in preset p,
// after one request of the viewer's (web activity), decoded, with the
// events the fake published meanwhile as "event <topic>".
func fakeAnswers(fx *fixtureSet, p *fakePreset) (map[string]any, error) {
	docs, err := fx.docs(p, time.Now())
	if err != nil {
		return nil, err
	}
	f := newDevFake(nil, nil, p, docs, p.installer())
	evs, cancel := f.hub.Subscribe()
	defer cancel()
	f.touch()
	byPath := map[string]string{"/status": "status"}
	for res, path := range fakeResources {
		byPath[path] = res
	}
	out := map[string]any{}
	f.routeTable(func(method, path string, access api.Access, h fakeHandler) {
		res := byPath[path]
		if method != "GET" || res == "" || res == "sunshine-logs" {
			return
		}
		r := httptest.NewRequest("GET", api.Prefix+path, nil)
		f.mu.Lock()
		v := h(httptest.NewRecorder(), r)
		if s, ok := v.(fakeStatus); ok {
			v = s.v
		}
		b, _ := json.Marshal(v)
		f.mu.Unlock()
		var x any
		json.Unmarshal(b, &x)
		out[res] = x
	})
	for {
		select {
		case ev := <-evs:
			var x any
			json.Unmarshal(ev.Data, &x)
			out["event "+ev.Topic] = x
		default:
			return out, nil
		}
	}
}

// TestFakeRoutesInContract: every route the fake registers is in the
// contract's table with the same access level, or in
// notInContractYetRoutes.
func TestFakeRoutesInContract(t *testing.T) {
	c := loadContract(t)
	for _, installer := range []bool{false, true} {
		for _, r := range fakeRouteList(installer) {
			key := r.Method + " " + r.Path + " " + accessNames[r.Access]
			cr, ok := c.route(r.Method, r.Path)
			switch {
			case !ok && !slices.Contains(notInContractYetRoutes, key):
				t.Errorf("the fake serves %s, which docs/CONTRACTS.md lacks (add it to notInContractYetRoutes only if the real API serves it)", key)
			case ok && cr.Access != accessNames[r.Access]:
				t.Errorf("the fake serves %s %s as %s; docs/CONTRACTS.md says %s", r.Method, r.Path, accessNames[r.Access], cr.Access)
			}
		}
	}
}
