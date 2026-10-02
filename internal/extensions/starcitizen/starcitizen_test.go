package starcitizen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
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

// Install refuses with a code the helper words: the card shows that
// sentence (extensions.Refuse), and the rest goes to the journal.
func TestInstallRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(b *box)
		disk  string
		want  string
	}{
		{"no drive picked", func(*box) {}, "", codeNoDrive},
		{"no setting at all", func(*box) {}, "none", codeNoDrive},
		{"unplugged", func(b *box) { b.mounted = false; b.writeMounts() }, "SATA1TB", codeNotConnected},
		{"NTFS", func(b *box) { b.gameFS = "ntfs3"; b.writeMounts() }, "SATA1TB", codeWrongFS},
		{"full", func(b *box) { b.free[b.mnt] = 120e9 }, "SATA1TB", codeNoSpace},
		{"full system drive", func(b *box) { b.free[b.root] = 80e9 }, "/var", codeNoSpace},
		{"not a game drive", func(*box) {}, "/var/lib/vos", codeUnknownDrive},
		{"no UUID", func(b *box) { must(t, os.Remove(filepath.Join(byUUIDDir, gameUUID))) }, "SATA1TB", codeUnknownDrive},
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
		if codeOf(err) != tc.want || said(err) == "" {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.want)
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
	if codeOf(err) != codeMismatch {
		t.Errorf("got %v", err)
	}
	// The prefix is recorded, so removing it finds it; Steam gets no shortcut yet.
	if st, ok := readState(x.DataDir); !ok || st.Prefix != b.mnt+"/VaporOS/star-citizen" || st.Installer != "" {
		t.Errorf("state %+v", st)
	}
	if parts := (helper{}).Steam(x); len(parts.Shortcuts) != 0 {
		t.Errorf("Steam parts %+v before the installer is there", parts)
	}

	// The download page doesn't answer.
	feedURL = f.srv.URL + "/rel/2/gone.yml"
	if err := (helper{}).Install(context.Background(), x); codeOf(err) != codeFeed {
		t.Errorf("no feed: %v", err)
	}

	// Only a code fetch-installer prints, on its last line, is the card's;
	// nothing else it says is.
	for name, out := range map[string]string{
		"runuser's words":    "runuser: user vapor does not exist",
		"a sentence":         "Star Citizen's drive, SATA1TB, isn't connected. Connect it, then try again.",
		"another code":       `{"refused":"no-drive"}`,
		"an unknown code":    `{"refused":"pwned"}`,
		"a code, then words": "{\"refused\":\"download-failed\"}\nruntime: out of memory",
	} {
		asGamer = func(context.Context, string, ...string) (string, error) { return out, os.ErrPermission }
		if err := (helper{}).Install(context.Background(), x); err == nil || codeOf(err) != "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	asGamer = func(context.Context, string, ...string) (string, error) {
		return "fetch-installer: download-failed: EOF\n{\"refused\":\"download-failed\"}", os.ErrPermission
	}
	if err := (helper{}).Install(context.Background(), x); codeOf(err) != codeDownload {
		t.Errorf("a failed download: %v", err)
	}
	for name, out := range map[string]string{"a stray name": `{"installer":"../x.exe"}`, "a refusal, exit 0": `{"refused":"download-failed"}`} {
		asGamer = func(context.Context, string, ...string) (string, error) { return out, nil }
		if err := (helper{}).Install(context.Background(), x); err == nil || codeOf(err) != "" {
			t.Errorf("%s: %v", name, err)
		}
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

	const notConnected = "warning: Star Citizen's drive isn't connected. Connect it, then try again."
	b.mounted = false
	b.writeMounts()
	check("unplugged", "It's set up on SATA1TB.", notConnected)
	b.mounted = true
	b.swapDrive()
	check("another drive with its name", "It's set up on SATA1TB.", notConnected)
	b.gameDev = "sdb1"
	b.writeMounts()
	must(t, os.Rename(filepath.Join(prefix, markerName), filepath.Join(prefix, "gone")))
	check("its files deleted", "It's set up on SATA1TB.", "warning: Star Citizen's files are missing from its drive. Remove Star Citizen and add it again.")
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
	check("its files deleted", "It's set up on the system drive.", "warning: Star Citizen's files are missing from its drive. Remove Star Citizen and add it again.")
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
	b.mounted = false
	b.writeMounts()
	if err := (helper{}).Remove(context.Background(), x, true); codeOf(err) != codeFilesStay {
		t.Errorf("unplugged: %v", err)
	}
	b.mounted = true
	b.swapDrive()
	if err := (helper{}).Remove(context.Background(), x, true); codeOf(err) != codeFilesStay {
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
	must(t, os.Remove(statePath(x.DataDir)))
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
	if code := cli([]string{"bogus"}); code != 2 {
		t.Errorf("an unknown verb: exit %d", code)
	}

	// A refusal is one line on stdout with its code, the detail on stderr.
	refused := func(name, prefix, want string) {
		t.Helper()
		out.Reset()
		errOut.Reset()
		code := fetchInstallerCmd(context.Background(), []string{"--prefix", prefix}, &out, &errOut)
		var res fetchResult
		if err := json.Unmarshal(out.Bytes(), &res); err != nil || code != 1 || res != (fetchResult{Refused: want}) || !fetchCodes[want] {
			t.Errorf("%s: exit %d, %q (%v), want %s", name, code, out.String(), err, want)
		}
		if !strings.HasPrefix(errOut.String(), "fetch-installer: "+want+": ") {
			t.Errorf("%s: stderr %q", name, errOut.String())
		}
	}
	refused("not a prefix", "/tmp", codeFilesElsewhere)
	refused("above the prefix", b.mnt+"/VaporOS", codeFilesElsewhere)
	refused("not the recorded prefix", systemPrefix(), codeFilesElsewhere)
	feedURL = f.srv.URL + "/rel/2/gone.yml"
	refused("no feed", prefix, codeFeed)
	b.mounted = false
	b.writeMounts()
	refused("unplugged", prefix, codeNotConnected)
}

// The card's status, the launch hook and fetch-installer judge Star
// Citizen's drive the same way (locate): another drive with its name,
// without its files, isn't connected, and its own drive without them has
// lost them. Neither says to set it up again on the wrong drive.
func TestOneJudgement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(b *box, prefix string)
		want  string
	}{
		{"another drive with its name", func(b *box, prefix string) {
			b.swapDrive()
			must(t, os.Rename(b.mnt+"/VaporOS", b.root+"/elsewhere")) // not on the other drive
		}, codeNotConnected},
		{"its own drive without its files", func(b *box, prefix string) {
			must(t, os.RemoveAll(prefix))
		}, codeFilesMissing},
		{"its own drive with another drive's files", func(b *box, prefix string) {
			writeFile(t, filepath.Join(prefix, markerName), otherUUID+"\n")
		}, codeFilesMissing},
	} {
		b := newBox(t)
		newFeed(t, "2.17.0")
		x := b.ext(b.mnt)
		must(t, (helper{}).Install(context.Background(), x))
		prefix := b.mnt + "/VaporOS/star-citizen"
		setup := prefix + "/installer/RSI Launcher-Setup-2.17.0.exe"
		tc.setup(b, prefix)
		sentence, _ := messageText(tc.want)

		lines := (helper{}).Status(context.Background(), x)
		if len(lines) != 2 || lines[0].Text != "It's set up on SATA1TB." || lines[1] != (extensions.StatusLine{Text: sentence, Tone: "warning"}) {
			t.Errorf("%s: status %q", tc.name, statusTexts(lines))
		}
		l := &extensions.Launch{Shortcut: "star-citizen/launcher", Argv: steamLine(setup, true), Env: slices.Clone(steamEnv)}
		if err := (helper{}).LaunchHook(context.Background(), l); codeOf(err) != tc.want || said(err) != sentence {
			t.Errorf("%s: the launch hook: %v", tc.name, err)
		}
		var out bytes.Buffer
		code := fetchInstallerCmd(context.Background(), []string{"--prefix", prefix}, &out, io.Discard)
		if got := strings.TrimSpace(out.String()); code != 1 || got != `{"refused":"`+tc.want+`"}` {
			t.Errorf("%s: fetch-installer: exit %d, %s", tc.name, code, got)
		}
	}
}

