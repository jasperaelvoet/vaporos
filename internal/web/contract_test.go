package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/auth"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
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
	"extensions":     func() any { return new(extensions.Document) },
}

// eventTopics are the topics vosd publishes (grep Publish in internal/).
var eventTopics = []string{"update.progress", "update.state", "install.progress", "session.begin", "session.end",
	"pairing.pending", "pairing.state", "sunshine.state", "display.changed", "power.idle", "system.message",
	"extensions.state"}

func testFixtures(t *testing.T) *fixtureSet {
	t.Helper()
	fx, err := loadFixtures(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	return fx
}

// appendixB is every preset of MASTER-PLAN Appendix B and every script,
// plus rollback-forward (a newer next_boot with nothing staged) and the
// extensions' (an image downloading, a change waiting for a restart, and
// extensions that need attention).
var appendixB = map[string][]string{
	"presets": {"idle", "headless", "streaming", "pairing-1", "pairing-2", "keep-awake", "busy-web", "idle-countdown",
		"no-wol", "empty", "ssh-on", "signed-out", "first-run",
		"update-available", "update-staging", "update-staged", "update-stale-check", "update-error",
		"update-check-failed", "update-failed-newer", "update-held", "update-trial", "rollback-pending", "rollback-forward",
		"no-gpu", "sunshine-starting", "sunshine-stopped", "sunshine-unreachable", "reboot-needed",
		"disk-low", "storage-missing", "storage-pending", "logs-empty", "logs-error",
		"installer-code", "installer-waived", "installer-one-disk", "installer-no-disk",
		"installer-source-error", "installer-two-vaporos", "installer-failed",
		"extensions-installing", "extensions-restart", "extensions-attention"},
	"scripts": {"stream", "stream-end", "pair", "update", "power-off", "wake", "reset"},
}

var (
	heroStates   = []string{"ready", "streaming", "updating", "fault-no-gpu", "starting", "fault-stopped", "fault-not-answering", "fault-unknown", "restart-needed"}
	restartKinds = []string{"rollback", "next", "display", "extensions"} // state.js pendingReasons: a staged update is not one
	cardIDs      = []string{"pair", "idle-soon", "update-progress", "update-ready", "update-available", "update-failed", "update-stopped", "cant-check", "low-space", "cant-wake"}
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

// ---- strict: the contract's fields and the real code

// notInContractYet is what the real API answers, the fake answers too, and
// docs/CONTRACTS.md does not list yet, per resource (the GET route's
// document) and per event topic. It is the list of contract rows to ask
// the backend for (MASTER-PLAN BE-06, BE-14); TestFixturesCoverContract
// prints it and fails on an entry the contract has since gained.
var notInContractYet = map[string][]string{
	"system":        {"card"},
	"install-probe": {"card"},
	"update":        {"running", "tries_left", "tries_done", "entry"},
	"storage":       {"parent", "type", "transport", "removable", "partlabel"},
}

func strictOnly(t *testing.T) {
	t.Helper()
	if os.Getenv("VOS_WEB_STRICT") != "1" {
		t.Skip("compares against the backend's contract and code; runs with VOS_WEB_STRICT=1")
	}
}

// fields lists the field names of a response cell: the quoted names that
// are keys ("name": or a bare "name" in an object or a plain list), not
// values in an array or after a colon; the optional ones carry a ?.
func fields(cell string) (all, optional map[string]bool) {
	all, optional = map[string]bool{}, map[string]bool{}
	var stack []byte
	prev := byte(0) // the last non-space character outside a string
	for i := 0; i < len(cell); i++ {
		switch c := cell[i]; c {
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case '"':
			j := strings.IndexByte(cell[i+1:], '"')
			if j < 0 {
				return all, optional
			}
			tok, rest := cell[i+1:i+1+j], strings.TrimLeft(cell[i+2+j:], " ")
			i += j + 1
			isKey := strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "?") ||
				(prev != ':' && (len(stack) == 0 || stack[len(stack)-1] == '{'))
			if isKey && regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(tok) {
				all[tok] = true
				if strings.HasPrefix(rest, "?") {
					optional[tok] = true
				}
			}
			prev = '"'
			continue
		case ' ':
			continue
		}
		prev = cell[i]
	}
	return all, optional
}

// eventFields lists the fields of an event's data cell: the names in its
// {…} schema, plus a GET route's fields when the cell says "as in GET".
func (c *contractDoc) eventFields(cell string) (all, optional map[string]bool) {
	all, optional = map[string]bool{}, map[string]bool{}
	for _, m := range backtickRe.FindAllStringSubmatch(cell, -1) {
		if !strings.HasPrefix(m[1], "{") {
			continue
		}
		for _, f := range eventFieldRe.FindAllStringSubmatch(m[1], -1) {
			all[f[1]] = true
			if f[2] == "?" {
				optional[f[1]] = true
			}
		}
	}
	if m := regexp.MustCompile("as in GET `([^`]+)`").FindStringSubmatch(cell); m != nil {
		if r, ok := c.route("GET", m[1]); ok {
			more, _ := c.responseFields(r)
			for k := range more {
				all[k] = true
			}
		}
	}
	return all, optional
}

var (
	eventFieldRe = regexp.MustCompile(`([a-z_][a-z0-9_]*)(\?)?`)
	elidedRe     = regexp.MustCompile(`"([a-z_]+)":\{…\}`)
	configRefRe  = regexp.MustCompile(`config\.([a-z_]+)`)
)

// responseFields is fields of a route's response cell, with what it only
// refers to filled in: "key":{…} takes the fields another row gives the
// same key, config.<section> the config.json section's keys, and GET
// /update the update-state document's keys.
func (c *contractDoc) responseFields(r contractRoute) (all, optional map[string]bool) {
	all, optional = fields(r.Response)
	for _, m := range elidedRe.FindAllStringSubmatch(r.Response, -1) {
		for _, o := range c.routes {
			i := strings.Index(o.Response, `"`+m[1]+`":{`)
			if i < 0 || strings.HasPrefix(o.Response[i+len(m[1])+4:], "…") {
				continue
			}
			rest := o.Response[i+len(m[1])+4:]
			if j := strings.IndexByte(rest, '}'); j >= 0 {
				more, _ := fields(rest[:j])
				for k := range more {
					all[k] = true
				}
			}
		}
	}
	for _, m := range configRefRe.FindAllStringSubmatch(r.Response, -1) {
		for _, k := range c.config[m[1]] {
			all[k] = true
		}
	}
	if r.Method == "GET" && r.Path == "/update" {
		for _, k := range c.updateState {
			all[k] = true
		}
	}
	return all, optional
}

// omitempty lists the JSON names of fields the real types leave out when
// empty: optional even when the contract does not mark them.
func omitempty(t reflect.Type, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && tag == "" {
			omitempty(f.Type, out)
			continue
		}
		if strings.Contains(opts, "omitempty") {
			out[name] = true
		}
		omitempty(f.Type, out)
	}
}

