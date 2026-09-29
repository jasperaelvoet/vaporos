package steam

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAddLibraryFolder(t *testing.T) {
	orig, err := os.ReadFile("testdata/libraryfolders.vdf")
	if err != nil {
		t.Fatal(err)
	}
	out, added, err := AddLibraryFolder(orig, "/var/mnt/New Disk/SteamLibrary", `Games "B"`, "4953745493258489329")
	if err != nil || !added {
		t.Fatalf("added %v, %v", added, err)
	}
	// Everything Steam wrote stays byte for byte; the entry comes last in
	// the block, as Steam writes one.
	want := strings.TrimSuffix(string(orig), "}\n") +
		"\t\"3\"\n\t{\n" +
		"\t\t\"path\"\t\t\"/var/mnt/New Disk/SteamLibrary\"\n" +
		"\t\t\"label\"\t\t\"Games \\\"B\\\"\"\n" +
		"\t\t\"contentid\"\t\t\"4953745493258489329\"\n" +
		"\t\t\"totalsize\"\t\t\"0\"\n" +
		"\t\t\"update_clean_bytes_tally\"\t\t\"0\"\n" +
		"\t\t\"time_last_update_verified\"\t\t\"0\"\n" +
		"\t\t\"apps\"\n\t\t{\n\t\t}\n" +
		"\t}\n}\n"
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	paths, err := ParseLibraryFolders(out)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"/home/jasper/.local/share/Steam", "/mnt/SATA500GB", "/mnt/SATA1TB", "/var/mnt/New Disk/SteamLibrary"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("paths = %q", paths)
	}
	root, _ := ParseVDF(out)
	if l := root.Child("libraryfolders").Child("3").Str("label"); l != `Games "B"` {
		t.Errorf("label round trip = %q", l)
	}

	// Listed already (also with a trailing slash): unchanged.
	again, added, err := AddLibraryFolder(out, "/var/mnt/New Disk/SteamLibrary/", "", "0")
	if err != nil || added || string(again) != string(out) {
		t.Errorf("second add: added %v, %v", added, err)
	}
}

func TestAddLibraryFolderOddFiles(t *testing.T) {
	// CRLF, a one-line block, a trailing comment and other top-level keys.
	crlf := "\"other\"\r\n{\r\n}\r\n\"libraryfolders\"\r\n{\r\n\t\"0\"\r\n\t{\r\n\t\t\"path\"\t\t\"/home/v\"\r\n\t}\r\n}\r\n// end\r\n"
	out, added, err := AddLibraryFolder([]byte(crlf), "/var/mnt/a", "", "x")
	if err != nil || !added {
		t.Fatalf("crlf: %v %v", added, err)
	}
	if strings.Contains(strings.ReplaceAll(string(out), "\r\n", ""), "\n") {
		t.Errorf("mixed line endings:\n%q", out)
	}
	if !strings.HasSuffix(string(out), "\t}\r\n}\r\n// end\r\n") || !strings.Contains(string(out), "\"contentid\"\t\t\"0\"") {
		t.Errorf("crlf result:\n%q", out)
	}
	if p, _ := ParseLibraryFolders(out); !reflect.DeepEqual(p, []string{"/home/v", "/var/mnt/a"}) {
		t.Errorf("crlf paths = %q", p)
	}

	one := `"libraryfolders" { "0" { "path" "/home/v" } }`
	out, added, err = AddLibraryFolder([]byte(one), "/var/mnt/b", "", "0")
	if p, _ := ParseLibraryFolders(out); err != nil || !added || !reflect.DeepEqual(p, []string{"/home/v", "/var/mnt/b"}) {
		t.Errorf("one line: %q %v %v", out, added, err)
	}

	for _, bad := range []string{``, `"libraryfolders" {`, `"LibraryFolders" "x"`, `"other" { }`} {
		if _, _, err := AddLibraryFolder([]byte(bad), "/var/mnt/c", "", "0"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, _, err := AddLibraryFolder([]byte(one), "relative", "", "0"); err == nil {
		t.Error("relative path accepted")
	}
}

func TestParseLibraryFolder(t *testing.T) {
	data, err := os.ReadFile("testdata/SATA1TB/libraryfolder.vdf")
	if err != nil {
		t.Fatal(err)
	}
	if label, id := ParseLibraryFolder(data); label != "" || id != "0" {
		t.Errorf("fixture: %q %q", label, id)
	}
	label, id := ParseLibraryFolder([]byte("\"libraryfolder\"\n{\n\t\"contentid\"\t\t\"123\"\n\t\"label\"\t\t\"Big\"\n}\n"))
	if label != "Big" || id != "123" {
		t.Errorf("got %q %q", label, id)
	}
	if _, id := ParseLibraryFolder([]byte(`"libraryfolder" { "contentid" "12; rm" }`)); id != "0" {
		t.Errorf("bad content id kept: %q", id)
	}
}
