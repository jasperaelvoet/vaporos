package display

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/drm"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

func call(t *testing.T, h http.HandlerFunc, method, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/x", strings.NewReader(body))
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestRoutesRegister(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	srv := api.New(api.Options{})
	m.Routes(srv) // must not panic on duplicate or malformed patterns
}

func TestGetDisplay(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	ctx := context.Background()
	m.init(ctx)
	m.reconcile(ctx, false)
	// The fresh gamescope gets composite_force asynchronously.
	waitFor(t, func() bool { on, _ := h.compositeState(); return on })
	h.modes = []edid.Mode{{W: 1920, H: 1080, Refresh: 60}, {W: 3840, H: 2160, Refresh: 60}, {W: 1920, H: 1080, Refresh: 120}}
	w, _ := call(t, m.handleGet, http.MethodGet, "")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var d Info
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Profile != "amd" || d.VirtualConnector != "DP-1" || d.State != StateGaming || !d.HDR || d.RebootNeeded {
		t.Errorf("display = %+v", d)
	}
	if !slices.Equal(d.Modes, []string{"3840x2160@60", "1920x1080@120", "1920x1080@60"}) {
		t.Errorf("modes = %v", d.Modes)
	}
	if d.Current == nil || *d.Current != "1920x1080@60" {
		t.Errorf("current = %v", d.Current)
	}
	// No monitor: nothing is physical (the forced DP-1 reads connected but
	// is the virtual display), and both free ports can host the virtual one.
	var names []string
	for _, c := range d.Connectors {
		names = append(names, c.Name)
		if c.Physical {
			t.Errorf("physical flag wrong for %+v", c)
		}
	}
	if !slices.Equal(names, []string{"DP-1", "DP-2", "HDMI-A-1"}) {
		t.Errorf("connectors = %v", names)
	}
	if !slices.Equal(d.AvailableConnectors, []string{"DP-2", "HDMI-A-1"}) {
		t.Errorf("available = %v", d.AvailableConnectors)
	}
	if d.Learned == nil || d.Connectors == nil {
		t.Error("lists must be [] not null")
	}
	if d.Planes != 1 {
		t.Errorf("planes while compositing = %d", d.Planes)
	}
	raw := w.Body.String()
	for _, k := range []string{`"profile"`, `"virtual_connector"`, `"connectors"`, `"available_connectors"`, `"modes"`, `"current"`, `"planes"`, `"hdr"`, `"learned"`, `"reboot_needed"`, `"state"`} {
		if !strings.Contains(raw, k) {
			t.Errorf("response lacks %s: %s", k, raw)
		}
	}
	// Steam turned composition off: the game scans out on two planes.
	h.steamWrites("0")
	_, out := call(t, m.handleGet, http.MethodGet, "")
	if out["planes"] != float64(2) {
		t.Errorf("planes after Steam's write = %v", out["planes"])
	}
	// No gamescope: nothing scanned out.
	h.setActive(GamescopeUnit, true, false)
	h.mu.Lock()
	h.scanOK = false
	h.mu.Unlock()
	if d := m.Info(); d.Planes != 0 || d.Current != nil {
		t.Errorf("idle info = planes %d current %v", d.Planes, d.Current)
	}
}

// TestDisplayConnectors: a monitor is physical, the virtual connector
// never is, and only free DP/HDMI ports are offered for the virtual one.
func TestDisplayConnectors(t *testing.T) {
	m, h, _, _ := newTestManager(t, true)
	h.conns = append(h.conns,
		drm.SysConnector{Card: "card1", Name: "eDP-1", Type: "eDP", Status: "disconnected"},
		drm.SysConnector{Card: "card1", Name: "DP-3", Type: "DP", Status: "unknown"})
	m.init(context.Background())
	d := m.Info()
	phys := map[string]bool{}
	for _, c := range d.Connectors {
		phys[c.Name] = c.Physical
	}
	want := map[string]bool{"DP-1": false, "DP-2": false, "HDMI-A-1": true, "eDP-1": false, "DP-3": false}
	if !maps.Equal(phys, want) {
		t.Errorf("physical = %v", phys)
	}
	if !slices.Equal(d.AvailableConnectors, []string{"DP-2"}) {
		t.Errorf("available = %v", d.AvailableConnectors)
	}
	// Each one offered is accepted by PUT /display/settings.
	for _, c := range d.AvailableConnectors {
		m.mu.Lock()
		ok := m.connectorUsableLocked(c)
		m.mu.Unlock()
		if !ok {
			t.Errorf("%s offered but not usable", c)
		}
	}
	// Without a supported GPU there is nothing to offer.
	m.mu.Lock()
	m.gpu.Supported = false
	m.mu.Unlock()
	if d := m.Info(); len(d.AvailableConnectors) != 0 || d.AvailableConnectors == nil {
		t.Errorf("available without GPU = %#v", d.AvailableConnectors)
	}
}

func TestAddMode(t *testing.T) {
	m, h, _, _ := newTestManager(t, false)
	h.applyErr = drm.ErrNoOverride // the kernel offers new modes after a reboot only
	m.init(context.Background())
	for body, code := range map[string]int{
		`{"mode":"garbage"}`:        400,
		`{"mode":"4096x2160@60"}`:   400,
		`{"mode":"3840x2160@144"}`:  400,
		`not json`:                  400,
		`{"mode":"2560x1080@100"}`:  200,
		` {"mode":"2560x1080@100"}`: 200, // idempotent
	} {
		w, out := call(t, m.handleAddMode, http.MethodPost, body)
		if w.Code != code {
			t.Errorf("%s: status %d (%v)", body, w.Code, out)
		}
		if code == 200 && out["reboot_needed"] != true {
			t.Errorf("%s: %v", body, out)
		}
		if code == 400 && out["error"] == nil {
			t.Errorf("%s: no error message", body)
		}
	}
	if !slices.Equal(m.cfg.Display.ExtraModes, []string{"2560x1080@100"}) {
		t.Errorf("extra_modes = %v", m.cfg.Display.ExtraModes)
	}
	saved, _ := config.Load()
	if !slices.Contains(saved.Display.ExtraModes, "2560x1080@100") {
		t.Error("config not saved")
	}
	b, err := os.ReadFile(config.LearnedEDIDPath())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := edid.Decode(b); err != nil || !info.Has(edid.Mode{W: 2560, H: 1080, Refresh: 100}) {
		t.Errorf("learned EDID lacks the mode: %v", err)
	}
	if l := m.learnedModes(); !slices.Equal(l, []string{"2560x1080@100"}) {
		t.Errorf("learned = %v", l)
	}
}

func TestGetDisplayAddedAndDevices(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	m.init(context.Background())
	if d := m.Info(); d.Added == nil || d.Devices == nil || len(d.Added)+len(d.Devices) != 0 {
		t.Errorf("fresh added %#v devices %#v", d.Added, d.Devices)
	}
	m.cfg.Mutate(func(c *config.Config) {
		c.Display.ExtraModes = []string{"2560x1080@100", "1920x1080@60", "junk", "3200x1800@90"}
	})
	mustWrite(t, config.ClientsPath(), `{
		"Deck":{"w":1280,"h":800,"fps":90,"hdr":false,"last_seen":"2026-09-28T12:00:00Z"},
		"Pixel":{"w":2400,"h":1080,"fps":120,"hdr":false,"last_seen":"2026-09-29T12:00:00Z"},
		"TV":{"w":3840,"h":2160,"fps":60,"hdr":true,"last_seen":"2026-09-27T12:00:00Z"}}`)
	w, _ := call(t, m.handleGet, http.MethodGet, "")
	var d Info
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Added, []string{"3200x1800@90", "2560x1080@100"}) {
		t.Errorf("added = %v", d.Added)
	}
	var names []string
	for _, dev := range d.Devices {
		names = append(names, dev.Name)
	}
	if !slices.Equal(names, []string{"Pixel", "Deck", "TV"}) {
		t.Errorf("devices = %+v", d.Devices)
	}
	if p := d.Devices[0]; p.Mode != "2400x1080@120" || p.HDR || !p.LastSeen.Equal(time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Pixel = %+v", p)
	}
	if tv := d.Devices[2]; tv.Mode != "3840x2160@60" || !tv.HDR {
		t.Errorf("TV = %+v", tv)
	}
	// learned stays the union of both, beyond the catalogue.
	if !slices.Equal(d.Learned, []string{"3200x1800@90", "2560x1080@100", "2400x1080@120"}) {
		t.Errorf("learned = %v", d.Learned)
	}
	for _, k := range []string{`"added"`, `"devices"`, `"name":"Deck"`, `"mode":"1280x800@90"`, `"last_seen"`} {
		if !strings.Contains(w.Body.String(), k) {
			t.Errorf("response lacks %s: %s", k, w.Body.String())
		}
	}
}

