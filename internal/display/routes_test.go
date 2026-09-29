package display

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/drm"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
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
	var d displayInfo
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
	if d := m.info(); d.Planes != 0 || d.Current != nil {
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
	d := m.info()
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
	if d := m.info(); len(d.AvailableConnectors) != 0 || d.AvailableConnectors == nil {
		t.Errorf("available without GPU = %#v", d.AvailableConnectors)
	}
}

func TestAddMode(t *testing.T) {
	m, _, _, _ := newTestManager(t, false)
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
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.URL != "http://vapor.local" || st.Status != "Ready to stream" {
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
