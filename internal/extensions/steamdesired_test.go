package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/catalog"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

// The v1 extensions' descriptors as the build ships them (trimmed to what
// Steam needs).
const (
	protonDesc = `{"schema":1,"id":"proton","name":"Proton (CachyOS)","summary":"Steam runs Windows games with it.",
		"category":"runtime","core":true,"upstream":{"name":"CachyOS","url":"https://github.com/CachyOS/proton-cachyos","license":"BSD-3-Clause"},
		"packages":["cachyos/proton-cachyos-slr","cachyos/umu-launcher"],"permissions":["compat-tool"],
		"steam":{"default_compat_tool":"proton-cachyos-slr"},
		"build":{"size":412000000,"permissions":["compat-tool"]}}`
	truckersmpDesc = `{"schema":1,"id":"truckersmp","name":"TruckersMP","summary":"Multiplayer for Euro Truck Simulator 2 and American Truck Simulator.",
		"category":"app","upstream":{"name":"TruckersMP","url":"https://truckersmp.com","license":"MIT"},"requires":["proton"],
		"steam":{"force_compat_tool":[227300,270880],"compat_tool":"proton-cachyos-slr","hooks":[{"apps":[227300,270880]}],
			"shortcuts":[{"key":"ets2-mp","name":"ETS2 Multiplayer"}]},
		"shares":[{"steam_app":227300},{"steam_app":270880}],
		"build":{"size":2000000,"permissions":[]}}`
	starCitizenDesc = `{"schema":1,"id":"star-citizen","name":"Star Citizen","summary":"The RSI Launcher, run with Proton.",
		"category":"app","upstream":{"name":"Roberts Space Industries","url":"https://robertsspaceindustries.com","license":"Proprietary"},
		"requires":["proton"],
		"steam":{"compat_tool":"proton-cachyos-slr","shortcuts":[{"key":"launcher","name":"Star Citizen","compat_tool":true,"art":"art"}]},
		"data":[{"name":"prefix","where":"library","min_free_gb":150,"fs":["ext4","btrfs","xfs","f2fs"]}],
		"settings":[{"key":"library","type":"disk","label":"Game library"},{"key":"tray","type":"bool","label":"Keep the launcher in the tray","default":false}],
		"build":{"size":1000000,"permissions":[]}}`
	coolercontrolDesc = `{"schema":1,"id":"coolercontrol","name":"CoolerControl","summary":"Fan curves for your PC.",
		"category":"system","upstream":{"name":"CoolerControl","url":"https://gitlab.com/coolercontrol/coolercontrol","license":"GPL-3.0-or-later"},
		"build":{"size":30000000,"permissions":[]}}`
)

const (
	scPrefix = "/var/mnt/SATA1TB/VaporOS/star-citizen"
	scExe    = scPrefix + "/RSI Launcher-Setup-2.4.0.exe"
)

// steamBox is the box after adding TruckersMP and Star Citizen: all four
// extensions in the catalog, Proton, TruckersMP and Star Citizen mounted,
// Proton's tool installed, Star Citizen's prefix placed.
func steamBox(t *testing.T) (*env, *[]string) {
	t.Helper()
	e := newEnv(t)
	proton := newImage(t, "proton", "", 100, true)
	tmp := newImage(t, "truckersmp", "", 100, false, "proton")
	sc := newImage(t, "star-citizen", "", 100, false, "proton")
	cc := newImage(t, "coolercontrol", "", 100, false)
	e.catalog(proton, tmp, sc, cc)
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "3", Mounted: mountedAs(proton, tmp, sc)})
	for id, d := range map[string]string{"proton": protonDesc, "truckersmp": truckersmpDesc, "star-citizen": starCitizenDesc, "coolercontrol": coolercontrolDesc} {
		writeFile(t, filepath.Join(config.ExtDescriptorsDir, id+".json"), d)
	}
	writeFile(t, config.ExtWantedPath(), "truckersmp\nstar-citizen\n")
	writeFile(t, filepath.Join(config.CompatToolsDir, "proton-cachyos-slr", "compatibilitytool.vdf"),
		"\"compatibilitytools\"\n{\n  \"compat_tools\"\n  {\n    \"proton-cachyos-slr\" { \"install_path\" \".\" \"display_name\" \"Proton CachyOS (SLR)\" \"from_oslist\" \"windows\" \"to_oslist\" \"linux\" }\n  }\n}\n")
	must(t, os.MkdirAll(filepath.Join(config.ExtMountedLibDir, "star-citizen", "art"), 0o755))
	var saw []string
	withHelper(t, "star-citizen", testHelper{id: "star-citizen", steam: SteamParts{
		Shortcuts: map[string]ShortcutTarget{"launcher": {Exe: scExe, StartDir: scPrefix}},
	}})
	withHelper(t, "truckersmp", settingsHelper{saw: &saw, parts: SteamParts{Beta: map[uint32]string{227300: "temporary_1_53"}}})
	return e, &saw
}

