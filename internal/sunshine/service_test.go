package sunshine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

func writeCreds(t *testing.T, user, pass string) {
	t.Helper()
	if err := config.WriteJSONAtomic(config.SunshineAPIPath(), apiCreds{User: user, Password: pass}, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeState writes Sunshine's state file as `sunshine --creds` leaves it.
func writeState(t *testing.T, user string) {
	t.Helper()
	os.MkdirAll(sunshineDir(), 0o700)
	st := fmt.Sprintf(`{"root":{"uniqueid":"x","named_devices":[]},"username":%q,"salt":"abc","password":"DEF"}`, user)
	if err := os.WriteFile(statePath(), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareFirstBoot(t *testing.T) {
	h := newHarness(t)
	h.s.prepare(context.Background())

	var c apiCreds
	if err := config.ReadJSON(config.SunshineAPIPath(), &c); err != nil {
		t.Fatal(err)
	}
	if c.User != "vosd" || len(c.Password) != 48 {
		t.Errorf("generated credentials = %+v", c)
	}
	if fi, _ := os.Stat(config.SunshineAPIPath()); fi.Mode().Perm() != 0o600 {
		t.Errorf("sunshine-api.json mode %v", fi.Mode().Perm())
	}
	want := [][]string{{"sunshine", confPath(), "--creds", "vosd", c.Password}}
	if !reflect.DeepEqual(h.rec.asGamer, want) {
		t.Errorf("asGamer = %q", h.rec.asGamer)
	}
	if calls := h.rec.calls(); !reflect.DeepEqual(calls, []string{"restart " + unitName}) {
		t.Errorf("systemctl = %q", calls)
	}
	for _, p := range []string{confPath(), appsPath()} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v", p, err)
		}
	}

	// Next start: same files, Sunshine knows the user. Nothing to do.
	writeState(t, "vosd")
	h.rec.systemctl, h.rec.asGamer = nil, nil
	h.s.prepare(context.Background())
	if len(h.rec.calls()) != 0 || len(h.rec.asGamer) != 0 {
		t.Errorf("unchanged start did work: %q %q", h.rec.calls(), h.rec.asGamer)
	}
}

func TestPrepareDefersRestartDuringStream(t *testing.T) {
	h := newHarness(t)
	writeCreds(t, h.f.user, h.f.pass)
	writeState(t, h.f.user)
	h.f.setBusy(true)
	h.s.prepare(context.Background()) // conf and apps are new
	if len(h.rec.calls()) != 0 || !h.s.pendingRestart {
		t.Fatalf("restarted during a stream: %q (pending %v)", h.rec.calls(), h.s.pendingRestart)
	}
	h.s.poll(context.Background())
	if len(h.rec.calls()) != 0 {
		t.Fatal("restarted while still streaming")
	}
	h.f.setBusy(false)
	h.s.poll(context.Background())
	if calls := h.rec.calls(); !reflect.DeepEqual(calls, []string{"restart " + unitName}) || h.s.pendingRestart {
		t.Errorf("after the stream: %q", calls)
	}
}

func TestPollRepairsRejectedCredentials(t *testing.T) {
	h := newHarness(t)
	h.f.pass = "reset-by-someone"
	h.s.poll(context.Background())
	if len(h.rec.asGamer) != 1 || h.rec.asGamer[0][2] != "--creds" {
		t.Fatalf("credentials not re-applied: %q", h.rec.asGamer)
	}
	if !slices.Contains(h.rec.calls(), "restart "+unitName) {
		t.Errorf("no restart after re-applying: %q", h.rec.calls())
	}
	h.s.poll(context.Background())
	if len(h.rec.asGamer) != 1 {
		t.Error("re-applied again right away")
	}
}

func TestPollAnnouncesNewPairings(t *testing.T) {
	h := newHarness(t)
	id := strings.Repeat("0f", 16)
	h.s.poll(context.Background())
	h.f.pairings = []Pairing{{ID: id, Name: "Steam Deck"}}
	h.s.poll(context.Background())
	h.s.poll(context.Background())
	var pending []string
	for _, e := range h.rec.events {
		if e.Topic == "pairing.pending" {
			pending = append(pending, string(e.Data))
		}
	}
	if !reflect.DeepEqual(pending, []string{`{"name":"Steam Deck"}`}) {
		t.Errorf("pairing.pending events = %q", pending)
	}
	if h.s.version != "2026.928.101500" {
		t.Errorf("version = %q", h.s.version)
	}
}

func TestMaintainPicksUpNewGamesAndConnector(t *testing.T) {
	h := newHarness(t)
	writeCreds(t, h.f.user, h.f.pass)
	writeState(t, h.f.user)
	h.s.prepare(context.Background())
	h.rec.systemctl = nil

	h.s.cfg.Display.VirtualConnector = "HDMI-A-1"
	h.s.nextConf = h.s.now() // due
	h.s.poll(context.Background())
	data, _ := os.ReadFile(confPath())
	if !strings.Contains(string(data), "output_name = HDMI-A-1") || len(h.rec.calls()) != 1 {
		t.Errorf("connector change not applied: %q\n%s", h.rec.calls(), data)
	}
}

func call(t *testing.T, h http.HandlerFunc, method, body string, pathValues ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/sunshine", strings.NewReader(body))
	for i := 0; i+1 < len(pathValues); i += 2 {
		r.SetPathValue(pathValues[i], pathValues[i+1])
	}
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestStatusHandler(t *testing.T) {
	h := newHarness(t)
	h.f.pairings = []Pairing{{ID: strings.Repeat("aa", 16), Name: "iPad", Address: "192.168.1.30"}}
	w, out := call(t, h.s.handleStatus, "GET", "")
	if w.Code != 200 || out["running"] != true || out["streaming"] != false || out["session"] != nil ||
		out["pending_pairing"] != true || out["version"] != "2026.928.101500" {
		t.Errorf("status = %d %v", w.Code, out)
	}
	h.f.setBusy(true)
	h.s.session = &sessionInfo{Client: "iPad", Mode: "2732x2048@60"}
	_, out = call(t, h.s.handleStatus, "GET", "")
	if out["streaming"] != true || out["session"].(map[string]any)["mode"] != "2732x2048@60" {
		t.Errorf("streaming status = %v", out)
	}
}

func TestPairHandler(t *testing.T) {
	h := newHarness(t)
	id := strings.Repeat("ab", 16)
	pair := func(body string) (int, string) {
		w, out := call(t, h.s.handlePair, "POST", body)
		msg, _ := out["error"].(string)
		return w.Code, msg
	}
	if code, _ := pair(`{"pin":"1234"}`); code != http.StatusConflict {
		t.Errorf("nobody waiting: %d", code)
	}
	h.f.pairings = []Pairing{{ID: id, Name: "iPhone"}}
	for _, bad := range []string{`{"pin":"12345"}`, `{"pin":"12a4"}`, `{"pin":"1234","pairing_id":"xyz"}`, `{"pin":"1234","name":"a\nb"}`, `{`} {
		if code, _ := pair(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, msg := pair(`{"pin":"0000"}`); code != http.StatusBadRequest || !strings.Contains(msg, "PIN") {
		t.Errorf("wrong PIN: %d %q", code, msg)
	}
	if code, _ := pair(`{"pin":" 1234 "}`); code != http.StatusOK {
		t.Errorf("pair: %d", code)
	}
	last := h.f.bodies[len(h.f.bodies)-1]
	if last["pairing_id"] != id || last["name"] != "iPhone" || last["pin"] != "1234" {
		t.Errorf("sent %v", last)
	}
	h.f.pairings = append(h.f.pairings, Pairing{ID: strings.Repeat("cd", 16), Name: "iPad"})
	if code, _ := pair(`{"pin":"1234"}`); code != http.StatusConflict {
		t.Errorf("two waiting: %d", code)
	}
	if code, _ := pair(`{"pin":"1234","name":"Living room iPad","pairing_id":"` + strings.Repeat("cd", 16) + `"}`); code != http.StatusOK {
		t.Errorf("chosen pairing: %d", code)
	}
	if last := h.f.bodies[len(h.f.bodies)-1]; last["name"] != "Living room iPad" {
		t.Errorf("name not passed: %v", last)
	}

	h.f.oldAPI = true
	if code, _ := pair(`{"pin":"1234"}`); code != http.StatusOK {
		t.Errorf("old Sunshine: %d", code)
	}
	if last := h.f.bodies[len(h.f.bodies)-1]; last["pairing_id"] != "" || last["name"] != "Moonlight" {
		t.Errorf("old-API body = %v", last)
	}

	h.f.pass = "changed"
	if code, _ := pair(`{"pin":"1234"}`); code != http.StatusServiceUnavailable {
		t.Errorf("rejected credentials: %d", code)
	}
}

func TestClientsHandlers(t *testing.T) {
	h := newHarness(t)
	h.f.clients = []PairedClient{{UUID: "C0FFEE-1", Name: "MacBook", Enabled: true}}
	w, out := call(t, h.s.handleClients, "GET", "")
	list, _ := out["clients"].([]any)
	if w.Code != 200 || len(list) != 1 || list[0].(map[string]any)["uuid"] != "C0FFEE-1" {
		t.Errorf("clients = %d %v", w.Code, out)
	}
	if w, _ := call(t, h.s.handleUnpair, "DELETE", "", "uuid", "../x"); w.Code != http.StatusBadRequest {
		t.Errorf("bad uuid: %d", w.Code)
	}
	if w, _ := call(t, h.s.handleUnpair, "DELETE", "", "uuid", "C0FFEE-1"); w.Code != http.StatusOK {
		t.Errorf("unpair: %d", w.Code)
	}
	if w, _ := call(t, h.s.handleUnpair, "DELETE", "", "uuid", "C0FFEE-1"); w.Code != http.StatusNotFound {
		t.Errorf("unpair unknown: %d", w.Code)
	}
	h.s.infoURL = "http://127.0.0.1:1/serverinfo"
	h.s.client = NewClient("https://127.0.0.1:1", "u", "p", certPath())
	if w, _ := call(t, h.s.handleClients, "GET", ""); w.Code != http.StatusBadGateway {
		t.Errorf("Sunshine down: %d", w.Code)
	}
	h.s.client = nil
	if w, _ := call(t, h.s.handleClients, "GET", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("before setup: %d", w.Code)
	}
}

func TestSettingsHandlers(t *testing.T) {
	h := newHarness(t)
	w, out := call(t, h.s.handleGetSettings, "GET", "")
	if w.Code != 200 || out["encoder"] != "vulkan" || out["gamepad"] != "xone" || out["bitrate_kbps_max"] != float64(0) {
		t.Errorf("GET settings = %v", out)
	}
	w, out = call(t, h.s.handlePutSettings, "PUT", `{"encoder":"vaapi","bitrate_kbps_max":60000}`)
	if w.Code != 200 || out["encoder"] != "vaapi" || out["gamepad"] != "xone" {
		t.Fatalf("PUT = %d %v", w.Code, out)
	}
	vars := parseConf(mustRead(t, confPath()))
	if vars["encoder"] != "vaapi" || vars["max_bitrate"] != "60000" {
		t.Errorf("conf = %v", vars)
	}
	if calls := h.rec.calls(); !reflect.DeepEqual(calls, []string{"restart " + unitName}) {
		t.Errorf("systemctl = %q", calls)
	}
	// The same values again: no rewrite, no restart.
	call(t, h.s.handlePutSettings, "PUT", `{"encoder":"vaapi"}`)
	if len(h.rec.calls()) != 1 {
		t.Errorf("no-op PUT restarted Sunshine: %q", h.rec.calls())
	}
	for _, bad := range []string{`{"encoder":"auto"}`, `{"gamepad":"n64"}`, `{"bitrate_kbps_max":-1}`, `{"audio_sink":"x#y"}`, `[`} {
		if w, _ := call(t, h.s.handlePutSettings, "PUT", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	if h.s.currentSettings().Encoder != "vaapi" {
		t.Error("rejected PUT changed the settings")
	}
	// During a stream the restart waits.
	h.f.setBusy(true)
	call(t, h.s.handlePutSettings, "PUT", `{"gamepad":"ds5"}`)
	if len(h.rec.calls()) != 1 || !h.s.pendingRestart {
		t.Errorf("restarted during a stream: %q", h.rec.calls())
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLogsHandler(t *testing.T) {
	h := newHarness(t)
	var b strings.Builder
	for i := 1; i <= 2500; i++ {
		fmt.Fprintf(&b, "log line %d\n", i)
	}
	h.f.logs = b.String()
	w, _ := call(t, h.s.handleLogs, "GET", "")
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
		!strings.HasPrefix(body, "log line 501\n") || !strings.HasSuffix(body, "log line 2500\n") ||
		strings.Count(body, "\n") != logLines {
		t.Errorf("logs: %d %q…", w.Code, body[:min(40, len(body))])
	}

	// Sunshine down: its log file, then the journal.
	h.s.client = NewClient("https://127.0.0.1:1", "u", "p", certPath())
	os.WriteFile(logPath(), []byte("from the file\n"), 0o600)
	if w, _ := call(t, h.s.handleLogs, "GET", ""); w.Body.String() != "from the file\n" {
		t.Errorf("file fallback = %q", w.Body.String())
	}
	os.Remove(logPath())
	h.s.journalTail = func(context.Context, int) (string, error) { return "from the journal", nil }
	if w, _ := call(t, h.s.handleLogs, "GET", ""); w.Body.String() != "from the journal\n" {
		t.Errorf("journal fallback = %q", w.Body.String())
	}
	h.s.journalTail = func(context.Context, int) (string, error) { return "", errors.New("nope") }
	if w, _ := call(t, h.s.handleLogs, "GET", ""); w.Code != http.StatusBadGateway {
		t.Errorf("no log at all: %d", w.Code)
	}
}

func TestTailLines(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 3, ""},
		{"a", 3, "a\n"},
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc", 3, "a\nb\nc\n"},
		{"a\nb\nc\n\n\n", 1, "c\n"},
	}
	for _, c := range cases {
		if got := tailLines(c.in, c.n); got != c.want {
			t.Errorf("tailLines(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestRestartHandler(t *testing.T) {
	h := newHarness(t)
	if w, _ := call(t, h.s.handleRestart, "POST", ""); w.Code != 200 || !reflect.DeepEqual(h.rec.calls(), []string{"restart " + unitName}) {
		t.Errorf("restart: %d %q", w.Code, h.rec.calls())
	}
	// No user manager: Sunshine restarts itself through its API.
	h.rec.failCtl = errors.New("Failed to connect to bus")
	if w, _ := call(t, h.s.handleRestart, "POST", ""); w.Code != 200 {
		t.Errorf("API fallback: %d", w.Code)
	}
	h.s.client = NewClient("https://127.0.0.1:1", "u", "p", certPath())
	if w, _ := call(t, h.s.handleRestart, "POST", ""); w.Code != http.StatusInternalServerError {
		t.Errorf("no way to restart: %d", w.Code)
	}
}

func TestRunDoesNothingLive(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(config.ProcCmdline, []byte("vos.mode=live\n"), 0o644)
	h.s.Run(context.Background()) // returns at once
	if len(h.rec.calls()) != 0 || len(h.rec.asGamer) != 0 {
		t.Error("live mode touched Sunshine")
	}
	if _, err := os.Stat(confPath()); !os.IsNotExist(err) {
		t.Error("live mode wrote sunshine.conf")
	}
}
