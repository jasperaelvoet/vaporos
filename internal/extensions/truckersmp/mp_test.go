package truckersmp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gameproc"
)

// fakeProc is a /proc with processes of the gaming user (1000) and others.
type fakeProc struct {
	t   *testing.T
	dir string
	mu  sync.Mutex
}

func newFakeProc(t *testing.T) *fakeProc {
	p := &fakeProc{t: t, dir: t.TempDir()}
	write(t, filepath.Join(p.dir, "self", "status"), "Name:\tvos\n")
	return p
}

// add puts a process with its parent and command line (words split by |).
func (p *fakeProc) add(pid, ppid, uid int, cmdline string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := filepath.Join(p.dir, strconv.Itoa(pid))
	u := strconv.Itoa(uid)
	write(p.t, filepath.Join(d, "status"), "Name:\tx\nState:\tS (sleeping)\nTgid:\t"+strconv.Itoa(pid)+
		"\nPid:\t"+strconv.Itoa(pid)+"\nPPid:\t"+strconv.Itoa(ppid)+"\nUid:\t"+u+"\t"+u+"\t"+u+"\t"+u+"\n")
	write(p.t, filepath.Join(d, "cmdline"), strings.ReplaceAll(cmdline, "|", "\x00")+"\x00")
}

func (p *fakeProc) remove(pid int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	os.RemoveAll(filepath.Join(p.dir, strconv.Itoa(pid)))
}

func (p *fakeProc) fs() procFS { return procFS{dir: p.dir, uid: 1000} }

const shortcutApp = 3228583970 // a shortcut's app id, top bit set

// A box where Steam runs the ETS2 shortcut: steam → reaper → wrapper →
// vos ext truckersmp mp ets2 (pid 40).
func shortcutProcs(t *testing.T) *fakeProc {
	p := newFakeProc(t)
	p.add(1, 0, 0, "/usr/lib/systemd/systemd")
	p.add(10, 1, 1000, steamHome+"/ubuntu12_32/steam|-gamepadui")
	p.add(20, 10, 1000, reaper+"|SteamLaunch|AppId="+strconv.Itoa(shortcutApp)+"|--|"+wrapper+"|--|/usr/bin/vos|ext|truckersmp|mp|ets2")
	p.add(30, 20, 1000, wrapper+"|--|/usr/bin/vos|ext|truckersmp|mp|ets2")
	p.add(40, 30, 1000, "/usr/bin/vos|ext|truckersmp|mp|ets2")
	p.add(50, 1, 0, reaper+"|SteamLaunch|AppId=227300|--|x") // root's: not the user's
	return p
}

func TestProcs(t *testing.T) {
	p := shortcutProcs(t)
	if got := p.fs().reapers(); !slices.Equal(got, []uint64{shortcutApp}) {
		t.Errorf("reapers %v", got)
	}
	if got := p.fs().reaperAbove(40); got != shortcutApp {
		t.Errorf("above the mp command: %d", got)
	}
	if got := p.fs().reaperAbove(10); got != 0 {
		t.Errorf("above Steam: %d", got)
	}
	if !sameApp(shortcutApp, uint64(shortcutApp)<<32|0x02000000) || sameApp(shortcutApp, 227300) || sameApp(0, 0) {
		t.Error("sameApp")
	}
}

// testMP is an mp on a box whose apps' launch options carry the
// dispatcher, recording the handoff it starts and what it tells the
// person.
func testMP(t *testing.T, p *fakeProc) (*mp, *[][]string, *[]string) {
	dispatcher(t, true)
	var started [][]string
	var told []string
	m := &mp{procs: p.fs(), pid: 40, flag: flagPath(), runtime: os.Getenv("XDG_RUNTIME_DIR"), home: homeDir(), now: now, libs: libraries,
		handOff: func(ctx context.Context, args []string) error { started = append(started, args); return nil },
		tell:    func(code string) { text, _ := messageText(code); told = append(told, text) }}
	return m, &started, &told
}

func TestMP(t *testing.T) {
	b := syncedBox(t, "ets2")
	b.install(b.disk, games[0])
	p := shortcutProcs(t)
	m, started, told := testMP(t, p)
	if err := m.run(context.Background(), games[0]); err != nil {
		t.Fatal(err)
	}
	f, err := readFlag(flagPath())
	if err != nil || f.Game != "ets2" || !f.valid(now()) {
		t.Fatalf("flag %+v %v", f, err)
	}
	want := []string{"/usr/bin/vos", "ext", "truckersmp", "handoff", "ets2", strconv.Itoa(shortcutApp), f.Nonce}
	if len(*started) != 1 || !slices.Equal((*started)[0], want) || len(*told) != 0 {
		t.Fatalf("started %q, told %q", *started, *told)
	}

	// From Moonlight: no shortcut above it.
	os.Remove(flagPath())
	m.pid = 10
	*started = nil
	if err := m.run(context.Background(), games[0]); err != nil || (*started)[0][5] != "0" {
		t.Fatalf("%v %q", err, *started)
	}
}

