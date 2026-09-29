package display

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/brand"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
	"github.com/jasperaelvoet/vaporos/internal/events"
)

func event(topic string, v any) events.Event {
	b, _ := json.Marshal(v)
	return events.Event{Topic: topic, Data: b}
}

func TestHostURL(t *testing.T) {
	for _, c := range []struct {
		https bool
		host  string
		port  int
		want  string
	}{
		{false, "vapor.local", 80, "http://vapor.local"},
		{false, "192.168.1.50", 8080, "http://192.168.1.50:8080"},
		{false, "fd00::5", 80, "http://[fd00::5]"},
		{true, "vapor.local", 80, "https://vapor.local"},
	} {
		if got := hostURL(c.https, c.host, c.port); got != c.want {
			t.Errorf("hostURL(%v,%q,%d) = %q", c.https, c.host, c.port, got)
		}
	}
}

// wantTone checks the fields the TV draws its look from.
func wantTone(t *testing.T, name string, st welcome.State, tone brand.State, attention string, progress int) {
	t.Helper()
	if st.Tone != tone || st.Attention != attention || st.Progress != progress {
		t.Errorf("%s: tone %q attention %q progress %d, want %q %q %d", name, st.Tone, st.Attention, st.Progress, tone, attention, progress)
	}
}

func TestBuildWelcome(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	base := welcomeInputs{hostname: "vapor", ips: []string{"192.168.1.50"}, port: 80, gpuSupported: true, version: "20260929.1", now: now}

	st := buildWelcome(base)
	if st.Mode != "os" || st.URL != "http://vapor.local" || st.IPURL != "http://192.168.1.50" ||
		st.QR != "http://192.168.1.50/" || st.Status != "Ready to stream" || st.Title != "VaporOS" || st.Version != "20260929.1" {
		t.Errorf("ready = %+v", st)
	}
	wantTone(t, "ready", st, brand.Ready, "", 0)

	in := base
	in.live, in.code = true, "ABCD-EFGH"
	st = buildWelcome(in)
	if st.Mode != "installer" || st.Status != "Ready to install" || st.Code != "ABCD-EFGH" || st.QR != "http://192.168.1.50/setup?code=ABCD-EFGH" {
		t.Errorf("installer = %+v", st)
	}
	wantTone(t, "installer", st, brand.Installing, "", 0)

	in = base
	in.code = "ABCD-EFGH"
	if st = buildWelcome(in); st.Status != "Almost ready" {
		t.Errorf("setup code = %+v", st)
	}
	wantTone(t, "setup code", st, brand.Installing, "", 0)

	in = base
	in.virtualPending = true
	if st = buildWelcome(in); st.Status != "Restart to finish setup" {
		t.Errorf("virtual pending = %+v", st)
	}
	wantTone(t, "virtual pending", st, brand.RestartNeeded, "", 0)

	in = base
	in.ips = nil
	st = buildWelcome(in)
	if st.Status != "Waiting for the network" || st.IPURL != "" || st.QR != "http://vapor.local/" {
		t.Errorf("offline = %+v", st)
	}
	wantTone(t, "offline", st, brand.Fault, "", 0)
	// A calm boot: no alarm while the links come up.
	in.upSince = now.Add(-14 * time.Second)
	if st = buildWelcome(in); st.Status != "Waiting for the network" {
		t.Errorf("offline at boot = %+v", st)
	}
	wantTone(t, "offline at boot", st, brand.Neutral, "", 0)
	in.upSince = now.Add(-bootGrace)
	wantTone(t, "offline after the boot grace", buildWelcome(in), brand.Fault, "", 0)
	in.ips, in.upSince = base.ips, now
	wantTone(t, "online at boot", buildWelcome(in), brand.Ready, "", 0)

	in = base
	in.gpuSupported, in.gpuName = false, "NVIDIA AD103 [GeForce RTX 4080]"
	if st = buildWelcome(in); st.Status != "No supported graphics card" || !strings.Contains(st.Detail, "RTX 4080") {
		t.Errorf("no gpu = %+v", st)
	}
	wantTone(t, "no gpu", st, brand.Fault, "", 0)

	in = base
	in.session = &sessionInfo{Client: "iPhone", Mode: "2796x1290@120", HDR: true}
	if st = buildWelcome(in); st.Status != "Streaming to iPhone" || st.Detail != "2796 × 1290 · 120 Hz · HDR" {
		t.Errorf("streaming = %+v", st)
	}
	wantTone(t, "streaming", st, brand.Streaming, "", 0)

	// Overlays in priority order.
	in = base
	in.overlay.apply(event("update.state", map[string]any{"staged": map[string]string{"version": "20261001.0"}}), now)
	if st = buildWelcome(in); !strings.Contains(st.Detail, "20261001.0 installs on the next restart") {
		t.Errorf("staged = %+v", st)
	}
	wantTone(t, "staged", st, brand.Ready, "", 0) // a staged update is ready
	in.overlay.apply(event("update.progress", map[string]any{"phase": "download", "percent": 42, "version": "20261002.0"}), now)
	if st = buildWelcome(in); st.Status != "Downloading update 20261002.0" || st.Detail != "Download… 42%" {
		t.Errorf("update = %+v", st)
	}
	wantTone(t, "update", st, brand.Updating, "", 42)
	u := in
	u.overlay.apply(event("pairing.pending", map[string]any{"name": "Deck"}), now)
	if st = buildWelcome(u); st.Status != "Deck wants to pair" {
		t.Errorf("pairing during an update = %+v", st)
	}
	wantTone(t, "pairing during an update", st, brand.Updating, welcome.AttentionPair, 42)
	for p, want := range map[int]int{140: 100, -5: 0} {
		u := in
		u.overlay.apply(event("update.progress", map[string]any{"phase": "write", "percent": p}), now)
		wantTone(t, "update progress out of range", buildWelcome(u), brand.Updating, "", want)
	}
	in.overlay.apply(event("update.progress", map[string]any{"phase": "staged", "percent": 100}), now)
	if st = buildWelcome(in); strings.HasPrefix(st.Status, "Downloading") {
		t.Errorf("finished update still shown: %+v", st)
	}
	wantTone(t, "finished update", st, brand.Ready, "", 0)
	in.overlay.apply(event("pairing.pending", map[string]any{}), now)
	if st = buildWelcome(in); st.Status != "A device wants to pair" || st.Detail != "Enter the PIN from Moonlight at http://192.168.1.50/pair" ||
		st.QR != "http://192.168.1.50/pair" {
		t.Errorf("pairing = %+v", st)
	}
	wantTone(t, "pairing", st, brand.Ready, welcome.AttentionPair, 0)
	offline := in
	offline.ips = nil
	if st = buildWelcome(offline); st.QR != "http://vapor.local/pair" {
		t.Errorf("pairing offline QR = %q", st.QR)
	}
	wantTone(t, "pairing offline", st, brand.Fault, welcome.AttentionPair, 0)
	in.overlay.apply(event("pairing.pending", map[string]any{"pending": false}), now)
	if st = buildWelcome(in); strings.Contains(st.Status, "pair") || st.QR != "http://192.168.1.50/" {
		t.Errorf("cleared pairing still shown: %+v", st)
	}
	wantTone(t, "cleared pairing", st, brand.Ready, "", 0)
	in.overlay.apply(event("install.progress", map[string]any{"step": "write", "percent": 37, "message": "Writing the system image", "state": "running"}), now)
	if st = buildWelcome(in); st.Status != "Installing VaporOS… 37%" || st.Detail != "Writing the system image" {
		t.Errorf("install = %+v", st)
	}
	wantTone(t, "install", st, brand.Installing, "", 37)
	in.overlay.apply(event("install.progress", map[string]any{"state": "failed", "message": "disk too small"}), now)
	if st = buildWelcome(in); st.Status != "Installation failed" || st.Detail != "disk too small" {
		t.Errorf("install failed = %+v", st)
	}
	wantTone(t, "install failed", st, brand.Fault, "", 0)
	done := in
	done.overlay.apply(event("install.progress", map[string]any{"state": "done", "percent": 100}), now)
	if st = buildWelcome(done); st.Status != "VaporOS is installed" {
		t.Errorf("install done = %+v", st)
	}
	wantTone(t, "install done", st, brand.Installing, "", 100)
	in.overlay.apply(event("system.message", map[string]string{"level": "warning", "text": "Disk almost full"}), now)
	in.overlay.install = nil
	if st = buildWelcome(in); st.Detail != "Disk almost full" {
		t.Errorf("message = %+v", st)
	}
	wantTone(t, "message", st, brand.Ready, "", 0)
	in.now = now.Add(10 * time.Minute)
	if st = buildWelcome(in); st.Detail == "Disk almost full" {
		t.Error("stale message still shown")
	}
	var o statusOverlay
	if o.apply(event("session.begin", map[string]any{}), now) {
		t.Error("unrelated topic changed the overlay")
	}
}