// jsonKeys collects every object key in v, at any depth.
func jsonKeys(v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			out[k] = true
			jsonKeys(e, out)
		}
	case []any:
		for _, e := range x {
			jsonKeys(e, out)
		}
	}
}

// TestFixturesCoverContract: each GET document has every field the
// contract promises (unless optional), and every key of every document and
// event is in the contract or in notInContractYet; the list is printed.
func TestFixturesCoverContract(t *testing.T) {
	strictOnly(t)
	fx := testFixtures(t)
	c := loadContract(t)
	now := time.Now()
	used := map[string]map[string]bool{} // resource or "event <topic>" → keys
	add := func(where string, v any) {
		if used[where] == nil {
			used[where] = map[string]bool{}
		}
		jsonKeys(v, used[where])
	}
	for name := range fakeResources {
		add(name, fx.base[name])
	}
	addEvents := func(evs []fakeEvent) {
		for _, ev := range evs {
			var v any
			json.Unmarshal(ev.Data, &v)
			add("event "+ev.Topic, v)
		}
	}
	for _, p := range fx.presets {
		docs, _ := fx.docs(p, now)
		for res := range p.Patch {
			add(res, docs[res])
		}
		answers, err := fakeAnswers(fx, p)
		if err != nil {
			t.Fatal(err)
		}
		for res, v := range answers {
			add(res, v)
		}
		addEvents(p.Events)
	}
	for _, s := range fx.scripts {
		for _, st := range s.Steps {
			addEvents(st.Events)
		}
	}

	var gaps []string
	delete(used, "status") // made of the other documents, each checked as itself
	for where, keys := range used {
		var promised, optional map[string]bool
		if topic, ok := strings.CutPrefix(where, "event "); ok {
			cell, known := c.topics[topic]
			promised, optional = c.eventFields(cell)
			if !known && !slices.Contains(notInContractYet[where], "") && len(notInContractYet[where]) == 0 {
				t.Errorf("topic %s is not in docs/CONTRACTS.md", topic)
			}
			if !known {
				gaps = append(gaps, "topic "+topic)
			}
		} else {
			cr, ok := c.route("GET", fakeResources[where])
			if !ok {
				t.Errorf("docs/CONTRACTS.md has no GET %s (document %s)", fakeResources[where], where)
				continue
			}
			promised, optional = c.responseFields(cr)
			omit := map[string]bool{}
			omitempty(reflect.TypeOf(fakeTypes[where]()), omit)
			for k := range promised {
				if !optional[k] && !omit[k] && !keys[k] && where != "sunshine-logs" {
					t.Errorf("no fixture of %s has %q, which GET %s promises", where, k, fakeResources[where])
				}
			}
		}
		allowed := notInContractYet[where]
		for k := range keys {
			switch {
			case promised[k]:
				if slices.Contains(allowed, k) {
					t.Errorf("notInContractYet[%q] lists %q, which docs/CONTRACTS.md now has: remove it", where, k)
				}
			case slices.Contains(allowed, k):
				gaps = append(gaps, where+" "+k)
			default:
				t.Errorf("%s: %q is neither in docs/CONTRACTS.md nor in notInContractYet (the fake gains a field only once its contract row lands, or the real API already has it)", where, k)
			}
		}
		for _, k := range allowed {
			if !keys[k] && !promised[k] {
				t.Errorf("notInContractYet[%q] lists %q, which no fixture uses", where, k)
			}
		}
	}
	for where, ks := range notInContractYet {
		if used[where] == nil {
			t.Errorf("notInContractYet lists %q, which no fixture or answer uses (%v)", where, ks)
		}
	}
	for _, r := range notInContractYetRoutes {
		gaps = append(gaps, "route "+r)
	}
	sort.Strings(gaps)
	t.Logf("notInContractYet (%d): the real API has these and docs/CONTRACTS.md does not:\n  %s", len(gaps), strings.Join(gaps, "\n  "))
}

