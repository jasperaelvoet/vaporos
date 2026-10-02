package starcitizen

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

func TestRegistered(t *testing.T) {
	if _, ok := extensions.HelperFor(ID).(helper); !ok {
		t.Fatal("the star-citizen helper is not registered")
	}
}

func TestInstallGameDrive(t *testing.T) {
	b := newBox(t)
	f := newFeed(t, "2.17.0")
	x := b.ext("/mnt/SATA1TB") // as /storage may show it
	if err := (helper{}).Install(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	prefix := b.mnt + "/VaporOS/star-citizen"
	if got := readFile(t, filepath.Join(prefix, markerName)); got != gameUUID+"\n" {
		t.Errorf("marker %q", got)
	}
	if got := readFile(t, filepath.Join(prefix, "installer", f.file)); got != string(f.installer) {
		t.Error("the installer is not the publisher's")
	}
	st, ok := readState(x.DataDir)
	if !ok || st != (state{Disk: b.mnt, Prefix: prefix, UUID: gameUUID, Installer: f.file, Version: "2.17.0"}) {
		t.Errorf("state %+v", st)
	}
	if len(b.calls) != 1 || !slices.Equal(b.calls[0], []string{"/usr/bin/vos", "ext", "star-citizen", "fetch-installer", "--prefix", prefix}) {
		t.Errorf("ran %q as vapor", b.calls)
	}

	parts := (helper{}).Steam(x)
	want := extensions.ShortcutTarget{Exe: prefix + "/installer/RSI Launcher-Setup-2.17.0.exe", StartDir: prefix}
	if len(parts.Shortcuts) != 1 || !reflect.DeepEqual(parts.Shortcuts["launcher"], want) {
		t.Errorf("Steam parts %+v", parts)
	}

	// Again, with a newer launcher out: the new installer replaces the old.
	f2 := newFeed(t, "2.18.0")
	if err := (helper{}).Install(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if names := dirNames(t, filepath.Join(prefix, "installer")); !slices.Equal(names, []string{f2.file}) {
		t.Errorf("installer/ holds %v", names)
	}
	if st, _ := readState(x.DataDir); st.Installer != f2.file || st.Version != "2.18.0" {
		t.Errorf("state %+v", st)
	}
}

func TestInstallSystemDrive(t *testing.T) {
	b := newBox(t)
	newFeed(t, "2.17.0")
	x := b.ext("")
	if err := (helper{}).Install(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(config.GamerHome, ".local/share/vaporos/ext/star-citizen")
	if got := readFile(t, filepath.Join(prefix, markerName)); got != sysUUID+"\n" {
		t.Errorf("marker %q", got)
	}
	if st, ok := readState(x.DataDir); !ok || st.Prefix != prefix || st.Disk != "" || st.UUID != sysUUID {
		t.Errorf("state %+v", st)
	}
}

func TestInstallRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(b *box)
		disk  string
		want  string
	}{
		{"unplugged", func(b *box) { b.mounted = false; b.writeMounts() }, "SATA1TB", "SATA1TB isn't connected. Connect it, or pick another drive for it."},
		{"NTFS", func(b *box) { b.gameFS = "ntfs3"; b.writeMounts() }, "SATA1TB", "SATA1TB is formatted as ntfs3, which Star Citizen can't run from. Pick a drive formatted as ext4, btrfs, xfs or f2fs."},
		{"full", func(b *box) { b.free[b.mnt] = 120e9 }, "SATA1TB", "SATA1TB has 120 GB free, and Star Citizen needs 150 GB. Free up space or pick another drive."},
		{"full system drive", func(b *box) { b.free[b.root] = 80e9 }, "", "the system drive has 80 GB free, and Star Citizen needs 150 GB. Free up space or pick another drive."},
		{"not a game drive", func(*box) {}, "/var/lib/vos", "VaporOS doesn't know the drive picked for it. Pick a game drive, or none for the system drive."},
		{"no UUID", func(b *box) { must(t, os.Remove(filepath.Join(byUUIDDir, gameUUID))) }, "SATA1TB", "VaporOS couldn't tell which drive SATA1TB is. Pick another drive."},
	} {
		b := newBox(t)
		newFeed(t, "2.17.0")
		tc.setup(b)
		disk := tc.disk
		if disk == "SATA1TB" {
			disk = b.mnt
		}
		x := b.ext(disk)
		err := (helper{}).Install(context.Background(), x)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v\nwant %s", tc.name, err, tc.want)
		}
		if len(b.calls) != 0 {
			t.Errorf("%s: downloaded anyway", tc.name)
		}
		if _, err := os.Stat(b.mnt + "/VaporOS"); err == nil {
			t.Errorf("%s: made its folder anyway", tc.name)
		}
	}
}

