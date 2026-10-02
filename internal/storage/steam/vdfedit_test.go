package steam

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/client/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// replaceOnce replaces the one copy of old in s, failing when there is
// not exactly one: every expected result below is the fixture with one
// known change, which is what proves the rest of the bytes were kept.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("%d copies of %q", n, old)
	}
	return strings.Replace(s, old, new, 1)
}

var steamPath = []string{"UserLocalConfigStore", "Software", "Valve", "Steam"}

func appPath(app string) []string {
	return append(append([]string{}, steamPath...), "apps", app)
}

func TestSetVDFReplacesOnlyTheValue(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")
	out, changed, err := SetVDF([]byte(orig), LocalConfigMax, appPath("227300"), "LaunchOptions", `say "hi" \o/`)
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	want := replaceOnce(t, orig, "\"LaunchOptions\"\t\t\"-nointro -64bit\"", "\"LaunchOptions\"\t\t\"say \\\"hi\\\" \\\\o/\"")
	if string(out) != want {
		t.Errorf("got:\n%s", out)
	}
	if v, ok, err := GetVDF(out, LocalConfigMax, appPath("227300"), "launchoptions"); err != nil || !ok || v != `say "hi" \o/` {
		t.Errorf("read back %q %v %v", v, ok, err)
	}

	// The same value again, here also through other spellings of the
	// path: nothing to do, and the very same bytes.
	again, changed, err := SetVDF(out, LocalConfigMax, []string{"userlocalconfigstore", "SOFTWARE", "valve", "steam", "Apps", "227300"}, "LAUNCHOPTIONS", `say "hi" \o/`)
	if err != nil || changed || !bytes.Equal(again, out) {
		t.Errorf("second set: changed %v, %v", changed, err)
	}
}

func TestSetVDFInserts(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")

	// A key missing from a block that exists goes last in it.
	out, changed, err := SetVDF([]byte(orig), LocalConfigMax, appPath("949230"), "LaunchOptions", "gamemoderun %command%")
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	anchor := "\"used_files\"\t\t\"3\"\n\t\t\t\t\t\t}\n"
	want := replaceOnce(t, orig, anchor, anchor+"\t\t\t\t\t\t\"LaunchOptions\"\t\t\"gamemoderun %command%\"\n")
	if string(out) != want {
		t.Errorf("into a block:\n%s", out)
	}

	// A missing app block is added at the end of "apps".
	out, _, err = SetVDF([]byte(orig), LocalConfigMax, appPath("2483190"), "LaunchOptions", "-dx11")
	if err != nil {
		t.Fatal(err)
	}
	anchor = "\t\t\t\t}\n\t\t\t\t\"LastPlayedTimesSyncTime\""
	want = replaceOnce(t, orig, anchor, "\t\t\t\t\t\"2483190\"\n\t\t\t\t\t{\n\t\t\t\t\t\t\"LaunchOptions\"\t\t\"-dx11\"\n\t\t\t\t\t}\n"+anchor)
	if string(out) != want {
		t.Errorf("new block:\n%s", out)
	}

	// Several missing levels, in a file Steam had just created.
	fresh := "\"UserLocalConfigStore\"\n{\n\t\"Software\"\n\t{\n\t\t\"Valve\"\n\t\t{\n\t\t\t\"Steam\"\n\t\t\t{\n\t\t\t}\n\t\t}\n\t}\n}\n"
	out, _, err = SetVDF([]byte(fresh), LocalConfigMax, appPath("227300"), "LaunchOptions", "x")
	if err != nil {
		t.Fatal(err)
	}
	want = replaceOnce(t, fresh, "\t\t\t{\n\t\t\t}\n", "\t\t\t{\n\t\t\t\t\"apps\"\n\t\t\t\t{\n\t\t\t\t\t\"227300\"\n\t\t\t\t\t{\n\t\t\t\t\t\t\"LaunchOptions\"\t\t\"x\"\n\t\t\t\t\t}\n\t\t\t\t}\n\t\t\t}\n")
	if string(out) != want {
		t.Errorf("new levels:\n%s", out)
	}
	if _, err := ParseVDF(out); err != nil {
		t.Error(err)
	}
}