func TestMPRefuses(t *testing.T) {
	b := syncedBox(t, "ets2")
	b.install(b.disk, games[0])
	p := shortcutProcs(t)
	m, started, told := testMP(t, p)
	ctx := context.Background()

	// ATS isn't installed; then it is, but its files are not there.
	err := m.run(ctx, games[1])
	if code, text := refused(err); code != "not-installed-ats" || (*told)[0] != text ||
		text != "TruckersMP didn't start because ATS isn't installed. Install it in Steam, then try again." {
		t.Fatalf("ATS not installed: %v %q", err, *told)
	}
	b.install(b.steam, games[1])
	if err := m.run(ctx, games[1]); err == nil ||
		(*told)[1] != "TruckersMP didn't start because its files for ATS aren't downloaded yet. Try again once its card in VaporOS says it's ready." {
		t.Fatalf("ATS without files: %v %q", err, *told)
	}
	// A game runs already.
	p.add(60, 10, 1000, reaper+"|SteamLaunch|AppId=270880|--|"+wrapper+"|--|x")
	if err := m.run(ctx, games[0]); err == nil || !strings.Contains((*told)[2], "ATS is already running") {
		t.Fatalf("with ATS running: %v %q", err, *told)
	}
	p.remove(60)
	// A handoff is under way.
	write(t, filepath.Join(m.runtime, "systemd", "transient", gameproc.HandoffUnit), "")
	if err := m.run(ctx, games[0]); err == nil || !strings.Contains((*told)[3], "already starting") {
		t.Fatalf("during a handoff: %v %q", err, *told)
	}
	os.Remove(filepath.Join(m.runtime, "systemd", "transient", gameproc.HandoffUnit))
	// systemd-run fails: no flag is left behind.
	m.handOff = func(context.Context, []string) error { return errors.New("no user manager") }
	if err := m.run(ctx, games[0]); err == nil || !strings.Contains((*told)[4], "couldn't pass the start on") {
		t.Fatalf("systemd-run failing: %v %q", err, *told)
	}
	if _, err := os.Stat(flagPath()); !os.IsNotExist(err) {
		t.Error("a flag stays after a failed handoff")
	}
	if len(*started) != 0 {
		t.Errorf("started %q", *started)
	}
}

// dispatcher writes steam.json with its dispatcher on or off.
func dispatcher(t *testing.T, on bool) {
	write(t, config.ExtSteamPath(), `{"set":"1","dispatcher":`+strconv.FormatBool(on)+`,"apps":[{"app":227300,"hooks":["truckersmp"]}]}`)
}

// While the games' launch options lack the dispatcher (a slot the box can
// boot was built before extensions), the hook never sees the flag, so mp
// refuses rather than let the game start in single-player; also when
// steam.json cannot be read. The person reads its sentence from vosd.
func TestMPNeedsTheDispatcher(t *testing.T) {
	b := syncedBox(t, "ets2")
	b.install(b.disk, games[0])
	m, started, _ := testMP(t, shortcutProcs(t))
	m.tell = tell // the record vosd reads
	ctx := context.Background()
	records := filepath.Join(config.GamerRuntimeDir, "vos", "ext-messages")
	for _, c := range []struct{ name, steamJSON string }{
		{"off", ""}, {"missing", "-"}, {"unreadable", "{"},
	} {
		switch c.steamJSON {
		case "":
			dispatcher(t, false)
		case "-":
			os.Remove(config.ExtSteamPath())
		default:
			write(t, config.ExtSteamPath(), c.steamJSON)
		}
		err := m.run(ctx, games[0])
		if code, text := refused(err); code != "needs-update-ets2" ||
			text != "TruckersMP didn't start because it needs the next VaporOS update. Until then, ETS2 starts from Steam in single-player." {
			t.Fatalf("%s: %v %q", c.name, err, text)
		}
		names, _ := os.ReadDir(records)
		if len(names) != 1 {
			t.Fatalf("%s: records %v", c.name, names)
		}
		var r struct{ Code, ID string }
		if err := json.Unmarshal([]byte(read(t, filepath.Join(records, names[0].Name()))), &r); err != nil ||
			r.Code != "needs-update-ets2" || r.ID != ID {
			t.Fatalf("%s: record %+v %v", c.name, r, err)
		}
		os.RemoveAll(records)
		if _, err := os.Stat(flagPath()); !os.IsNotExist(err) {
			t.Fatalf("%s: a flag without the dispatcher", c.name)
		}
		if len(*started) != 0 {
			t.Fatalf("%s: handed off %q", c.name, *started)
		}
	}

	dispatcher(t, true)
	if err := m.run(ctx, games[0]); err != nil || len(*started) != 1 {
		t.Fatalf("with the dispatcher: %v %q", err, *started)
	}
	if _, err := readFlag(flagPath()); err != nil {
		t.Fatal(err)
	}
}

