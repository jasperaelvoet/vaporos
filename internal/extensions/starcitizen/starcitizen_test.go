package starcitizen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

const launcherPath = "/pfx/drive_c/Program Files/Roberts Space Industries/RSI Launcher/RSI Launcher.exe"

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

	// Again, with a newer launcher out: the shortcut gets the new installer,
	// and the old one stays until Steam starts the new one (the launch hook).
	f2 := newFeed(t, "2.18.0")
	if err := (helper{}).Install(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if names := dirNames(t, filepath.Join(prefix, "installer")); !slices.Equal(names, []string{f.file, f2.file}) {
		t.Errorf("installer/ holds %v", names)
	}
	if st, _ := readState(x.DataDir); st.Installer != f2.file || st.Version != "2.18.0" {
		t.Errorf("state %+v", st)
	}
}

func TestInstallSystemDrive(t *testing.T) {
	for _, disk := range []string{"/var", "/state"} {
		b := newBox(t)
		newFeed(t, "2.17.0")
		x := b.ext(disk)
		if err := (helper{}).Install(context.Background(), x); err != nil {
			t.Fatalf("%s: %v", disk, err)
		}
		prefix := filepath.Join(config.GamerHome, ".local/share/vaporos/ext/star-citizen")
		if got := readFile(t, filepath.Join(prefix, markerName)); got != sysUUID+"\n" {
			t.Errorf("%s: marker %q", disk, got)
		}
		if st, ok := readState(x.DataDir); !ok || st.Prefix != prefix || st.Disk != "/var" || st.UUID != sysUUID {
			t.Errorf("%s: state %+v", disk, st)
		}
	}
}

func TestInstallRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(b *box)
		disk  string
		want  string
	}{
		{"no drive picked", func(*box) {}, "", "Pick a game drive for Star Citizen on its card, then select Try again."},
		{"no setting at all", func(*box) {}, "none", "Pick a game drive for Star Citizen on its card, then select Try again."},
		{"unplugged", func(b *box) { b.mounted = false; b.writeMounts() }, "SATA1TB", "Star Citizen's drive, SATA1TB, isn't connected. Connect it, then try again."},
		{"NTFS", func(b *box) { b.gameFS = "ntfs3"; b.writeMounts() }, "SATA1TB", "SATA1TB is formatted as ntfs3, which Star Citizen can't run from. Pick a drive formatted as ext4, btrfs, xfs or f2fs."},
		{"full", func(b *box) { b.free[b.mnt] = 120e9 }, "SATA1TB", "SATA1TB has 120 GB free, and Star Citizen needs 150 GB. Free up space or pick another drive."},
		{"full system drive", func(b *box) { b.free[b.root] = 80e9 }, "/var", "The system drive has 80 GB free, and Star Citizen needs 150 GB. Free up space or pick another drive."},
		{"not a game drive", func(*box) {}, "/var/lib/vos", "VaporOS doesn't know the drive picked for Star Citizen. Pick another one on its card, then select Try again."},
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
		if disk == "none" {
			delete(x.Settings, "disk")
		}
		err := (helper{}).Install(context.Background(), x)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v\nwant %s", tc.name, err, tc.want)
		}
		if len(b.calls) != 0 {
			t.Errorf("%s: downloaded anyway", tc.name)
		}
		for _, p := range []string{b.mnt + "/VaporOS", filepath.Join(config.GamerHome, ".local/share/vaporos/ext/star-citizen"), x.DataDir} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s: made %s anyway", tc.name, p)
			}
		}
	}
}

func TestInstallKeepsAnInstalledGame(t *testing.T) {
	b := newBox(t)
	newFeed(t, "2.17.0")
	prefix := b.installed()
	writeFile(t, prefix+launcherPath, "MZ")
	b.free[b.mnt] = 20e9 // the game took it
	if err := (helper{}).Install(context.Background(), b.ext(b.mnt)); err != nil {
		t.Errorf("installing again over its own game: %v", err)
	}
}

// Added again over an installed launcher, Star Citizen keeps the installer
// it has: the installer only runs on a first start.
func TestInstallSkipsTheDownloadOverAnInstalledLauncher(t *testing.T) {
	b := newBox(t)
	f := newFeed(t, "2.17.0")
	x := b.ext(b.mnt)
	must(t, (helper{}).Install(context.Background(), x))
	prefix := b.mnt + "/VaporOS/star-citizen"
	writeFile(t, prefix+launcherPath, "MZ")

	newFeed(t, "2.18.0")
	b.calls = nil
	must(t, (helper{}).Install(context.Background(), x))
	if len(b.calls) != 0 {
		t.Errorf("ran %q", b.calls)
	}
	if st, _ := readState(x.DataDir); st.Installer != f.file || st.Version != "2.17.0" {
		t.Errorf("state %+v", st)
	}

	// Without its installer, or before the launcher is in, it downloads.
	must(t, os.Remove(filepath.Join(prefix, "installer", f.file)))
	must(t, (helper{}).Install(context.Background(), x))
	if st, _ := readState(x.DataDir); len(b.calls) != 1 || st.Installer != "RSI Launcher-Setup-2.18.0.exe" {
		t.Errorf("ran %q, state %+v", b.calls, st)
	}
	must(t, os.Remove(prefix+launcherPath))
	newFeed(t, "2.19.0")
	must(t, (helper{}).Install(context.Background(), x))
	if st, _ := readState(x.DataDir); len(b.calls) != 2 || st.Installer != "RSI Launcher-Setup-2.19.0.exe" {
		t.Errorf("ran %q, state %+v", b.calls, st)
	}
}

