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
	withHelper(t, "truckersmp", settingsHelper{saw: &saw, parts: SteamParts{Beta: map[uint32]BetaRequest{227300: ets2Beta}}})
	fakeGamescope(t, "active")
	return e, &saw
}

// fakeGamescope stands in for gamescope's unit in state, and returns the
// `vos steam prepare` runs vosd makes as vapor.
func fakeGamescope(t *testing.T, state string) <-chan struct{} {
	t.Helper()
	ran := make(chan struct{}, 10)
	st, prep, every, limit := gamescopeState, prepareAsGamer, settleEvery, settleFor
	t.Cleanup(func() { gamescopeState, prepareAsGamer, settleEvery, settleFor = st, prep, every, limit })
	gamescopeState = func(context.Context) string { return state }
	prepareAsGamer = func(context.Context) (string, error) {
		ran <- struct{}{}
		return "vos steam: prepare: done (dispatcher off); nothing needed changing", nil
	}
	settleEvery, settleFor = 5*time.Millisecond, 50*time.Millisecond
	return ran
}

// ets2Beta is TruckersMP asking for the branch its mod supports.
var ets2Beta = BetaRequest{Branch: "temporary_1_53", Request: "1759400000"}

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
	beta := ets2Beta
	want := SteamDesired{Set: "3", Dispatcher: true, DefaultCompatTool: "proton-cachyos-slr",
		Apps: []SteamApp{
			{App: 227300, CompatTool: "proton-cachyos-slr", Hooks: []string{"truckersmp"}, Beta: &beta},
			{App: 270880, CompatTool: "proton-cachyos-slr", Hooks: []string{"truckersmp"}},
		},
		// ETS2 Multiplayer has no target yet, so it is left out.
		Shortcuts: []SteamShortcut{{Owner: "star-citizen", Key: "launcher", Name: "Star Citizen", Exe: scExe, StartDir: scPrefix,
			CompatTool: "proton-cachyos-slr", Art: filepath.Join(config.ExtMountedLibDir, "star-citizen", "art")}},
		Release: []SteamRelease{},
		Owners:  []string{"proton", "star-citizen", "truckersmp"}}
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

	// Only mounted ∩ (wanted ∪ core) count: with nothing wanted, core
	// Proton alone is left, and Star Citizen's shortcut goes too.
	writeFile(t, config.ExtWantedPath(), "")
	e, err := loadSteamEntries()
	must(t, err)
	if d := e.desiredSteam(true); !slices.Equal(e.ids, []string{"proton"}) || len(d.Shortcuts)+len(d.Apps) != 0 {
		t.Errorf("with nothing wanted: ids %q, %+v", e.ids, d)
	}
}

// Only an extension that was wanted or mounted on this box may have set
// something in Steam, so only its apps are released: one in the catalog
// the user never added hands nothing over.
func TestSteamReleaseOnlyWhatVaporOSMayOwn(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	proton := newImage(t, "proton", "", 100, true)
	sc := newImage(t, "star-citizen", "", 100, false, "proton")
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "3", Mounted: mountedAs(proton, sc)})
	writeFile(t, config.ExtWantedPath(), "star-citizen\n")
	if d := loadDesired(t); len(d.Release) != 0 {
		t.Fatalf("TruckersMP was never added, yet: %+v", d.Release)
	}
	_, err := s.SyncSteam()
	must(t, err)
	var owned steamOwned
	must(t, config.ReadJSON(config.ExtSteamOwnedPath(), &owned))
	if !slices.Equal(owned.IDs, []string{"proton", "star-citizen"}) {
		t.Fatalf("steam-owned.json %+v", owned)
	}

	// Added, then removed before it was ever mounted: still its apps go back.
	writeFile(t, config.ExtWantedPath(), "star-citizen\ntruckersmp\n")
	_, err = s.SyncSteam()
	must(t, err)
	writeFile(t, config.ExtWantedPath(), "star-citizen\n")
	_, err = s.SyncSteam()
	must(t, err)
	d, err := readSteamDesired()
	must(t, err)
	if !reflect.DeepEqual(d.Release, []SteamRelease{{227300}, {270880}}) {
		t.Fatalf("release %+v", d.Release)
	}
	must(t, config.ReadJSON(config.ExtSteamOwnedPath(), &owned))
	if !slices.Equal(owned.IDs, []string{"proton", "star-citizen", "truckersmp"}) {
		t.Fatalf("steam-owned.json %+v", owned)
	}

	// A file that is not one owns nothing more than what is wanted or
	// mounted now, and is written anew.
	writeFile(t, config.ExtSteamOwnedPath(), `{"ids":["../x","TRUCKERSMP"]}`)
	if d := loadDesired(t); len(d.Release) != 0 {
		t.Fatalf("release %+v", d.Release)
	}
	_, err = s.SyncSteam()
	must(t, err)
	must(t, config.ReadJSON(config.ExtSteamOwnedPath(), &owned))
	if !slices.Equal(owned.IDs, []string{"proton", "star-citizen"}) {
		t.Fatalf("steam-owned.json %+v", owned)
	}
}