// realRoute is one srv.Handle call in the backend's code.
type realRoute struct {
	Method, Path, Access, Pkg string
}

// realRoutes finds every srv.Handle(method, path, access, h) call in the
// service packages' code (not tests), with the access level as written.
func realRoutes(t *testing.T) []realRoute {
	t.Helper()
	var out []realRoute
	for _, pkg := range []string{"system", "display", "update", "sunshine", "storage", "power", "install", "daemon", "extensions"} {
		dir := filepath.Join("..", pkg)
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		fset := token.NewFileSet()
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 4 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Handle" {
					return true
				}
				method := ""
				switch a := call.Args[0].(type) {
				case *ast.BasicLit:
					method, _ = strconv.Unquote(a.Value)
				case *ast.SelectorExpr: // http.MethodGet
					method = strings.ToUpper(strings.TrimPrefix(a.Sel.Name, "Method"))
				}
				lit, ok := call.Args[1].(*ast.BasicLit)
				acc, ok2 := call.Args[2].(*ast.SelectorExpr)
				if method == "" || !ok || !ok2 {
					return true
				}
				path, _ := strconv.Unquote(lit.Value)
				out = append(out, realRoute{method, path, acc.Sel.Name, pkg})
				return true
			})
		}
	}
	if len(out) < 25 {
		t.Fatalf("found only %d routes in the service packages; has Routes changed shape?", len(out))
	}
	return out
}

