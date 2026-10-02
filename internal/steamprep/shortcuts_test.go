package steamprep

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

var scApp = steam.ShortcutAppID(scOwner, scKey)

func (b *box) grid(acct, appid uint32, suffix string) string {
	return filepath.Join(b.root, "userdata", acctKey(acct), "config", "grid", strconv.FormatUint(uint64(appid), 10)+suffix+".png")
}

func TestShortcutsAdded(t *testing.T) {
	b := newBox(t)
	// The second account has signed in but has no shortcuts yet.
	b.desire(b.starCitizen(proton()))
	before := b.shortcuts(acctA)
	b.run(false)

	list := b.shortcuts(acctA)
	if len(list) != 3 || !reflect.DeepEqual(list[:2], before) {
		t.Fatalf("the user's shortcuts changed or none added: %+v", list)
	}
	sc := list[2]
	want := steam.NewShortcut(scApp, "Star Citizen", "/var/mnt/games/VaporOS/star-citizen/RSI Launcher-Setup-2.3.1.exe",
		"/var/mnt/games/VaporOS/star-citizen", "/usr/bin/vos ext launch --shortcut star-citizen/launcher %command%")
	want.Tags = []string{"VaporOS"}
	if sc.AppID != want.AppID || sc.Exe != want.Exe || sc.StartDir != want.StartDir ||
		sc.LaunchOptions != want.LaunchOptions || !reflect.DeepEqual(sc.Tags, want.Tags) || sc.AllowOverlay != 1 {
		t.Errorf("shortcut %+v", sc)
	}
	if got, ok := b.vaporShortcut(acctB); !ok || got.AppID != scApp {
		t.Errorf("second account: %+v", got)
	}
	if got, _ := b.mapping(scApp); got != oursApp {
		t.Errorf("mapping %+v", got)
	}
	ss := b.state().Shortcuts["52079950"]["star-citizen/launcher"]
	if ss == nil || ss.AppID != scApp || ss.GameID != strconv.FormatUint(steam.GameID(scApp), 10) || ss.Deleted {
		t.Errorf("record %+v", ss)
	}
	for suffix, content := range map[string]string{"p": "capsule", "_hero": "hero"} {
		if data, err := os.ReadFile(b.grid(acctA, scApp, suffix)); err != nil || string(data) != content {
			t.Errorf("art %s: %q %v", suffix, data, err)
		}
	}
	if _, err := os.Stat(b.grid(acctA, scApp, "_logo")); !os.IsNotExist(err) {
		t.Errorf("art the extension does not have: %v", err)
	}

	// Art the user picked stays theirs.
	b.write(b.grid(acctA, scApp, "_hero"), []byte("mine"))
	b.desire(b.starCitizen(proton()))
	b.run(false)
	if data, _ := os.ReadFile(b.grid(acctA, scApp, "_hero")); string(data) != "mine" {
		t.Errorf("user's art replaced: %q", data)
	}
}

func TestShortcutAppIDReadBack(t *testing.T) {
	b := newBox(t)
	b.desire(b.starCitizen(proton()))
	b.run(false)
	// Steam gives the shortcut another app id (and the user renames it):
	// it is still VaporOS's, found by its launch options.
	const other = 0x9abcdef0
	b.edit("userdata/52079950/config/shortcuts.vdf", func(d []byte) []byte {
		list, err := steam.ParseShortcuts(d)
		b.check(err)
		list[2].AppID, list[2].AppName = other, "SC"
		out, err := steam.MarshalShortcuts(list)
		b.check(err)
		return out
	})
	b.run(false)
	sc, ok := b.vaporShortcut(acctA)
	if !ok || sc.AppID != other || sc.AppName != "Star Citizen" || len(b.shortcuts(acctA)) != 3 {
		t.Fatalf("%+v %v", sc, ok)
	}
	if ss := b.state().Shortcuts["52079950"]["star-citizen/launcher"]; ss.AppID != other ||
		ss.GameID != strconv.FormatUint(uint64(other)<<32|0x02000000, 10) {
		t.Errorf("record %+v", ss)
	}
	if got, _ := b.mapping(other); got != oursApp {
		t.Errorf("mapping of the new id %+v", got)
	}
	if got, _ := b.mapping(scApp); got != oursApp {
		t.Errorf("the other account's id lost its mapping: %+v", got)
	}
}

