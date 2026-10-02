package steam

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompatToolMapping(t *testing.T) {
	orig := readFixture(t, "config.vdf")
	for app, want := range map[uint32]CompatTool{
		0:       {Name: "proton_experimental", Priority: "75"},
		1091500: {Name: "proton_9", Priority: "250"},
		2483190: {Priority: "250"},
	} {
		got, ok, err := CompatToolMapping([]byte(orig), app)
		if err != nil || !ok || got != want {
			t.Errorf("%d: %+v %v %v", app, got, ok, err)
		}
	}
	if _, ok, err := CompatToolMapping([]byte(orig), 227300); ok || err != nil {
		t.Errorf("227300: %v %v", ok, err)
	}

	// The default: only the name's value changes.
	out, changed, err := SetCompatToolMapping([]byte(orig), 0, CompatTool{Name: "proton-cachyos-slr", Priority: "75"})
	if err != nil || !changed {
		t.Fatalf("default: %v %v", changed, err)
	}
	if want := replaceOnce(t, orig, `"proton_experimental"`, `"proton-cachyos-slr"`); string(out) != want {
		t.Errorf("default:\n%s", out)
	}

	// A new entry goes last, laid out as Steam writes one; removing it
	// gives back the original bytes.
	sc := CompatTool{Name: "proton-cachyos-slr", Priority: "250"}
	out, _, err = SetCompatToolMapping([]byte(orig), 227300, sc)
	if err != nil {
		t.Fatal(err)
	}
	entry := "\t\t\t\t\t\"227300\"\n\t\t\t\t\t{\n\t\t\t\t\t\t\"name\"\t\t\"proton-cachyos-slr\"\n\t\t\t\t\t\t\"config\"\t\t\"\"\n\t\t\t\t\t\t\"priority\"\t\t\"250\"\n\t\t\t\t\t}\n"
	anchor := "\t\t\t\t}\n\t\t\t\t\"DownloadThrottleKbps\""
	if want := replaceOnce(t, orig, anchor, entry+anchor); string(out) != want {
		t.Errorf("new entry:\n%s", out)
	}
	if got, ok, _ := CompatToolMapping(out, 227300); !ok || got != sc {
		t.Errorf("read back %+v", got)
	}
	if again, changed, _ := SetCompatToolMapping(out, 227300, sc); changed || string(again) != string(out) {
		t.Error("setting the same entry changed the file")
	}
	back, changed, err := DeleteCompatToolMapping(out, 227300)
	if err != nil || !changed || string(back) != orig {
		t.Errorf("delete: %v %v\n%s", changed, err, back)
	}

	// A shortcut's entry is keyed by its unsigned app id.
	out, _, _ = SetCompatToolMapping([]byte(orig), ShortcutAppID("star-citizen", "launcher"), sc)
	if !strings.Contains(string(out), "\t\t\t\t\t\"3799105208\"\n") {
		t.Errorf("shortcut key:\n%s", out)
	}
}

