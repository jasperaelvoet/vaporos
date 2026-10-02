package starcitizen

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

// Inside the prefix (Proton's pfx/ is the Wine prefix, drive_c its C:).
const (
	rsiDir      = "pfx/drive_c/Program Files/Roberts Space Industries"
	launcherRel = rsiDir + "/RSI Launcher/RSI Launcher.exe"
	liveRel     = rsiDir + "/StarCitizen/LIVE"
	firstStart  = "first-start.bat"
	regFile     = "vaporos.reg"
	userCfg     = "USER.cfg"
	cmdExe      = `C:\windows\system32\cmd.exe`
)

// launcherFlags keep the launcher's Electron window from staying blank
// under gamescope: it draws in software, in its own process. It is a page
// of buttons, so nothing is lost.
var launcherFlags = []string{"--in-process-gpu", "--disable-gpu"}

// userCfgText makes the mouse cursor visible in Star Citizen's interaction
// mode under gamescope.
const userCfgText = "pl_pit.forceSoftwareCursor = 1\r\n"

// regText hides the Windows tray. The launcher closes to the tray, which
// nobody can reach under gamescope; without a tray, closing it quits.
const regText = "REGEDIT4\r\n\r\n" +
	`[HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Policies\Explorer]` + "\r\n" +
	`"NoTrayItemsDisplay"=dword:00000001` + "\r\n"

// LaunchHook turns the Star Citizen shortcut, whose target is the RSI
// Launcher's installer Install recorded (gone, when a newer download
// replaced it before Steam picked up the new target), into a start of the
// launcher: the installed launcher, or on the first start a batch file
// that installs it silently and then starts it, all in one Proton run.
// A target in the other place, where Star Citizen was before it was set
// up again, starts it in the recorded prefix instead. It refuses with a
// code its helper words (MessageText), so the person reads that sentence.
// It runs as vapor, in front of Steam's command line:
//
//	…/_v2-entry-point --verb=waitforexitandrun -- …/proton waitforexitandrun <prefix>/installer/RSI Launcher-Setup-<v>.exe
func (helper) LaunchHook(ctx context.Context, l *extensions.Launch) error {
	if l.Shortcut != ID+"/"+shortcutKey {
		return nil
	}
	i, given := setupArg(l.Argv)
	if i < 0 {
		return nil // not the installer: Steam starts what it was asked to
	}
	st, err := recorded(given, true)
	if err != nil {
		return err
	}
	prefix := st.Prefix
	dir := filepath.Join(prefix, installerDir)
	named := filepath.Base(l.Argv[i])
	if prefix != given {
		named = st.Installer // what the shortcut names once Steam picks up the recorded prefix
	}
	var with []string
	if launcher := filepath.Join(prefix, launcherRel); isFile(launcher) {
		if err := seedLive(prefix); err != nil {
			log.Printf("star-citizen: %v", err)
		}
		with = append([]string{launcher}, launcherFlags...)
	} else {
		setup := pickInstaller(dir, named)
		switch {
		case setup == "" && st.Installer == "":
			return refuse(codeSettingUp, "no installer in %s yet", dir) // Install is still downloading one, or that failed
		case setup == "":
			return refuse(codeInstallerMissing, "no installer in %s", dir)
		}
		if err := writeFirstStart(prefix, setup); err != nil {
			return extensions.Refuse(codeCantWrite, err)
		}
		with = []string{cmdExe, "/c", dosPath(filepath.Join(dir, firstStart))}
	}
	pruneInstallers(dir, named)
	l.Env = setEnv(l.Env, "STEAM_COMPAT_DATA_PATH", prefix)
	l.Env = setEnv(l.Env, "UMU_ID", umuID) // protonfixes add what the launcher needs
	l.Argv = slices.Concat(l.Argv[:i], with, l.Argv[i+1:])
	return nil
}

// installers are the RSI Launcher's installers in dir, with the time each
// was written.
func installers(dir string) map[string]time.Time {
	ents, _ := os.ReadDir(dir)
	out := map[string]time.Time{}
	for _, e := range ents {
		if !installerRe.MatchString(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out[e.Name()] = fi.ModTime()
		}
	}
	return out
}