// TestFakeRoutesComplete: the fake serves exactly the real services'
// routes, with the real access levels, in the mode vosd serves them
// (internal/daemon/daemon.go); notInContractYetRoutes lists exactly the
// real routes the contract lacks; and every route in the contract is
// served by the real core or the fake.
func TestFakeRoutesComplete(t *testing.T) {
	strictOnly(t)
	c := loadContract(t)
	exempt := map[string]bool{"GET /welcome": true} // Local, for the welcome screen; no page calls it
	fakes := map[bool]map[string]string{}
	for _, installer := range []bool{false, true} {
		fakes[installer] = map[string]string{}
		for _, r := range fakeRouteList(installer) {
			fakes[installer][r.Method+" "+r.Path] = accessNames[r.Access]
		}
	}
	real := map[string]bool{}
	for _, r := range realRoutes(t) {
		key := r.Method + " " + r.Path
		real[key] = true
		if exempt[key] {
			continue
		}
		modes := []bool{false, true} // display and system serve in both modes
		switch r.Pkg {
		case "install":
			modes = []bool{true}
		case "update", "sunshine", "storage", "power", "daemon", "extensions": // daemon: GET /status
			modes = []bool{false}
		}
		for _, m := range modes {
			acc, ok := fakes[m][key]
			switch {
			case !ok:
				t.Errorf("the fake lacks %s (%s, installer=%v)", key, r.Pkg, m)
			case acc != r.Access:
				t.Errorf("the fake serves %s as %s; internal/%s registers it as %s", key, acc, r.Pkg, r.Access)
			}
		}
		if _, ok := c.route(r.Method, r.Path); !ok && !slices.Contains(notInContractYetRoutes, key+" "+r.Access) {
			t.Errorf("%s (internal/%s) is not in docs/CONTRACTS.md: add %q to notInContractYetRoutes", key, r.Pkg, key+" "+r.Access)
		}
	}
	for _, m := range []bool{false, true} {
		for key := range fakes[m] {
			if !real[key] {
				t.Errorf("the fake serves %s, which no service registers", key)
			}
		}
	}
	for _, r := range notInContractYetRoutes {
		f := strings.Fields(r)
		if _, ok := c.route(f[0], f[1]); ok {
			t.Errorf("notInContractYetRoutes lists %s, which docs/CONTRACTS.md now has: remove it", r)
		}
	}
	core := fakeRouteKeys(false) // placeholder for the set below
	for k := range core {
		delete(core, k)
	}
	for _, r := range c.routes {
		key := r.Method + " " + r.Path
		if exempt[key] || strings.HasPrefix(r.Path, "/auth/") || r.Path == "/ping" || r.Path == "/events" {
			continue // the real core serves these
		}
		if _, ok := fakes[false][key]; ok {
			continue
		}
		if _, ok := fakes[true][key]; ok {
			continue
		}
		t.Errorf("docs/CONTRACTS.md lists %s, which the fake does not serve", key)
	}
}

// ---- strict: the fake refuses what the real handlers refuse, in the same words

// parityCase is one request that the real handler refuses before it does
// anything (the vectors never reach code that touches the machine).
type parityCase struct {
	method, path, body string
}