// settingsHelper records the Ext its Steam gets.
type settingsHelper struct {
	NopHelper
	saw   *[]string
	parts SteamParts
}

func (h settingsHelper) Steam(x *Ext) SteamParts {
	*h.saw = append(*h.saw, x.ID+" "+x.DataDir+" "+x.HomeDir)
	return h.parts
}

func loadDesired(t *testing.T) SteamDesired {
	t.Helper()
	e, err := loadSteamEntries()
	must(t, err)
	return e.desiredSteam(true)
}

func TestSteamDesired(t *testing.T) {
	_, saw := steamBox(t)
	beta := "temporary_1_53"
	want := SteamDesired{Set: "3", Dispatcher: true, DefaultCompatTool: "proton-cachyos-slr",
		Apps: []SteamApp{
			{App: 227300, CompatTool: "proton-cachyos-slr", Hooks: []string{"truckersmp"}, Beta: &beta},
			{App: 270880, CompatTool: "proton-cachyos-slr", Hooks: []string{"truckersmp"}},
		},
		// ETS2 Multiplayer has no target yet, so it is left out.
		Shortcuts: []SteamShortcut{{Owner: "star-citizen", Key: "launcher", Name: "Star Citizen", Exe: scExe, StartDir: scPrefix,
			CompatTool: "proton-cachyos-slr", Art: filepath.Join(config.ExtMountedLibDir, "star-citizen", "art")}},
		Release: []SteamRelease{}}
	if got := loadDesired(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("steam.json:\n%+v\nwant\n%+v", got, want)
	}
	wantExt := "truckersmp " + filepath.Join(config.ExtDataDir(), "truckersmp") + " " +
		filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, "truckersmp")
	if !slices.Equal(*saw, []string{wantExt}) {
		t.Errorf("the helper got %q", *saw)
	}

	// No tool, no mapping: Steam would start nothing with it.
	must(t, os.RemoveAll(config.CompatToolsDir))
	d := loadDesired(t)
	if d.DefaultCompatTool != "" || d.Apps[0].CompatTool != "" || d.Apps[1].CompatTool != "" || d.Shortcuts[0].CompatTool != "" {
		t.Errorf("a missing tool is still named: %+v", d)
	}

	// Removing TruckersMP drops its entries at once, though it stays
	// mounted until the restart, and hands ETS2 and ATS back to the user.
	writeFile(t, config.ExtWantedPath(), "star-citizen\n")
	d = loadDesired(t)
	if len(d.Apps) != 0 || len(d.Shortcuts) != 1 || !reflect.DeepEqual(d.Release, []SteamRelease{{227300}, {270880}}) {
		t.Errorf("after removing TruckersMP: %+v", d)
	}
}

func TestSteamDesiredWithNothingMounted(t *testing.T) {
	e, _ := steamBox(t)
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonSkipOnce})
	d := loadDesired(t)
	// Still wanted: nothing is released, prepare keeps what it set.
	if d.Set != "" || len(d.Apps)+len(d.Shortcuts)+len(d.Release) != 0 || d.DefaultCompatTool != "" {
		t.Errorf("skip-once: %+v", d)
	}
}

func TestShortcutTargetsAreChecked(t *testing.T) {
	steamBox(t)
	for _, bad := range []ShortcutTarget{
		{Exe: "RSI Launcher.exe", StartDir: scPrefix},
		{Exe: scExe, StartDir: scPrefix + "/../x"},
		{Exe: scPrefix + "/a\x00b.exe", StartDir: scPrefix},
		{Exe: scPrefix + "/a\nb.exe", StartDir: scPrefix},
		{Exe: "", StartDir: ""},
	} {
		withHelper(t, "star-citizen", testHelper{steam: SteamParts{Shortcuts: map[string]ShortcutTarget{"launcher": bad}}})
		if d := loadDesired(t); len(d.Shortcuts) != 0 {
			t.Errorf("%q in %q: listed", bad.Exe, bad.StartDir)
		}
	}
}