// A batch on one parse gives what the same edits one at a time give, a
// delete of an entry the batch itself set (also in blocks it adds)
// included.
func TestConfigVDFBatch(t *testing.T) {
	orig := readFixture(t, "config.vdf")
	noBlock := "\"InstallConfigStore\"\n{\n\t\"Software\"\n\t{\n\t\t\"Valve\"\n\t\t{\n\t\t\t\"Steam\"\n\t\t\t{\n\t\t\t}\n\t\t}\n\t}\n}\n"
	sc := CompatTool{Name: "proton-cachyos-slr", Priority: "250"}
	type edit struct {
		app uint32
		set *CompatTool // nil: delete
	}
	for name, c := range map[string]struct {
		data  string
		edits []edit
	}{
		"set then delete":           {orig, []edit{{227300, &sc}, {227300, nil}}},
		"delete then set":           {orig, []edit{{1091500, nil}, {1091500, &sc}}},
		"in a block the batch adds": {noBlock, []edit{{227300, &sc}, {270880, &sc}, {227300, nil}}},
		"only to delete again":      {noBlock, []edit{{227300, &sc}, {227300, nil}}},
		"the default and another":   {orig, []edit{{0, &sc}, {2483190, nil}, {270880, &sc}}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := ParseConfigVDF([]byte(c.data))
			if err != nil {
				t.Fatal(err)
			}
			want := []byte(c.data)
			for _, e := range c.edits {
				if e.set != nil {
					err = cfg.SetCompatToolMapping(e.app, *e.set)
					want, _, _ = SetCompatToolMapping(want, e.app, *e.set)
				} else {
					err = cfg.DeleteCompatToolMapping(e.app)
					want, _, _ = DeleteCompatToolMapping(want, e.app)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			got, changed, err := cfg.Bytes()
			if err != nil || string(got) != string(want) || changed != (string(want) != c.data) {
				t.Errorf("changed %v, %v:\n%s\nwant:\n%s", changed, err, got, want)
			}
		})
	}
	// What a read gives is the file as parsed.
	cfg, err := ParseConfigVDF([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := cfg.CompatToolMapping(1091500); !ok || got.Name != "proton_9" {
		t.Errorf("read %+v %v", got, ok)
	}
}

func TestCompatToolMappingOtherCasing(t *testing.T) {
	// Older config.vdf files say "valve" and "Priority", and an entry may
	// carry keys of its own; an update keeps all of that.
	orig := readFixture(t, "config.vdf")
	orig = replaceOnce(t, orig, "\t\t\"Valve\"\n", "\t\t\"valve\"\n")
	orig = replaceOnce(t, orig, "\"proton_9\"\n\t\t\t\t\t\t\"config\"\t\t\"\"\n\t\t\t\t\t\t\"priority\"",
		"\"proton_9\"\n\t\t\t\t\t\t\"config\"\t\t\"\"\n\t\t\t\t\t\t\"extra\"\t\t\"1\"\n\t\t\t\t\t\t\"Priority\"")
	got, ok, err := CompatToolMapping([]byte(orig), 1091500)
	if err != nil || !ok || got != (CompatTool{Name: "proton_9", Priority: "250"}) {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	out, _, err := SetCompatToolMapping([]byte(orig), 1091500, CompatTool{Name: "proton-cachyos-slr", Config: "noesync", Priority: "251"})
	if err != nil {
		t.Fatal(err)
	}
	want := replaceOnce(t, orig, "\"proton_9\"\n\t\t\t\t\t\t\"config\"\t\t\"\"\n\t\t\t\t\t\t\"extra\"\t\t\"1\"\n\t\t\t\t\t\t\"Priority\"\t\t\"250\"",
		"\"proton-cachyos-slr\"\n\t\t\t\t\t\t\"config\"\t\t\"noesync\"\n\t\t\t\t\t\t\"extra\"\t\t\"1\"\n\t\t\t\t\t\t\"Priority\"\t\t\"251\"")
	if string(out) != want {
		t.Errorf("got:\n%s", out)
	}
}

func TestLaunchOptionsInLocalConfig(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")
	v, ok, err := LaunchOptions([]byte(orig), 1091500)
	if err != nil || !ok || v != `PROTON_LOG=1 WINEDLLOVERRIDES="dxgi=n,b" %command% -skipStartScreen` {
		t.Fatalf("%q %v %v", v, ok, err)
	}
	if _, ok, _ := LaunchOptions([]byte(orig), 949230); ok {
		t.Error("949230 has none")
	}

	wrapped, _ := WrapLaunchOptions(v, 1091500)
	out, changed, err := SetLaunchOptions([]byte(orig), 1091500, wrapped)
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	want := replaceOnce(t, orig, `"PROTON_LOG=1 WINEDLLOVERRIDES=\"dxgi=n,b\" %command% -skipStartScreen"`,
		`"PROTON_LOG=1 WINEDLLOVERRIDES=\"dxgi=n,b\" /usr/bin/vos ext launch --app 1091500 %command% -skipStartScreen"`)
	if string(out) != want {
		t.Errorf("got:\n%s", out)
	}
	if got, _, _ := LaunchOptions(out, 1091500); UnwrapLaunchOptions(got) != v {
		t.Errorf("unwrap after a round trip = %q", got)
	}

	// The "Apps" spelling some files have is the same block.
	apps := replaceOnce(t, orig, "\t\t\t\t\"apps\"\n", "\t\t\t\t\"Apps\"\n")
	out, _, err = SetLaunchOptions([]byte(apps), 227300, "-x")
	if err != nil || string(out) != replaceOnce(t, apps, `"-nointro -64bit"`, `"-x"`) {
		t.Errorf("Apps: %v\n%s", err, out)
	}
	out, changed, err = DeleteLaunchOptions([]byte(apps), 227300)
	if err != nil || !changed || strings.Contains(string(out), "-nointro") {
		t.Errorf("delete: %v %v", changed, err)
	}
}

func TestBetaKey(t *testing.T) {
	orig := readFixture(t, "appmanifest_227300.acf")
	if b, ok, err := BetaKey([]byte(orig)); err != nil || !ok || b != "temporary_1_53" {
		t.Fatalf("%q %v %v", b, ok, err)
	}
	user := "\"UserConfig\"\n\t{\n\t\t\"language\"\t\t\"english\"\n\t\t\"BetaKey\"\t\t\"temporary_1_53\"\n\t}"

	// Only UserConfig changes: MountedConfig is what Steam installed.
	out, changed, err := SetBetaKey([]byte(orig), "temporary_1_61")
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if want := replaceOnce(t, orig, user, strings.Replace(user, "1_53", "1_61", 1)); string(out) != want {
		t.Errorf("set:\n%s", out)
	}
	out, _, err = SetBetaKey([]byte(orig), "")
	if err != nil {
		t.Fatal(err)
	}
	if want := replaceOnce(t, orig, user, "\"UserConfig\"\n\t{\n\t\t\"language\"\t\t\"english\"\n\t}"); string(out) != want {
		t.Errorf("public branch:\n%s", out)
	}
	if _, ok, _ := BetaKey(out); ok {
		t.Error("BetaKey still there")
	}

	// A manifest without UserConfig gets one.
	bare := "\"AppState\"\n{\n\t\"appid\"\t\t\"227300\"\n}\n"
	out, _, err = SetBetaKey([]byte(bare), "temporary_1_61")
	if want := "\"AppState\"\n{\n\t\"appid\"\t\t\"227300\"\n\t\"UserConfig\"\n\t{\n\t\t\"BetaKey\"\t\t\"temporary_1_61\"\n\t}\n}\n"; err != nil || string(out) != want {
		t.Errorf("bare: %v\n%s", err, out)
	}
}

func TestClientPaths(t *testing.T) {
	const root = "/var/home/vapor/.local/share/Steam"
	for got, want := range map[string]string{
		ConfigVDFPath(root):             root + "/config/config.vdf",
		LoginUsersPath(root):            root + "/config/loginusers.vdf",
		LocalConfigPath(root, 52079950): root + "/userdata/52079950/config/localconfig.vdf",
		ShortcutsPath(root, 52079950):   root + "/userdata/52079950/config/shortcuts.vdf",
	} {
		if got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
}

func TestReadFileMax(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "localconfig.vdf")
	if err := os.WriteFile(p, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFileMax(p, 100); err != nil || len(data) != 100 {
		t.Errorf("at the cap: %d %v", len(data), err)
	}
	if _, err := ReadFileMax(p, 99); err == nil {
		t.Error("over the cap accepted")
	}
	link := filepath.Join(dir, "link.vdf")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{link, dir, filepath.Join(dir, "missing")} {
		if _, err := ReadFileMax(bad, 100); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestAccounts(t *testing.T) {
	got, err := Accounts([]byte(readFixture(t, "loginusers.vdf")))
	if err != nil {
		t.Fatal(err)
	}
	want := []Account{
		{SteamID64: 76561198012345678, AccountID: 52079950, AccountName: "jasperae", MostRecent: true},
		{SteamID64: 76561198087654321, AccountID: 127388593, AccountName: "truckfan"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}

	odd := `"users" {
		"0" { "AccountName" "zero" }
		"117093590311645241" { "AccountName" "anon user type" }
		"76561197960265728" { "AccountName" "account id 0" }
		"76561198000000001" { "AccountName" "anonymous" }
		"7656119800000000x" { "AccountName" "bad id" }
		"76561198000000002" "not a block"
		"76561198000000003" { "AccountName" "ok" }
		"76561198000000003" { "AccountName" "again" }
	}`
	got, err = Accounts([]byte(odd))
	if err != nil || len(got) != 1 || got[0].AccountName != "ok" || got[0].AccountID != 39734275 {
		t.Errorf("odd: %+v %v", got, err)
	}
	if got, err := Accounts([]byte(`"other" { }`)); err != nil || got != nil {
		t.Errorf("no users: %v %v", got, err)
	}
	if _, err := Accounts([]byte(`"users" {`)); err == nil {
		t.Error("broken file accepted")
	}
}