func TestPreSleepNotice(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	in := welcomeInputs{hostname: "vapor", ips: []string{"192.168.1.50"}, port: 80, gpuSupported: true, now: now}
	idle := func(shutdownIn any) {
		t.Helper()
		if !in.overlay.apply(event("power.idle", map[string]any{"idle_seconds": 60, "shutdown_in": shutdownIn}), now) {
			t.Fatal("power.idle left the overlay alone")
		}
	}
	idle(300)
	if st := buildWelcome(in); st.Status != "Ready to stream" || st.Tone != brand.Ready {
		t.Errorf("5 min before sleep = %+v", st)
	}
	idle(120)
	st := buildWelcome(in)
	if st.Status != "Going to sleep in 2 min" || st.Detail != "Moonlight wakes it: open Moonlight and pick this PC" {
		t.Errorf("2 min before sleep = %+v", st)
	}
	wantTone(t, "2 min before sleep", st, brand.Asleep, "", 0)
	for _, c := range []struct {
		after time.Duration
		want  string
	}{{59 * time.Second, "Going to sleep in 2 min"}, {60 * time.Second, "Going to sleep in 1 min"}, {119 * time.Second, "Going to sleep in 1 min"}, {2 * time.Minute, "Going to sleep now"}, {3 * time.Minute, "Ready to stream"}} {
		in.now = now.Add(c.after)
		if st := buildWelcome(in); st.Status != c.want {
			t.Errorf("%v after the event: %q, want %q", c.after, st.Status, c.want)
		}
	}
	in.now = now
	s := in
	s.session = &sessionInfo{Client: "Deck", Mode: "1280x800@90"}
	if st := buildWelcome(s); st.Status != "Streaming to Deck" || st.Tone != brand.Streaming {
		t.Errorf("session = %+v", st)
	}
	s = in
	s.live = true
	if st := buildWelcome(s); st.Status != "Ready to install" {
		t.Errorf("installer = %+v", st)
	}
	// Busy again, or idle shutdown switched off: shutdown_in is null.
	idle(nil)
	if st := buildWelcome(in); st.Status != "Ready to stream" || st.Tone != brand.Ready {
		t.Errorf("after a busy tick = %+v", st)
	}
}