func TestShortcutIDs(t *testing.T) {
	id := ShortcutAppID("star-citizen", "launcher")
	if id&0x80000000 == 0 || id != ShortcutAppID("star-citizen", "launcher") || id == ShortcutAppID("truckersmp", "ets2-mp") {
		t.Fatalf("app id %d", id)
	}
	g := ShortcutGameID(id)
	if g>>32 != uint64(id) || uint32(g) != 0x02000000 || !isShortcutGameID(g) || isShortcutGameID(uint64(id)) {
		t.Fatalf("game id %d", g)
	}
	if g := ShortcutGameID(0xC0705022); g != 0xC070502202000000 {
		t.Fatalf("game id %#x", g)
	}
}

func TestDispatcherReady(t *testing.T) {
	steamBox(t)
	cat := loadSteamCatalog(t)
	entry := func(e *boot.Entry, err error) {
		slotEntry = func(slot string) (*boot.Entry, error) {
			if slot != "b" {
				t.Fatalf("asked for slot %s", slot)
			}
			return e, err
		}
	}
	slotB := func(version string, exts map[string]manifest.Extension) {
		locked(t, func() error { return store.WriteSlot("b", version, exts) })
	}
	proton := map[string]manifest.Extension{"proton": {Name: "ext-proton.raw", Size: 100, SHA256: strings.Repeat("a", 64), FSVerity: strings.Repeat("b", 64), Core: true}}

	entry(nil, nil)
	if !dispatcherReady(cat) {
		t.Error("nothing to roll back to: not ready")
	}
	entry(&boot.Entry{Version: otherVersion, Slot: "b"}, nil)
	if dispatcherReady(cat) {
		t.Error("ready with an image in slot b nobody recorded")
	}
	slotB(otherVersion, map[string]manifest.Extension{})
	if dispatcherReady(cat) {
		t.Error("ready with an image without extensions in slot b")
	}
	slotB(otherVersion, proton)
	if !dispatcherReady(cat) {
		t.Error("an image with extensions in slot b: not ready")
	}
	entry(&boot.Entry{Version: "20260701.000000", Slot: "b"}, nil)
	if dispatcherReady(cat) {
		t.Error("ready with slot b's file for another version")
	}
	entry(nil, errors.New("the ESP is not mounted"))
	if dispatcherReady(cat) {
		t.Error("ready without the ESP")
	}
	entry(nil, nil)
	cat.Dispatcher = 0
	if dispatcherReady(cat) {
		t.Error("ready with a catalog without the dispatcher")
	}
	cat.Dispatcher = 1
	writeFile(t, config.ProcCmdline, "quiet\n")
	if dispatcherReady(cat) {
		t.Error("ready outside a slot")
	}
}

func loadSteamCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	e, err := loadSteamEntries()
	must(t, err)
	return e.catalog
}

func TestSyncSteam(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	var restarts []string
	s.SetSteamRestarter(func(reason string) { restarts = append(restarts, reason) })

	changed, err := s.SyncSteam()
	if err != nil || !changed || len(restarts) != 1 {
		t.Fatalf("first sync: %v %v, restarts %q", changed, err, restarts)
	}
	fi, err := os.Stat(config.ExtSteamPath())
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("steam.json: %v %v", fi, err)
	}
	d, err := readSteamDesired()
	if err != nil || d.Set != "3" || !d.Dispatcher || len(d.Apps) != 2 {
		t.Fatalf("read back %+v %v", d, err)
	}
	b, _ := os.ReadFile(config.ExtSteamPath())
	for _, field := range []string{`"release": []`, `"beta": null`, `"hooks": [`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("steam.json lacks %s:\n%s", field, b)
		}
	}

	// Nothing changed: nothing written, no restart.
	if changed, err := s.SyncSteam(); changed || err != nil || len(restarts) != 1 {
		t.Fatalf("second sync: %v %v, restarts %q", changed, err, restarts)
	}
	// A new boot's set is a change.
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "4", Mounted: mountedAs(newImage(t, "proton", "", 100, true))})
	if changed, err := s.SyncSteam(); !changed || err != nil || len(restarts) != 2 {
		t.Fatalf("new set: %v %v, restarts %q", changed, err, restarts)
	}
}

