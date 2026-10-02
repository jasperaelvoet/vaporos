package steam

import (
	"bytes"
	"encoding/binary"
	"os"
	"reflect"
	"testing"
)

// kv is a hand-written binary KeyValues entry for the tests' own encoder,
// written apart from the package's so the two check each other.
type kv struct {
	key string
	val any // string, int32 or []kv
}

func bs(k, v string) kv          { return kv{k, v} }
func bi(k string, v int32) kv    { return kv{k, v} }
func bm(k string, kids ...kv) kv { return kv{k, kids} }

func encode(kids []kv) []byte {
	var b []byte
	for _, e := range kids {
		switch v := e.val.(type) {
		case string:
			b = append(append(append(append(b, 0x01), e.key...), 0), v...)
			b = append(b, 0)
		case int32:
			b = append(append(append(b, 0x02), e.key...), 0)
			b = binary.LittleEndian.AppendUint32(b, uint32(v))
		case []kv:
			b = append(append(append(b, 0x00), e.key...), 0)
			b = append(b, encode(v)...)
		}
	}
	return append(b, 0x08)
}

// fixtureSpec is testdata/client/shortcuts.vdf: a launcher added by hand
// and a Lutris game with a key this package does not know ("sortas").
func fixtureSpec() []kv {
	heroic := []kv{
		bi("appid", int32(-182310939)), // 0xf52227e5
		bs("AppName", "Heroic Games Launcher"),
		bs("Exe", `"/var/home/vapor/Applications/Heroic-2.15.2.AppImage"`),
		bs("StartDir", `"/var/home/vapor/Applications/"`),
		bs("icon", "/var/home/vapor/.local/share/icons/heroic.png"),
		bs("ShortcutPath", ""),
		bs("LaunchOptions", "--no-sandbox"),
		bi("IsHidden", 0), bi("AllowDesktopConfig", 1), bi("AllowOverlay", 1), bi("OpenVR", 0), bi("Devkit", 0),
		bs("DevkitGameID", ""), bi("DevkitOverrideAppID", 0), bi("LastPlayTime", 1790400000), bs("FlatpakAppID", ""),
		bm("tags", bs("0", "favorite"), bs("1", "Launchers")),
	}
	lutris := []kv{
		bi("appid", int32(-891995762)), // 0xcad5398e
		bs("AppName", "Star Citizen"),
		bs("Exe", `"/usr/bin/env"`),
		bs("StartDir", `"/var/home/vapor/Games/star-citizen/"`),
		bs("icon", ""), bs("ShortcutPath", ""),
		bs("LaunchOptions", `LUTRIS_SKIP_INIT=1 lutris "lutris:rungameid/3"`),
		bi("IsHidden", 0), bi("AllowDesktopConfig", 1), bi("AllowOverlay", 1), bi("OpenVR", 0), bi("Devkit", 0),
		bs("DevkitGameID", ""), bi("DevkitOverrideAppID", 0), bi("LastPlayTime", 0), bs("FlatpakAppID", ""),
		bs("sortas", "Star Citizen (Lutris)"),
		bm("tags"),
	}
	return []kv{bm("shortcuts", bm("0", heroic...), bm("1", lutris...))}
}

func TestShortcutsFixtureRoundTrip(t *testing.T) {
	data, err := os.ReadFile("testdata/client/shortcuts.vdf")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encode(fixtureSpec()), data) {
		t.Fatal("the committed fixture and its spec differ")
	}
	nodes, err := ParseBinaryVDF(data)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := MarshalBinaryVDF(nodes); err != nil || !bytes.Equal(out, data) {
		t.Errorf("generic round trip differs: %v", err)
	}

	list, err := ParseShortcuts(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("%d shortcuts", len(list))
	}
	h, l := list[0], list[1]
	if h.AppID != 0xf52227e5 || h.AppName != "Heroic Games Launcher" || h.Exe != `"/var/home/vapor/Applications/Heroic-2.15.2.AppImage"` ||
		h.LaunchOptions != "--no-sandbox" || h.AllowOverlay != 1 || h.LastPlayTime != 1790400000 ||
		!reflect.DeepEqual(h.Tags, []string{"favorite", "Launchers"}) || h.Extra != nil {
		t.Errorf("heroic: %+v", h)
	}
	if l.AppID != 0xcad5398e || l.LaunchOptions != `LUTRIS_SKIP_INIT=1 lutris "lutris:rungameid/3"` || l.Tags != nil ||
		len(l.Extra) != 1 || l.Extra[0].Key != "sortas" || l.Extra[0].Str != "Star Citizen (Lutris)" {
		t.Errorf("lutris: %+v", l)
	}
	out, err := MarshalShortcuts(list)
	if err != nil || !bytes.Equal(out, data) {
		t.Errorf("typed round trip differs: %v", err)
	}
}