func TestDeletedShortcutStaysDeleted(t *testing.T) {
	b := newBox(t)
	b.desire(b.starCitizen(proton()))
	b.run(false)
	// The user removes it in Steam on one account.
	b.edit("userdata/52079950/config/shortcuts.vdf", func(d []byte) []byte {
		list, err := steam.ParseShortcuts(d)
		b.check(err)
		out, err := steam.MarshalShortcuts(list[:2])
		b.check(err)
		return out
	})
	for range 2 {
		b.run(false)
		if sc, ok := b.vaporShortcut(acctA); ok {
			t.Fatalf("added again: %+v", sc)
		}
	}
	if ss := b.state().Shortcuts["52079950"]["star-citizen/launcher"]; ss == nil || !ss.Deleted {
		t.Errorf("record %+v", ss)
	}
	if _, ok := b.vaporShortcut(acctB); !ok {
		t.Error("the other account's went too")
	}

	// A shortcuts.vdf that is gone altogether (Steam's data was reset) is
	// not a choice: the shortcut comes back.
	b.check(os.Remove(filepath.Join(b.root, "userdata", "127388593", "config", "shortcuts.vdf")))
	b.run(false)
	if _, ok := b.vaporShortcut(acctB); !ok {
		t.Error("not added to a new shortcuts.vdf")
	}
}

func TestShortcutsGoWithTheirExtension(t *testing.T) {
	b := newBox(t)
	orig := b.steamFile("userdata/52079950/config/shortcuts.vdf")
	cfg := b.steamFile("config/config.vdf")
	b.desire(b.starCitizen(proton()))
	b.run(false)
	b.desire(Desired{Dispatcher: true}) // Star Citizen removed, Proton not mounted
	b.removeTool(tool)
	b.run(false)
	if got := b.steamFile("userdata/52079950/config/shortcuts.vdf"); string(got) != string(orig) {
		t.Error("shortcuts.vdf not as before")
	}
	if _, ok := b.vaporShortcut(acctB); ok {
		t.Error("second account keeps it")
	}
	if got := b.steamFile("config/config.vdf"); string(got) != string(cfg) {
		t.Errorf("config.vdf not as before:\n%s", got)
	}
	if _, err := os.Stat(b.grid(acctA, scApp, "p")); !os.IsNotExist(err) {
		t.Errorf("art left: %v", err)
	}
	st := b.state()
	if len(st.Shortcuts) != 0 || st.peekApp(scApp) != nil {
		t.Errorf("records left: %+v %+v", st.Shortcuts, st.peekApp(scApp))
	}
}

func TestUnwrapAndBack(t *testing.T) {
	b := newBox(t)
	full := b.starCitizen(truckers(proton()))
	b.desire(full)
	cfg := b.steamFile("config/config.vdf")
	lcA := b.steamFile("userdata/52079950/config/localconfig.vdf")
	b.run(false)

	// Before booting a VaporOS without the dispatcher.
	b.run(true)
	if got := b.steamFile("config/config.vdf"); string(got) != string(cfg) {
		t.Errorf("config.vdf:\n%s", got)
	}
	if got := b.steamFile("userdata/52079950/config/localconfig.vdf"); string(got) != string(lcA) {
		t.Errorf("localconfig.vdf:\n%s", got)
	}
	list := b.shortcuts(acctA)
	if len(list) != 3 || list[2].AppID != scApp || list[2].LaunchOptions != "%command%" {
		t.Errorf("shortcut after unwrap: %+v", list)
	}
	st := b.state()
	if !st.Default.Suspended || !st.peekApp(ets2).Mapping.Suspended {
		t.Errorf("records %+v %+v", st.Default, st.peekApp(ets2).Mapping)
	}

	// Back on a VaporOS with it: everything as it was, nothing twice.
	b.run(false)
	if got, _ := b.mapping(0); got != ours {
		t.Errorf("default %+v", got)
	}
	if o, _ := b.launchOptions(acctA, ets2); o != tokenETS2+"%command% -nointro -64bit" {
		t.Errorf("ETS2 %q", o)
	}
	sc, ok := b.vaporShortcut(acctA)
	if !ok || sc.AppID != scApp || len(b.shortcuts(acctA)) != 3 {
		t.Errorf("shortcut %+v %v", sc, ok)
	}
	if got, _ := b.mapping(scApp); got != oursApp {
		t.Errorf("shortcut mapping %+v", got)
	}
}

