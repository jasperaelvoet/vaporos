package display

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

func TestGamescopeEnv(t *testing.T) {
	for _, e := range []gamescopeEnv{{"DP-1", false}, {"HDMI-A-1", true}} {
		b := e.render()
		if got := parseGamescopeEnv(b); got != e {
			t.Errorf("round trip %+v -> %q -> %+v", e, b, got)
		}
	}
	want := "# Written by vosd; read by vos-gamescope.service.\nVOS_OUTPUT=DP-1\nVOS_GS_EXTRA=--hdr-enabled\n"
	if got := string(gamescopeEnv{"DP-1", true}.render()); got != want {
		t.Errorf("render = %q", got)
	}
	if got := parseGamescopeEnv([]byte("VOS_OUTPUT=\"DP-3\"\nVOS_GS_EXTRA='--foo --hdr-enabled'\n")); got != (gamescopeEnv{"DP-3", true}) {
		t.Errorf("quoted parse = %+v", got)
	}
}

func TestUpdateModesCfg(t *testing.T) {
	m := edid.Mode{W: 2560, H: 1440, Refresh: 120}
	// Empty file.
	if got := string(updateModesCfg(nil, []string{"VOS VaporOS"}, m)); got != "VOS VaporOS:2560x1440@120\n" {
		t.Errorf("empty = %q", got)
	}
	// Existing entries for other displays (including the "Best Buy VaporOS"
	// an image with the old VPR id left) and a stale one for ours (with a
	// broadcast-RGB suffix gamescope may write) are handled.
	old := "Dell Inc. DELL U2723QE:3840x2160@60 0\nVOS VaporOS:1920x1080@60 1\n\nBest Buy VaporOS:1920x1080@120\n"
	got := string(updateModesCfg([]byte(old), []string{"VOS VaporOS", "Some Vendor VaporOS"}, m))
	want := "Dell Inc. DELL U2723QE:3840x2160@60 0\nBest Buy VaporOS:1920x1080@120\nVOS VaporOS:2560x1440@120\nSome Vendor VaporOS:2560x1440@120\n"
	if got != want {
		t.Errorf("update =\n%q\nwant\n%q", got, want)
	}
}

func TestParseGamescopectl(t *testing.T) {
	// The format the hardware spike saw (gamescope 3.16.25, a real monitor).
	out := `gamescope version 3.16.25 (gcc 15.2.1)
gamescope_control info:
  - Connector Name: DP-1
  - Display Make: Dell Inc.
  - Display Model: DELL U2723QE
  - Display Flags: 0x0
  - ValidRefreshRates: 60
  Features:
  - Reshade Shaders (1) - Version: 1 - Flags: 0x0
You can execute any debug command in Gamescope using this tool.
`
	info, ok := parseGamescopectl(out)
	if !ok || info.Connector != "DP-1" || info.Key() != "Dell Inc. DELL U2723QE" {
		t.Errorf("parse = %+v %v", info, ok)
	}
	// The virtual display: pnp.ids has no VOS, so Make is the raw id.
	info, ok = parseGamescopectl("gamescope_control info:\n  - Connector Name: DP-2\n  - Display Make: VOS\n  - Display Model: VaporOS\n")
	if !ok || info.Connector != "DP-2" || info.Key() != "VOS VaporOS" {
		t.Errorf("parse virtual = %+v %v", info, ok)
	}
	if _, ok := parseGamescopectl("Failed to open GAMESCOPE_WAYLAND_DISPLAY.\n"); ok {
		t.Error("parsed an error message")
	}
}