func TestShortcutsEdit(t *testing.T) {
	data, _ := os.ReadFile("testdata/client/shortcuts.vdf")
	list, err := ParseShortcuts(data)
	if err != nil {
		t.Fatal(err)
	}

	// Change one, drop the other, add one of ours: the kept entry keeps
	// its order (the unknown key stays before tags), the new one gets
	// every key in Steam's order, and the entries are numbered again.
	l := list[1]
	l.LaunchOptions = ShortcutLaunchOptions("star-citizen", "launcher")
	l.Tags = []string{"VaporOS"}
	id := ShortcutAppID("star-citizen", "launcher")
	ours := NewShortcut(id, "Star Citizen", "/var/mnt/games/VaporOS/star-citizen/launcher.sh", "/var/mnt/games/VaporOS/star-citizen", ShortcutLaunchOptions("star-citizen", "launcher"))
	out, err := MarshalShortcuts([]Shortcut{l, ours})
	if err != nil {
		t.Fatal(err)
	}

	spec := fixtureSpec()[0].val.([]kv)[1].val.([]kv)
	spec[6] = bs("LaunchOptions", "/usr/bin/vos ext launch --shortcut star-citizen/launcher %command%")
	spec[17] = bm("tags", bs("0", "VaporOS"))
	want := encode([]kv{bm("shortcuts", bm("0", spec...), bm("1",
		bi("appid", int32(id)), bs("AppName", "Star Citizen"),
		bs("Exe", `"/var/mnt/games/VaporOS/star-citizen/launcher.sh"`), bs("StartDir", `"/var/mnt/games/VaporOS/star-citizen"`),
		bs("icon", ""), bs("ShortcutPath", ""),
		bs("LaunchOptions", "/usr/bin/vos ext launch --shortcut star-citizen/launcher %command%"),
		bi("IsHidden", 0), bi("AllowDesktopConfig", 1), bi("AllowOverlay", 1), bi("OpenVR", 0), bi("Devkit", 0),
		bs("DevkitGameID", ""), bi("DevkitOverrideAppID", 0), bi("LastPlayTime", 0), bs("FlatpakAppID", ""),
		bm("tags"),
	))})
	if !bytes.Equal(out, want) {
		t.Errorf("got  %q\nwant %q", out, want)
	}
	back, err := ParseShortcuts(out)
	if err != nil || back[1].AppID != id {
		t.Fatalf("%v %+v", err, back)
	}
	if owner, key, ok := ShortcutRef(back[1].LaunchOptions); !ok || owner != "star-citizen" || key != "launcher" {
		t.Errorf("ref %q %q %v", owner, key, ok)
	}
}