// A branch request goes to steam.json only when prepare would take it.
func TestBetaRequestsAreChecked(t *testing.T) {
	steamBox(t)
	for _, bad := range []BetaRequest{
		{Branch: "temporary_1_53"},
		{Branch: "temporary 1.53", Request: "1"},
		{Branch: "-x", Request: "1"},
		{Branch: "temporary_1_53", Request: "../1"},
		{Branch: "temporary_1_53", Request: strings.Repeat("1", 65)},
	} {
		withHelper(t, "truckersmp", testHelper{steam: SteamParts{Beta: map[uint32]BetaRequest{227300: bad}}})
		if d := loadDesired(t); d.Apps[0].Beta != nil {
			t.Errorf("%+v: listed", bad)
		}
	}
	public := BetaRequest{Branch: "", Request: "2"}
	withHelper(t, "truckersmp", testHelper{steam: SteamParts{Beta: map[uint32]BetaRequest{227300: public, 0: {Branch: "x", Request: "3"}}}})
	if d := loadDesired(t); len(d.Apps) != 2 || d.Apps[0].Beta == nil || *d.Apps[0].Beta != public {
		t.Errorf("the public branch: %+v", d.Apps)
	}
}

func TestSteamDesiredWithNothingMounted(t *testing.T) {
	e, _ := steamBox(t)
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonSkipOnce})
	d := loadDesired(t)
	// Still wanted: nothing is released, prepare keeps what it set.
	if d.Set != "" || len(d.Apps)+len(d.Shortcuts)+len(d.Release) != 0 || d.DefaultCompatTool != "" ||
		!slices.Equal(d.Owners, []string{"proton", "star-citizen", "truckersmp"}) {
		t.Errorf("skip-once: %+v", d)
	}
}

// Owners are the extensions whose shortcuts stay while this boot lists
// none of theirs: wanted ∪ core ∪ mounted, wanted as it is.
func TestSteamOwners(t *testing.T) {
	e, _ := steamBox(t)
	proton := newImage(t, "proton", "", 100, true)
	tmp := newImage(t, "truckersmp", "", 100, false, "proton")

	// A trial with Star Citizen fell back to a set without it: it is
	// still wanted, so its shortcut stays.
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "2", Mounted: mountedAs(proton, tmp)})
	if d := loadDesired(t); len(d.Shortcuts) != 0 || !slices.Equal(d.Owners, []string{"proton", "star-citizen", "truckersmp"}) {
		t.Errorf("fallback: %+v", d)
	}
	// Mounted, with no target for its only shortcut yet: listed nowhere
	// else, owned all the same.
	sc0 := newImage(t, "star-citizen", "", 100, false, "proton")
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "3", Mounted: mountedAs(proton, tmp, sc0)})
	withHelper(t, "star-citizen", testHelper{})
	if d := loadDesired(t); len(d.Shortcuts) != 0 || !slices.Contains(d.Owners, "star-citizen") {
		t.Errorf("no target yet: %+v", d)
	}
	// Removed but mounted until the restart: its entries go at once,
	// while its shortcut waits for the boot that no longer mounts it.
	// One the booted catalog lacks still counts while it is wanted.
	sc := newImage(t, "star-citizen", "", 100, false, "proton")
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "3", Mounted: mountedAs(proton, tmp, sc)})
	writeFile(t, config.ExtWantedPath(), "gone-from-catalog\ntruckersmp\n")
	if d := loadDesired(t); len(d.Shortcuts) != 0 ||
		!slices.Equal(d.Owners, []string{"gone-from-catalog", "proton", "star-citizen", "truckersmp"}) {
		t.Errorf("removed: %+v", d)
	}
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "4", Mounted: mountedAs(proton, tmp)})
	if d := loadDesired(t); !slices.Equal(d.Owners, []string{"gone-from-catalog", "proton", "truckersmp"}) {
		t.Errorf("after the restart: %+v", d.Owners)
	}
	// wanted cannot be read: nothing is known, so prepare removes none.
	must(t, os.Remove(config.ExtWantedPath()))
	must(t, os.Mkdir(config.ExtWantedPath(), 0o755))
	d := loadDesired(t)
	if d.Owners != nil {
		t.Errorf("unknown wanted: %+v", d.Owners)
	}
	b, err := marshalSteamDesired(d)
	must(t, err)
	if !strings.Contains(string(b), `"owners": null`) {
		t.Errorf("steam.json:\n%s", b)
	}
}