func TestUnreadableShortcutsKeepTheirMapping(t *testing.T) {
	b := newBox(t)
	b.desire(b.starCitizen(proton()))
	b.run(false)
	// The second account's shortcut is gone and the first's file broken:
	// whether the shortcut still exists is not known, so its mapping stays.
	b.check(os.Remove(filepath.Join(b.root, "userdata", "127388593", "config", "shortcuts.vdf")))
	b.check(os.RemoveAll(filepath.Join(b.root, "userdata", "127388593", "config")))
	b.edit("userdata/52079950/config/shortcuts.vdf", func(d []byte) []byte { return d[:len(d)-3] })
	broken := b.steamFile("userdata/52079950/config/shortcuts.vdf")
	b.run(false)
	if got := b.steamFile("userdata/52079950/config/shortcuts.vdf"); string(got) != string(broken) {
		t.Error("a shortcuts.vdf that does not parse was changed")
	}
	if got, _ := b.mapping(scApp); got != oursApp {
		t.Errorf("mapping %+v", got)
	}
	if !b.state().peekApp(scApp).Mapping.owned() {
		t.Error("mapping no longer owned")
	}
}

func TestDecideShortcutsKeepsTheUsers(t *testing.T) {
	heroic := steam.NewShortcut(0x81234567, "Heroic", "/heroic", "/", "")
	theirs := steam.NewShortcut(0x87654321, "Star Citizen", "/usr/bin/env", "/", "lutris")
	want := []Shortcut{{Owner: scOwner, Key: scKey, Name: "Star Citizen", Exe: "/x/setup.exe", StartDir: "/x"}}
	res := decideShortcuts(shortcutInput{list: []steam.Shortcut{heroic, theirs}, existed: true, want: want,
		keep: func(string) bool { return true }})
	if len(res.list) != 3 || res.list[0].AppName != "Heroic" || res.list[1].LaunchOptions != "lutris" || len(res.removed) != 0 {
		t.Errorf("%+v %v", res.list, res.removed)
	}
	if res.next["star-citizen/launcher"].AppID != scApp {
		t.Errorf("%+v", res.next)
	}
}

// A shortcut's args follow %command% in its launch options, which still
// name it; --unwrap leaves them after %command%.
func TestShortcutArgs(t *testing.T) {
	want := []Shortcut{{Owner: "truckersmp", Key: "ets2", Name: "TruckersMP (ETS2)", Exe: "/usr/bin/vos", StartDir: "/home",
		Args: []string{"ext", "truckersmp", "mp", "ets2"}}}
	res := decideShortcuts(shortcutInput{want: want, keep: func(string) bool { return true }})
	opts := "/usr/bin/vos ext launch --shortcut truckersmp/ets2 %command% ext truckersmp mp ets2"
	if len(res.list) != 1 || res.list[0].LaunchOptions != opts {
		t.Fatalf("%+v", res.list)
	}
	if owner, key, ok := steam.ShortcutRef(opts); !ok || owner != "truckersmp" || key != "ets2" {
		t.Errorf("ref %q %q %v", owner, key, ok)
	}
	again := decideShortcuts(shortcutInput{list: res.list, existed: true, want: want, st: res.next, keep: func(string) bool { return true }})
	if len(again.list) != 1 || again.list[0].LaunchOptions != opts {
		t.Errorf("a second run: %+v", again.list)
	}
	un := decideShortcuts(shortcutInput{list: again.list, existed: true, st: again.next, unwrap: true})
	if len(un.list) != 1 || un.list[0].LaunchOptions != "%command% ext truckersmp mp ets2" {
		t.Errorf("unwrapped: %+v", un.list)
	}
}