var parityOS = []parityCase{
	{"PUT", "/system/hostname", `{"hostname":"-vapor"}`},
	{"PUT", "/system/hostname", `{"hostname":"localhost"}`},
	{"PUT", "/system/hostname", `{"hostname":"a.b"}`},
	{"PUT", "/system/hostname", `{"hostname":`},
	{"PUT", "/ssh", `{"enabled":true,"keys":[]}`},
	{"PUT", "/ssh", `{"enabled":false,"keys":["ssh-ed25519 notbase64"]}`},
	{"PUT", "/power", `{"idle_minutes":0}`},
	{"PUT", "/power", `{"idle_minutes":1441}`},
	{"POST", "/power/keep-awake", `{}`},
	{"POST", "/power/keep-awake", `{"minutes":-1}`},
	{"POST", "/power/keep-awake", `{"minutes":10081}`},
	{"PUT", "/sunshine/settings", `{"encoder":"auto"}`},
	{"PUT", "/sunshine/settings", `{"gamepad":"xbox"}`},
	{"PUT", "/sunshine/settings", `{"bitrate_kbps_max":1000001}`},
	{"PUT", "/sunshine/settings", `{"audio_sink":"a#b"}`},
	{"POST", "/sunshine/pair", `{"pin":"12a4"}`},
	{"POST", "/sunshine/pair", `{"pin":"1234","name":"two\nlines"}`},
	{"POST", "/sunshine/pair", `{"pin":"1234","pairing_id":"xyz"}`},
	{"POST", "/sunshine/pair", `{"pin":"1234","kind":"auto"}`},
	{"POST", "/sunshine/pair", `{"pin":"1234","kind":"unknown"}`},
	{"POST", "/sunshine/pair", `{"pin":"12a4","kind":"auto"}`},
	{"POST", "/sunshine/pair", `{"pin":"1234","kind":"tv","pairing_id":"xyz"}`},
	{"POST", "/sunshine/pair", `{"pin":"1234","kind":5}`},
	{"DELETE", "/sunshine/clients/bad_uuid", ``},
	{"POST", "/display/modes", `{"mode":"100x100@60"}`},
	{"POST", "/display/modes", `{"mode":"1920x1080@300"}`},
	{"POST", "/display/modes", `{"mode":"4096x2160@60"}`},
	{"POST", "/display/modes", `{"mode":"wide"}`},
	{"DELETE", "/display/modes/wide", ``},
	{"PUT", "/display/settings", `{"ui_scaling":"yes"}`},
	{"PUT", "/display/screens/nope", `{"kind":"auto"}`},
	{"PUT", "/display/screens/nope", `{}`},
	{"PUT", "/display/screens/nope", ``},
	{"PUT", "/display/screens/nope", `{"kind":"unknown"}`},
	{"PUT", "/display/screens/nope", `{"kind":"TV"}`},
	{"PUT", "/display/screens/nope", `{"kind":""}`},
	{"PUT", "/display/screens/nope", `{"size":3}`},
	{"PUT", "/display/screens/nope", `{"size":0.39}`},
	{"PUT", "/display/screens/nope", `{"kind":"tv","size":0.3}`},
	{"PUT", "/display/screens/nope", `{"kind":"auto","size":2.6,"steam_auto":true}`},
	{"PUT", "/display/screens/nope", `{"size":"big"}`},
	{"PUT", "/display/screens/nope", `{"steam_auto":1}`},
	{"DELETE", "/display/screens/nope", ``},
	{"POST", "/display/hint", `{}`},
	{"POST", "/display/hint", ``},
	{"POST", "/display/hint", `{"w":-1,"h":800,"dpr":2,"touch":0}`},
	{"POST", "/display/hint", `{"w":1280,"h":20001,"dpr":2}`},
	{"POST", "/display/hint", `{"w":1280,"h":800}`},
	{"POST", "/display/hint", `{"w":1280,"h":800,"dpr":8.5}`},
	{"POST", "/display/hint", `{"w":0,"h":800,"dpr":0,"touch":-1}`},
	{"POST", "/display/hint", `{"w":1280,"h":800,"dpr":2,"touch":21}`},
	{"POST", "/display/hint", `{"w":1280,"h":800,"dpr":2,"touch":-1}`},
	{"POST", "/display/hint", `{"w":1.5,"h":800,"dpr":2}`},
	{"POST", "/display/hint", `{"w":1280,"h":800,"dpr":"2"}`},
	{"POST", "/update/stage", `{"version":"-1"}`},
	{"PUT", "/update/settings", `{"channel":"a/b"}`},
	{"PUT", "/update/settings", `{"auto":"always"}`},
	{"POST", "/storage/libraries", `{"uuid":"-x"}`},
	{"DELETE", "/storage/libraries/-x", ``},
	{"POST", "/extensions/nope", `{}`},
	{"POST", "/extensions/nope", ``},
	{"POST", "/extensions/Not_An_ID", `{"password":"x"}`},
	{"POST", "/extensions/nope", `{"options":`},
	{"POST", "/extensions/nope", `{"options":[]}`},
	{"DELETE", "/extensions/nope", ``},
	{"DELETE", "/extensions/nope?purge=2", ``},
	{"PUT", "/extensions/nope/settings", `{"settings":{"overdrive":true}}`},
	{"PUT", "/extensions/nope/settings", `{"settings":`},
	{"POST", "/extensions/nope/actions/copy-profiles", `{}`},
	{"POST", "/extensions/nope/actions/copy-profiles", `{"args":[1]}`},
	{"POST", "/extensions/nope/retry", ``},
}