func TestGamescopeKeyForEDID(t *testing.T) {
	setupPaths(t)
	res, _ := edid.Generate(nil)
	// pnp.ids lists DEL and VPR but not VOS: gamescope's GetMake falls
	// back to the raw id.
	k, err := gamescopeKeyForEDID(res.EDID)
	if err != nil || k != "VOS VaporOS" {
		t.Errorf("key = %q, %v", k, err)
	}
	if k := pnpName("VPR"); k != "Best Buy" {
		t.Errorf("VPR = %q", k)
	}
	// A pnp.ids that did list VOS would name it.
	resetPNPCache()
	mustWrite(t, PNPIDsPath, "VOS\tSome Vendor\n")
	if k, _ := gamescopeKeyForEDID(res.EDID); k != "Some Vendor VaporOS" {
		t.Errorf("listed key = %q", k)
	}
	// Without hwdata at all, the raw id again.
	resetPNPCache()
	PNPIDsPath = filepath.Join(t.TempDir(), "missing")
	if k, _ := gamescopeKeyForEDID(res.EDID); k != "VOS VaporOS" {
		t.Errorf("no hwdata key = %q", k)
	}
	if k := pnpName("VPR"); k != "VPR" {
		t.Errorf("fallback = %q", k)
	}
	if _, err := gamescopeKeyForEDID([]byte("junk")); err == nil {
		t.Error("junk EDID accepted")
	}
}

// TestParsePNPIDs pins gamescope's load_pnps behaviour: split at the first
// tab, name verbatim, no-tab lines skipped, the last duplicate wins, and a
// last line without a newline still counts.
func TestParsePNPIDs(t *testing.T) {
	in := "# comment without a tab\nDEL\tDell Inc.\nABC\tName\twith tab \n\nDEL\tDell Again\nXYZ\tNo newline"
	db := parsePNPIDs(strings.NewReader(in))
	want := map[string]string{"DEL": "Dell Again", "ABC": "Name\twith tab ", "XYZ": "No newline"}
	if len(db) != len(want) {
		t.Errorf("db = %q", db)
	}
	for k, v := range want {
		if db[k] != v {
			t.Errorf("%s = %q, want %q", k, db[k], v)
		}
	}
	if _, ok := db["VOS"]; ok {
		t.Error("VOS listed")
	}
}

func TestParseXpropCardinal(t *testing.T) {
	for _, c := range []struct {
		out string
		v   int64
		set bool
	}{
		{"GAMESCOPE_COMPOSITE_FORCE(CARDINAL) = 1", 1, true},
		{"GAMESCOPE_COMPOSITE_FORCE(CARDINAL) = 0\n", 0, true},
		{"GAMESCOPE_COMPOSITE_FORCE(CARDINAL) = 1, 0", 1, true},
		{"GAMESCOPE_COMPOSITE_FORCE:  not found.", 0, false},
		{"GAMESCOPE_COMPOSITE_FORCE:  no such atom on any window.", 0, false},
		{"GAMESCOPE_COMPOSITE_FORCE_EXTRA(CARDINAL) = 1\nGAMESCOPE_COMPOSITE_FORCE(CARDINAL) = 0", 0, true},
		{"", 0, false},
	} {
		v, set := parseXpropCardinal(c.out, compositeForceProp)
		if v != c.v || set != c.set {
			t.Errorf("parse(%q) = %d %v, want %d %v", c.out, v, set, c.v, c.set)
		}
	}
}

func TestSocketDiscovery(t *testing.T) {
	// Unix socket paths are short on macOS: keep the dir near the root.
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	save, saveX := config.GamerRuntimeDir, X11SocketDir
	defer func() { config.GamerRuntimeDir, X11SocketDir = save, saveX }()
	config.GamerRuntimeDir, X11SocketDir = dir, dir
	if got := waylandDisplay(); got != "gamescope-0" {
		t.Errorf("default = %q", got)
	}
	for _, n := range []string{"gamescope-2", "gamescope-1", "X1", "X0"} {
		l, err := net.Listen("unix", filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
	}
	os.WriteFile(filepath.Join(dir, "gamescope-0.lock"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "gamescope-0"), nil, 0o644) // not a socket
	if got := waylandDisplay(); got != "gamescope-1" {
		t.Errorf("wayland = %q", got)
	}
	if got := xDisplay(); got != ":0" {
		t.Errorf("x = %q", got)
	}
}
