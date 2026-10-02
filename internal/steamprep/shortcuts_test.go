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
	out, next, removed := decideShortcuts([]steam.Shortcut{heroic, theirs}, true, want, nil, false)
	if len(out) != 3 || out[0].AppName != "Heroic" || out[1].LaunchOptions != "lutris" || len(removed) != 0 {
		t.Errorf("%+v %v", out, removed)
	}
	if next["star-citizen/launcher"].AppID != scApp {
		t.Errorf("%+v", next)
	}
}
