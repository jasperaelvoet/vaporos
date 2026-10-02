package steamprep

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

// The Steam client files the steam package's tests use: two accounts,
// Heroic and a Lutris Star Citizen as shortcuts, ETS2 installed.
const fixtures = "../storage/steam/testdata/client"

const (
	acctA   = 52079950  // jasperae
	acctB   = 127388593 // truckfan
	tool    = "proton-cachyos-slr"
	ets2    = 227300
	ats     = 270880
	cpName  = "capsule.png"
	theSet  = "4"
	scOwner = "star-citizen"
	scKey   = "launcher"
)

// box is a fake VaporOS with the gaming user's home and Steam in it.
type box struct {
	t    testing.TB
	dir  string
	home string
	root string // Steam's directory
	logs bytes.Buffer
}

func newBox(t testing.TB) *box {
	t.Helper()
	dir := t.TempDir()
	b := &box{t: t, dir: dir, home: filepath.Join(dir, "home", "vapor")}
	b.root = filepath.Join(b.home, ".local", "share", "Steam")

	saved := []*string{&config.StateDir, &config.RunDir, &config.CompatToolsDir, &config.ExtMountedLibDir, &config.GamerRuntimeDir}
	old := make([]string, len(saved))
	for i, p := range saved {
		old[i] = *p
	}
	oldEuid := geteuid
	t.Cleanup(func() {
		for i, p := range saved {
			*p = old[i]
		}
		geteuid = oldEuid
	})
	geteuid = func() int { return 1000 } // also when the tests run as root
	config.StateDir = filepath.Join(dir, "var", "lib", "vos")
	config.RunDir = filepath.Join(dir, "run", "vos")
	config.CompatToolsDir = filepath.Join(dir, "usr", "share", "steam", "compatibilitytools.d")
	config.ExtMountedLibDir = filepath.Join(dir, "usr", "lib", "vos", "ext")
	config.GamerRuntimeDir = filepath.Join(dir, "run", "user", "1000")
	for _, d := range []string{"run/user/1000", "proc", "home/vapor/.steam"} {
		b.mkdir(filepath.Join(dir, d))
	}
	if err := os.Symlink(b.root, filepath.Join(b.home, ".steam", "root")); err != nil {
		t.Fatal(err)
	}

	b.copyFixture("config.vdf", "config/config.vdf")
	b.copyFixture("loginusers.vdf", "config/loginusers.vdf")
	b.copyFixture("localconfig.vdf", "userdata/52079950/config/localconfig.vdf")
	b.copyFixture("localconfig.vdf", "userdata/127388593/config/localconfig.vdf")
	b.copyFixture("shortcuts.vdf", "userdata/52079950/config/shortcuts.vdf")
	b.copyFixture("appmanifest_227300.acf", "steamapps/appmanifest_227300.acf")
	// Steam's default here is VaporOS's to set: the fixture's user choice
	// goes, and the tests that want one put it back.
	b.edit("config/config.vdf", func(d []byte) []byte {
		out, _, err := steam.DeleteCompatToolMapping(d, 0)
		b.check(err)
		return out
	})
	b.installTool(tool)
	b.bootReport(theSet)
	return b
}

func (b *box) check(err error) {
	b.t.Helper()
	if err != nil {
		b.t.Fatal(err)
	}
}

func (b *box) mkdir(dir string) { b.t.Helper(); b.check(os.MkdirAll(dir, 0o755)) }

func (b *box) write(path string, data []byte) {
	b.t.Helper()
	b.mkdir(filepath.Dir(path))
	b.check(os.WriteFile(path, data, 0o644))
}

func (b *box) copyFixture(name, rel string) {
	b.t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, name))
	b.check(err)
	b.write(filepath.Join(b.root, rel), data)
}

// steamFile reads a file under Steam's directory ("" when missing).
func (b *box) steamFile(rel string) []byte {
	b.t.Helper()
	data, err := os.ReadFile(filepath.Join(b.root, rel))
	if os.IsNotExist(err) {
		return nil
	}
	b.check(err)
	return data
}

func (b *box) edit(rel string, f func([]byte) []byte) {
	b.t.Helper()
	b.write(filepath.Join(b.root, rel), f(b.steamFile(rel)))
}

func (b *box) installTool(name string) {
	b.write(filepath.Join(config.CompatToolsDir, name, "compatibilitytool.vdf"),
		[]byte(`"compatibilitytools" { "compat_tools" { "`+name+`" { "install_path" "." "display_name" "Proton CachyOS" "from_oslist" "windows" "to_oslist" "linux" } } }`+"\n"))
}

func (b *box) removeTool(name string) {
	b.check(os.RemoveAll(filepath.Join(config.CompatToolsDir, name)))
}