func TestInstallKeepsAnInstalledGame(t *testing.T) {
	b := newBox(t)
	newFeed(t, "2.17.0")
	prefix := b.installed()
	writeFile(t, prefix+"/pfx/drive_c/Program Files/Roberts Space Industries/RSI Launcher/RSI Launcher.exe", "MZ")
	b.free[b.mnt] = 20e9 // the game took it
	if err := (helper{}).Install(context.Background(), b.ext(b.mnt)); err != nil {
		t.Errorf("installing again over its own game: %v", err)
	}
}

func TestInstallDownloadFails(t *testing.T) {
	b := newBox(t)
	f := newFeed(t, "2.17.0")
	f.served = bytes.ToUpper(f.installer)
	x := b.ext(b.mnt)
	err := (helper{}).Install(context.Background(), x)
	if err == nil || err.Error() != "the RSI Launcher's installer didn't match the fingerprint its publisher lists, so VaporOS deleted it. Try again later." {
		t.Errorf("got %v", err)
	}
	// The prefix is recorded, so removing it finds it; Steam gets no shortcut yet.
	if st, ok := readState(x.DataDir); !ok || st.Prefix != b.mnt+"/VaporOS/star-citizen" || st.Installer != "" {
		t.Errorf("state %+v", st)
	}
	if parts := (helper{}).Steam(x); len(parts.Shortcuts) != 0 {
		t.Errorf("Steam parts %+v before the installer is there", parts)
	}
}

func TestSteamWithoutInstall(t *testing.T) {
	b := newBox(t)
	x := b.ext(b.mnt)
	if parts := (helper{}).Steam(x); len(parts.Shortcuts) != 0 {
		t.Errorf("Steam parts %+v", parts)
	}
	// A state naming a place VaporOS never uses gives Steam nothing.
	must(t, os.MkdirAll(x.DataDir, 0o755))
	must(t, writeState(x.DataDir, state{Prefix: "/etc", Installer: "RSI Launcher-Setup-2.17.0.exe"}))
	if parts := (helper{}).Steam(x); len(parts.Shortcuts) != 0 {
		t.Errorf("Steam parts %+v", parts)
	}
}

func statusTexts(lines []extensions.StatusLine) []string {
	var out []string
	for _, l := range lines {
		s := l.Text
		if l.Tone != "" {
			s = l.Tone + ": " + s
		}
		out = append(out, s)
	}
	return out
}

func TestStatus(t *testing.T) {
	b := newBox(t)
	newFeed(t, "2.17.0")
	x := b.ext(b.mnt)
	if got := (helper{}).Status(context.Background(), x); len(got) != 0 {
		t.Errorf("before the install: %q", statusTexts(got))
	}
	must(t, (helper{}).Install(context.Background(), x))
	check := func(name string, want ...string) {
		t.Helper()
		if got := statusTexts((helper{}).Status(context.Background(), x)); !slices.Equal(got, want) {
			t.Errorf("%s:\n%q\nwant\n%q", name, got, want)
		}
	}
	check("installed", "Its files are on SATA1TB, which has 900 GB free.", "Start Star Citizen in Steam: its first start installs the RSI Launcher.")

	writeFile(t, b.mnt+"/VaporOS/star-citizen/pfx/drive_c/Program Files/Roberts Space Industries/RSI Launcher/RSI Launcher.exe", "MZ")
	check("launcher in", "Its files are on SATA1TB, which has 900 GB free.", "The RSI Launcher is installed.")

	x.Settings["disk"] = ""
	check("another drive picked", "Its files are on SATA1TB, which has 900 GB free.", "The RSI Launcher is installed.",
		"warning: You picked another drive. Star Citizen stays on SATA1TB until you remove it and install it again.")
	x.Settings["disk"] = "/mnt/SATA1TB"
	check("the same drive, spelled otherwise", "Its files are on SATA1TB, which has 900 GB free.", "The RSI Launcher is installed.")

	b.mounted = false
	b.writeMounts()
	check("unplugged", "warning: Its drive, SATA1TB, isn't connected. Connect it to play.")

	b.mounted = true
	b.writeMounts()
	b.memory(16, 16)
	check("16 GB", "Its files are on SATA1TB, which has 900 GB free.", "The RSI Launcher is installed.",
		"warning: This PC has 32 GB of memory and swap together, and Star Citizen wants 48 GB. It may stutter or close in busy places.")
	b.memory(8, 8)
	check("8 GB", "Its files are on SATA1TB, which has 900 GB free.", "The RSI Launcher is installed.",
		"warning: This PC has 8 GB of memory, and Star Citizen needs 16 GB. It may not start, or close while you play.")
}

