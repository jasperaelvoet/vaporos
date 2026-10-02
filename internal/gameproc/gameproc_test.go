package gameproc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Command lines as /proc shows them (NUL-separated), from a box running
// ETS2 through Proton, a Star Citizen shortcut and gamescope.
const (
	steamHome = "/var/home/vapor/.local/share/Steam"
	reaper    = steamHome + "/ubuntu12_32/reaper"
	wrapper   = steamHome + "/ubuntu12_32/steam-launch-wrapper"

	gameLine = reaper + "\x00SteamLaunch\x00AppId=227300\x00--\x00" + wrapper + "\x00--\x00" +
		steamHome + "/steamapps/common/SteamLinuxRuntime_sniper/_v2-entry-point\x00--verb=waitforexitandrun\x00--\x00" +
		"/usr/share/steam/compatibilitytools.d/proton-cachyos-slr/proton\x00waitforexitandrun\x00" +
		"/var/mnt/SATA1TB/SteamLibrary/steamapps/common/Euro Truck Simulator 2/bin/win_x64/eurotrucks2.exe\x00"
	shortcutLine = reaper + "\x00SteamLaunch\x00AppId=3228583970\x00--\x00" + wrapper + "\x00--\x00" +
		"/usr/bin/vos\x00ext\x00launch\x00--shortcut\x00star-citizen/launcher\x00" +
		"/usr/share/steam/compatibilitytools.d/proton-cachyos-slr/proton\x00waitforexitandrun\x00" +
		"/var/mnt/SATA1TB/VaporOS/star-citizen/RSI Launcher-Setup-2.4.0.exe\x00"
	installLine = reaper + "\x00SteamLaunch\x00AppId=227300\x00Install=1\x00--\x00" + wrapper + "\x00--\x00" +
		steamHome + "/legacycompat/iscriptevaluator.exe\x00--get-current-step\x00227300\x00"
	gamescopeReaperLine = "/usr/bin/gamescopereaper\x00--new-session-id\x00--\x00/usr/bin/steam\x00-gamepadui\x00-steamos3\x00"
	steamClientLine     = steamHome + "/ubuntu12_32/steam\x00-gamepadui\x00-steamos3\x00-srt-logger-opened\x00"
)

// fakeProc is a /proc with processes of the gaming user (uid 1000) and others.
type fakeProc struct {
	t   *testing.T
	dir string
}

func newProc(t *testing.T) *fakeProc {
	p := &fakeProc{t: t, dir: t.TempDir()}
	// Entries that are not processes.
	p.write("self", "status", "Name:\tvosd\nUid:\t0\t0\t0\t0\n")
	p.write("sys", "kernel", "")
	return p
}

