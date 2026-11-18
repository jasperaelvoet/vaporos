package power

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// busctl's answers for Profiles: a PC with amd_pstate (read from a
// VaporOS box, power-profiles-daemon 0.30), and one whose processor has
// no driver the daemon knows (a VM).
const (
	busProfilesAMD = `{"type":"aa{sv}","data":[{"Profile":{"type":"s","data":"power-saver"},"CpuDriver":{"type":"s","data":"amd_pstate"},"PlatformDriver":{"type":"s","data":"placeholder"},"Driver":{"type":"s","data":"multiple"}},{"Profile":{"type":"s","data":"balanced"},"CpuDriver":{"type":"s","data":"amd_pstate"},"PlatformDriver":{"type":"s","data":"placeholder"},"Driver":{"type":"s","data":"multiple"}},{"Profile":{"type":"s","data":"performance"},"CpuDriver":{"type":"s","data":"amd_pstate"},"Driver":{"type":"s","data":"amd_pstate"}}]}`
	busProfilesVM  = `{"type":"aa{sv}","data":[{"Profile":{"type":"s","data":"balanced"},"PlatformDriver":{"type":"s","data":"placeholder"},"Driver":{"type":"s","data":"placeholder"}},{"Profile":{"type":"s","data":"power-saver"},"PlatformDriver":{"type":"s","data":"placeholder"},"Driver":{"type":"s","data":"placeholder"}}]}`
)

