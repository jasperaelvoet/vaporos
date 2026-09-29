package display

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestModeLabel(t *testing.T) {
	for _, c := range []struct {
		mode string
		hdr  bool
		want string
	}{
		{"3840x2160@120", true, "3840 × 2160 · 120 Hz · HDR"},
		{"1280x800@90", false, "1280 × 800 · 90 Hz"},
		{"odd", false, "odd"},
		{"", true, "HDR"},
		{"", false, ""},
	} {
		if got := modeLabel(c.mode, c.hdr); got != c.want {
			t.Errorf("modeLabel(%q, %v) = %q, want %q", c.mode, c.hdr, got, c.want)
		}
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

	in := base
	in.live, in.code = true, "ABCD-EFGH"
	st = buildWelcome(in)
	if st.Mode != "installer" || st.Status != "Ready to install" || st.Code != "ABCD-EFGH" || st.QR != "http://192.168.1.50/setup?code=ABCD-EFGH" {
		t.Errorf("installer = %+v", st)
	}

	in = base
	in.ips = nil
	st = buildWelcome(in)
	if st.Status != "Waiting for the network" || st.IPURL != "" || st.QR != "http://vapor.local/" {
		t.Errorf("offline = %+v", st)
	}

	in = base
	in.gpuSupported, in.gpuName = false, "NVIDIA AD103 [GeForce RTX 4080]"
	if st = buildWelcome(in); st.Status != "No supported graphics card" || !strings.Contains(st.Detail, "RTX 4080") {
		t.Errorf("no gpu = %+v", st)
	}

	in = base
	in.session = &sessionInfo{Client: "iPhone", Mode: "2796x1290@120", HDR: true}
	if st = buildWelcome(in); st.Status != "Streaming to iPhone" || st.Detail != "2796 × 1290 · 120 Hz · HDR" {
		t.Errorf("streaming = %+v", st)
	}

	// Overlays in priority order.
	in = base
	in.overlay.apply(event("update.state", map[string]any{"staged": map[string]string{"version": "20261001.0"}}), now)
	if st = buildWelcome(in); !strings.Contains(st.Detail, "20261001.0 installs on the next restart") {
		t.Errorf("staged = %+v", st)
	}
	in.overlay.apply(event("update.progress", map[string]any{"phase": "download", "percent": 42, "version": "20261002.0"}), now)
	if st = buildWelcome(in); st.Status != "Downloading update 20261002.0" || st.Detail != "Download… 42%" {
		t.Errorf("update = %+v", st)
	}
	in.overlay.apply(event("update.progress", map[string]any{"phase": "staged", "percent": 100}), now)
	if st = buildWelcome(in); strings.HasPrefix(st.Status, "Downloading") {
		t.Errorf("finished update still shown: %+v", st)
	}
	in.overlay.apply(event("pairing.pending", map[string]any{}), now)
	if st = buildWelcome(in); st.Status != "A device wants to pair" || st.Detail != "Enter the PIN from Moonlight at http://192.168.1.50/pair" {
		t.Errorf("pairing = %+v", st)
	}
	in.overlay.apply(event("pairing.pending", map[string]any{"pending": false}), now)
	if st = buildWelcome(in); strings.Contains(st.Status, "pair") {
		t.Errorf("cleared pairing still shown: %+v", st)
	}
	in.overlay.apply(event("install.progress", map[string]any{"step": "write", "percent": 37, "message": "Writing the system image", "state": "running"}), now)
	if st = buildWelcome(in); st.Status != "Installing VaporOS… 37%" || st.Detail != "Writing the system image" {
		t.Errorf("install = %+v", st)
	}
	in.overlay.apply(event("install.progress", map[string]any{"state": "failed", "message": "disk too small"}), now)
	if st = buildWelcome(in); st.Status != "Installation failed" || st.Detail != "disk too small" {
		t.Errorf("install failed = %+v", st)
	}
	in.overlay.apply(event("system.message", map[string]string{"level": "warning", "text": "Disk almost full"}), now)
	in.overlay.install = nil
	if st = buildWelcome(in); st.Detail != "Disk almost full" {
		t.Errorf("message = %+v", st)
	}
	in.now = now.Add(10 * time.Minute)
	if st = buildWelcome(in); st.Detail == "Disk almost full" {
		t.Error("stale message still shown")
	}
	var o statusOverlay
	if o.apply(event("session.begin", map[string]any{}), now) {
		t.Error("unrelated topic changed the overlay")
	}
}
