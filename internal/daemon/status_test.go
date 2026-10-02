package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/display"
	"github.com/jasperaelvoet/vaporos/internal/power"
	"github.com/jasperaelvoet/vaporos/internal/sunshine"
	"github.com/jasperaelvoet/vaporos/internal/system"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

type ctxKey struct{}

func stubSources() statusSources {
	return statusSources{
		system: func() system.Info { return system.Info{Hostname: "vapor", IPs: []string{"192.168.1.50"}} },
		sunshine: func(ctx context.Context) sunshine.Summary {
			return sunshine.Summary{Running: ctx.Value(ctxKey{}) == "request", Pairings: []sunshine.Pairing{}}
		},
		stream:  func() *display.Session { return nil },
		display: func() display.Info { return display.Info{Profile: "amd", State: display.StateWelcome} },
		update: func() update.View {
			return update.View{State: update.State{Booted: "20260929.1"}, BootedSlot: "a"}
		},
		power: func() power.Summary { return power.Summary{IdleShutdown: true, IdleMinutes: 15} },
	}
}

func getStatus(t *testing.T, src statusSources) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/status", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, "request"))
	w := httptest.NewRecorder()
	src.handle(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET /status = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func obj(m map[string]any, key string) map[string]any {
	o, _ := m[key].(map[string]any)
	return o
}

func TestStatusView(t *testing.T) {
	src := stubSources()
	m := getStatus(t, src)
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"display", "power", "restart", "stream", "sunshine", "system", "update"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if m["stream"] != nil {
		t.Errorf("stream without a session = %v", m["stream"])
	}
	if obj(m, "system")["hostname"] != "vapor" || obj(m, "display")["profile"] != "amd" ||
		obj(m, "update")["booted"] != "20260929.1" || obj(m, "update")["next_boot"] != nil ||
		obj(m, "power")["idle_minutes"] != float64(15) {
		t.Errorf("parts = %v", m)
	}
	if s := obj(m, "sunshine"); s["running"] != true || s["pairings"] == nil {
		t.Errorf("sunshine (with the request's context) = %v", s)
	}
	if _, ok := obj(m, "sunshine")["session"]; ok {
		t.Error("sunshine carries a session; stream is where it goes")
	}
	if _, ok := obj(m, "power")["wol"]; ok {
		t.Error("power carries wol")
	}
	if r := obj(m, "restart"); r["needed"] != false || !reflect.DeepEqual(r["reasons"], []any{}) {
		t.Errorf("restart = %v", r)
	}

	since := time.Date(2026, 9, 29, 19, 12, 3, 0, time.UTC)
	src.stream = func() *display.Session {
		return &display.Session{Client: "Steam Deck", App: "Hades II", Mode: "1280x800@90", Since: since}
	}
	want := map[string]any{"client": "Steam Deck", "app": "Hades II", "mode": "1280x800@90", "hdr": false, "since": "2026-09-29T19:12:03Z"}
	if got := obj(getStatus(t, src), "stream"); !reflect.DeepEqual(got, want) {
		t.Errorf("stream = %v, want %v", got, want)
	}

	src.extensions = func() bool { return true }
	wantRestart := map[string]any{"needed": true, "reasons": []any{map[string]any{"kind": "extensions"}}}
	if r := obj(getStatus(t, src), "restart"); !reflect.DeepEqual(r, wantRestart) {
		t.Errorf("restart with an extension change pending = %v", r)
	}
}

func TestStatusPartFails(t *testing.T) {
	src := stubSources()
	src.display = func() display.Info { panic("DRM went away") }
	m := getStatus(t, src)
	if m["display"] != nil || obj(m, "system") == nil || obj(m, "update") == nil {
		t.Errorf("one failing part = %v", m)
	}
	if r := obj(m, "restart"); r["needed"] != false {
		t.Errorf("restart without display = %v", r)
	}
}

func TestRestartReasons(t *testing.T) {
	up := func(next string) *update.View {
		v := &update.View{State: update.State{Booted: "20260929.120000"}, BootedSlot: "a"}
		if next != "" {
			v.NextBoot = &update.NextBoot{Slot: "b", Version: next}
		}
		return v
	}
	reboot := &display.Info{RebootNeeded: true}
	for _, c := range []struct {
		name string
		u    *update.View
		d    *display.Info
		ext  bool
		want []restartReason
	}{
		{"nothing", up(""), &display.Info{}, false, []restartReason{}},
		{"parts missing", nil, nil, false, []restartReason{}},
		{"staged update", up("20260930.101010"), nil, false, []restartReason{{"update", "20260930.101010"}}},
		{"rollback", up("20260928.090000"), nil, false, []restartReason{{"rollback", "20260928.090000"}}},
		{"same version", up("20260929.120000"), nil, false, []restartReason{{"rollback", "20260929.120000"}}},
		{"virtual display", up(""), reboot, false, []restartReason{{Kind: "display"}}},
		{"both", up("20260930.101010"), reboot, false, []restartReason{{"update", "20260930.101010"}, {Kind: "display"}}},
		{"extensions", up(""), &display.Info{}, true, []restartReason{{Kind: "extensions"}}},
		{"all", up("20260928.090000"), reboot, true, []restartReason{{"rollback", "20260928.090000"}, {Kind: "display"}, {Kind: "extensions"}}},
	} {
		r := restartFor(c.u, c.d, c.ext)
		if !reflect.DeepEqual(r.Reasons, c.want) || r.Needed != (len(c.want) > 0) {
			t.Errorf("%s: %+v, want %+v", c.name, r, c.want)
		}
	}
}
