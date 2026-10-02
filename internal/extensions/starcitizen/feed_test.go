package starcitizen

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// latestYML is latest.yml as install.robertsspaceindustries.com/rel/2/
// serves it (electron-builder's NSIS feed).
const latestYML = `version: 2.17.0
files:
  - url: RSI Launcher-Setup-2.17.0.exe
    sha512: 2k87XwgcnAvc3QOPtVuYdxmOCfNbjhuQDtLg0B7xt4p9bWXspQOBDPJGRnECKdck4x5+vXHfK5cpIwtnigbPgA==
    size: 342574256
    blockMapSize: 361234
path: RSI Launcher-Setup-2.17.0.exe
sha512: 2k87XwgcnAvc3QOPtVuYdxmOCfNbjhuQDtLg0B7xt4p9bWXspQOBDPJGRnECKdck4x5+vXHfK5cpIwtnigbPgA==
releaseDate: '2025-09-30T17:04:52.513Z'
`

func TestParseFeed(t *testing.T) {
	r, err := parseFeed([]byte(latestYML))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "2.17.0" || r.File != "RSI Launcher-Setup-2.17.0.exe" || r.Size != 342574256 || len(r.SHA512) != 64 {
		t.Fatalf("got %+v", r)
	}
	if u := installerURL(r.File); u != "https://install.robertsspaceindustries.com/rel/2/RSI%20Launcher-Setup-2.17.0.exe" {
		t.Errorf("installer URL %s", u)
	}
	crlf, err := parseFeed([]byte(strings.ReplaceAll(latestYML, "\n", "\r\n")))
	if err != nil || crlf.File != r.File || crlf.Size != r.Size {
		t.Errorf("CRLF: %+v, %v", crlf, err)
	}
}

func TestParseFeedVariants(t *testing.T) {
	sum := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 64))
	other := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 64))
	for _, tc := range []struct {
		name, yml string
		file      string
		sha       byte
	}{
		{"no path: the first file", "version: 2.18.0\nfiles:\n  - url: RSI Launcher-Setup-2.18.0.exe\n    sha512: " + sum + "\n    size: 10\n", "RSI Launcher-Setup-2.18.0.exe", 7},
		{"path picks its file", "version: 2.18.0\nfiles:\n  - url: other.blockmap\n    sha512: " + other + "\n    size: 3\n  - url: 'RSI Launcher-Setup-2.18.0.exe'\n    sha512: " + sum + "\n    size: 10\npath: \"RSI Launcher-Setup-2.18.0.exe\"\n", "RSI Launcher-Setup-2.18.0.exe", 7},
		{"sha512 only at the top", "# feed\nversion: 2.18.0\nfiles:\n  - url: RSI Launcher-Setup-2.18.0.exe\n    size: 10\npath: RSI Launcher-Setup-2.18.0.exe\nsha512: " + sum + "\n", "RSI Launcher-Setup-2.18.0.exe", 7},
		{"the file's own sha512 first", "version: 2.18.0\nfiles:\n  - url: RSI Launcher-Setup-2.18.0.exe\n    sha512: " + sum + "\n    size: 10\npath: RSI Launcher-Setup-2.18.0.exe\nsha512: " + other + "\n", "RSI Launcher-Setup-2.18.0.exe", 7},
	} {
		r, err := parseFeed([]byte(tc.yml))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if r.File != tc.file || r.SHA512[0] != tc.sha || r.Size != 10 {
			t.Errorf("%s: got %+v", tc.name, r)
		}
	}
}

func TestParseFeedRefuses(t *testing.T) {
	sum := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 64))
	feed := func(version, file, sha, size string) string {
		return "version: " + version + "\nfiles:\n  - url: " + file + "\n    sha512: " + sha + "\n    size: " + size + "\npath: " + file + "\n"
	}
	for name, yml := range map[string]string{
		"empty":                    "",
		"no files":                 "version: 2.17.0\npath: RSI Launcher-Setup-2.17.0.exe\n",
		"another file":             feed("2.17.0", "evil.exe", sum, "10"),
		"a path":                   feed("2.17.0", "../RSI Launcher-Setup-2.17.0.exe", sum, "10"),
		"a folder in the name":     feed("2.17.0", "RSI Launcher-Setup-2.17.0/x.exe", sum, "10"),
		"a quote in the name":      feed("2.17.0", `RSI Launcher-Setup-2"x.exe`, sum, "10"),
		"a percent in the name":    feed("2.17.0", "RSI Launcher-Setup-%PATH%.exe", sum, "10"),
		"no version":               feed("", "RSI Launcher-Setup-2.17.0.exe", sum, "10"),
		"a short hash":             feed("2.17.0", "RSI Launcher-Setup-2.17.0.exe", "AAAA", "10"),
		"not base64":               feed("2.17.0", "RSI Launcher-Setup-2.17.0.exe", "*not base64*", "10"),
		"size 0":                   feed("2.17.0", "RSI Launcher-Setup-2.17.0.exe", sum, "0"),
		"size not a number":        feed("2.17.0", "RSI Launcher-Setup-2.17.0.exe", sum, "big"),
		"too large":                feed("2.17.0", "RSI Launcher-Setup-2.17.0.exe", sum, "99999999999"),
		"path not among the files": "version: 2.17.0\nfiles:\n  - url: RSI Launcher-Setup-2.16.0.exe\n    sha512: " + sum + "\n    size: 10\npath: RSI Launcher-Setup-2.17.0.exe\n",
		"not YAML":                 "<html>maintenance</html>\n",
	} {
		if r, err := parseFeed([]byte(yml)); err == nil {
			t.Errorf("%s: accepted %+v", name, r)
		}
	}
}