// The control center's changes reach steam.json at once: TruckersMP,
// removed, leaves Steam though it stays mounted until the restart.
func TestRemoveSyncsSteam(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	s.cc.systemctl = func(context.Context, bool, ...string) error { return nil }
	var restarts []string
	s.SetSteamRestarter(func(reason string) { restarts = append(restarts, reason) })
	if _, err := s.SyncSteam(); err != nil {
		t.Fatal(err)
	}
	must(t, s.Remove(t.Context(), "truckersmp", false))
	d, err := readSteamDesired()
	must(t, err)
	if len(d.Apps) != 0 || !reflect.DeepEqual(d.Release, []SteamRelease{{227300}, {270880}}) || len(restarts) != 2 {
		t.Errorf("after removing TruckersMP: %+v, restarts %q", d, restarts)
	}
}

// Run writes steam.json before its first pass (vosd's first gamescope
// start waits for it), and publishes what `vos ext launch` left.
func TestRunWritesSteamJSON(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	var mu sync.Mutex
	var published []string
	s.publish = func(topic string, data any) {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, topic)
	}
	every := steamWatchEvery
	t.Cleanup(func() { steamWatchEvery = every })
	steamWatchEvery = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, func() bool { return exists(config.ExtSteamPath()) })
	must(t, writeMessage("Star Citizen did not start because its extension is not active right now."))
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(published, "system.message")
	})
}

// A shortcut's arguments reach steam.json as plain words; a target with
// anything else is left out.
func TestShortcutArgs(t *testing.T) {
	steamBox(t)
	home := filepath.Join(config.GamerHome, config.ExtGamerDataSubdir, "truckersmp")
	args := []string{"ext", "truckersmp", "mp", "ets2"}
	withHelper(t, "truckersmp", testHelper{steam: SteamParts{Shortcuts: map[string]ShortcutTarget{
		"ets2-mp": {Exe: "/usr/bin/vos", StartDir: home, Args: args}}}})
	d := loadDesired(t)
	i := slices.IndexFunc(d.Shortcuts, func(s SteamShortcut) bool { return s.Owner == "truckersmp" })
	if i < 0 || !slices.Equal(d.Shortcuts[i].Args, args) || d.Shortcuts[i].Exe != "/usr/bin/vos" || d.Shortcuts[i].CompatTool != "" {
		t.Fatalf("shortcuts %+v", d.Shortcuts)
	}
	for _, bad := range [][]string{{"mp", "ets 2"}, {`"x"`}, {""}, {"a;b"}, make([]string, 17)} {
		withHelper(t, "truckersmp", testHelper{steam: SteamParts{Shortcuts: map[string]ShortcutTarget{
			"ets2-mp": {Exe: "/usr/bin/vos", StartDir: home, Args: bad}}}})
		if d := loadDesired(t); slices.ContainsFunc(d.Shortcuts, func(s SteamShortcut) bool { return s.Owner == "truckersmp" }) {
			t.Errorf("args %q: listed", bad)
		}
	}
}

// branchHelper's Steam parts follow its action, as TruckersMP's branch does.
type branchHelper struct {
	NopHelper
	beta *string
}

func (h branchHelper) Steam(*Ext) SteamParts {
	if *h.beta == "" {
		return SteamParts{}
	}
	return SteamParts{Beta: map[uint32]string{227300: *h.beta}}
}

func (h branchHelper) Action(_ context.Context, _ *Ext, name string, _ json.RawMessage) error {
	*h.beta = "temporary_1_61"
	return nil
}

// An action whose helper changes what it asks of Steam rewrites
// steam.json at once.
func TestActionSyncsSteam(t *testing.T) {
	e, _ := steamBox(t)
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "truckersmp.json"), strings.Replace(truckersmpDesc,
		`"shares":`, `"actions":[{"name":"switch-branch","label":"Switch to supported version"}],"shares":`, 1))
	beta := ""
	withHelper(t, "truckersmp", branchHelper{beta: &beta})
	s, _ := e.service()
	var restarts []string
	s.SetSteamRestarter(func(reason string) { restarts = append(restarts, reason) })
	if _, err := s.SyncSteam(); err != nil {
		t.Fatal(err)
	}
	must(t, s.Action(t.Context(), "truckersmp", "switch-branch", nil))
	d, err := readSteamDesired()
	must(t, err)
	if d.Apps[0].App != 227300 || d.Apps[0].Beta == nil || *d.Apps[0].Beta != "temporary_1_61" || len(restarts) != 2 {
		t.Errorf("after the action: %+v, restarts %q", d.Apps, restarts)
	}
}