func TestSetVDFOddLayouts(t *testing.T) {
	// One-line blocks, CRLF and a trailing comment: the result parses
	// and keeps the file's line endings.
	one := `"a" { "b" "c" "d" { "e" "f" } }`
	out, _, err := SetVDF([]byte(one), VDFMax, []string{"a", "d"}, "g", "h")
	if err != nil {
		t.Fatal(err)
	}
	if want := "\"a\" { \"b\" \"c\" \"d\" { \"e\" \"f\" \n\t\t\"g\"\t\t\"h\"\n} }"; string(out) != want {
		t.Errorf("one line: %q", out)
	}
	out, _, err = SetVDF([]byte(one), VDFMax, []string{"a"}, "b", "new")
	if err != nil || string(out) != `"a" { "b" "new" "d" { "e" "f" } }` {
		t.Errorf("one line replace: %q %v", out, err)
	}
	unquoted := "a\n{\n\tb c\n}\n"
	if out, _, err = SetVDF([]byte(unquoted), VDFMax, []string{"a"}, "b", "d e"); err != nil || string(out) != "a\n{\n\tb \"d e\"\n}\n" {
		t.Errorf("unquoted: %q %v", out, err)
	}

	crlf := "\"a\"\r\n{\r\n\t\"b\"\t\t\"c\"\r\n}\r\n// end\r\n"
	out, _, err = SetVDF([]byte(crlf), VDFMax, []string{"a", "x"}, "y", "z")
	if err != nil {
		t.Fatal(err)
	}
	if want := "\"a\"\r\n{\r\n\t\"b\"\t\t\"c\"\r\n\t\"x\"\r\n\t{\r\n\t\t\"y\"\t\t\"z\"\r\n\t}\r\n}\r\n// end\r\n"; string(out) != want {
		t.Errorf("crlf: %q", out)
	}
}

func TestSetVDFConditional(t *testing.T) {
	// The copy Steam reads on Linux is the one that counts; the one for
	// Windows before it stays as it is, and so do the conditionals.
	orig := readFixture(t, "localconfig.vdf")
	out, changed, err := SetVDF([]byte(orig), LocalConfigMax, steamPath, "ShowFriendsListOnStartup", "0")
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	want := replaceOnce(t, orig, "\"1\" [$LINUX]", "\"0\" [$LINUX]")
	if string(out) != want {
		t.Errorf("got:\n%s", out)
	}
	if v, ok, _ := GetVDF([]byte(orig), LocalConfigMax, steamPath, "ShowFriendsListOnStartup"); !ok || v != "1" {
		t.Errorf("read %q %v", v, ok)
	}
	// An entry after a conditional one is found and edited as usual.
	system := []string{"UserLocalConfigStore", "system"}
	out, _, err = SetVDF([]byte(orig), LocalConfigMax, system, "InGameOverlayScreenshotSaveUncompressedPath", `D:\Shots`)
	if err != nil || string(out) != replaceOnce(t, orig, `"C:\\Screenshots"`, `"D:\\Shots"`) {
		t.Errorf("after a conditional: %v\n%s", err, out)
	}
	// One only Windows reads is not there on Linux: a copy is added.
	if v, ok, _ := GetVDF([]byte(orig), LocalConfigMax, system, "InGameOverlayShortcutKey"); ok {
		t.Errorf("Windows entry read: %q", v)
	}
	out, _, err = SetVDF([]byte(orig), LocalConfigMax, system, "InGameOverlayShortcutKey", "F12")
	anchor := "\"C:\\\\Screenshots\"\n"
	if err != nil || string(out) != replaceOnce(t, orig, anchor, anchor+"\t\t\"InGameOverlayShortcutKey\"\t\t\"F12\"\n") {
		t.Errorf("Windows entry: %v\n%s", err, out)
	}
	if v, ok, _ := GetVDF(out, LocalConfigMax, system, "InGameOverlayShortcutKey"); !ok || v != "F12" {
		t.Errorf("read back %q %v", v, ok)
	}
}