func removeMode(t *testing.T, m *Manager, mode string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/display/modes/x", nil)
	r.SetPathValue("mode", mode)
	w := httptest.NewRecorder()
	m.handleRemoveMode(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func learnedEDIDHas(t *testing.T, mode edid.Mode) bool {
	t.Helper()
	b, err := os.ReadFile(config.LearnedEDIDPath())
	if err != nil {
		t.Fatal(err)
	}
	info, err := edid.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return info.Has(mode)
}

func TestRemoveMode(t *testing.T) {
	m, h, _, hub := newTestManager(t, false)
	h.applyErr = drm.ErrNoOverride
	m.init(context.Background())
	evs, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	if w, _ := call(t, m.handleAddMode, http.MethodPost, `{"mode":"2560x1080@100"}`); w.Code != 200 {
		t.Fatalf("add: %d", w.Code)
	}
	if !learnedEDIDHas(t, md(2560, 1080, 100)) {
		t.Fatal("added mode missing from the EDID")
	}
	w, out := removeMode(t, m, "2560x1080@100")
	if w.Code != 200 || out["reboot_needed"] != true {
		t.Fatalf("remove added: %d %v", w.Code, out)
	}
	if len(m.cfg.Display.ExtraModes) != 0 {
		t.Errorf("extra_modes = %v", m.cfg.Display.ExtraModes)
	}
	if saved, _ := config.Load(); len(saved.Display.ExtraModes) != 0 {
		t.Errorf("saved extra_modes = %v", saved.Display.ExtraModes)
	}
	if learnedEDIDHas(t, md(2560, 1080, 100)) {
		t.Error("removed mode still in the EDID")
	}

	// A learned mode goes with every client that asked for it.
	m.learn("Pixel", md(2400, 1080, 120), false, false)
	m.learn("Pixel 2", md(2400, 1080, 120), false, false)
	m.learn("Deck", md(1280, 800, 90), false, true)
	if !learnedEDIDHas(t, md(2400, 1080, 120)) {
		t.Fatal("learned mode missing from the EDID")
	}
	drain(evs, "")
	if w, out := removeMode(t, m, "2400x1080@120"); w.Code != 200 || out["reboot_needed"] != true {
		t.Fatalf("remove learned: %d %v", w.Code, out)
	}
	clients, err := LoadClients(config.ClientsPath())
	if err != nil || len(clients) != 1 || clients["Deck 1280x800@90"].Name != "Deck" {
		t.Errorf("clients.json = %+v %v", clients, err)
	}
	if learnedEDIDHas(t, md(2400, 1080, 120)) {
		t.Error("forgotten mode still in the EDID")
	}
	if drain(evs, "display.changed") == nil {
		t.Error("no display.changed after a removal")
	}
	if l := m.learnedModes(); len(l) != 0 {
		t.Errorf("learned = %v", l)
	}

	for mode, code := range map[string]int{"garbage": 400, "3440x1080@100": 404, "2400x1080@120": 404, "": 400} {
		w, out := removeMode(t, m, mode)
		if w.Code != code || out["error"] == nil {
			t.Errorf("%q: %d %v", mode, w.Code, out)
		}
	}
}

// TestModeEditsSerialise: learning clients while a mode is removed loses
// none of them (both rewrite clients.json).
func TestModeEditsSerialise(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	m.init(context.Background())
	m.learn("Old", md(2400, 1080, 120), false, true)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.learn(fmt.Sprintf("client %d", i), md(1920, 1080, 60), false, true)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := m.removeMode(md(2400, 1080, 120)); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	clients, err := LoadClients(config.ClientsPath())
	if err != nil || len(clients) != 12 {
		t.Errorf("clients.json holds %d entries (%v): %v", len(clients), err, slices.Sorted(maps.Keys(clients)))
	}
}

func TestSettings(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	m.init(context.Background())
	w, _ := call(t, m.handleSettings, http.MethodPut, `{"hdr":false}`)
	if w.Code != 200 || m.cfg.Display.HDR {
		t.Fatalf("hdr off: %d %v", w.Code, m.cfg.Display.HDR)
	}
	if saved, _ := config.Load(); saved.Display.HDR {
		t.Error("hdr not saved")
	}
	for _, bad := range []string{`{"virtual_connector":"eDP-1"}`, `{"virtual_connector":"DP-9"}`, `{"virtual_connector":"Writeback-1"}`, `[`} {
		if w, _ := call(t, m.handleSettings, http.MethodPut, bad); w.Code != 400 {
			t.Errorf("%s: status %d", bad, w.Code)
		}
	}
	if m.cfg.Display.VirtualConnector != "DP-1" {
		t.Error("a rejected change took effect")
	}
	// Same connector: nothing to rewrite.
	if w, _ := call(t, m.handleSettings, http.MethodPut, `{"hdr":true,"virtual_connector":"DP-1"}`); w.Code != 200 || !m.cfg.Display.HDR {
		t.Errorf("no-op change: %d", w.Code)
	}
	m.gpu.Supported = false
	if w, _ := call(t, m.handleSettings, http.MethodPut, `{"virtual_connector":"DP-2"}`); w.Code != 409 {
		t.Errorf("no GPU: status %d", w.Code)
	}
}

func TestSetVirtualWritesCmdline(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	m.init(context.Background())
	mustWrite(t, config.MachineCmdlinePath(), MachineCmdlineFor("DP-1")+"\n")
	w, out := call(t, m.handleSettings, http.MethodPut, `{"virtual_connector":"DP-2"}`)
	// boot.SetMachineCmdline belongs to the boot package; until it is
	// implemented the handler reports its error, after having saved config.
	if w.Code == 200 {
		b, _ := os.ReadFile(config.MachineCmdlinePath())
		if !strings.Contains(string(b), "video=DP-2:e") {
			t.Errorf("machine cmdline = %q", b)
		}
	} else if w.Code != 500 || out["error"] == nil {
		t.Errorf("status %d %v", w.Code, out)
	}
	if m.cfg.Display.VirtualConnector != "DP-2" || !m.rebootNeededNow() {
		t.Errorf("virtual = %q", m.cfg.Display.VirtualConnector)
	}
}

func TestGetWelcome(t *testing.T) {
	m, _, _, _ := newTestManager(t, true)
	w, _ := call(t, m.handleWelcome, http.MethodGet, "")
	var st welcome.State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.URL != "http://vapor.local" || st.Status != "Ready to stream" || st.Tone != "ready" {
		t.Errorf("welcome = %+v %v", st, err)
	}
	// Any local process (a game) may call it: never the setup code.
	m.SetSetupCode("ABCD-EFGH")
	w, _ = call(t, m.handleWelcome, http.MethodGet, "")
	if strings.Contains(w.Body.String(), "ABCD") {
		t.Errorf("GET /welcome leaks the setup code: %s", w.Body.String())
	}
	st = welcome.State{}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.QR != "http://192.168.1.50/" || st.Status != "Almost ready" {
		t.Errorf("welcome with a code = %+v %v", st, err)
	}
}

func TestCLIEdid(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "sub", "vaporos.bin")
	clients := filepath.Join(dir, "clients.json")
	os.WriteFile(clients, []byte(`{"Pixel":{"w":2400,"h":1080,"fps":120,"hdr":false,"last_seen":"2026-09-29T12:00:00Z"}}`), 0o644)
	var stdout, stderr bytes.Buffer
	if code := cliEdid([]string{"generate", "--out", out, "--modes-from", clients, "--extra", "2560x1080@75,4096x2160@60", "--extra", "1280x720@240"}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate: %d %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "skipped 4096x2160@60") {
		t.Errorf("stderr = %q", stderr.String())
	}
	stdout.Reset()
	if code := cliEdid([]string{"decode", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("decode: %d %s", code, stderr.String())
	}
	for _, m := range []string{"2400x1080@120", "2560x1080@75", "1280x720@240", "3840x2160@60", "preferred"} {
		if !strings.Contains(stdout.String(), m) {
			t.Errorf("decode output lacks %s:\n%s", m, stdout.String())
		}
	}
	for _, bad := range [][]string{nil, {"generate"}, {"generate", "--out", out, "--extra", "junk"}, {"decode"}, {"decode", clients}, {"frob"}} {
		stderr.Reset()
		if code := cliEdid(bad, &stdout, &stderr); code == 0 {
			t.Errorf("%v succeeded", bad)
		}
	}
}

// TestLearnKeepsEveryDevicesMode: devices that Sunshine all reports as
// "Moonlight" each keep their mode in the learned EDID.
func TestLearnKeepsEveryDevicesMode(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	m.init(context.Background())
	m.learn("Moonlight", md(2560, 1664, 60), false, false)
	m.learn("Moonlight", md(2532, 1170, 60), false, false)
	for _, want := range []edid.Mode{md(2560, 1664, 60), md(2532, 1170, 60)} {
		if !learnedEDIDHas(t, want) {
			t.Errorf("%s missing from the learned EDID", want)
		}
	}
	if l := m.learnedModes(); len(l) != 2 {
		t.Errorf("learned = %v", l)
	}
}

// callID calls the handler of a route with an {id}.
func callID(t *testing.T, h http.HandlerFunc, method, id, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/x", strings.NewReader(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// TestGetDisplayScaling: GET /display's scaling fields, screens never null.
func TestGetDisplayScaling(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
	m.init(context.Background())
	w, _ := call(t, m.handleGet, http.MethodGet, "")
	for _, k := range []string{`"ui_scaling":true`, `"steam_ui":"idle"`, `"screens":[]`} {
		if !strings.Contains(w.Body.String(), k) {
			t.Errorf("GET /display lacks %s: %s", k, w.Body)
		}
	}
	if w, _ := call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":false}`); w.Code != 200 {
		t.Fatalf("PUT = %d", w.Code)
	}
	if d := m.Info(); d.UIScaling || d.SteamUI != steamUIOff {
		t.Errorf("off: %v %q", d.UIScaling, d.SteamUI)
	}
	if w, _ := call(t, m.handleSettings, http.MethodPut, `{"ui_scaling":"no"}`); w.Code != 400 {
		t.Errorf("bad ui_scaling: %d", w.Code)
	}
}

func TestScreenRoutes(t *testing.T) {
	m, h, _, hub := newScaleTest(t)
	ctx := context.Background()
	m.Begin(ctx, iPhone) // a screen seen before, not streaming now
	m.End(ctx)
	id := ScreenID("iPhone")
	evs, cancel := hub.Subscribe()
	defer cancel()
	put := func(id, body string) (*httptest.ResponseRecorder, map[string]any) {
		return callID(t, m.handleScreenPut, http.MethodPut, id, body)
	}
	const badKind, badSize = "kind must be auto, phone, handheld, tablet, laptop, monitor or tv", "size must be between 0.4 and 2.5"
	for _, c := range []struct {
		id, body string
		code     int
		msg      string
	}{
		{id, `{"kind":"watch"}`, 400, badKind},
		{id, `{"kind":"unknown"}`, 400, badKind},
		{id, `{"size":3}`, 400, badSize},
		{id, `{"size":0.3}`, 400, badSize},
		{"000000000000", `{"size":1.2}`, 404, "no such screen"},
		{"000000000000", `{"kind":"watch"}`, 400, badKind}, // the body before the id
		{id, `{"size":"big"}`, 400, "bad request body"},
	} {
		w, out := put(c.id, c.body)
		if msg, _ := out["error"].(string); w.Code != c.code || !strings.HasPrefix(msg, c.msg) {
			t.Errorf("%s %s = %d %v", c.id, c.body, w.Code, out)
		}
	}
	if sc, _ := storedScreen(t, id); sc.Kind != "" || sc.Size != 1 {
		t.Fatalf("a refused PUT changed the screen: %+v", sc)
	}

	for _, c := range []struct {
		body, kind, from string
		size             float64
	}{
		{`{"size":1.2}`, "phone", "you", 1.2},                  // a size alone pins the kind in effect
		{`{"kind":"tablet"}`, "tablet", "you", 1},              // a new kind resets the size
		{`{"kind":"laptop","size":0.8}`, "laptop", "you", 0.8}, // unless the body has one
		{`{"kind":"laptop"}`, "laptop", "you", 0.8},            // the same kind keeps it
		{`{"kind":"auto"}`, "phone", "name", 1},                // automatic again
	} {
		w, out := put(id, c.body)
		if w.Code != 200 || out["id"] != id || out["kind"] != c.kind || out["kind_from"] != c.from || out["size"] != c.size || out["savable"] != true {
			t.Errorf("PUT %s = %d %v", c.body, w.Code, out)
		}
		if drain(evs, "display.changed") == nil {
			t.Errorf("PUT %s: no display.changed", c.body)
		}
	}

	// While it streams: the session's kind, applied at once.
	m.Begin(ctx, iPhone)
	m.scaleRound(ctx)
	gen := wantGen(m)
	w, out := put(id, `{"size":0.5}`)
	if w.Code != 200 || out["kind"] != "phone" || out["kind_from"] != "you" || out["size"] != 0.5 || out["mode"] != "2796x1290@120" {
		t.Errorf("PUT while streaming = %d %v", w.Code, out)
	}
	if wantGen(m) == gen {
		t.Error("PUT while streaming did not start a new generation")
	}
	m.scaleRound(ctx)
	if want := ScaleFor(KindPhone, iPhoneM, 0.5, Panel{2796, 1290}); h.ui.snapshot().current != want {
		t.Errorf("Steam = %.2f, want %.2f", h.ui.snapshot().current, want)
	}

	// DELETE: an unknown id; the streaming screen starts afresh.
	if w, out := callID(t, m.handleScreenDelete, http.MethodDelete, "000000000000", ""); w.Code != 404 || out["error"] != "no such screen" {
		t.Errorf("DELETE unknown = %d %v", w.Code, out)
	}
	if w, _ := callID(t, m.handleScreenDelete, http.MethodDelete, id, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Errorf("DELETE = %d %s", w.Code, w.Body)
	}
	if sc, ok := storedScreen(t, id); !ok || sc.Kind != "" || sc.Size != 1 || len(sc.Modes) != 1 {
		t.Errorf("after DELETE while streaming: %+v %v", sc, ok)
	}
	m.scaleRound(ctx)
	if v := m.Info().Screens[0]; v.KindFrom != FromName || v.Size != 1 || h.ui.snapshot().current != 2.7 {
		t.Errorf("after DELETE: %+v, Steam %.2f", v, h.ui.snapshot().current)
	}
	// Not streaming: gone.
	m.End(ctx)
	callID(t, m.handleScreenDelete, http.MethodDelete, id, "")
	if _, ok := storedScreen(t, id); ok {
		t.Error("DELETE kept the screen")
	}
	if w, _ := call(t, m.handleGet, http.MethodGet, ""); !strings.Contains(w.Body.String(), `"screens":[]`) {
		t.Errorf("screens after DELETE: %s", w.Body)
	}
}

func TestHintRoute(t *testing.T) {
	m, h, _, _ := newScaleTest(t)
	ctx := context.Background()
	post := func(remote, ua, body string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/display/hint", strings.NewReader(body))
		r.RemoteAddr = remote
		r.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		m.handleHint(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}
	const ua = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148"
	const good = `{"w":430,"h":932,"dpr":3,"touch":5}`
	from := phoneAddr.String() + ":50123"
	const wh, dpr, touch = "w and h must be between 1 and 20000", "dpr must be between 0.5 and 8", "touch must be between 0 and 20"
	for _, c := range []struct{ body, msg string }{
		{`{"w":0,"h":932,"dpr":3,"touch":5}`, wh},
		{`{"w":430,"h":20001,"dpr":3,"touch":5}`, wh},
		{`{"w":-1,"h":0,"dpr":0,"touch":-1}`, wh}, // checked in order
		{`{"h":932,"dpr":3,"touch":5}`, wh},
		{`{"w":430,"h":932,"dpr":0,"touch":5}`, dpr},
		{`{"w":430,"h":932,"dpr":8.5,"touch":5}`, dpr},
		{`{"w":430,"h":932,"dpr":3,"touch":21}`, touch},
		{`{"w":430,"h":932,"dpr":3,"touch":-1}`, touch},
		{`{"w":430.5,"h":932,"dpr":3,"touch":5}`, "bad request body"},
	} {
		w, out := post(from, ua, c.body)
		if msg, _ := out["error"].(string); w.Code != 400 || !strings.HasPrefix(msg, c.msg) {
			t.Errorf("%s = %d %v", c.body, w.Code, out)
		}
	}
	// Nothing kept from loopback, or from IPv6 without a MAC.
	for _, remote := range []string{"127.0.0.1:5000", "[::1]:5000", "[fd00::5]:5000"} {
		if w, _ := post(remote, ua, good); w.Code != 200 || len(m.screensSnapshot().Hints) != 0 {
			t.Errorf("%s: %d, hints %v", remote, w.Code, m.screensSnapshot().Hints)
		}
	}
	// The phone's browser: kept under its MAC, with the kind its User-Agent
	// gives and nothing of the User-Agent itself.
	if w, out := post(from, ua, good); w.Code != 200 || len(out) != 0 {
		t.Fatalf("POST = %d %v", w.Code, out)
	}
	if hint := m.screensSnapshot().Hints[phoneMAC]; hint.Kind != KindPhone || hint.W != 430 || hint.H != 932 || hint.DPR != 3 || hint.Touch != 5 || hint.IP != phoneAddr.String() || hint.You {
		t.Errorf("hint = %+v", hint)
	}
	if b, _ := os.ReadFile(config.ScreensPath()); strings.Contains(string(b), "iPhone OS") || !strings.Contains(string(b), phoneMAC) {
		t.Errorf("screens.json = %s", b)
	}
	// Over IPv6, the same MAC is the same device.
	v6 := netip.MustParseAddr("fe80::1")
	h.mu.Lock()
	h.macs[v6] = otherMAC
	h.mu.Unlock()
	post("[fe80::1%en0]:5000", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0)", `{"w":1512,"h":982,"dpr":2,"touch":0}`)
	if hint := m.screensSnapshot().Hints[otherMAC]; hint.Kind != KindLaptop || hint.IP != "" {
		t.Errorf("IPv6 hint = %+v", hint)
	}
	// The phone's next session knows it by its browser.
	m.Begin(ctx, session.Request{Op: "begin", Client: "roth", Width: 1920, Height: 1080, FPS: 60})
	if s := m.CurrentSession(); s.Screen == nil || s.Screen.Kind != KindPhone || s.Screen.KindFrom != FromBrowser {
		t.Errorf("screen = %+v", s.Screen)
	}
}