// listStarCitizen is steam.json without the Star Citizen shortcut, with
// its extension still named (a hook of its own), as when its install has
// no target for the shortcut yet.
func listStarCitizen(d Desired, listed bool) Desired {
	if !listed {
		d.Shortcuts = nil
		d.Apps = append(d.Apps, AppWant{App: 12345, Hooks: []string{scOwner}})
	}
	return d
}

func TestShortcutMissingOneRun(t *testing.T) {
	b := newBox(t)
	b.desire(b.starCitizen(proton()))
	b.run(false)
	before := b.steamFile("userdata/52079950/config/shortcuts.vdf")

	// Not listed for a run while its extension is still named: nothing
	// is taken out, and nothing is added again after.
	b.desire(listStarCitizen(b.starCitizen(proton()), false))
	b.run(false)
	if got := b.steamFile("userdata/52079950/config/shortcuts.vdf"); string(got) != string(before) {
		t.Error("shortcuts.vdf changed")
	}
	if got, _ := b.mapping(scApp); got != oursApp {
		t.Errorf("mapping %+v", got)
	}
	if _, err := os.Stat(b.grid(acctA, scApp, "p")); err != nil {
		t.Errorf("art: %v", err)
	}
	if ss := b.state().Shortcuts["52079950"]["star-citizen/launcher"]; ss == nil || ss.AppID != scApp || ss.Deleted {
		t.Errorf("record %+v", ss)
	}
	// Meanwhile the tool goes: no mapping to it is left behind.
	b.removeTool(tool)
	b.run(false)
	if _, ok := b.mapping(scApp); ok {
		t.Error("mapped to a missing tool")
	}
	b.installTool(tool)

	b.desire(b.starCitizen(proton()))
	b.run(false)
	if got := b.steamFile("userdata/52079950/config/shortcuts.vdf"); string(got) != string(before) {
		t.Error("shortcuts.vdf changed when listed again")
	}
	if got, _ := b.mapping(scApp); got != oursApp {
		t.Errorf("mapping when listed again %+v", got)
	}
}

func TestDeletedShortcutStaysDeletedAcrossAnAbsence(t *testing.T) {
	for name, absent := range map[string]Desired{
		"extension still named": listStarCitizen(proton(), false),
		"extension not named":   proton(),
	} {
		t.Run(name, func(t *testing.T) {
			b := newBox(t)
			b.desire(b.starCitizen(proton()))
			b.run(false)
			b.edit("userdata/52079950/config/shortcuts.vdf", func(d []byte) []byte {
				list, err := steam.ParseShortcuts(d)
				b.check(err)
				out, err := steam.MarshalShortcuts(list[:2])
				b.check(err)
				return out
			})
			b.run(false) // the user deleted it

			b.desire(absent)
			b.run(false)
			if ss := b.state().Shortcuts["52079950"]["star-citizen/launcher"]; ss == nil || !ss.Deleted {
				t.Errorf("record while absent %+v", ss)
			}
			b.desire(b.starCitizen(proton()))
			b.run(false)
			if sc, ok := b.vaporShortcut(acctA); ok {
				t.Errorf("added again: %+v", sc)
			}
			if ss := b.state().Shortcuts["52079950"]["star-citizen/launcher"]; ss == nil || !ss.Deleted {
				t.Errorf("record %+v", ss)
			}
		})
	}
}