func TestSetVDFRefuses(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")
	for name, c := range map[string]struct {
		data string
		path []string
		key  string
		val  string
	}{
		"broken file":     {orig[:len(orig)-3], appPath("227300"), "LaunchOptions", "x"},
		"empty file":      {"", appPath("227300"), "LaunchOptions", "x"},
		"no top block":    {`"other" { }`, appPath("227300"), "LaunchOptions", "x"},
		"empty path":      {orig, nil, "LaunchOptions", "x"},
		"path is a value": {orig, []string{"UserLocalConfigStore", "Software", "Valve", "Steam", "PlayerLevel", "x"}, "k", "v"},
		"key is a block":  {orig, appPath("227300"), "cloud", "x"},
		"NUL":             {orig, appPath("227300"), "LaunchOptions", "a\x00b"},
		"over the cap":    {orig, appPath("227300"), "LaunchOptions", "x"},
	} {
		limit := LocalConfigMax
		if name == "over the cap" {
			limit = len(orig) - 1
		}
		out, changed, err := SetVDF([]byte(c.data), limit, c.path, c.key, c.val)
		if err == nil || changed || out != nil {
			t.Errorf("%s: %v %v %q", name, changed, err, out)
		}
	}
	if _, _, err := GetVDF([]byte(orig[:10]), VDFMax, appPath("227300"), "LaunchOptions"); err == nil {
		t.Error("get on a broken file")
	}
	if _, _, err := DeleteVDF([]byte(orig), VDFMax, nil, "x"); err == nil {
		t.Error("delete with an empty path")
	}
}

func TestDeleteVDF(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")
	out, changed, err := DeleteVDF([]byte(orig), LocalConfigMax, appPath("227300"), "LaunchOptions")
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	if want := replaceOnce(t, orig, "\t\t\t\t\t\t\"LaunchOptions\"\t\t\"-nointro -64bit\"\n", ""); string(out) != want {
		t.Errorf("value:\n%s", out)
	}

	// A whole block, and every copy of a repeated key with its
	// conditional.
	out, _, err = DeleteVDF([]byte(orig), LocalConfigMax, appPath("949230"), "cloud")
	if err != nil {
		t.Fatal(err)
	}
	cloud := "\t\t\t\t\t\t\"cloud\"\n\t\t\t\t\t\t{\n\t\t\t\t\t\t\t\"quota_bytes\"\t\t\"1000000000\"\n\t\t\t\t\t\t\t\"quota_files\"\t\t\"2000\"\n" +
		"\t\t\t\t\t\t\t\"used_bytes\"\t\t\"91234\"\n\t\t\t\t\t\t\t\"used_files\"\t\t\"3\"\n\t\t\t\t\t\t}\n"
	if want := replaceOnce(t, orig, cloud, ""); string(out) != want {
		t.Errorf("block:\n%s", out)
	}
	out, _, err = DeleteVDF([]byte(orig), LocalConfigMax, steamPath, "showfriendslistonstartup")
	if err != nil {
		t.Fatal(err)
	}
	if want := replaceOnce(t, orig, "\t\t\t\t\"ShowFriendsListOnStartup\"\t\t\"1\" [$LINUX]\n", ""); string(out) != want {
		t.Errorf("repeated, Linux copy:\n%s", out)
	}
	twice := `"a" { "b" "1" "b" "2" "b" "3" [$OSX] }`
	if out, _, err := DeleteVDF([]byte(twice), VDFMax, []string{"a"}, "b"); err != nil || string(out) != `"a" { "b" "3" [$OSX] }` {
		t.Errorf("repeated: %q %v", out, err)
	}

	// Missing key, missing block: nothing to do.
	for _, path := range [][]string{appPath("949230"), appPath("1"), {"nope"}} {
		if out, changed, err := DeleteVDF([]byte(orig), LocalConfigMax, path, "LaunchOptions"); err != nil || changed || string(out) != orig {
			t.Errorf("%v: changed %v, %v", path, changed, err)
		}
	}

	// Entries sharing a line.
	for _, c := range []struct{ in, key, want string }{
		{`"a" { "b" "c" "d" "e" }`, "b", `"a" { "d" "e" }`},
		{`"a" { "b" "c" "d" "e" }`, "d", `"a" { "b" "c" }`},
		{"\"a\"\n{\n\t\"b\" \"c\" \"d\" \"e\"\n}\n", "d", "\"a\"\n{\n\t\"b\" \"c\"\n}\n"},
		{"\"a\"\n{\n\t\"b\" \"c\" // note\n}\n", "b", "\"a\"\n{\n\t// note\n}\n"},
		{"\"a\"\r\n{\r\n\t\"b\"\t\"c\"\r\n}\r\n", "b", "\"a\"\r\n{\r\n}\r\n"},
	} {
		out, _, err := DeleteVDF([]byte(c.in), VDFMax, []string{"a"}, c.key)
		if err != nil || string(out) != c.want {
			t.Errorf("%q - %s = %q (%v), want %q", c.in, c.key, out, err, c.want)
		}
	}
}