// Owners known to be none are [], which prepare reads as "none stays";
// only an unknown wanted gives null.
func TestSteamOwnersNone(t *testing.T) {
	e := newEnv(t)
	e.catalog(newImage(t, "coolercontrol", "", 100, false))
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonSkipOnce})
	writeFile(t, config.ExtWantedPath(), "")
	d := loadDesired(t)
	if d.Owners == nil || len(d.Owners) != 0 {
		t.Fatalf("owners %#v", d.Owners)
	}
	b, err := marshalSteamDesired(d)
	must(t, err)
	if !strings.Contains(string(b), `"owners": []`) {
		t.Errorf("steam.json:\n%s", b)
	}
}

// Owners alone only matter to prepare's cleanup, which the next Steam
// start does anyway: adding an extension that sets nothing in Steam
// writes them and restarts nothing, while removing one with entries in
// Steam still restarts it.
func TestOwnersAloneRestartNothing(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	var restarts []string
	s.SetSteamRestarter(func(reason string) { restarts = append(restarts, reason) })
	if _, err := s.SyncSteam(); err != nil || len(restarts) != 1 {
		t.Fatalf("first sync: %v, restarts %q", err, restarts)
	}
	for _, wanted := range []string{"truckersmp\nstar-citizen\ncoolercontrol\n", "truckersmp\nstar-citizen\n"} {
		writeFile(t, config.ExtWantedPath(), wanted)
		changed, err := s.SyncSteam()
		d, rerr := readSteamDesired()
		if err != nil || rerr != nil || !changed || slices.Contains(d.Owners, "coolercontrol") != strings.Contains(wanted, "coolercontrol") {
			t.Fatalf("wanted %q: changed %v, %v, %v, owners %q", wanted, changed, err, rerr, d.Owners)
		}
		if len(restarts) != 1 {
			t.Fatalf("wanted %q: restarts %q", wanted, restarts)
		}
	}
	writeFile(t, config.ExtWantedPath(), "star-citizen\n")
	if changed, err := s.SyncSteam(); err != nil || !changed || len(restarts) != 2 {
		t.Fatalf("removing TruckersMP: %v %v, restarts %q", changed, err, restarts)
	}
	// A file that is not one restarts Steam as before.
	writeFile(t, config.ExtSteamPath(), "{")
	if changed, err := s.SyncSteam(); err != nil || !changed || len(restarts) != 3 {
		t.Fatalf("over a damaged steam.json: %v %v, restarts %q", changed, err, restarts)
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
	e, _ := steamBox(t)
	s, _ := e.service()
	cat := loadSteamCatalog(t)
	entry := func(e *boot.Entry, err error) {
		slotEntry = func(slot string) (*boot.Entry, error) {
			if slot != "b" {
				t.Fatalf("asked for slot %s", slot)
			}
			return e, err
		}
		s.forgetSlots() // as SlotsChanged does
	}
	slotB := func(version string, exts map[string]manifest.Extension) {
		locked(t, func() error { return store.WriteSlot("b", version, exts) })
	}
	proton := map[string]manifest.Extension{"proton": {Name: "ext-proton.raw", Size: 100, SHA256: strings.Repeat("a", 64), FSVerity: strings.Repeat("b", 64), Core: true}}
	ready := func() bool { return s.dispatcherReady(cat, false) }

	entry(nil, nil)
	if !ready() {
		t.Error("nothing to roll back to: not ready")
	}
	entry(&boot.Entry{Version: otherVersion, Slot: "b"}, nil)
	if ready() {
		t.Error("ready with an image in slot b nobody recorded")
	}
	slotB(otherVersion, map[string]manifest.Extension{})
	if ready() {
		t.Error("ready with an image without extensions in slot b")
	}
	slotB(otherVersion, proton)
	if !ready() {
		t.Error("an image with extensions in slot b: not ready")
	}
	entry(&boot.Entry{Version: "20260701.000000", Slot: "b"}, nil)
	if ready() {
		t.Error("ready with slot b's file for another version")
	}
	entry(nil, errors.New("the ESP is not mounted"))
	if ready() {
		t.Error("ready without the ESP")
	}
	entry(nil, nil)
	cat.Dispatcher = 0
	if ready() {
		t.Error("ready with a catalog without the dispatcher")
	}
	cat.Dispatcher = 1
	writeFile(t, config.ProcCmdline, "quiet\n")
	if ready() {
		t.Error("ready outside a slot")
	}
}

// The minute's check reads the ESP again only when slot b's file or
// update-state.json changed, after 30 minutes or after SlotsChanged, and
// one failed read does not turn the dispatcher off.
func TestDispatcherReadsTheESPSparingly(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	cat := loadSteamCatalog(t)
	clock := time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC)
	savedNow := now
	t.Cleanup(func() { now = savedNow })
	now = func() time.Time { return clock }
	reads := 0
	var fail error
	slotEntry = func(slot string) (*boot.Entry, error) {
		reads++
		return &boot.Entry{Version: otherVersion, Slot: slot}, fail
	}
	slotB(t, true)
	check := func(current, want bool, wantReads int, what string) {
		t.Helper()
		if got := s.dispatcherReady(cat, current); got != want || reads != wantReads {
			t.Errorf("%s: ready %v, %d ESP reads; want %v, %d", what, got, reads, want, wantReads)
		}
	}
	check(false, true, 1, "first check")
	check(true, true, 1, "a minute later")
	clock = clock.Add(29 * time.Minute)
	check(true, true, 1, "29 minutes later")
	writeFile(t, config.UpdateStatePath(), `{"booted":"`+bootedVersion+`"}`)
	check(true, true, 2, "update-state.json written")
	slotB(t, false)
	check(true, false, 3, "slot b's file written")
	clock = clock.Add(31 * time.Minute)
	check(false, false, 4, "after 30 minutes")
	s.forgetSlots()
	check(false, false, 5, "after SlotsChanged")

	// A read that fails keeps what steam.json says; two in a row turn
	// it off, and the next good read counts again.
	slotB(t, true)
	fail = errors.New("the ESP is not mounted")
	s.forgetSlots()
	check(true, true, 6, "one failed read")
	check(true, false, 7, "two failed reads")
	fail = nil
	check(false, true, 8, "the ESP is back")
	fail = errors.New("the ESP is not mounted")
	s.forgetSlots()
	check(true, true, 9, "one failed read again")
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
// start waits for it).
func TestRunWritesSteamJSON(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, func() bool { return exists(config.ExtSteamPath()) })
}