// bootWithoutExtensions writes the report of a boot that mounted none
// (reason cmdline for vos.ext=0, skip-once).
func (b *box) bootWithoutExtensions(reason string) {
	b.write(config.ExtBootPath(), []byte(`{"mode":"off","set":"","tries_left":0,"reason":"`+reason+`","mounted":[],"skipped":[]}`+"\n"))
}

func (b *box) bootReport(set string) {
	b.write(config.ExtBootPath(), []byte(`{"mode":"enabled","set":"`+set+`","tries_left":0,"reason":"","mounted":[{"id":"proton","sha256":"`+
		strings.Repeat("a", 64)+`","fsverity":"`+strings.Repeat("b", 64)+`"}],"skipped":[]}`+"\n"))
}

// desire writes steam.json as vosd would. Without Owners, the
// extensions it names are the owners, as when nothing else is wanted.
func (b *box) desire(d Desired) {
	b.t.Helper()
	if d.Set == "" {
		d.Set = theSet
	}
	if d.Owners == nil {
		d.Owners = []string{}
		for _, a := range d.Apps {
			d.Owners = append(d.Owners, a.Hooks...)
		}
		for _, s := range d.Shortcuts {
			d.Owners = append(d.Owners, s.Owner)
		}
	}
	b.desireExact(d)
}

// desireExact writes d as steam.json, set and owners as they are.
func (b *box) desireExact(d Desired) {
	b.t.Helper()
	data, err := json.Marshal(d)
	b.check(err)
	b.write(config.ExtSteamPath(), data)
}

// proton is steam.json with the core extension alone.
func proton() Desired {
	return Desired{Dispatcher: true, DefaultCompatTool: tool}
}

// truckers adds TruckersMP: ETS2 and ATS forced onto the tool and hooked.
func truckers(d Desired) Desired {
	d.Apps = append(d.Apps,
		AppWant{App: ets2, CompatTool: tool, Hooks: []string{"truckersmp"}},
		AppWant{App: ats, CompatTool: tool, Hooks: []string{"truckersmp"}})
	return d
}

// starCitizen adds the Star Citizen shortcut, with art.
func (b *box) starCitizen(d Desired) Desired {
	art := filepath.Join(config.ExtMountedLibDir, scOwner, "art")
	b.write(filepath.Join(art, cpName), []byte("capsule"))
	b.write(filepath.Join(art, "hero.png"), []byte("hero"))
	d.Shortcuts = append(d.Shortcuts, Shortcut{
		Owner: scOwner, Key: scKey, Name: "Star Citizen",
		Exe:        "/var/mnt/games/VaporOS/star-citizen/RSI Launcher-Setup-2.3.1.exe",
		StartDir:   "/var/mnt/games/VaporOS/star-citizen",
		CompatTool: tool, Art: art,
	})
	return d
}

// run is one `vos steam prepare` with a generous budget.
func (b *box) run(unwrap bool) {
	b.t.Helper()
	b.runWith(Options{Unwrap: unwrap, Budget: 20 * time.Second})
}

func (b *box) runWith(o Options) {
	b.t.Helper()
	o.Home = b.home
	o.ProcDir = filepath.Join(b.dir, "proc")
	o.Log = log.New(&b.logs, "", 0)
	Run(context.Background(), o)
}

// lastLine is the last line the runs logged.
func (b *box) lastLine() string {
	lines := strings.Split(strings.TrimRight(b.logs.String(), "\n"), "\n")
	return lines[len(lines)-1]
}

func (b *box) state() *State {
	b.t.Helper()
	st, err := loadState(StatePath(b.home))
	b.check(err)
	return st
}

func (b *box) mapping(app uint32) (steam.CompatTool, bool) {
	b.t.Helper()
	m, ok, err := steam.CompatToolMapping(b.steamFile("config/config.vdf"), app)
	b.check(err)
	return m, ok
}

func (b *box) launchOptions(acct, app uint32) (string, bool) {
	b.t.Helper()
	o, ok, err := steam.LaunchOptions(b.steamFile(relName(b.root, steam.LocalConfigPath(b.root, acct))), app)
	b.check(err)
	return o, ok
}

func (b *box) shortcuts(acct uint32) []steam.Shortcut {
	b.t.Helper()
	list, err := steam.ParseShortcuts(b.steamFile(relName(b.root, steam.ShortcutsPath(b.root, acct))))
	b.check(err)
	return list
}

// vaporShortcut returns the account's Star Citizen shortcut of VaporOS.
func (b *box) vaporShortcut(acct uint32) (steam.Shortcut, bool) {
	for _, s := range b.shortcuts(acct) {
		if o, k, ok := steam.ShortcutRef(s.LaunchOptions); ok && o == scOwner && k == scKey {
			return s, true
		}
	}
	return steam.Shortcut{}, false
}