// TestEditLargeLocalConfig edits a localconfig.vdf larger than the 4 MiB
// every other Steam file stays under, as a big library makes one.
func TestEditLargeLocalConfig(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")
	var apps strings.Builder
	for i := 0; apps.Len() < 4<<20+1024; i++ {
		id := strconv.Itoa(3000000 + i)
		apps.WriteString("\t\t\t\t\t\"" + id + "\"\n\t\t\t\t\t{\n\t\t\t\t\t\t\"LastPlayed\"\t\t\"1790000000\"\n\t\t\t\t\t\t\"Playtime\"\t\t\"12\"\n" +
			"\t\t\t\t\t\t\"cloud\"\n\t\t\t\t\t\t{\n\t\t\t\t\t\t\t\"last_sync_state\"\t\t\"synchronized\"\n\t\t\t\t\t\t}\n\t\t\t\t\t}\n")
	}
	anchor := "\t\t\t\t\t\"7\"\n"
	big := replaceOnce(t, orig, anchor, apps.String()+anchor)
	if _, err := ParseVDF([]byte(big)); err == nil {
		t.Fatal("fixture is not over the usual cap")
	}
	out, changed, err := SetLaunchOptions([]byte(big), 227300, "-nointro")
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	if want := replaceOnce(t, big, `"-nointro -64bit"`, `"-nointro"`); string(out) != want {
		t.Error("large file: more than the value changed")
	}
	if v, ok, err := LaunchOptions(out, 227300); err != nil || !ok || v != "-nointro" {
		t.Errorf("read back %q %v %v", v, ok, err)
	}

	huge := make([]byte, LocalConfigMax+1)
	if _, _, err := SetLaunchOptions(huge, 227300, "x"); err == nil {
		t.Error("over 64 MiB accepted")
	}
}