func (p *fakeProc) write(pid, name, content string) {
	p.t.Helper()
	path := filepath.Join(p.dir, pid, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// proc adds a process of uid with its command line and environment.
func (p *fakeProc) proc(pid int, uid int, cmdline, environ string) {
	s := strconv.Itoa(pid)
	u := strconv.Itoa(uid)
	p.write(s, "status", "Name:\tx\nUmask:\t0022\nState:\tS (sleeping)\nTgid:\t"+s+"\nPid:\t"+s+
		"\nPPid:\t1\nUid:\t"+u+"\t"+u+"\t"+u+"\t"+u+"\nGid:\t"+u+"\t"+u+"\t"+u+"\t"+u+"\n")
	p.write(s, "cmdline", cmdline)
	p.write(s, "environ", environ)
}

func (p *fakeProc) remove(pid int) {
	os.RemoveAll(filepath.Join(p.dir, strconv.Itoa(pid)))
}

func (p *fakeProc) probe() Probe {
	return Probe{ProcDir: p.dir, UID: 1000, RuntimeDir: filepath.Join(p.dir, "run")}
}

func TestReaperApp(t *testing.T) {
	split := func(line string) []string { return strings.Split(strings.TrimSuffix(line, "\x00"), "\x00") }
	for _, c := range []struct {
		name string
		argv []string
		app  uint64
		ok   bool
	}{
		{"a Steam game", split(gameLine), 227300, true},
		{"a shortcut, above 2^31", split(shortcutLine), 3228583970, true},
		{"a game's install step", split(installLine), 227300, true},
		{"a 64-bit id", []string{"reaper", "SteamLaunch", "AppId=13866272215130325530", "--"}, 13866272215130325530, true},
		{"gamescope's reaper", split(gamescopeReaperLine), 0, false},
		{"gamescopereaper with Steam's words", []string{"/usr/bin/gamescopereaper", "SteamLaunch", "AppId=227300", "--"}, 0, false},
		{"the Steam client", split(steamClientLine), 0, false},
		{"AppId after --", []string{reaper, "SteamLaunch", "--", "AppId=227300"}, 0, false},
		{"AppId 0", []string{reaper, "SteamLaunch", "AppId=0", "--", "x"}, 0, false},
		{"AppId not a number", []string{reaper, "SteamLaunch", "AppId=abc", "--", "x"}, 0, false},
		{"AppId signed", []string{reaper, "SteamLaunch", "AppId=+5", "--", "x"}, 0, false},
		{"AppId too large", []string{reaper, "SteamLaunch", "AppId=18446744073709551616", "--"}, 0, false},
		{"no SteamLaunch", []string{reaper, "AppId=227300", "--", "x"}, 0, false},
		{"empty", nil, 0, false},
	} {
		app, ok := ReaperApp(c.argv)
		if app != c.app || ok != c.ok {
			t.Errorf("%s: got %d %v, want %d %v", c.name, app, ok, c.app, c.ok)
		}
	}
}

func TestGameRunning(t *testing.T) {
	p := newProc(t)
	pr := p.probe()
	p.proc(1, 0, "/usr/lib/systemd/systemd\x00", "")
	p.proc(900, 1000, "/usr/bin/gamescope\x00--backend\x00drm\x00", "HOME=/var/home/vapor\x00")
	p.proc(901, 1000, gamescopeReaperLine, "HOME=/var/home/vapor\x00")
	p.proc(902, 1000, steamClientLine, "HOME=/var/home/vapor\x00SteamAppId=0\x00")
	p.proc(903, 1000, steamHome+"/ubuntu12_64/steamwebhelper\x00", "SteamAppIdX=730\x00STEAM=1\x00SteamAppId=abc\x00")
	p.proc(904, 0, gameLine, "SteamAppId=730\x00") // not the gaming user
	if pr.GameRunning() {
		t.Fatal("a game was seen with only Steam and gamescope running")
	}

	for name, c := range map[string]struct{ cmdline, environ string }{
		"a reaper for a game":     {gameLine, "HOME=/var/home/vapor\x00"},
		"a reaper for a shortcut": {shortcutLine, "HOME=/var/home/vapor\x00"},
		"a reaper installing":     {installLine, "HOME=/var/home/vapor\x00"},
		"SteamAppId in a game":    {"Z:\\eurotrucks2.exe\x00", "DISPLAY=:0\x00SteamAppId=227300\x00"},
	} {
		p.proc(1200, 1000, c.cmdline, c.environ)
		if !pr.GameRunning() {
			t.Errorf("%s: not seen", name)
		}
		p.remove(1200)
	}
	if pr.GameRunning() {
		t.Fatal("seen after the game exited")
	}

	// The handoff unit is loaded while one game waits for another.
	transient := filepath.Join(pr.RuntimeDir, "systemd", "transient", HandoffUnit)
	os.MkdirAll(filepath.Dir(transient), 0o755)
	if err := os.WriteFile(transient, []byte("[Unit]\nDescription=/usr/bin/vos ext truckersmp handoff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !pr.GameRunning() {
		t.Error("the handoff unit does not count")
	}
	os.Remove(transient)

	if (Probe{ProcDir: filepath.Join(p.dir, "missing"), UID: 1000}).GameRunning() {
		t.Error("a missing /proc reported a game")
	}
}

// A game can make its environment huge; the scan is bounded and still
// finds SteamAppId past a very long variable.
func TestGameRunningHugeEnviron(t *testing.T) {
	p := newProc(t)
	pr := p.probe()
	long := "JUNK=" + strings.Repeat("SteamAppId=1", 10000)
	p.proc(200, 1000, "game\x00", long+"\x00SteamAppId=440\x00")
	if !pr.GameRunning() {
		t.Error("SteamAppId after a long variable not found")
	}
	p.proc(200, 1000, "game\x00", long+"\x00")
	if pr.GameRunning() {
		t.Error("a long variable's tail was taken for SteamAppId")
	}
	p.proc(200, 1000, "game\x00", strings.Repeat("A=1\x00", maxEnviron/4+10)+"SteamAppId=440\x00")
	if pr.GameRunning() {
		t.Error("read past the environ bound")
	}
}

// A process the user controls may put anything where /proc's files are
// in the fake; a symlink is never followed.
func TestProbeIgnoresSymlinks(t *testing.T) {
	p := newProc(t)
	pr := p.probe()
	p.proc(300, 1000, "game\x00", "HOME=/x\x00")
	elsewhere := filepath.Join(t.TempDir(), "environ")
	os.WriteFile(elsewhere, []byte("SteamAppId=440\x00"), 0o644)
	env := filepath.Join(p.dir, "300", "environ")
	os.Remove(env)
	if err := os.Symlink(elsewhere, env); err != nil {
		t.Fatal(err)
	}
	if pr.GameRunning() {
		t.Error("followed a symlinked environ")
	}
}

func TestSteamClientAndEnviron(t *testing.T) {
	p := newProc(t)
	pr := p.probe()
	pipe := "/var/home/vapor/.steam/steam.pipe"
	fd := func(pid int, n int, target string) {
		dir := filepath.Join(p.dir, strconv.Itoa(pid), "fd")
		os.MkdirAll(dir, 0o755)
		if err := os.Symlink(target, filepath.Join(dir, strconv.Itoa(n))); err != nil {
			t.Fatal(err)
		}
	}
	p.proc(900, 1000, gamescopeReaperLine, "")
	fd(900, 0, "/dev/null")
	p.proc(950, 0, steamClientLine, "")
	fd(950, 3, pipe) // root's: not the gaming user's Steam
	if pid := pr.SteamClient(pipe); pid != 0 {
		t.Fatalf("SteamClient = %d with no Steam of the user", pid)
	}
	p.proc(1002, 1000, steamClientLine, "DISPLAY=:0\x00WAYLAND_DISPLAY=\x00GAMESCOPE_WAYLAND_DISPLAY=gamescope-0\x00LD_PRELOAD=x.so\x00")
	fd(1002, 0, "/dev/null")
	fd(1002, 17, pipe)
	if pid := pr.SteamClient(pipe); pid != 1002 {
		t.Fatalf("SteamClient = %d", pid)
	}
	env := pr.Environ(1002, "DISPLAY", "WAYLAND_DISPLAY", "GAMESCOPE_WAYLAND_DISPLAY", "XAUTHORITY")
	if len(env) != 3 || env["DISPLAY"] != ":0" || env["GAMESCOPE_WAYLAND_DISPLAY"] != "gamescope-0" || env["WAYLAND_DISPLAY"] != "" {
		t.Fatalf("Environ = %v", env)
	}
	if _, ok := env["XAUTHORITY"]; ok {
		t.Fatal("an unset variable came back")
	}
}
