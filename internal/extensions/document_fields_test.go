package extensions

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// A card says what it changes in Steam by the names Steam shows (from the
// appmanifests of any library, "" for an app without one), whether it
// sets kernel module options, and which settings take the password.
func TestDocumentSteamAndPasswords(t *testing.T) {
	r := newRig(t)
	steamapps := filepath.Join(config.GamerHome, steamRel, "steamapps")
	writeFile(t, filepath.Join(steamapps, "appmanifest_227300.acf"),
		"\"AppState\"\n{\n\t\"appid\"\t\t\"227300\"\n\t\"name\"\t\t\"Euro Truck Simulator 2\"\n\t\"StateFlags\"\t\t\"4\"\n}\n")
	lib := t.TempDir()
	writeFile(t, filepath.Join(lib, "steamapps", "appmanifest_270880.acf"),
		"\"AppState\"\n{\n\t\"appid\"\t\t\"270880\"\n\t\"name\"\t\t\"American  Truck\tSimulator\"\n}\n")
	writeFile(t, filepath.Join(steamapps, "libraryfolders.vdf"),
		"\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\""+filepath.Join(config.GamerHome, steamRel)+
			"\"\n\t}\n\t\"1\"\n\t{\n\t\t\"path\"\t\t\""+lib+"\"\n\t}\n}\n")

	tm := r.card("truckersmp")
	want := &SteamDoc{CompatTool: "proton-cachyos-slr",
		Forces:    []SteamAppDoc{{227300, "Euro Truck Simulator 2"}, {270880, "American Truck Simulator"}},
		Hooks:     []SteamAppDoc{{227300, "Euro Truck Simulator 2"}, {270880, "American Truck Simulator"}},
		Shortcuts: []ShortcutDoc{}}
	if got, w := mustJSON(t, tm.Steam), mustJSON(t, want); got != w || tm.ModuleOptions {
		t.Fatalf("truckersmp steam = %s, want %s (module options %v)", got, w, tm.ModuleOptions)
	}
	must(t, os.Remove(filepath.Join(lib, "steamapps", "appmanifest_270880.acf")))
	if tm := r.card("truckersmp"); tm.Steam.Forces[1] != (SteamAppDoc{App: 270880}) {
		t.Fatalf("an app without a manifest = %+v", tm.Steam.Forces)
	}
	if p := r.card("proton"); p.Steam != nil || p.ModuleOptions {
		t.Fatalf("proton steam %+v, module options %v", p.Steam, p.ModuleOptions)
	}
	c := r.card("coolercontrol")
	if c.Steam != nil || !c.ModuleOptions || !c.NeedsPassword || !c.Settings[0].NeedsPassword || c.Settings[1].NeedsPassword {
		t.Fatalf("coolercontrol = %+v", c)
	}
	b, _ := json.Marshal(r.doc())
	for _, k := range []string{`"steam":null`, `"module_options":true`, `"skip_once":false`, `"needs_password":true`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("the document has no %s", k)
		}
	}
}

// Adding an extension takes the password when it, or a requirement not
// added yet, runs as root or sets module options.
func TestNeedsPasswordCoversRequirements(t *testing.T) {
	r := newRig(t)
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "fan-profiles.json"), `{"schema":1,"id":"fan-profiles","name":"Fan profiles",
		"summary":"Ready-made fan curves.","category":"app","upstream":{"name":"VaporOS","url":"https://github.com/jasperaelvoet/vaporos","license":"MIT"},
		"requires":["coolercontrol"],"build":{"size":10,"permissions":[]}}`)
	cat := &catalog.Catalog{Entries: []catalog.Entry{{ID: "coolercontrol"}, {ID: "fan-profiles", Requires: []string{"coolercontrol"}}}}
	if r.s.desc("fan-profiles") == nil {
		t.Fatal("the test descriptor does not read")
	}
	if !r.s.needsPassword(cat, map[string]bool{}, "fan-profiles") {
		t.Fatal("no password while CoolerControl is still to be added")
	}
	if r.s.needsPassword(cat, map[string]bool{"coolercontrol": true}, "fan-profiles") {
		t.Fatal("a password once CoolerControl is added")
	}
}

// A wanted extension whose next step will not come by itself needs
// attention, never stays installing: a boot whose tries could not be
// written, one without a report, or a requirement whose image cannot be
// had.
func TestDocumentNoPermanentInstalling(t *testing.T) {
	for _, reason := range []string{store.ReasonTriesWrite, store.ReasonNoReport} {
		r := newRig(t)
		if code, _ := r.do("POST", "/extensions/truckersmp", `{}`); code != 200 {
			t.Fatal("install")
		}
		r.pass()
		if x := r.card("truckersmp"); x.State != StateRestartNeeded {
			t.Fatalf("before = %+v", x)
		}
		if reason == store.ReasonNoReport {
			must(t, os.Remove(config.ExtBootPath()))
		} else {
			rep, err := store.LoadBootReport()
			must(t, err)
			rep.Reason = reason
			r.report(*rep)
		}
		r.s, r.b = r.service()
		r.wire()
		r.pass()
		if x := r.card("truckersmp"); x.State != StateNeedsAttention || x.Reason != notTriedText {
			t.Fatalf("%s: truckersmp = %+v", reason, x)
		}
		if d := r.doc(); d.Restart.Needed {
			t.Fatalf("%s: restart = %+v", reason, d.Restart)
		}
	}

	r := newRig(t)
	must(t, os.Remove(filepath.Join(r.src, "ext-star-citizen.raw")))
	r.seal(r.imgs["sc-hotas"])
	if code, _ := r.do("POST", "/extensions/sc-hotas", `{}`); code != 200 {
		t.Fatal("install")
	}
	r.pass()
	if x := r.card("star-citizen"); x.State != StateNeedsAttention || x.Reason == "" {
		t.Fatalf("star-citizen = %+v", x)
	}
	if x := r.card("sc-hotas"); x.State != StateNeedsAttention || x.Reason != needsText("Star Citizen") {
		t.Fatalf("sc-hotas = %+v", x)
	}
}

// A core extension whose helper failed cannot be removed: its card only
// says to try again.
func TestCoreSetupFailure(t *testing.T) {
	useHelper(t, "proton", &recHelper{installErr: errors.New("boom")})
	r := newRig(t)
	if x := r.card("proton"); x.State != StateNeedsAttention || x.Reason != "Setting up CachyOS Proton didn't finish. Try again." {
		t.Fatalf("proton = %+v", x)
	}
}