func TestInstallDownloadFails(t *testing.T) {
	b := newBox(t)
	f := newFeed(t, "2.17.0")
	f.served = bytes.ToUpper(f.installer)
	x := b.ext(b.mnt)
	err := (helper{}).Install(context.Background(), x)
	if err == nil || err.Error() != "The RSI Launcher's installer didn't match the fingerprint its publisher lists, so VaporOS deleted it. Try again later." {
		t.Errorf("got %v", err)
	}
	// The prefix is recorded, so removing it finds it; Steam gets no shortcut yet.
	if st, ok := readState(x.DataDir); !ok || st.Prefix != b.mnt+"/VaporOS/star-citizen" || st.Installer != "" {
		t.Errorf("state %+v", st)
	}
	if parts := (helper{}).Steam(x); len(parts.Shortcuts) != 0 {
		t.Errorf("Steam parts %+v before the installer is there", parts)
	}

	// fetch-installer's words that are not for the person are not the card's.
	asGamer = func(context.Context, string, ...string) (string, error) {
		return "runuser: user vapor does not exist", os.ErrPermission
	}
	if err := (helper{}).Install(context.Background(), x); err == nil || err.Error() != "The RSI Launcher's installer didn't download. Check the internet connection, then try again." {
		t.Errorf("got %v", err)
	}
	asGamer = func(context.Context, string, ...string) (string, error) { return `{"installer":"../x.exe"}`, nil }
	if err := (helper{}).Install(context.Background(), x); err == nil || err.Error() != "The RSI Launcher's installer didn't download. Check the internet connection, then try again." {
		t.Errorf("got %v", err)
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
	const where = "Its files are on SATA1TB, which has 900 GB free."
	check("installed", where, "Start Star Citizen in Steam: its first start installs the RSI Launcher.")

	prefix := b.mnt + "/VaporOS/star-citizen"
	writeFile(t, prefix+launcherPath, "MZ")
	check("launcher in", where, "The RSI Launcher is installed.")

	x.Settings["disk"] = "/var"
	check("another drive picked", where, "The RSI Launcher is installed.",
		"warning: You picked another drive. Star Citizen stays on SATA1TB until you remove it and add it again.")
	x.Settings["disk"] = ""
	check("no drive picked", where, "The RSI Launcher is installed.")
	x.Settings["disk"] = "/mnt/SATA1TB"
	check("the same drive, spelled otherwise", where, "The RSI Launcher is installed.")

	b.mounted = false
	b.writeMounts()
	check("unplugged", "warning: Star Citizen's drive, SATA1TB, isn't connected. Connect it, then try again.")
	b.mounted = true
	b.swapDrive()
	check("another drive with its name", "warning: Star Citizen's drive, SATA1TB, isn't connected. Connect it, then try again.")
	b.gameDev = "sdb1"
	b.writeMounts()
	must(t, os.Rename(filepath.Join(prefix, markerName), filepath.Join(prefix, "gone")))
	check("its files deleted", "warning: Star Citizen's files on SATA1TB are missing. Remove Star Citizen and add it again.")
	must(t, os.Rename(filepath.Join(prefix, "gone"), filepath.Join(prefix, markerName)))

	b.memory(16, 4)
	check("16 GB and a little swap", where, "The RSI Launcher is installed.",
		"warning: This PC has 20 GB of memory and swap together, and Star Citizen wants 32 GB. It may stutter or close in busy places.")
	b.memory(8, 8)
	check("8 GB", where, "The RSI Launcher is installed.",
		"warning: This PC has 8 GB of memory, and Star Citizen needs 16 GB. It may not start, or it may close while you play.")
}

func TestStatusSystemDrive(t *testing.T) {
	b := newBox(t)
	newFeed(t, "2.17.0")
	x := b.ext("/var")
	must(t, (helper{}).Install(context.Background(), x))
	check := func(name string, want ...string) {
		t.Helper()
		if got := statusTexts((helper{}).Status(context.Background(), x)); !slices.Equal(got, want) {
			t.Errorf("%s:\n%q\nwant\n%q", name, got, want)
		}
	}
	check("installed", "Its files are on the system drive, which has 400 GB free.", "Start Star Citizen in Steam: its first start installs the RSI Launcher.")
	x.Settings["disk"] = "/state"
	check("the system drive, spelled otherwise", "Its files are on the system drive, which has 400 GB free.", "Start Star Citizen in Steam: its first start installs the RSI Launcher.")
	must(t, os.RemoveAll(systemPrefix()))
	check("its files deleted", "warning: Star Citizen's files on the system drive are missing. Remove Star Citizen and add it again.")
}

func TestMemoryWarning(t *testing.T) {
	newBox(t)
	const gib = 1 << 20 // in kB
	for _, tc := range []struct {
		name      string
		ram, swap uint64 // kB
		want      string
	}{
		{"a 16 GB PC with zram as large", 15*gib + gib/4, 15*gib + gib/4, ""},
		{"32 GB, no swap", 31 * gib, 0, ""},
		{"16 GB, no swap", 15*gib + gib/2, 0, "This PC has 16 GB of memory and swap together, and Star Citizen wants 32 GB. It may stutter or close in busy places."},
		{"12 GB", 12 * gib, 12 * gib, "This PC has 12 GB of memory, and Star Citizen needs 16 GB. It may not start, or it may close while you play."},
	} {
		writeFile(t, meminfoPath, fmt.Sprintf("MemTotal: %d kB\nSwapTotal: %d kB\n", tc.ram, tc.swap))
		if got := memoryWarning(); got != tc.want {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	writeFile(t, meminfoPath, "SwapTotal: 0 kB\n")
	if got := memoryWarning(); got != "" {
		t.Errorf("without MemTotal: %q", got)
	}
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

	// Unplugged, or another drive with its name: its files cannot go, and
	// the card says so.
	const stay = "Star Citizen's drive, SATA1TB, isn't connected, so its files stay on it."
	b.mounted = false
	b.writeMounts()
	if err := (helper{}).Remove(context.Background(), x, true); err == nil || err.Error() != stay {
		t.Errorf("unplugged: %v", err)
	}
	b.mounted = true
	b.swapDrive()
	if err := (helper{}).Remove(context.Background(), x, true); err == nil || err.Error() != stay {
		t.Errorf("another drive: %v", err)
	}
	if len(b.calls) != 0 {
		t.Errorf("deleted on a drive that isn't the recorded one: %q", b.calls)
	}
	if _, err := os.Stat(prefix); err != nil {
		t.Error("its files went")
	}

	b.gameDev = "sdb1"
	b.writeMounts()
	if err := (helper{}).Remove(context.Background(), x, true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"rm", "-rf", "--one-file-system", "--", prefix},
		{"rmdir", "--ignore-fail-on-non-empty", "--", b.mnt + "/VaporOS"},
	}
	if !reflect.DeepEqual(b.calls, want) {
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
	x.Settings["disk"] = "/var" // only the recorded prefix goes
	b.calls = nil
	must(t, (helper{}).Remove(context.Background(), x, true))
	var rm []string
	for _, c := range b.calls {
		if c[0] == "rm" {
			rm = append(rm, c[len(c)-1])
		}
	}
	if !slices.Equal(rm, []string{b.mnt + "/VaporOS/star-citizen"}) {
		t.Errorf("deleted %q", rm)
	}
	if _, err := os.Stat(b.mnt + "/VaporOS/truckersmp/x"); err != nil {
		t.Error("another extension's files went")
	}
}

// On the system drive the prefix is the home data area, which vosd's purge
// deletes; without a record, the helper deletes nothing.
func TestRemoveLeavesTheRestToVosd(t *testing.T) {
	b := newBox(t)
	newFeed(t, "2.17.0")
	x := b.ext("/var")
	must(t, (helper{}).Install(context.Background(), x))
	b.calls = nil
	must(t, (helper{}).Remove(context.Background(), x, true))
	if len(b.calls) != 0 {
		t.Errorf("ran %q", b.calls)
	}

	x = b.ext(b.mnt)
	b.installed()
	must(t, (helper{}).Remove(context.Background(), x, true))
	if len(b.calls) != 0 {
		t.Errorf("without a record, ran %q", b.calls)
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
		errOut.Reset()
		if code := fetchInstallerCmd(context.Background(), []string{"--prefix", p}, &out, &errOut); code != 1 ||
			lastLine(errOut.String()) != "Star Citizen's files aren't where VaporOS put them. Remove Star Citizen and add it again." {
			t.Errorf("%s: exit %d, %s", p, code, errOut.String())
		}
	}
	b.mounted = false
	b.writeMounts()
	errOut.Reset()
	if code := fetchInstallerCmd(context.Background(), []string{"--prefix", prefix}, &out, &errOut); code != 1 ||
		lastLine(errOut.String()) != "Star Citizen's drive, SATA1TB, isn't connected. Connect it, then try again." {
		t.Errorf("unplugged: exit %d, %s", code, errOut.String())
	}
	errOut.Reset()
	if code := fetchInstallerCmd(context.Background(), []string{"--prefix", systemPrefix()}, &out, &errOut); code != 1 ||
		lastLine(errOut.String()) != "Star Citizen's files on the system drive are missing. Remove Star Citizen and add it again." {
		t.Errorf("system drive without its files: exit %d, %s", code, errOut.String())
	}
	if code := cli([]string{"bogus"}); code != 2 {
		t.Errorf("an unknown verb: exit %d", code)
	}
}
