package extensions

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// goldenSteamJSON is the steam.json both sides test: vosd writes these
// bytes and `vos steam prepare` must take every entry of them
// (internal/steamprep TestGoldenSteamJSON).
var goldenSteamJSON = filepath.Join("..", "steamprep", "testdata", "steam.json")

// goldenSteamDesired has one of each kind of entry: a branch request, an
// app without one, a shortcut with art and one with arguments, a release,
// and an owner that lists nothing this boot.
func goldenSteamDesired() SteamDesired {
	return SteamDesired{
		Set: "3", Dispatcher: true, DefaultCompatTool: "proton-cachyos-slr",
		Apps: []SteamApp{
			{App: 227300, CompatTool: "proton-cachyos-slr", Hooks: []string{"truckersmp"},
				Beta: &BetaRequest{Branch: "temporary_1_61", Request: "20261002T200000.000000000"}},
			{App: 270880, CompatTool: "proton-cachyos-slr", Hooks: []string{"truckersmp"}},
			{App: 1172470, Hooks: []string{}, Beta: &BetaRequest{Branch: "", Request: "1759400000"}},
		},
		Shortcuts: []SteamShortcut{
			{Owner: "star-citizen", Key: "launcher", Name: "Star Citizen",
				Exe:      "/var/mnt/games/VaporOS/star-citizen/installer/RSI Launcher-Setup-2.17.0.exe",
				StartDir: "/var/mnt/games/VaporOS/star-citizen", CompatTool: "proton-cachyos-slr",
				Art: "/usr/lib/vos/ext/star-citizen/art"},
			{Owner: "truckersmp", Key: "ets2", Name: "TruckersMP (ETS2)", Exe: "/usr/bin/vos",
				StartDir: "/var/home/vapor/.local/share/vaporos/ext/truckersmp", Args: []string{"ext", "truckersmp", "mp", "ets2"}},
		},
		Release: []SteamRelease{{App: 292030}},
		Owners:  []string{"coolercontrol", "proton", "star-citizen", "truckersmp"},
	}
}

// VOS_GEN_STEAM_JSON=1 writes the golden file anew.
func TestGoldenSteamJSON(t *testing.T) {
	d := goldenSteamDesired()
	// Entries vosd itself would write.
	for _, a := range d.Apps {
		if a.Beta != nil && (!steamNameRe.MatchString(a.Beta.Request) || (a.Beta.Branch != "" && !steamNameRe.MatchString(a.Beta.Branch))) {
			t.Errorf("app %d: vosd leaves out %+v", a.App, *a.Beta)
		}
	}
	for _, s := range d.Shortcuts {
		if !validPath(s.Exe) || !validPath(s.StartDir) || !validArgs(s.Args) {
			t.Errorf("%s/%s: vosd leaves it out", s.Owner, s.Key)
		}
	}
	b, err := marshalSteamDesired(d)
	must(t, err)
	if os.Getenv("VOS_GEN_STEAM_JSON") == "1" {
		must(t, os.WriteFile(goldenSteamJSON, b, 0o644))
	}
	want, err := os.ReadFile(goldenSteamJSON)
	must(t, err)
	if !bytes.Equal(b, want) {
		t.Fatalf("vosd's steam.json is not %s (VOS_GEN_STEAM_JSON=1 writes it anew, and internal/steamprep must still take it):\n%s", goldenSteamJSON, b)
	}
}