func TestShortcutsOlderFiles(t *testing.T) {
	// Old Steam wrote lower-case keys and fewer of them. A file like that
	// comes back byte for byte; a key set that it lacked is added.
	entry := []kv{
		bi("appid", -2000000000), bs("appname", "Old Game"), bs("exe", `"/opt/old/run"`), bs("StartDir", `"/opt/old/"`),
		bs("icon", ""), bs("ShortcutPath", ""), bs("LaunchOptions", ""), bi("IsHidden", 0), bi("AllowDesktopConfig", 1),
		bi("OpenVR", 0), bi("LastPlayTime", 0), bm("tags"),
	}
	old := encode([]kv{bm("shortcuts", bm("0", entry...))})
	list, err := ParseShortcuts(old)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].AppName != "Old Game" || list[0].Exe != `"/opt/old/run"` || list[0].AppID != 2294967296 {
		t.Errorf("%+v", list[0])
	}
	if out, err := MarshalShortcuts(list); err != nil || !bytes.Equal(out, old) {
		t.Errorf("round trip: %v\n%q", err, out)
	}
	list[0].AllowOverlay = 1
	out, _ := MarshalShortcuts(list)
	if want := encode([]kv{bm("shortcuts", bm("0", append(entry, bi("AllowOverlay", 1))...))}); !bytes.Equal(out, want) {
		t.Errorf("added key: %q", out)
	}

	// A repeated key is kept where it was.
	dup := encode([]kv{bm("shortcuts", bm("0", bi("appid", 5), bs("AppName", "a"), bs("AppName", "b")))})
	list, err = ParseShortcuts(dup)
	if err != nil || list[0].AppName != "a" || len(list[0].Extra) != 1 {
		t.Fatalf("%v %+v", err, list)
	}
	if out, _ := MarshalShortcuts(list); !bytes.Equal(out, dup) {
		t.Errorf("duplicate: %q", out)
	}
}

func TestShortcutsEmptyAndBroken(t *testing.T) {
	if list, err := ParseShortcuts(nil); err != nil || list != nil {
		t.Errorf("no file: %v %v", list, err)
	}
	empty := []byte("\x00shortcuts\x00\x08\x08")
	if list, err := ParseShortcuts(empty); err != nil || len(list) != 0 {
		t.Errorf("empty: %v %v", list, err)
	}
	if out, err := MarshalShortcuts(nil); err != nil || !bytes.Equal(out, empty) {
		t.Errorf("marshal none: %q %v", out, err)
	}

	data, _ := os.ReadFile("testdata/client/shortcuts.vdf")
	deep := []kv{bs("x", "y")}
	for i := 0; i < maxBinVDFDepth+1; i++ {
		deep = []kv{bm("d", deep...)}
	}
	for name, b := range map[string][]byte{
		"truncated":       data[:len(data)-1],
		"cut in a string": data[:40],
		"cut in an int":   data[:len("\x00shortcuts\x00\x000\x00\x02appid\x00")+2],
		"trailing bytes":  append(append([]byte{}, data...), 0),
		"float type":      []byte("\x00shortcuts\x00\x000\x00\x03f\x00\x00\x00\x00\x00\x08\x08\x08"),
		"too deep":        encode(deep),
		"too large":       make([]byte, maxBinVDFSize+1),
	} {
		if _, err := ParseBinaryVDF(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := ParseShortcuts(b); err == nil {
			t.Errorf("%s: shortcuts accepted", name)
		}
	}
	for name, spec := range map[string][]kv{
		"no shortcuts map": {bm("other")},
		"two top entries":  {bm("shortcuts"), bm("shortcuts")},
		"entry not a map":  {bm("shortcuts", bs("0", "x"))},
		"appid as string":  {bm("shortcuts", bm("0", bs("appid", "1")))},
		"tag not a string": {bm("shortcuts", bm("0", bm("tags", bi("0", 1))))},
	} {
		if _, err := ParseShortcuts(encode(spec)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := MarshalShortcuts([]Shortcut{{AppName: "a\x00b"}}); err == nil {
		t.Error("NUL in a string written")
	}
	if _, err := MarshalBinaryVDF([]*BinNode{{Type: 0x07, Key: "u"}}); err == nil {
		t.Error("unknown type written")
	}
}

func TestShortcutIDs(t *testing.T) {
	// Values from Python's zlib.crc32, not this package.
	for _, c := range []struct {
		owner, key string
		appid      uint32
		gameid     uint64
	}{
		{"star-citizen", "launcher", 3799105208, 16317032622456832000},
		{"truckersmp", "ets2", 2705538558, 11620199624710553600},
	} {
		id := ShortcutAppID(c.owner, c.key)
		if id != c.appid || id&0x80000000 == 0 {
			t.Errorf("%s/%s: appid %d", c.owner, c.key, id)
		}
		g := GameID(id)
		if g != c.gameid || g < 1<<63 {
			t.Errorf("%s/%s: gameid %d", c.owner, c.key, g)
		}
	}
	if u := RunGameURL(3799105208); u != "steam://rungameid/16317032622456832000" {
		t.Errorf("url %q", u)
	}
}