// A drive's label is shown as it is: never capitalized, and no sentence
// starts with one.
func TestLabelsStayAsTheyAre(t *testing.T) {
	b := newBoxNamed(t, "games")
	newFeed(t, "2.17.0")
	b.free[b.mnt] = 900e9
	x := b.ext(b.mnt)
	must(t, (helper{}).Install(context.Background(), x))
	want := []string{"Its files are on games, which has 900 GB free.", "Start Star Citizen in Steam: its first start installs the RSI Launcher."}
	if got := statusTexts((helper{}).Status(context.Background(), x)); !slices.Equal(got, want) {
		t.Errorf("status %q", got)
	}
	b.mounted = false
	b.writeMounts()
	if got := statusTexts((helper{}).Status(context.Background(), x)); len(got) == 0 || got[0] != "It's set up on games." {
		t.Errorf("unplugged: %q", got)
	}
	x.Settings["disk"] = "/var"
	if got := statusTexts((helper{}).Status(context.Background(), x)); !slices.Contains(got, "warning: You picked another drive. Star Citizen stays on games until you remove it and add it again.") {
		t.Errorf("another drive picked: %q", got)
	}
}

func TestMessageText(t *testing.T) {
	words, ok := extensions.HelperFor(ID).(extensions.MessageWords)
	if !ok {
		t.Fatal("the helper words no codes")
	}
	codes := []string{codeNotConnected, codeFilesMissing, codeFilesElsewhere, codeInstallerMissing, codeNoDrive,
		codeUnknownDrive, codeWrongFS, codeNoSpace, codeCantWrite, codeFeed, codeDownload, codeMismatch,
		codeFilesStay, codeCantDelete}
	for _, code := range codes {
		text, ok := words.MessageText(code)
		if !ok || text == "" || !strings.HasSuffix(text, ".") || strings.Count(text, ". ") > 1 || strings.Contains(text, "  ") {
			t.Errorf("%s: %q", code, text)
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`).MatchString(code) || code == "not-mounted" || code == "hook-failed" {
			t.Errorf("%s is not a helper's code", code)
		}
	}
	for code := range fetchCodes {
		if !slices.Contains(codes, code) {
			t.Errorf("fetch-installer's %s has no words", code)
		}
	}
	if _, ok := words.MessageText("starting"); ok {
		t.Error("an unknown code has words")
	}
	// The formats it names are the ones the shipped descriptor takes.
	want := "Pick a drive formatted as " + orList(games(shipped(t)).FS) + ", then select Try again."
	if text, _ := messageText(codeWrongFS); !strings.HasSuffix(text, want) {
		t.Errorf("wrong-filesystem: %q", text)
	}
	if said(errors.New("plain")) != "" || said(nil) != "" {
		t.Error("a plain error has words")
	}
}
