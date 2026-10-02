package extensions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

func TestMakeDataAreas(t *testing.T) {
	newEnv(t)
	dir := t.TempDir()
	savedDesc, savedHome, savedUID := config.ExtDescriptorsDir, config.GamerHome, config.GamerUID
	t.Cleanup(func() { config.ExtDescriptorsDir, config.GamerHome, config.GamerUID = savedDesc, savedHome, savedUID })
	config.ExtDescriptorsDir = filepath.Join(dir, "descriptors")
	config.GamerHome = filepath.Join(dir, "home")
	config.GamerUID = -1 // tests cannot chown to vapor
	must(t, os.MkdirAll(config.GamerHome, 0o700))
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "demo.json"), `{"schema":1,"id":"demo","name":"Demo","summary":"A demo.",
		"category":"app","upstream":{"name":"Demo","url":"https://example.com","license":"MIT"},
		"data":[{"name":"config","where":"system"},{"name":"files","where":"home"},{"name":"games","where":"library"}]}`)
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "other.json"), `{"schema":1,"id":"other","name":"Other","summary":"Not mounted.",
		"category":"app","upstream":{"name":"Other","url":"https://example.com","license":"MIT"},
		"data":[{"name":"config","where":"system"}]}`)

	makeDataAreas(&store.BootReport{Mode: store.ModeEnabled, Mounted: []store.Mounted{{ID: "demo"}, {ID: "nodescriptor"}}})

	for _, p := range []string{
		filepath.Join(config.ExtDataDir(), "demo"),
		filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, "demo"),
	} {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Errorf("%s: not made: %v", p, err)
		}
	}
	if exists(filepath.Join(config.ExtDataDir(), "other")) {
		t.Error("made a data area for an extension that is not mounted")
	}
	makeDataAreas(nil) // no report: nothing to do, no panic
}
