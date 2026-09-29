package display

import (
	"net"
	"os"
	"path/filepath"
	"testing"

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
	if got := string(updateModesCfg(nil, []string{"Best Buy VaporOS"}, m)); got != "Best Buy VaporOS:2560x1440@120\n" {
		t.Errorf("empty = %q", got)
	}
	// Existing entries for other displays and a stale one for ours (with a
	// broadcast-RGB suffix gamescope may write) are handled.
	old := "Dell Inc. DELL U2723QE:3840x2160@60 0\nBest Buy VaporOS:1920x1080@60 1\n\nLG Electronics LG TV:1920x1080@120\n"
	got := string(updateModesCfg([]byte(old), []string{"Best Buy VaporOS", "VPR VaporOS"}, m))
	want := "Dell Inc. DELL U2723QE:3840x2160@60 0\nLG Electronics LG TV:1920x1080@120\nBest Buy VaporOS:2560x1440@120\nVPR VaporOS:2560x1440@120\n"
	if got != want {
		t.Errorf("update =\n%q\nwant\n%q", got, want)
	}
}

func TestParseGamescopectl(t *testing.T) {
	out := `gamescope version 3.16.31 (gcc 15.2.1)
gamescope_control info:
  - Connector Name: DP-1
  - Display Make: Best Buy
  - Display Model: VaporOS
  - Display Flags: 0x0
  - ValidRefreshRates: 60, 120
  Features:
  - Reshade Shaders (1) - Version: 1 - Flags: 0x0
You can execute any debug command in Gamescope using this tool.
`
	info, ok := parseGamescopectl(out)
	if !ok || info.Connector != "DP-1" || info.Key() != "Best Buy VaporOS" {
		t.Errorf("parse = %+v %v", info, ok)
	}
	if _, ok := parseGamescopectl("Failed to open GAMESCOPE_WAYLAND_DISPLAY.\n"); ok {
		t.Error("parsed an error message")
	}
}

func TestGamescopeKeyForEDID(t *testing.T) {
	setupPaths(t)
	res, _ := edid.Generate(nil)
	k, err := gamescopeKeyForEDID(res.EDID)
	if err != nil || k != "Best Buy VaporOS" {
		t.Errorf("key = %q, %v", k, err)
	}
	// Without hwdata, gamescope falls back to the raw PNP id.
	resetPNPCache()
	PNPIDsPath = filepath.Join(t.TempDir(), "missing")
	if k := pnpName("VPR"); k != "VPR" {
		t.Errorf("fallback = %q", k)
	}
	if _, err := gamescopeKeyForEDID([]byte("junk")); err == nil {
		t.Error("junk EDID accepted")
	}
}

func TestSocketDiscovery(t *testing.T) {
	// Unix socket paths are short on macOS: keep the dir near the root.
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	save, saveX := UserRuntimeDir, X11SocketDir
	defer func() { UserRuntimeDir, X11SocketDir = save, saveX }()
	UserRuntimeDir, X11SocketDir = dir, dir
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