func TestEditsKeepTheFileParsing(t *testing.T) {
	// Taking out an entry between two unquoted tokens leaves a space
	// between them, so they stay two.
	for _, c := range []struct{ in, want string }{
		{`"a" { b c"d" "e"f g }`, `"a" { b c f g }`},
		{`"a" { b c"d" "e""d" "f"g h }`, `"a" { b c g h }`},
		{"\"a\" { b c\"d\" \"e\"// note\n}", "\"a\" { b c // note\n}"},
	} {
		out, changed, err := DeleteVDF([]byte(c.in), VDFMax, []string{"a"}, "d")
		if err != nil || !changed || string(out) != c.want {
			t.Errorf("%q: %q %v %v, want %q", c.in, out, changed, err, c.want)
		}
	}

	// A result over the limit is refused like a file over it.
	data := []byte(`"a" { "b" "c" }`)
	if out, changed, err := SetVDF(data, len(data)+3, []string{"a"}, "b", "longer"); err == nil || changed || out != nil {
		t.Errorf("over the limit: %q %v %v", out, changed, err)
	}
	if _, _, err := SetVDF(data, len(data)+3, []string{"a"}, "b", "cde"); err != nil {
		t.Errorf("within the limit: %v", err)
	}

	// No NUL anywhere, the path included.
	for _, path := range [][]string{{"a\x00"}, {"a", "x\x00y"}} {
		if _, _, err := SetVDF(data, VDFMax, path, "k", "v"); err == nil {
			t.Errorf("set %q accepted", path)
		}
		if _, _, err := DeleteVDF(data, VDFMax, path, "k"); err == nil {
			t.Errorf("delete %q accepted", path)
		}
	}
	if _, _, err := DeleteVDF(data, VDFMax, []string{"a"}, "b\x00"); err == nil {
		t.Error("delete of a NUL key accepted")
	}
}

func TestLocalConfigBatch(t *testing.T) {
	orig := readFixture(t, "localconfig.vdf")
	lc, err := ParseLocalConfig([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	if got := lc.LaunchOptions(); len(got) != 2 || got[227300] != "-nointro -64bit" {
		t.Errorf("read %q", got)
	}
	for _, err := range []error{
		lc.SetLaunchOptions(227300, "-x"),
		lc.SetLaunchOptions(2483190, "-dx11"),
		lc.SetLaunchOptions(270880, "a"),
		lc.SetLaunchOptions(270880, "b"), // the last one counts
		lc.DeleteLaunchOptions(1091500),
		lc.DeleteLaunchOptions(949230), // has none: nothing
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	out, changed, err := lc.Bytes()
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}

	// The same, one edit at a time.
	want := []byte(orig)
	for _, step := range []func([]byte) ([]byte, bool, error){
		func(d []byte) ([]byte, bool, error) { return SetLaunchOptions(d, 227300, "-x") },
		func(d []byte) ([]byte, bool, error) { return SetLaunchOptions(d, 2483190, "-dx11") },
		func(d []byte) ([]byte, bool, error) { return SetLaunchOptions(d, 270880, "b") },
		func(d []byte) ([]byte, bool, error) { return DeleteLaunchOptions(d, 1091500) },
	} {
		if want, _, err = step(want); err != nil {
			t.Fatal(err)
		}
	}
	if string(out) != string(want) {
		t.Errorf("batch:\n%s\nwant:\n%s", out, want)
	}

	// Launch options added and taken out again leave no trace; an app
	// block with anything else in it stays.
	lc, err = ParseLocalConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []uint32{2483190, 270880, 227300} {
		if err := lc.DeleteLaunchOptions(app); err != nil {
			t.Fatal(err)
		}
	}
	back, _, err := lc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	gone, _, err := DeleteLaunchOptions([]byte(orig), 227300)
	if err != nil {
		t.Fatal(err)
	}
	if gone, _, err = DeleteLaunchOptions(gone, 1091500); err != nil || string(back) != string(gone) {
		t.Errorf("taken out again:\n%s", back)
	}

	// Apps added where there is no "apps" block yet share a new one.
	fresh := "\"UserLocalConfigStore\"\n{\n\t\"Software\"\n\t{\n\t\t\"Valve\"\n\t\t{\n\t\t\t\"Steam\"\n\t\t\t{\n\t\t\t}\n\t\t}\n\t}\n}\n"
	lc, err = ParseLocalConfig([]byte(fresh))
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(lc.SetLaunchOptions(1, "a"), lc.SetLaunchOptions(2, "b")); err != nil {
		t.Fatal(err)
	}
	out, _, err = lc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := AppLaunchOptions(out); err != nil || len(got) != 2 || got[1] != "a" || got[2] != "b" || strings.Count(string(out), `"apps"`) != 1 {
		t.Errorf("new apps: %q %v\n%s", got, err, out)
	}
}