func TestFetchInstaller(t *testing.T) {
	newBox(t)
	f := newFeed(t, "2.17.0")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "RSI Launcher-Setup-2.16.0.exe"), "old")
	writeFile(t, filepath.Join(dir, "RSI Launcher-Setup-2.15.0.exe.part"), "old part")
	writeFile(t, filepath.Join(dir, "first-start.bat"), "kept")
	ctx := context.Background()
	r, err := readFeed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Half of it came before the connection dropped.
	half := len(f.installer) / 2
	writeFile(t, filepath.Join(dir, r.File+".part"), string(f.installer[:half]))
	if err := fetchInstaller(ctx, r, dir); err != nil {
		t.Fatal(err)
	}
	if len(f.ranges) != 1 || f.ranges[0] != "bytes="+strconv.Itoa(half)+"-" {
		t.Errorf("asked for %q, want the missing half", f.ranges)
	}
	if got := readFile(t, filepath.Join(dir, r.File)); got != string(f.installer) {
		t.Errorf("installer has %d bytes, not the publisher's", len(got))
	}
	names := dirNames(t, dir)
	if strings.Join(names, ",") != "RSI Launcher-Setup-2.17.0.exe,first-start.bat" {
		t.Errorf("left %v", names)
	}

	// A file that is already there is checked, not fetched again.
	if err := fetchInstaller(ctx, r, dir); err != nil || len(f.ranges) != 1 {
		t.Errorf("fetched again (%v, %v)", f.ranges, err)
	}
}

func TestFetchInstallerServerWithoutRanges(t *testing.T) {
	newBox(t)
	f := newFeed(t, "2.17.0")
	f.noRange = true
	dir := t.TempDir()
	r, err := readFeed(context.Background())
	must(t, err)
	writeFile(t, filepath.Join(dir, r.File+".part"), "garbage that is not the start")
	if err := fetchInstaller(context.Background(), r, dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, r.File)); got != string(f.installer) {
		t.Error("a whole answer to a range was appended, not started over")
	}
}

func TestFetchInstallerMismatch(t *testing.T) {
	newBox(t)
	f := newFeed(t, "2.17.0")
	f.served = bytes.ToUpper(f.installer) // same size, other bytes
	dir := t.TempDir()
	r, err := readFeed(context.Background())
	must(t, err)
	if err := fetchInstaller(context.Background(), r, dir); !errors.Is(err, errMismatch) {
		t.Fatalf("got %v, want errMismatch", err)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("kept %v", names)
	}
}

func TestCheckFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "setup.exe")
	data := []byte("MZ the installer")
	writeFile(t, p, string(data))
	sum := sha512.Sum512(data)
	r := release{File: "setup.exe", SHA512: sum[:], Size: int64(len(data))}
	if ok, err := checkFile(p, r); !ok || err != nil {
		t.Errorf("the right file: %v, %v", ok, err)
	}
	r.Size++
	if ok, _ := checkFile(p, r); ok {
		t.Error("accepted another size")
	}
	r.Size--
	r.SHA512 = append([]byte{^sum[0]}, sum[1:]...)
	if ok, _ := checkFile(p, r); ok {
		t.Error("accepted another hash")
	}
	if ok, err := checkFile(p+".missing", r); ok || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file: %v, %v", ok, err)
	}
}

func TestRangeStart(t *testing.T) {
	for in, want := range map[string]int64{"bytes 100-199/200": 100, "bytes 0-9/10": 0, "": -1, "bytes */200": -1, "items 1-2/3": -1} {
		if got := rangeStart(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	must(t, err)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}