// Files that fail the quick check are either not downloaded for the game
// yet or updating.
func TestNotReady(t *testing.T) {
	syncedBox(t, "ets2")
	ets2, ats := games[0], games[1]
	if got := notReady(homeDir(), ats); got != "no-files-ats" {
		t.Errorf("ATS never synced: %q", got)
	}
	// A file changed under a finished sync.
	write(t, modFilePath("data/ets2mp.adb"), "changed")
	if got := notReady(homeDir(), ets2); got != msgUpdating {
		t.Errorf("a changed file: %q", got)
	}
	// A sync that moves files into place deleted the manifest.
	os.Remove(filepath.Join(homeDir(), manifestRel))
	if got := notReady(homeDir(), ets2); got != msgUpdating {
		t.Errorf("during a sync: %q", got)
	}
	if got := notReady(homeDir(), ats); got != "no-files-ats" {
		t.Errorf("ATS during a sync: %q", got)
	}
	newBox(t)
	if got := notReady(homeDir(), ets2); got != "no-files-ets2" {
		t.Errorf("nothing synced: %q", got)
	}
	for _, code := range []string{"not-installed-ets2", "no-files-ats", "running-ets2"} {
		if text, ok := messageText(code); !ok || !strings.HasPrefix(text, "TruckersMP didn't start because ") {
			t.Errorf("%s: %q", code, text)
		}
	}
	if _, ok := messageText("no-files-x"); ok {
		t.Error("a code for no game has words")
	}
}

// The handoff waits until the shortcut's reaper has gone, a moment more,
// then asks Steam for the game.
func TestHandoff(t *testing.T) {
	newBox(t)
	p := shortcutProcs(t)
	var slept []time.Duration
	var launched []string
	h := &handoff{procs: p.fs(), flag: flagPath(), poll: time.Millisecond, settle: 2 * time.Second, maxWait: 5 * time.Second,
		sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			if len(slept) == 3 {
				p.remove(20) // the shortcut's reaper exits
			}
			return nil
		},
		launch: func(ctx context.Context, url string) bool { launched = append(launched, url); return true },
		tell:   func(string) { t.Error("told") },
	}
	f, _ := writeFlag(flagPath(), games[0], now())
	if err := h.run(context.Background(), games[0], shortcutApp, f.Nonce); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slept, []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, 2 * time.Second}) {
		t.Errorf("slept %v", slept)
	}
	if !slices.Equal(launched, []string{"steam://rungameid/227300"}) {
		t.Errorf("launched %q", launched)
	}
	if _, err := os.Stat(flagPath()); err != nil {
		t.Error("the flag is gone before the game took it")
	}

	// Its game id names the shortcut too.
	p.add(20, 10, 1000, reaper+"|SteamLaunch|AppId="+strconv.FormatUint(uint64(shortcutApp)<<32|0x02000000, 10)+"|--|x")
	slept, launched = nil, nil
	h.run(context.Background(), games[1], shortcutApp, f.Nonce)
	if len(slept) < 4 || launched[0] != "steam://rungameid/270880" {
		t.Errorf("slept %v, launched %q", slept, launched)
	}
}

// A shortcut that never ends: the handoff gives up waiting and starts
// the game anyway; Steam that never answers: the flag goes.
func TestHandoffLimits(t *testing.T) {
	newBox(t)
	p := shortcutProcs(t)
	var told []string
	h := &handoff{procs: p.fs(), flag: flagPath(), poll: 5 * time.Millisecond, settle: time.Millisecond, maxWait: 30 * time.Millisecond,
		sleep:  sleepCtx,
		launch: func(context.Context, string) bool { return false },
		tell:   func(code string) { text, _ := messageText(code); told = append(told, text) },
	}
	f, _ := writeFlag(flagPath(), games[0], now())
	start := time.Now()
	err := h.run(context.Background(), games[0], shortcutApp, f.Nonce)
	if err == nil || len(told) != 1 || !strings.Contains(told[0], "Steam didn't respond") {
		t.Fatalf("%v %q", err, told)
	}
	if d := time.Since(start); d < 30*time.Millisecond || d > 5*time.Second {
		t.Errorf("waited %s", d)
	}
	if _, err := os.Stat(flagPath()); !os.IsNotExist(err) {
		t.Error("the flag stays though Steam never got the start")
	}
}
