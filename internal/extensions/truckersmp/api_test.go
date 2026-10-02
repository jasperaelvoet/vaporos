package truckersmp

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// The descriptor is valid and says what the helper relies on: Proton for
// both games, hooks on both, a shortcut per game without a compatibility
// tool (they run vos), the actions the helper runs, and the injector the
// build fetches as the hook expects to find it.
func TestDescriptor(t *testing.T) {
	d, err := descriptor.Load(filepath.Join("..", "..", "..", "extensions", ID, "extension.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ValidateSource(); err != nil {
		t.Fatal(err)
	}
	if d.ID != ID || d.Category != descriptor.App || !slices.Equal(d.Requires, []string{"proton"}) || len(d.Permissions) != 0 {
		t.Errorf("identity: %+v", d)
	}
	s := d.Steam
	if s == nil || s.CompatTool != "proton-cachyos-slr" || !slices.Equal(s.ForceCompatTool, []uint32{227300, 270880}) ||
		len(s.Hooks) != 1 || !slices.Equal(s.Hooks[0].Apps, []uint32{227300, 270880}) {
		t.Fatalf("steam: %+v", s)
	}
	for _, g := range games {
		i := slices.IndexFunc(s.Shortcuts, func(sc descriptor.Shortcut) bool { return sc.Key == g.key })
		if i < 0 || s.Shortcuts[i].CompatTool || s.Shortcuts[i].Name != "TruckersMP ("+g.short+")" {
			t.Errorf("shortcut %s: %+v", g.key, s.Shortcuts)
		}
		if !slices.ContainsFunc(d.Shares, func(sh descriptor.Share) bool { return sh.SteamApp == g.app }) {
			t.Errorf("does not share %d", g.app)
		}
	}
	if len(d.Fetch) != 1 || d.Fetch[0].Dest != filepath.Base(injectorSource()) || d.Fetch[0].License != "MIT" ||
		!strings.HasSuffix(d.Fetch[0].Extract, "/truckersmp-cli.exe") || d.Fetch[0].LicenseFile == "" {
		t.Errorf("fetch: %+v", d.Fetch)
	}
	if !slices.ContainsFunc(d.Data, func(x descriptor.Data) bool { return x.Where == "home" }) ||
		!slices.ContainsFunc(d.Data, func(x descriptor.Data) bool { return x.Where == "system" }) {
		t.Errorf("data: %+v", d.Data)
	}
	// What it downloads: the mod at install and at its updates, checked
	// by TruckersMP's MD5s, and what each of TruckersMP's servers tells.
	dl := map[string]descriptor.Download{}
	for _, x := range d.Downloads {
		dl[x.From+" "+x.When] = x
	}
	for _, k := range []string{"download.ets2mp.com install", "download.ets2mp.com update"} {
		if x := dl[k]; !x.RunsCode || x.Checked != "publisher-hash" {
			t.Errorf("%s: %+v", k, d.Downloads)
		}
	}
	for _, u := range []string{versionURL, filesURL, downloadBase} {
		host, _, _ := strings.Cut(strings.TrimPrefix(u, "https://"), "/")
		if !slices.ContainsFunc(d.Downloads, func(x descriptor.Download) bool { return x.From == host }) {
			t.Errorf("downloads do not name %s: %+v", host, d.Downloads)
		}
	}
	if len(d.Downloads) != 4 {
		t.Errorf("downloads: %+v", d.Downloads)
	}
	for _, name := range []string{"copy-profiles", "switch-branch", "latest-branch"} {
		if !slices.ContainsFunc(d.Actions, func(a descriptor.Action) bool { return a.Name == name && a.Confirm != nil }) {
			t.Errorf("no confirmed action %s", name)
		}
	}
	if d.Copy.Install == "" || d.Copy.Remove == "" || len(d.Caveats) != 2 {
		t.Errorf("copy: %+v %q", d.Copy, d.Caveats)
	}
}

func TestParseVersion(t *testing.T) {
	v, err := parseVersion([]byte(`{"name":"0.7.7.9","numeric":"7790","stage":"Release",
		"ets2mp_checksum":{"dll":"0123456789ABCDEF0123456789abcdef","adb":"x"},
		"atsmp_checksum":{"dll":"not-a-sum","adb":"y"},
		"supported_game_version":"1.61.1.1s","supported_ats_game_version":"1.61.3.1s; rm -rf"}`))
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "0.7.7.9" || v.ETS2.DLL != "0123456789abcdef0123456789abcdef" || v.ATS.DLL != "" ||
		v.SupportedETS2 != "1.61.1.1s" || v.SupportedATS != "" {
		t.Errorf("%+v", v)
	}
	for _, bad := range []string{`{"name":""}`, `{"name":"a b"}`, `[]`, `{`} {
		if _, err := parseVersion([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestParseFiles(t *testing.T) {
	ok := `{"Files":[{"Md5":"0123456789ABCDEF0123456789ABCDEF","Type":"ets2","FilePath":"/core_ets2mp.dll"},
		{"Md5":"0123456789abcdef0123456789abcdef","Type":"system","FilePath":"/data/ui/x.zip"},
		{"Md5":"0123456789abcdef0123456789abcdef","Type":"launcher","FilePath":"/../launcher.exe"}]}`
	files, err := parseFiles([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	want := []modFile{{Path: "core_ets2mp.dll", Type: "ets2", MD5: "0123456789abcdef0123456789abcdef"},
		{Path: "data/ui/x.zip", Type: "system", MD5: "0123456789abcdef0123456789abcdef"}}
	if !slices.Equal(files, want) {
		t.Errorf("%+v", files)
	}
	for name, path := range map[string]string{
		"climbs out": "/../x.dll", "absolute twice": "//x.dll", "no slash": "x.dll", "dot dir": "/a/./x.dll",
		"hidden": "/.sync.lock", "backslash": "/a\\x.dll", "drive": "/c:/x.dll", "empty": "/", "control": "/a\nb",
	} {
		b := `{"Files":[{"Md5":"0123456789abcdef0123456789abcdef","Type":"ets2","FilePath":"` +
			strings.ReplaceAll(strings.ReplaceAll(path, "\\", "\\\\"), "\n", "\\n") + `"}]}`
		if _, err := parseFiles([]byte(b)); err == nil {
			t.Errorf("%s (%q): accepted", name, path)
		}
	}
	for name, b := range map[string]string{
		"no files": `{"Files":[]}`,
		"bad md5":  `{"Files":[{"Md5":"xyz","Type":"ets2","FilePath":"/a.dll"}]}`,
		"twice":    `{"Files":[{"Md5":"0123456789abcdef0123456789abcdef","Type":"ets2","FilePath":"/a.dll"},{"Md5":"0123456789abcdef0123456789abcdef","Type":"ats","FilePath":"/A.dll"}]}`,
	} {
		if _, err := parseFiles([]byte(b)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.61.1.1s", "1.61.3.1s", 0, true},
		{"1.62.0.5s", "1.61.1.1s", 1, true},
		{"1.53.3.14s", "1.61.1.1s", -1, true},
		{"2.0", "1.99.9", 1, true},
		{"", "1.61", 0, false},
		{"1", "1.61", 0, false},
		{"1.x", "1.61", 0, false},
	} {
		got, ok := compareMinor(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("compareMinor(%q, %q) = %d %v", c.a, c.b, got, ok)
		}
	}
	if b, ok := branchFor("1.61.1.1s"); !ok || b != "temporary_1_61" {
		t.Errorf("branch %q %v", b, ok)
	}
	if _, ok := branchFor("latest"); ok {
		t.Error("a branch for no version")
	}
	if m := minorOf("1.61.1.1s"); m != "1.61" {
		t.Errorf("minor %q", m)
	}
}