func TestPairingStateClearsWelcomePrompt(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	in := welcomeInputs{hostname: "vapor", ips: []string{"192.168.1.50"}, port: 80, gpuSupported: true, now: now}
	if in.overlay.apply(event("pairing.state", map[string]any{"pairings": []any{}}), now) {
		t.Error("an empty list with no prompt changed the overlay")
	}
	in.overlay.apply(event("pairing.pending", map[string]any{"name": "Deck"}), now)
	if st := buildWelcome(in); st.Status != "Deck wants to pair" || st.QR != "http://192.168.1.50/pair" {
		t.Fatalf("pairing = %+v", st)
	}
	deck := map[string]any{"id": strings.Repeat("7f", 16), "name": "Deck", "address": "192.168.1.31"}
	if in.overlay.apply(event("pairing.state", map[string]any{"pairings": []any{deck}}), now) {
		t.Error("a waiting device changed the overlay")
	}
	if st := buildWelcome(in); st.Status != "Deck wants to pair" {
		t.Errorf("prompt gone while the device waits: %+v", st)
	}
	if !in.overlay.apply(event("pairing.state", map[string]any{"pairings": []any{}}), now) {
		t.Error("the end of pairing left the overlay alone")
	}
	st := buildWelcome(in)
	if st.Status != "Ready to stream" || st.QR != "http://192.168.1.50/" {
		t.Errorf("after pairing = %+v", st)
	}
	wantTone(t, "after pairing", st, brand.Ready, "", 0)
}
