package starcitizen

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

const steamHome = "/var/home/vapor/.local/share/Steam"

// steamLine is what Steam runs for the shortcut, after the dispatcher's
// own words (`vos ext launch --shortcut star-citizen/launcher`): the
// command a compatibility tool on a Steam Linux Runtime gets, with the
// reaper line in front when Steam puts it there.
func steamLine(exe string, reaper bool) []string {
	var argv []string
	if reaper {
		argv = []string{steamHome + "/ubuntu12_32/reaper", "SteamLaunch", "AppId=3228583970", "--",
			steamHome + "/ubuntu12_32/steam-launch-wrapper", "--"}
	}
	return append(argv,
		steamHome+"/steamapps/common/SteamLinuxRuntime_sniper/_v2-entry-point", "--verb=waitforexitandrun", "--",
		"/usr/share/steam/compatibilitytools.d/proton-cachyos-slr/proton", "waitforexitandrun", exe)
}

var steamEnv = []string{
	"HOME=/var/home/vapor",
	"STEAM_COMPAT_DATA_PATH=" + steamHome + "/steamapps/compatdata/3228583970",
	"STEAM_COMPAT_CLIENT_INSTALL_PATH=" + steamHome,
	"SteamGameId=13866272215130325530",
	"LD_PRELOAD=" + steamHome + "/ubuntu12_32/gameoverlayrenderer.so",
}

func envOf(env []string, key string) []string {
	var out []string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			out = append(out, v)
		}
	}
	return out
}

// installed makes the prefix as Install leaves it on the game drive.
func (b *box) installed() string {
	prefix := b.mnt + "/VaporOS/star-citizen"
	writeFile(b.t, filepath.Join(prefix, markerName), gameUUID+"\n")
	writeFile(b.t, filepath.Join(prefix, "installer/RSI Launcher-Setup-2.17.0.exe"), "MZ")
	return prefix
}