func TestShortcutArtAndIcon(t *testing.T) {
	b := newBox(t)
	d := b.starCitizen(proton())
	b.write(filepath.Join(d.Shortcuts[0].Art, "capsule-wide.png"), []byte("wide"))
	b.write(filepath.Join(d.Shortcuts[0].Art, "icon.png"), []byte("icon"))
	b.desire(d)
	b.run(false)
	for suffix, content := range map[string]string{"": "wide", "_icon": "icon", "p": "capsule"} {
		if data, err := os.ReadFile(b.grid(acctA, scApp, suffix)); err != nil || string(data) != content {
			t.Errorf("art %q: %q %v", suffix, data, err)
		}
	}
	sc, _ := b.vaporShortcut(acctA)
	if sc.Icon != b.grid(acctA, scApp, "_icon") {
		t.Errorf("icon %q", sc.Icon)
	}
	if sc, _ := b.vaporShortcut(acctB); sc.Icon != b.grid(acctB, scApp, "_icon") {
		t.Errorf("second account's icon %q", sc.Icon)
	}

	// An icon the user picked stays.
	b.edit("userdata/52079950/config/shortcuts.vdf", func(data []byte) []byte {
		list, err := steam.ParseShortcuts(data)
		b.check(err)
		list[2].Icon = "/home/mine.png"
		out, err := steam.MarshalShortcuts(list)
		b.check(err)
		return out
	})
	b.run(false)
	if sc, _ := b.vaporShortcut(acctA); sc.Icon != "/home/mine.png" {
		t.Errorf("user's icon replaced: %q", sc.Icon)
	}

	// All of it goes with the shortcut.
	b.desire(proton())
	b.run(false)
	for _, suffix := range []string{"", "_icon", "p", "_hero"} {
		if _, err := os.Stat(b.grid(acctA, scApp, suffix)); !os.IsNotExist(err) {
			t.Errorf("art %q left: %v", suffix, err)
		}
	}
}

func TestUnknownAccountsKeepShortcutMappings(t *testing.T) {
	for name, setup := range map[string]func(b *box){
		"loginusers.vdf broken": func(b *box) {
			b.edit("config/loginusers.vdf", func(d []byte) []byte { return d[:len(d)-3] })
		},
		"account signed out": func(b *box) {
			b.edit("config/loginusers.vdf", func([]byte) []byte { return []byte("\"users\"\n{\n}\n") })
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBox(t)
			b.desire(b.starCitizen(proton()))
			b.run(false)
			setup(b)
			b.desire(proton()) // Star Citizen removed
			b.run(false)
			if got, _ := b.mapping(scApp); got != oursApp {
				t.Errorf("mapping %+v", got)
			}
			if !b.state().peekApp(scApp).Mapping.owned() {
				t.Error("mapping no longer owned")
			}
		})
	}
}

// shortcutKept checks that the Star Citizen shortcut is as the first run
// left it: in shortcuts.vdf, recorded, mapped and with its art.
func (b *box) shortcutKept(t *testing.T, vdf []byte) {
	t.Helper()
	if got := b.steamFile("userdata/52079950/config/shortcuts.vdf"); string(got) != string(vdf) {
		t.Error("shortcuts.vdf changed")
	}
	if _, ok := b.vaporShortcut(acctB); !ok {
		t.Error("the second account's went")
	}
	if ss := b.state().Shortcuts["52079950"]["star-citizen/launcher"]; ss == nil || ss.AppID != scApp || ss.Deleted || len(ss.Art) != 2 {
		t.Errorf("record %+v", ss)
	}
	if got, _ := b.mapping(scApp); got != oursApp || !b.state().peekApp(scApp).Mapping.owned() {
		t.Errorf("mapping %+v", got)
	}
	if _, err := os.Stat(b.grid(acctA, scApp, "p")); err != nil {
		t.Errorf("art: %v", err)
	}
}

// A boot without extensions (vos.ext=0, starting once without them)
// lists no shortcut and says nothing about which stay: none goes, even
// when steam.json owns no extension, and the next boot adds none again.
func TestShortcutsStayOnABootWithoutExtensions(t *testing.T) {
	for _, reason := range []string{"cmdline", "skip-once"} {
		t.Run(reason, func(t *testing.T) {
			b := newBox(t)
			b.desire(b.starCitizen(proton()))
			b.run(false)
			vdf := b.steamFile("userdata/52079950/config/shortcuts.vdf")

			b.bootWithoutExtensions(reason)
			b.desireExact(Desired{Dispatcher: true, Owners: []string{}})
			b.run(false)
			b.shortcutKept(t, vdf)

			b.bootReport("5")
			b.desire(Desired{Set: "5", Dispatcher: true, DefaultCompatTool: tool, Shortcuts: b.starCitizen(proton()).Shortcuts})
			b.run(false)
			b.shortcutKept(t, vdf)
		})
	}
}