// watching runs WatchSteam on a fast clock until the test ends, and
// returns the topics it published and the Steam restarts it asked for.
func watching(t *testing.T, s *Service, syncs int) (topics func() []string, restarts func() []string) {
	t.Helper()
	var mu sync.Mutex
	var published, asked []string
	s.publish = func(topic string, data any) {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, topic)
	}
	s.SetSteamRestarter(func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, reason)
	})
	savedEvery, savedSyncs := steamWatchEvery, syncEvery
	t.Cleanup(func() { steamWatchEvery, syncEvery = savedEvery, savedSyncs })
	steamWatchEvery, syncEvery = 5*time.Millisecond, syncs
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.WatchSteam(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(published)
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(asked)
		}
}

// WatchSteam runs on its own, without Run: it writes steam.json and
// publishes what `vos ext launch` left.
func TestWatchSteam(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	topics, _ := watching(t, s, 20)
	waitFor(t, func() bool { return exists(config.ExtSteamPath()) })
	must(t, writeMessage(launchRecord{Code: codeNotMounted, ID: "star-citizen", Detail: "shortcut star-citizen/launcher: this boot did not mount it"}))
	waitFor(t, func() bool { return slices.Contains(topics(), "system.message") })
}

// slotB records slot b's image, otherVersion, with Proton or, as an image
// built before extensions has it, with none. Slot b boots otherVersion
// (slotEntry, set before anything runs).
func slotB(t *testing.T, withExtensions bool) {
	t.Helper()
	exts := map[string]manifest.Extension{}
	if withExtensions {
		exts["proton"] = manifest.Extension{Name: "ext-proton.raw", Size: 100, SHA256: strings.Repeat("a", 64), FSVerity: strings.Repeat("b", 64), Core: true}
	}
	locked(t, func() error { return store.WriteSlot("b", otherVersion, exts) })
}