var parityInstaller = []parityCase{
	{"POST", "/install", `{"disk":""}`},
	{"POST", "/install", `{"disk":"sdz","mode":"wipe"}`},
	{"POST", "/install", `{"disk":"sdz","hostname":"Vapor_1"}`},
	{"POST", "/install", `{"disk":"sdz","hostname":"localhost"}`},
	{"POST", "/install", `{"disk":"sdz","password":"short"}`},
	{"POST", "/install", `{"disk":"sdz","timezone":"../etc"}`},
	{"POST", "/install", `{"disk":"sdz","libraries":["bad uuid"]}`},
}

// TestValidationParity sends each parity case to the real handlers and to
// the fake, both behind a real api.Server and signed in, and wants the
// same status and the same message.
func TestValidationParity(t *testing.T) {
	strictOnly(t)
	t.Run("client", testClientValidationParity)
	dir := t.TempDir()
	redirectConfig(t, dir)
	if err := os.MkdirAll(config.RunDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFileAtomic(config.HostnamePath, []byte("vapor\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetAdminPassword("", devPassword); err != nil {
		t.Fatal(err)
	}
	fx := testFixtures(t)
	cfg := config.Defaults()

	realOS := api.New(api.Options{})
	system.NewService(cfg).Routes(realOS)
	display.NewManager(cfg).Routes(realOS)
	update.NewService(cfg).Routes(realOS)
	sunshine.NewService(cfg).Routes(realOS)
	storage.NewService(cfg).Routes(realOS)
	power.NewService(cfg).Routes(realOS)
	extensions.NewService(cfg).Routes(realOS)
	realISO := api.New(api.Options{Installer: true})
	realISO.SetSetupCode(devSetupCode)
	install.NewService(cfg).Routes(realISO)

	fake := func(installer bool) *api.Server {
		name := defaultPreset
		if installer {
			name = "installer-code"
		}
		p := fx.presets[name]
		docs, err := fx.docs(p, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		srv := api.New(api.Options{Installer: installer})
		if installer {
			srv.SetSetupCode(devSetupCode)
		}
		newDevFake(nil, nil, p, docs, installer).register(srv)
		return srv
	}
	fakeOS, fakeISO := fake(false), fake(true)

	type client struct {
		h            http.Handler
		cookie, csrf string
	}
	send := func(c client, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, api.Prefix+path, strings.NewReader(body))
		r.RemoteAddr, r.Host = "127.0.0.1:40000", "127.0.0.1"
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if c.cookie != "" {
			r.Header.Set("Cookie", c.cookie)
			r.Header.Set("X-VOS-CSRF", c.csrf)
		} else {
			r.Header.Set("X-VOS-Setup", devSetupCode)
		}
		w := httptest.NewRecorder()
		c.h.ServeHTTP(w, r)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	signIn := func(srv *api.Server) client {
		c := client{h: srv.Handler()}
		r := httptest.NewRequest("POST", api.Prefix+"/auth/login", strings.NewReader(`{"password":"`+devPassword+`"}`))
		r.RemoteAddr, r.Host = "127.0.0.1:40000", "127.0.0.1"
		w := httptest.NewRecorder()
		c.h.ServeHTTP(w, r)
		var res struct {
			CSRF string `json:"csrf"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
			t.Fatalf("signing in: %d %s", w.Code, w.Body)
		}
		for _, ck := range w.Result().Cookies() {
			c.cookie = ck.Name + "=" + ck.Value
		}
		c.csrf = res.CSRF
		return c
	}
	pairs := []struct {
		real, fake client
		cases      []parityCase
	}{
		{signIn(realOS), signIn(fakeOS), parityOS},
		{client{h: realISO.Handler()}, client{h: fakeISO.Handler()}, parityInstaller},
	}
	for _, pr := range pairs {
		for _, pc := range pr.cases {
			rs, rb := send(pr.real, pc.method, pc.path, pc.body)
			fs, fb := send(pr.fake, pc.method, pc.path, pc.body)
			if rs < 400 {
				t.Errorf("%s %s %s: the real handler accepted it (%d %s); a parity case must be refused before anything happens", pc.method, pc.path, pc.body, rs, rb)
				continue
			}
			if rs != fs || rb != fb {
				t.Errorf("%s %s %s:\n  real %d %s\n  fake %d %s", pc.method, pc.path, pc.body, rs, rb, fs, fb)
			}
		}
	}
}

// testClientValidationParity feeds jstest/testdata/validate-vectors.json to
// the client's rules (static/js/validate.js, under Node) and to the server's
// own validators: the client must refuse exactly what the server refuses
// (T6), except the CVT limits a mode check leaves to the server.
func testClientValidationParity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	raw, err := os.ReadFile(filepath.Join("jstest", "testdata", "validate-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Hostname, Password, Channel, AudioSink []struct {
			V      string
			Repeat string
			Times  int
			OK     bool
		}
		Mode []struct {
			W, H, Hz int
			OK, CVT  bool
		}
		Bitrate []struct {
			V  int
			OK bool
		} `json:"bitrate_mbps"`
	}
	var sink struct {
		AudioSink []struct {
			V  string
			OK bool
		} `json:"audio_sink"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &sink); err != nil {
		t.Fatal(err)
	}
	script := `import { readFileSync } from 'node:fs';
import * as V from './static/js/validate.js';
const d = JSON.parse(readFileSync('./jstest/testdata/validate-vectors.json', 'utf8'));
const val = (c) => (c.repeat ? c.repeat.repeat(c.times) : c.v);
console.log(JSON.stringify({
  hostname: d.hostname.map((c) => V.hostnameError(c.v) === ''),
  password: d.password.map((c) => V.passwordError(val(c)) === ''),
  mode: d.mode.map((c) => V.modeError(c.w, c.h, c.hz) === ''),
  channel: d.channel.map((c) => V.channelError(c.v) === ''),
  sink: d.audio_sink.map((c) => V.audioSinkError(c.v) === ''),
  bitrate: d.bitrate_mbps.map((c) => V.bitrateError(c.v) === ''),
}));`
	out, err := exec.Command(node, "--input-type=module", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var js struct{ Hostname, Password, Mode, Channel, Sink, Bitrate []bool }
	if err := json.Unmarshal(out, &js); err != nil {
		t.Fatalf("node said %s: %v", out, err)
	}
	check := func(what string, i int, client, server, want bool) {
		t.Helper()
		if client != server || server != want {
			t.Errorf("%s #%d: client %v, server %v, vectors say %v", what, i, client, server, want)
		}
	}
	for i, c := range v.Hostname {
		check("hostname "+strconv.Quote(c.V), i, js.Hostname[i], system.ValidateHostname(c.V) == nil, c.OK)
	}
	for i, c := range v.Password {
		pw := c.V
		if c.Repeat != "" {
			pw = strings.Repeat(c.Repeat, c.Times)
		}
		check("password", i, js.Password[i], auth.ValidatePassword(pw) == nil, c.OK)
	}
	for i, c := range v.Mode {
		server := edid.Check(edid.Mode{W: c.W, H: c.H, Refresh: c.Hz}) == nil
		if c.CVT {
			if !js.Mode[i] || server {
				t.Errorf("mode #%d %dx%d@%d: the client should pass it and the server's CVT check refuse it (client %v, server %v)", i, c.W, c.H, c.Hz, js.Mode[i], server)
			}
			continue
		}
		check(fmt.Sprintf("mode %dx%d@%d", c.W, c.H, c.Hz), i, js.Mode[i], server, c.OK)
	}
	for i, c := range v.Channel {
		check("channel "+strconv.Quote(c.V), i, js.Channel[i], update.ValidChannel(c.V), c.OK)
	}
	for i, c := range sink.AudioSink {
		s := sunshine.DefaultSettings()
		s.AudioSink = c.V
		check("audio_sink "+strconv.Quote(c.V), i, js.Sink[i], s.Validate() == nil, c.OK)
	}
	for i, c := range v.Bitrate {
		s := sunshine.DefaultSettings()
		s.BitrateKbpsMax = c.V * 1000
		check(fmt.Sprintf("bitrate %d Mbps", c.V), i, js.Bitrate[i], s.Validate() == nil, c.OK)
	}
}
