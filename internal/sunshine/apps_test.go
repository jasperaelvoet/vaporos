package sunshine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// steamFixture installs the reference PC's Steam libraries (shared with
// internal/storage/steam) into the gaming user's home: the Steam root
// under ~/.local/share/Steam and two SATA libraries elsewhere.
func steamFixture(t *testing.T) {
	t.Helper()
	const src = "../storage/steam/testdata"
	root := filepath.Join(config.GamerHome, ".local", "share", "Steam")
	mnt := t.TempDir()
	copyDir(t, filepath.Join(src, "Steam"), root)
	copyDir(t, filepath.Join(src, "SATA1TB"), filepath.Join(mnt, "SATA1TB"))
	copyDir(t, filepath.Join(src, "SATA500GB"), filepath.Join(mnt, "SATA500GB"))
	vdf, err := os.ReadFile(filepath.Join(src, "libraryfolders.vdf"))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer("/home/jasper/.local/share/Steam", root, "/mnt/", mnt+"/").Replace(string(vdf))
	if err := os.WriteFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAppsFromReferenceLibraries(t *testing.T) {
	isolate(t)
	steamFixture(t)
	var f appsFile
	if err := json.Unmarshal(renderApps(installedGames(config.GamerHome)), &f); err != nil {
		t.Fatal(err)
	}
	if f.Env["PATH"] == "" {
		t.Error("env missing: Sunshine refuses an apps.json without it")
	}
	want := []appEntry{
		{Name: "Steam", ImagePath: "steam.png"},
		{Name: "Cities: Skylines II", Cmd: "steam steam://rungameid/949230"},
		{Name: "Cyberpunk 2077", Cmd: "steam steam://rungameid/1091500"},
		{Name: "Forza Horizon 6", Cmd: "steam steam://rungameid/2483190"},
		{Name: "Microsoft Flight Simulator 2024", Cmd: "steam steam://rungameid/2537590"},
		{Name: "Red Dead Redemption 2", Cmd: "steam steam://rungameid/1174180"},
		{Name: "The Bus", Cmd: "steam steam://rungameid/491540"},
		{Name: "Train Sim World® 7", Cmd: "steam steam://rungameid/4678800"},
	}
	if !reflect.DeepEqual(f.Apps, want) {
		t.Errorf("apps:\n%+v\nwant\n%+v", f.Apps, want)
	}
}

func TestAppsWithoutSteam(t *testing.T) {
	isolate(t)
	var f appsFile
	if err := json.Unmarshal(renderApps(installedGames(config.GamerHome)), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Apps) != 1 || f.Apps[0].Name != "Steam" {
		t.Errorf("apps = %+v", f.Apps)
	}
}

func TestAppsNamesUniqueAndEscaped(t *testing.T) {
	out := renderApps([]steam.App{
		{ID: 10, Name: "Steam"},
		{ID: 20, Name: "Twin"},
		{ID: 30, Name: "twin"},
		{ID: 40, Name: "Cash $(HOME) Grab"},
	})
	var f appsFile
	json.Unmarshal(out, &f)
	var names []string
	for _, a := range f.Apps {
		names = append(names, a.Name)
	}
	want := []string{"Steam", "Steam (10)", "Twin", "twin (30)", "Cash $$(HOME) Grab"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %q", names)
	}
	if !json.Valid(out) || out[len(out)-1] != '\n' {
		t.Error("apps.json is not valid JSON ending in a newline")
	}
}