func dispatcherOn() bool {
	d, err := readSteamDesired()
	return err == nil && d.Dispatcher
}

func bootsOtherVersion() {
	slotEntry = func(slot string) (*boot.Entry, error) { return &boot.Entry{Version: otherVersion, Slot: slot}, nil }
}

// Staging a VaporOS built before extensions into the other slot turns the
// dispatcher off at once, and asks for the Steam restart at which prepare
// unwraps the launch options.
func TestSlotsChangedFollowsTheDispatcher(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	bootsOtherVersion()
	slotB(t, true)
	_, restarts := watching(t, s, 1000)
	// The restart is asked for just after the file is written.
	waitFor(t, func() bool { return dispatcherOn() && len(restarts()) > 0 })
	asked := len(restarts())

	slotB(t, false)
	s.SlotsChanged()
	waitFor(t, func() bool { return !dispatcherOn() && len(restarts()) == asked+1 })
}

// The dispatcher turning off while gamescope is down: vosd runs prepare
// as vapor at once, since no Steam start or restart is coming that would.
// While gamescope runs it leaves that to the unit, a unit that never
// settles runs nothing (TestUnwrapWaitsForGamescope has those that do),
// and a dispatcher that stays off runs nothing more.
func TestDispatcherOffRunsPrepare(t *testing.T) {
	for state, runs := range map[string]bool{"inactive": true, "failed": true, "active": false, "activating": false, "deactivating": false, "": false} {
		t.Run(state, func(t *testing.T) {
			e, _ := steamBox(t)
			s, _ := e.service()
			ran := fakeGamescope(t, state)
			bootsOtherVersion()
			slotB(t, true)
			if _, err := s.SyncSteam(); err != nil || !dispatcherOn() {
				t.Fatalf("on: %v", err)
			}
			slotB(t, false)
			if _, err := s.SyncSteam(); err != nil || dispatcherOn() {
				t.Fatalf("off: %v", err)
			}
			wait := 200 * time.Millisecond
			if runs {
				wait = 5 * time.Second
			}
			select {
			case <-ran:
				if !runs {
					t.Fatal("ran prepare")
				}
			case <-time.After(wait):
				if runs {
					t.Fatal("did not run prepare")
				}
			}
			locked(t, func() error { return store.WriteSlot("b", otherVersion, nil) }) // still none
			e.report(store.BootReport{Mode: store.ModeEnabled, Set: "4", Mounted: mountedAs(newImage(t, "proton", "", 100, true))})
			if changed, err := s.SyncSteam(); err != nil || !changed {
				t.Fatalf("again: %v %v", changed, err)
			}
			select {
			case <-ran:
				t.Fatal("ran prepare for a dispatcher that was off already")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// A slot file written behind vosd's back (`vos update` from a shell) is
// caught by the re-check about once a minute.
func TestWatchSteamRechecksTheDispatcher(t *testing.T) {
	e, _ := steamBox(t)
	s, _ := e.service()
	bootsOtherVersion()
	slotB(t, true)
	_, restarts := watching(t, s, 2)
	// The restart is asked for just after the file is written.
	waitFor(t, func() bool { return dispatcherOn() && len(restarts()) > 0 })
	asked := len(restarts())
	slotB(t, false)
	waitFor(t, func() bool { return !dispatcherOn() && len(restarts()) == asked+1 })
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
	return SteamParts{Beta: map[uint32]BetaRequest{227300: {Branch: *h.beta, Request: "1"}}}
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
		`"shares":`, `"actions":[{"name":"switch-branch","label":"Switch to supported version","run_as":"root"}],"shares":`, 1))
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
	if d.Apps[0].App != 227300 || d.Apps[0].Beta == nil || d.Apps[0].Beta.Branch != "temporary_1_61" || len(restarts) != 2 {
		t.Errorf("after the action: %+v, restarts %q", d.Apps, restarts)
	}
}