func TestLaunchFirstStart(t *testing.T) {
	for _, reaper := range []bool{false, true} {
		b := newBox(t)
		prefix := b.installed()
		setup := prefix + "/installer/RSI Launcher-Setup-2.17.0.exe"
		l := &extensions.Launch{Shortcut: "star-citizen/launcher", Argv: steamLine(setup, reaper), Env: slices.Clone(steamEnv)}
		before := slices.Clone(l.Argv)
		if err := (helper{}).LaunchHook(context.Background(), l); err != nil {
			t.Fatal(err)
		}
		bat := `Z:` + strings.ReplaceAll(prefix, "/", `\`) + `\installer\first-start.bat`
		want := append(before[:len(before)-1:len(before)-1], `C:\windows\system32\cmd.exe`, "/c", bat)
		if !slices.Equal(l.Argv, want) {
			t.Errorf("reaper %v: argv\n%q\nwant\n%q", reaper, l.Argv, want)
		}
		if got := envOf(l.Env, "STEAM_COMPAT_DATA_PATH"); !slices.Equal(got, []string{prefix}) {
			t.Errorf("STEAM_COMPAT_DATA_PATH %q", got)
		}
		if got := envOf(l.Env, "UMU_ID"); !slices.Equal(got, []string{"umu-starcitizen"}) {
			t.Errorf("UMU_ID %q", got)
		}
		if got := envOf(l.Env, "LD_PRELOAD"); len(got) != 1 {
			t.Error("the game lost Steam's environment")
		}

		text := readFile(t, filepath.Join(prefix, "installer/first-start.bat"))
		for _, line := range []string{
			`start "" /wait "%~dp0RSI Launcher-Setup-2.17.0.exe" /S`,
			`reg import "%~dp0vaporos.reg"`,
			`if not exist "C:\Program Files\Roberts Space Industries\StarCitizen\LIVE" mkdir "C:\Program Files\Roberts Space Industries\StarCitizen\LIVE"`,
			`copy /y "%~dp0USER.cfg" "C:\Program Files\Roberts Space Industries\StarCitizen\LIVE\USER.cfg"`,
			`start "" /wait "C:\Program Files\Roberts Space Industries\RSI Launcher\RSI Launcher.exe" --in-process-gpu --disable-gpu`,
		} {
			if !strings.Contains(text, line) {
				t.Errorf("first-start.bat lacks %s", line)
			}
		}
		if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
			t.Error("first-start.bat has lines without CRLF")
		}
		if strings.Index(text, "/S") > strings.Index(text, "RSI Launcher.exe") {
			t.Error("the launcher starts before the setup")
		}
		if reg := readFile(t, filepath.Join(prefix, "installer/vaporos.reg")); !strings.HasPrefix(reg, "REGEDIT4\r\n") ||
			!strings.Contains(reg, `[HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Policies\Explorer]`) ||
			!strings.Contains(reg, `"NoTrayItemsDisplay"=dword:00000001`) {
			t.Errorf("vaporos.reg:\n%s", reg)
		}
		if cfg := readFile(t, filepath.Join(prefix, "installer/USER.cfg")); cfg != "pl_pit.forceSoftwareCursor = 1\r\n" {
			t.Errorf("USER.cfg %q", cfg)
		}
	}
}

func TestLaunchLaterStart(t *testing.T) {
	b := newBox(t)
	prefix := b.installed()
	launcher := prefix + "/pfx/drive_c/Program Files/Roberts Space Industries/RSI Launcher/RSI Launcher.exe"
	writeFile(t, launcher, "MZ")
	setup := prefix + "/installer/RSI Launcher-Setup-2.17.0.exe"
	l := &extensions.Launch{Shortcut: "star-citizen/launcher", Argv: steamLine(setup, true), Env: slices.Clone(steamEnv)}
	before := slices.Clone(l.Argv)
	if err := (helper{}).LaunchHook(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	want := append(before[:len(before)-1:len(before)-1], launcher, "--in-process-gpu", "--disable-gpu")
	if !slices.Equal(l.Argv, want) {
		t.Errorf("argv\n%q\nwant\n%q", l.Argv, want)
	}
	if got := envOf(l.Env, "STEAM_COMPAT_DATA_PATH"); !slices.Equal(got, []string{prefix}) {
		t.Errorf("STEAM_COMPAT_DATA_PATH %q", got)
	}
	live := prefix + "/pfx/drive_c/Program Files/Roberts Space Industries/StarCitizen/LIVE"
	if cfg := readFile(t, live+"/USER.cfg"); cfg != "pl_pit.forceSoftwareCursor = 1\r\n" {
		t.Errorf("USER.cfg %q", cfg)
	}

	// A USER.cfg of the player's own, in any case, stays as it is.
	b2 := newBox(t)
	prefix = b2.installed()
	launcher = prefix + "/pfx/drive_c/Program Files/Roberts Space Industries/RSI Launcher/RSI Launcher.exe"
	writeFile(t, launcher, "MZ")
	live = prefix + "/pfx/drive_c/Program Files/Roberts Space Industries/StarCitizen/LIVE"
	writeFile(t, live+"/user.cfg", "r_DisplayInfo = 1\n")
	l = &extensions.Launch{Shortcut: "star-citizen/launcher", Argv: steamLine(prefix+"/installer/RSI Launcher-Setup-2.17.0.exe", false)}
	must(t, (helper{}).LaunchHook(context.Background(), l))
	if names := dirNames(t, live); !slices.Equal(names, []string{"user.cfg"}) {
		t.Errorf("LIVE holds %v", names)
	}
}

func TestLaunchSystemDrive(t *testing.T) {
	newBox(t)
	p, _ := placeFor("")
	writeFile(t, filepath.Join(p.Prefix(), markerName), sysUUID+"\n")
	l := &extensions.Launch{Shortcut: "star-citizen/launcher", Argv: steamLine(p.Prefix()+"/installer/RSI Launcher-Setup-2.17.0.exe", false)}
	if err := (helper{}).LaunchHook(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if l.Argv[len(l.Argv)-3] != `C:\windows\system32\cmd.exe` {
		t.Errorf("argv %q", l.Argv)
	}
}

func TestLaunchRefuses(t *testing.T) {
	b := newBox(t)
	prefix := b.installed()
	setup := prefix + "/installer/RSI Launcher-Setup-2.17.0.exe"

	// The drive is unplugged: nothing may start on the system drive.
	b.mounted = false
	b.writeMounts()
	l := &extensions.Launch{Shortcut: "star-citizen/launcher", Argv: steamLine(setup, true), Env: slices.Clone(steamEnv)}
	before := slices.Clone(l.Argv)
	err := (helper{}).LaunchHook(context.Background(), l)
	if !errors.Is(err, errDrive) {
		t.Errorf("unplugged: %v", err)
	}
	if !slices.Equal(l.Argv, before) || !slices.Equal(l.Env, steamEnv) {
		t.Error("a refused launch was changed")
	}
	if !strings.HasPrefix(err.Error(), "its drive isn't connected.") {
		t.Errorf("message %q", err)
	}
	b.mounted = true
	b.writeMounts()

	// An installer somewhere VaporOS never put one.
	l = &extensions.Launch{Shortcut: "star-citizen/launcher", Argv: steamLine("/var/home/vapor/Downloads/installer/RSI Launcher-Setup-2.17.0.exe", false)}
	if err := (helper{}).LaunchHook(context.Background(), l); err == nil || errors.Is(err, errDrive) {
		t.Errorf("a stray installer: %v", err)
	}
}

func TestLaunchPassesThrough(t *testing.T) {
	b := newBox(t)
	prefix := b.installed()
	setup := prefix + "/installer/RSI Launcher-Setup-2.17.0.exe"
	for name, l := range map[string]*extensions.Launch{
		"another shortcut":  {Shortcut: "star-citizen/other", Argv: steamLine(setup, false)},
		"an app":            {App: 227300, Argv: steamLine(setup, false)},
		"not the installer": {Shortcut: "star-citizen/launcher", Argv: steamLine(prefix+"/installer/other.exe", false)},
		"a relative path":   {Shortcut: "star-citizen/launcher", Argv: []string{"installer/RSI Launcher-Setup-2.17.0.exe"}},
	} {
		before := slices.Clone(l.Argv)
		if err := (helper{}).LaunchHook(context.Background(), l); err != nil || !slices.Equal(l.Argv, before) || l.Env != nil {
			t.Errorf("%s: %v, %q", name, err, l.Argv)
		}
	}
}

func TestSetupArg(t *testing.T) {
	i, prefix := setupArg([]string{"/x/proton", "waitforexitandrun", "/var/mnt/A/VaporOS/star-citizen/installer/RSI Launcher-Setup-2.17.0.exe", "-arg"})
	if i != 2 || prefix != "/var/mnt/A/VaporOS/star-citizen" {
		t.Errorf("got %d %q", i, prefix)
	}
	if i, _ := setupArg([]string{"/var/mnt/A/VaporOS/star-citizen/RSI Launcher-Setup-2.17.0.exe"}); i != -1 {
		t.Error("an installer outside installer/ was taken")
	}
}