// While steam.json owns its extension, an unlisted shortcut stays: a
// trial with Star Citizen that fell back to a set without it, or Star
// Citizen mounted with no target for its shortcut yet (steam.json is
// the same for both). Once it is owned no more, it goes with its record,
// mapping and the art VaporOS wrote; art the user put there stays.
func TestShortcutsStayWhileOwned(t *testing.T) {
	for name, owners := range map[string][]string{
		"owned":          {"proton", "star-citizen"},
		"owners unknown": nil,
	} {
		t.Run(name, func(t *testing.T) {
			b := newBox(t)
			b.desire(b.starCitizen(proton()))
			b.run(false)
			vdf := b.steamFile("userdata/52079950/config/shortcuts.vdf")
			d := proton()
			d.Set, d.Owners = theSet, owners
			b.desireExact(d)
			b.run(false)
			b.shortcutKept(t, vdf)
		})
	}

	b := newBox(t)
	orig := b.steamFile("userdata/52079950/config/shortcuts.vdf")
	b.desire(b.starCitizen(proton()))
	b.run(false)
	b.write(b.grid(acctA, scApp, "_hero"), []byte("the user's own"))
	b.write(b.grid(acctA, scApp, "_logo"), []byte("the user's too"))
	b.desire(proton()) // owns Proton alone
	b.run(false)
	if got := b.steamFile("userdata/52079950/config/shortcuts.vdf"); string(got) != string(orig) {
		t.Error("shortcut left")
	}
	if _, ok := b.mapping(scApp); ok || b.state().peekApp(scApp) != nil || len(b.state().Shortcuts) != 0 {
		t.Errorf("mapping or records left: %+v", b.state().Shortcuts)
	}
	if _, err := os.Stat(b.grid(acctA, scApp, "p")); !os.IsNotExist(err) {
		t.Errorf("VaporOS's art left: %v", err)
	}
	for _, suffix := range []string{"_hero", "_logo"} {
		if _, err := os.Stat(b.grid(acctA, scApp, suffix)); err != nil {
			t.Errorf("the user's art %s went: %v", suffix, err)
		}
	}
}

// An existing VaporOS shortcut without an icon gets the extension's, at
// the app id Steam keeps for it; one with an icon keeps it
// (TestShortcutArtAndIcon).
func TestIconForAnExistingShortcut(t *testing.T) {
	b := newBox(t)
	d := b.starCitizen(proton())
	b.desire(d)
	b.run(false)
	if sc, _ := b.vaporShortcut(acctA); sc.Icon != "" {
		t.Fatalf("icon without icon.png: %q", sc.Icon)
	}
	const other = 0x9abcdef0
	b.edit("userdata/52079950/config/shortcuts.vdf", func(data []byte) []byte {
		list, err := steam.ParseShortcuts(data)
		b.check(err)
		list[2].AppID = other
		out, err := steam.MarshalShortcuts(list)
		b.check(err)
		return out
	})
	b.write(filepath.Join(d.Shortcuts[0].Art, iconArt), []byte("icon"))
	b.run(false)
	sc, _ := b.vaporShortcut(acctA)
	if sc.Icon != b.grid(acctA, other, "_icon") || sc.AppID != other || sc.AppName != "Star Citizen" {
		t.Errorf("icon %q, app id %d, name %q", sc.Icon, sc.AppID, sc.AppName)
	}
	if data, err := os.ReadFile(b.grid(acctA, other, "_icon")); err != nil || string(data) != "icon" {
		t.Errorf("icon file %q %v", data, err)
	}
	ss := b.state().Shortcuts["52079950"]["star-citizen/launcher"]
	if ss == nil || ss.Art[strconv.FormatUint(other, 10)+"_icon.png"].Size != 4 {
		t.Errorf("record %+v", ss)
	}
}
