package steam

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseVDFBasics(t *testing.T) {
	src := `// comment
"AppState"
{
	"appid"		"42"
	"Name"		"A \"quoted\" \\ name"
	unquoted	value
	"Block"
	{
		"inner"	"x" [$WIN32]
	}
	"cond" "y" [$LINUX]
}`
	root, err := ParseVDF([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	st := root.Child("appstate") // keys are case-insensitive
	if st == nil || !st.Block {
		t.Fatalf("AppState block missing: %+v", root)
	}
	if got := st.Str("appid"); got != "42" {
		t.Errorf("appid = %q", got)
	}
	if got := st.Str("name"); got != `A "quoted" \ name` {
		t.Errorf("name = %q", got)
	}
	if got := st.Str("unquoted"); got != "value" {
		t.Errorf("unquoted = %q", got)
	}
	if got := st.Child("Block").Str("inner"); got != "x" {
		t.Errorf("inner = %q", got)
	}
	if got := st.Str("cond"); got != "y" {
		t.Errorf("cond = %q", got)
	}
	if st.Str("Block") != "" {
		t.Error("Str on a block must be empty")
	}
}

func TestParseVDFErrors(t *testing.T) {
	for _, src := range []string{
		`"a" {`,
		`"a" "b" }`,
		`"a"`,
		`"a" "unterminated`,
		`{ "a" "b" }`,
		strings.Repeat(`"a" {`, maxVDFDepth+2) + strings.Repeat("}", maxVDFDepth+2),
	} {
		if _, err := ParseVDF([]byte(src)); err == nil {
			t.Errorf("ParseVDF(%q) succeeded, want error", src)
		}
	}
}

func TestParseLibraryFoldersFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/libraryfolders.vdf")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseLibraryFolders(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/jasper/.local/share/Steam", "/mnt/SATA500GB", "/mnt/SATA1TB"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
}

func TestParseLibraryFoldersOldFormat(t *testing.T) {
	src := `"LibraryFolders"
{
	"TimeNextStatsReport"	"1690000000"
	"ContentStatsID"	"-123"
	"1"	"/mnt/games"
	"2"	"relative/ignored"
}`
	got, err := ParseLibraryFolders([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/mnt/games"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
	if _, err := ParseLibraryFolders([]byte(`"other" { }`)); err == nil {
		t.Error("a file without libraryfolders must fail")
	}
}

// fixtureSteam lays the reference PC's Steam libraries out under a temp
// dir and returns the Steam root with a libraryfolders.vdf pointing there.
func fixtureSteam(t *testing.T) (root string, libs []string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "Steam")
	copyTree(t, "testdata/Steam", root)
	copyTree(t, "testdata/SATA1TB", filepath.Join(base, "mnt", "SATA1TB"))
	copyTree(t, "testdata/SATA500GB", filepath.Join(base, "mnt", "SATA500GB"))
	vdf, err := os.ReadFile("testdata/libraryfolders.vdf")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer(
		"/home/jasper/.local/share/Steam", root,
		"/mnt/", filepath.Join(base, "mnt")+"/",
	).Replace(string(vdf))
	if err := os.WriteFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, []string{root, filepath.Join(base, "mnt", "SATA500GB"), filepath.Join(base, "mnt", "SATA1TB")}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLibrariesDedupesAndReadsBothFiles(t *testing.T) {
	root, want := fixtureSteam(t)
	// config/libraryfolders.vdf repeats one library via a symlink and adds one.
	extra := filepath.Join(filepath.Dir(root), "extra")
	link := filepath.Join(filepath.Dir(root), "link1tb")
	if err := os.Symlink(want[2], link); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "config"), 0o755)
	cfg := `"libraryfolders" { "0" { "path" "` + link + `" } "1" { "path" "` + extra + `" } }`
	if err := os.WriteFile(filepath.Join(root, "config", "libraryfolders.vdf"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Libraries(root)
	if want := append(want, extra); !reflect.DeepEqual(got, want) {
		t.Errorf("Libraries = %q\nwant %q", got, want)
	}
}

func TestLibrariesWithoutVDF(t *testing.T) {
	root := t.TempDir()
	if got := Libraries(root); !reflect.DeepEqual(got, []string{root}) {
		t.Errorf("Libraries = %q", got)
	}
}

func TestInstalledGamesFixture(t *testing.T) {
	root, _ := fixtureSteam(t)
	games := InstalledGames(Libraries(root))
	var names []string
	for _, g := range games {
		names = append(names, g.Name)
	}
	want := []string{
		"Cities: Skylines II",
		"Cyberpunk 2077",
		"Forza Horizon 6",
		"Microsoft Flight Simulator 2024",
		"Red Dead Redemption 2",
		"The Bus",
		"Train Sim World® 7",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("games = %q\nwant %q", names, want)
	}
	for _, g := range games {
		if g.Name == "The Bus" && (g.ID != 491540 || !strings.HasSuffix(g.Library, "SATA1TB")) {
			t.Errorf("The Bus = %+v", g)
		}
	}
}

func TestInstalledGamesSkipsIncompleteAndDuplicates(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write := func(lib, id, name, flags string) {
		os.MkdirAll(filepath.Join(lib, "steamapps"), 0o755)
		acf := `"AppState" { "appid" "` + id + `" "name" "` + name + `" "StateFlags" "` + flags + `" }`
		os.WriteFile(filepath.Join(lib, "steamapps", "appmanifest_"+id+".acf"), []byte(acf), 0o644)
	}
	write(a, "10", "Downloading", "1026") // update-required + downloading, never completed
	write(a, "20", "Game", "6")           // installed, update pending: launchable
	write(b, "20", "Game", "4")           // same app in another library
	write(b, "30", "Proton 9.0", "4")
	os.WriteFile(filepath.Join(b, "steamapps", "appmanifest_99.acf"), []byte("garbage {"), 0o644)
	games := InstalledGames([]string{a, b, filepath.Join(a, "missing")})
	if len(games) != 1 || games[0].ID != 20 || games[0].Library != a {
		t.Errorf("games = %+v", games)
	}
}

func TestLibraryIn(t *testing.T) {
	cases := map[string]struct {
		files   []string
		wantDir string
		wantOK  bool
	}{
		"root steamapps":     {[]string{"steamapps/"}, ".", true},
		"root marker":        {[]string{"libraryfolder.vdf"}, ".", true},
		"old SteamApps case": {[]string{"SteamApps/"}, ".", true},
		"SteamLibrary":       {[]string{"SteamLibrary/steamapps/"}, "SteamLibrary", true},
		"steamlibrary lower": {[]string{"steamlibrary/libraryfolder.vdf"}, "steamlibrary", true},
		"empty SteamLibrary": {[]string{"SteamLibrary/"}, "", false},
		"plain disk":         {[]string{"photos/", "steamapps.txt"}, "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range c.files {
				p := filepath.Join(dir, f)
				if strings.HasSuffix(f, "/") {
					os.MkdirAll(p, 0o755)
					continue
				}
				os.MkdirAll(filepath.Dir(p), 0o755)
				os.WriteFile(p, nil, 0o644)
			}
			got, ok := LibraryIn(dir)
			if got != c.wantDir || ok != c.wantOK {
				t.Errorf("LibraryIn = %q, %v; want %q, %v", got, ok, c.wantDir, c.wantOK)
			}
		})
	}
	if _, ok := LibraryIn(filepath.Join(t.TempDir(), "missing")); ok {
		t.Error("missing dir must not be a library")
	}
}

func TestRoot(t *testing.T) {
	home := t.TempDir()
	if got, want := Root(home), filepath.Join(home, ".local", "share", "Steam"); got != want {
		t.Errorf("Root without symlink = %q, want %q", got, want)
	}
	real := filepath.Join(home, "elsewhere", "Steam")
	os.MkdirAll(real, 0o755)
	os.MkdirAll(filepath.Join(home, ".steam"), 0o755)
	os.Symlink(real, filepath.Join(home, ".steam", "root"))
	want, _ := filepath.EvalSymlinks(real)
	if got := Root(home); got != want {
		t.Errorf("Root = %q, want %q", got, want)
	}
}

func TestIsTool(t *testing.T) {
	for _, a := range []App{
		{ID: 1, Name: "Proton 12.0"},
		{ID: 1, Name: "Steam Linux Runtime 5.0 (future)"},
		{ID: 228980, Name: "whatever"},
	} {
		if !a.IsTool() {
			t.Errorf("%+v should be a tool", a)
		}
	}
	for _, a := range []App{{ID: 2, Name: "Protonic Game"}, {ID: 3, Name: "Portal 2"}} {
		if a.IsTool() {
			t.Errorf("%+v should be a game", a)
		}
	}
}