// pickInstaller is the installer a first start runs: the one Steam's
// shortcut names, or, when a newer download replaced it, the newest in
// dir; "" when there is none.
func pickInstaller(dir, named string) string {
	all := installers(dir)
	if _, ok := all[named]; ok {
		return named
	}
	newest := ""
	for n, t := range all {
		if newest == "" || t.After(all[newest]) || (t.Equal(all[newest]) && n > newest) {
			newest = n
		}
	}
	return newest
}

// pruneInstallers deletes the other installers in dir once Steam's
// shortcut names the newest one. Until then the shortcut may still name an
// older one, which stays.
func pruneInstallers(dir, named string) {
	all := installers(dir)
	t, ok := all[named]
	if !ok {
		return
	}
	for n, u := range all {
		if n != named && !u.Before(t) {
			return
		}
	}
	for n := range all {
		if n != named {
			if err := os.Remove(filepath.Join(dir, n)); err != nil {
				log.Printf("star-citizen: %v", err)
			}
		}
	}
}

// setupArg finds the installer in Steam's command line: the last argument
// <prefix>/installer/RSI Launcher-Setup-<v>.exe.
func setupArg(argv []string) (int, string) {
	for i := len(argv) - 1; i >= 0; i-- {
		dir, file := filepath.Split(argv[i])
		if !filepath.IsAbs(argv[i]) || !installerRe.MatchString(file) {
			continue
		}
		dir = filepath.Clean(dir)
		if filepath.Base(dir) == installerDir {
			return i, filepath.Dir(dir)
		}
	}
	return -1, ""
}

// setEnv sets key in env, replacing every value it had.
func setEnv(env []string, key, val string) []string {
	env = slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, key+"=") })
	return append(env, key+"="+val)
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// dosPath is p as Wine sees it: Proton maps Z: to /.
func dosPath(p string) string { return "Z:" + strings.ReplaceAll(p, "/", `\`) }

// firstStartBat installs the launcher silently, hides the tray, makes the
// game's folder with its USER.cfg, and starts the launcher. Each step
// waits for the one before; %~dp0 is the installer folder.
func firstStartBat(setup string) string {
	rsi := `C:\Program Files\Roberts Space Industries`
	lines := []string{
		"@echo off",
		"rem VaporOS writes this file for Star Citizen's first start.",
		fmt.Sprintf(`start "" /wait "%%~dp0%s" /S`, setup),
		`reg import "%~dp0` + regFile + `"`,
		`if not exist "` + rsi + `\StarCitizen\LIVE" mkdir "` + rsi + `\StarCitizen\LIVE"`,
		`if not exist "` + rsi + `\StarCitizen\LIVE\` + userCfg + `" copy /y "%~dp0` + userCfg + `" "` + rsi + `\StarCitizen\LIVE\` + userCfg + `" >nul`,
		`if not exist "` + rsi + `\RSI Launcher\RSI Launcher.exe" exit /b 1`,
		`start "" /wait "` + rsi + `\RSI Launcher\RSI Launcher.exe" ` + strings.Join(launcherFlags, " "),
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

// writeFirstStart writes the batch file and what it imports and copies
// into the installer folder.
func writeFirstStart(prefix, setup string) error {
	dir := filepath.Join(prefix, installerDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, text := range map[string]string{
		firstStart: firstStartBat(setup),
		regFile:    regText,
		userCfg:    userCfgText,
	} {
		if err := writeIfChanged(filepath.Join(dir, name), text); err != nil {
			return err
		}
	}
	return nil
}

// seedLive makes the game's folder and its USER.cfg once the launcher is
// there, unless a USER.cfg (in any case: Wine does not tell them apart)
// already is.
func seedLive(prefix string) error {
	live := filepath.Join(prefix, liveRel)
	if err := os.MkdirAll(live, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(live)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if strings.EqualFold(e.Name(), userCfg) {
			return nil
		}
	}
	return writeIfChanged(filepath.Join(live, userCfg), userCfgText)
}

// writeIfChanged replaces p with text (temp file, rename) unless it holds
// text already.
func writeIfChanged(p, text string) error {
	if b, err := os.ReadFile(p); err == nil && string(b) == text {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}