func TestRemove(t *testing.T) {
	b := newBox(t)
	newFeed(t, "2.17.0")
	x := b.ext(b.mnt)
	must(t, (helper{}).Install(context.Background(), x))
	prefix := b.mnt + "/VaporOS/star-citizen"
	b.calls = nil

	must(t, (helper{}).Remove(context.Background(), x, false))
	if len(b.calls) != 0 {
		t.Errorf("removing without purge ran %q", b.calls)
	}
	if _, err := os.Stat(prefix); err != nil {
		t.Error("removing without purge deleted the game")
	}

	// Unplugged: its files cannot go, and the card says so.
	b.mounted = false
	b.writeMounts()
	err := (helper{}).Remove(context.Background(), x, true)
	if err == nil || err.Error() != "SATA1TB isn't connected, so the files in VaporOS/star-citizen on it stay." {
		t.Errorf("unplugged: %v", err)
	}
	if len(b.calls) != 0 {
		t.Errorf("deleted on an unplugged drive: %q", b.calls)
	}

	b.mounted = true
	b.writeMounts()
	if err := (helper{}).Remove(context.Background(), x, true); err != nil {
		t.Fatal(err)
	}
	// The setting's place is the same; the system drive's area is vosd's to purge.
	if len(b.calls) != 1 || !slices.Equal(b.calls[0], []string{"rm", "-rf", "--one-file-system", "--", prefix}) {
		t.Errorf("ran %q", b.calls)
	}
	if _, err := os.Lstat(b.mnt + "/VaporOS"); err == nil {
		t.Error("an empty VaporOS folder stayed")
	}
}

func TestRemoveKeepsOthersFolder(t *testing.T) {
	b := newBox(t)
	newFeed(t, "2.17.0")
	x := b.ext(b.mnt)
	must(t, (helper{}).Install(context.Background(), x))
	writeFile(t, b.mnt+"/VaporOS/truckersmp/x", "another extension's")
	x.Settings["disk"] = ""
	must(t, (helper{}).Remove(context.Background(), x, true))
	var rm []string
	for _, c := range b.calls {
		if c[0] == "rm" {
			rm = append(rm, c[len(c)-1])
		}
	}
	sys, _ := placeFor("")
	if !slices.Equal(rm, []string{b.mnt + "/VaporOS/star-citizen", sys.Prefix()}) {
		t.Errorf("deleted %q", rm)
	}
	if _, err := os.Stat(b.mnt + "/VaporOS/truckersmp/x"); err != nil {
		t.Error("another extension's files went")
	}
}

func TestFetchInstallerCommand(t *testing.T) {
	b := newBox(t)
	f := newFeed(t, "2.17.0")
	prefix := b.installed()
	var out, errOut bytes.Buffer
	if code := fetchInstallerCmd(context.Background(), []string{"--prefix", prefix}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var res fetchResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res != (fetchResult{Installer: f.file, Version: "2.17.0"}) {
		t.Errorf("printed %q (%v)", out.String(), err)
	}
	for _, args := range [][]string{nil, {"--prefix"}, {"--prefix", prefix, "extra"}, {"--bogus"}} {
		out.Reset()
		errOut.Reset()
		if code := fetchInstallerCmd(context.Background(), args, &out, &errOut); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	for _, p := range []string{"/tmp", b.mnt + "/VaporOS"} {
		if code := fetchInstallerCmd(context.Background(), []string{"--prefix", p}, &out, &errOut); code != 1 {
			t.Errorf("%s: exit %d", p, code)
		}
	}
	b.mounted = false
	b.writeMounts()
	errOut.Reset()
	if code := fetchInstallerCmd(context.Background(), []string{"--prefix", prefix}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "isn't connected") {
		t.Errorf("unplugged: exit %d, %s", code, errOut.String())
	}
	if code := cli([]string{"bogus"}); code != 2 {
		t.Errorf("an unknown verb: exit %d", code)
	}
}