func TestParseProfiles(t *testing.T) {
	for _, c := range []struct {
		name, out string
		want      []string
	}{
		{"amd_pstate", busProfilesAMD, []string{"power-saver", "balanced", "performance"}},
		{"least power first, whatever the daemon's order", busProfilesVM, []string{"power-saver", "balanced"}},
		{"a plan vosd does not know", `{"data":[{"Profile":{"type":"s","data":"balanced"}},{"Profile":{"type":"s","data":"turbo"}},{"Driver":{"type":"s","data":"x"}},{"Profile":{"type":"u","data":3}}]}`, []string{"balanced"}},
		{"no plan", `{"type":"aa{sv}","data":[]}`, nil},
		{"not JSON", `Failed to get property Profiles: The name is not activatable`, nil},
	} {
		got, err := parseProfiles(c.out)
		if !slices.Equal(got, c.want) || (err == nil) != (c.want != nil) {
			t.Errorf("%s: %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestEffectiveProfile(t *testing.T) {
	all := []string{"power-saver", "balanced", "performance"}
	for _, c := range []struct {
		saved   string
		offered []string
		want    string
	}{
		{"performance", all, "performance"},
		{"power-saver", all, "power-saver"},
		{"performance", all[:2], "balanced"}, // the PC has none
		{"performance", nil, "performance"},  // the daemon has not answered: nothing says it has none
		{"", all, "balanced"},                // a config.json from before the plan
		{"turbo", nil, "balanced"},
	} {
		if got := effectiveProfile(c.saved, c.offered); got != c.want {
			t.Errorf("effectiveProfile(%q, %v) = %q, want %q", c.saved, c.offered, got, c.want)
		}
	}
}

// The start sets the saved plan once, falls back to balanced on a PC
// without it, and keeps trying on the passes after it while the daemon
// does not answer, eight times in all.
func TestApplyProfileAtStart(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 15, true)
	r.cfg.Power.Profile = "performance"
	r.applyProfile(ctx)
	r.applyProfile(ctx)
	if !slices.Equal(r.planSets, []string{"performance"}) || r.planAsks != 1 {
		t.Errorf("start: switched to %v after %d asks, want performance once", r.planSets, r.planAsks)
	}
	if s := r.Summary(); s.Profile != "performance" || len(s.Profiles) != 3 {
		t.Errorf("Summary after the start = %q of %v", s.Profile, s.Profiles)
	}

	r = newRig(t, 15, true)
	r.cfg.Power.Profile = "performance"
	r.plans = []string{"power-saver", "balanced"}
	r.applyProfile(ctx)
	if !slices.Equal(r.planSets, []string{"balanced"}) {
		t.Errorf("a PC without performance: switched to %v, want balanced", r.planSets)
	}
	if s := r.Summary(); s.Profile != "balanced" || r.cfg.Power.Profile != "performance" {
		t.Errorf("a PC without performance: Summary %q, saved %q; want balanced in use and performance kept", s.Profile, r.cfg.Power.Profile)
	}

	r = newRig(t, 15, true)
	r.plansErr = errors.New("the name is not activatable")
	for range profileAttempts + 3 {
		r.applyProfile(ctx)
	}
	if r.planAsks != profileAttempts || len(r.planSets) != 0 {
		t.Errorf("no daemon: %d asks and %v, want %d asks and no switch", r.planAsks, r.planSets, profileAttempts)
	}
	if s := r.Summary(); s.Profile != "balanced" || s.Profiles == nil || len(s.Profiles) != 0 {
		t.Errorf("no daemon: Summary %q of %#v, want balanced of an empty list", s.Profile, s.Profiles)
	}

	r = newRig(t, 15, true)
	r.planErr = errors.New("access denied")
	r.applyProfile(ctx)
	r.planErr = nil
	r.applyProfile(ctx)
	r.applyProfile(ctx)
	if !slices.Equal(r.planSets, []string{"balanced"}) || r.planAsks != 2 {
		t.Errorf("a daemon that answers late: %v after %d asks, want balanced on the second", r.planSets, r.planAsks)
	}
}

func TestPutProfile(t *testing.T) {
	r := newRig(t, 15, true)
	put := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		r.handlePut(w, httptest.NewRequest("PUT", "/api/v1/power", strings.NewReader(body)))
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	saved := func() config.PowerConfig {
		var c config.Config
		config.ReadJSON(config.ConfigPath(), &c)
		return c.Power
	}

	code, m := put(`{"profile":"performance"}`)
	if code != http.StatusOK || m["profile"] != "performance" || len(m["profiles"].([]any)) != 3 {
		t.Fatalf("PUT performance: %d %v", code, m)
	}
	if !slices.Equal(r.planSets, []string{"performance"}) || saved().Profile != "performance" {
		t.Errorf("PUT performance: switched to %v, saved %q", r.planSets, saved().Profile)
	}
	// The start's attempts are over: they would undo this.
	r.cfg.Power.Profile = "balanced"
	r.applyProfile(context.Background())
	if len(r.planSets) != 1 {
		t.Errorf("the start's plan ran after a PUT: %v", r.planSets)
	}
	r.cfg.Power.Profile = "performance"
	if m := getPower(t, r); m["profile"] != "performance" || len(m["profiles"].([]any)) != 3 {
		t.Errorf("GET after PUT: %v of %v", m["profile"], m["profiles"])
	}

	// A refusal changes nothing, the other fields of the same PUT included.
	refused := func(name, body string, want int, msg string) {
		t.Helper()
		before, sets := saved(), len(r.planSets)
		code, m := put(body)
		if code != want || m["error"] != msg {
			t.Errorf("%s: %d %v, want %d %q", name, code, m["error"], want, msg)
		}
		if saved() != before || r.cfg.Power != before || len(r.planSets) != sets {
			t.Errorf("%s: saved %+v (was %+v), switched to %v", name, saved(), before, r.planSets[sets:])
		}
	}
	refused("no such plan", `{"profile":"turbo","idle_minutes":30}`, 400, "profile must be power-saver, balanced or performance")
	refused("an empty plan", `{"profile":""}`, 400, "profile must be power-saver, balanced or performance")
	refused("minutes first", `{"profile":"turbo","idle_minutes":0}`, 400, "idle_minutes must be between 1 and 1440")
	r.plans = []string{"power-saver", "balanced"}
	refused("the PC has none", `{"profile":"performance","idle_minutes":30}`, 409, "this PC does not offer the performance power plan")
	r.plans = []string{"power-saver", "balanced", "performance"}
	r.planErr = errors.New("access denied")
	refused("the daemon refuses", `{"profile":"balanced","idle_minutes":30}`, 502, "power-profiles-daemon did not switch to the balanced power plan")
	r.planErr = nil
	r.plansErr = errors.New("the name is not activatable")
	refused("no daemon", `{"profile":"balanced","idle_shutdown":false}`, 502, "power-profiles-daemon is not answering")
	if m := getPower(t, r); m["profile"] != "performance" || len(m["profiles"].([]any)) != 0 {
		t.Errorf("GET without the daemon: %v of %v, want the saved plan and no plans", m["profile"], m["profiles"])
	}
	r.plansErr = nil

	// The other settings never ask the daemon.
	asks := r.planAsks
	if code, m := put(`{"idle_minutes":45}`); code != http.StatusOK || m["profile"] != "performance" || r.planAsks != asks {
		t.Errorf("PUT idle_minutes: %d, profile %v, %d asks", code, m["profile"], r.planAsks-asks)
	}

	// A plan that cannot be saved goes back to the one before it.
	if err := os.Remove(config.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(config.ConfigPath(), 0o755); err != nil { // nothing can be renamed over a directory with a file in it
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.ConfigPath(), "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sets := len(r.planSets)
	if code, _ := put(`{"profile":"power-saver"}`); code != http.StatusInternalServerError {
		t.Fatalf("PUT that cannot be saved: %d", code)
	}
	if !slices.Equal(r.planSets[sets:], []string{"power-saver", "performance"}) || r.cfg.Power.Profile != "performance" {
		t.Errorf("PUT that cannot be saved: switched to %v, in memory %q; want power-saver then back to performance", r.planSets[sets:], r.cfg.Power.Profile)
	}
}
