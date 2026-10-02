package steamprep

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/steamlock"
	"github.com/jasperaelvoet/vaporos/internal/storage/steam"
)

func TestFirstStartSetsTheDefaultOnly(t *testing.T) {
	b := newBox(t)
	// Steam's first start wrote config.vdf; nobody signed in yet.
	b.check(os.Remove(filepath.Join(b.root, "config", "loginusers.vdf")))
	b.check(os.RemoveAll(filepath.Join(b.root, "userdata")))
	b.desire(b.starCitizen(truckers(proton())))
	before := b.steamFile("config/config.vdf")
	b.run(false)

	got, ok := b.mapping(0)
	if !ok || got != (steam.CompatTool{Name: tool, Priority: "75"}) {
		t.Fatalf("default %+v %v\n%s", got, ok, b.logs.String())
	}
	// Forced apps are mapped too; nothing else changed.
	want, _, err := steam.SetCompatToolMapping(before, 0, steam.CompatTool{Name: tool, Priority: "75"})
	b.check(err)
	for _, app := range []uint32{ets2, ats} {
		want, _, err = steam.SetCompatToolMapping(want, app, steam.CompatTool{Name: tool, Priority: "250"})
		b.check(err)
	}
	if got := b.steamFile("config/config.vdf"); !bytes.Equal(got, want) {
		t.Errorf("config.vdf:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(b.root, "userdata")); !os.IsNotExist(err) {
		t.Errorf("userdata made: %v", err)
	}
	st := b.state()
	if st.Default.Wrote != tool || st.Default.Before != nil || st.Default.Suspended || len(st.Accounts) != 0 ||
		st.Vos != config.BinaryVersion || st.Error != "" || st.Fingerprint == "" {
		t.Errorf("state %+v", st)
	}

	// Before Steam ever ran there is no config.vdf, and VaporOS makes none.
	c := newBox(t)
	c.check(os.RemoveAll(c.root))
	c.desire(proton())
	c.run(false)
	if _, err := os.Stat(filepath.Join(c.root, "config")); !os.IsNotExist(err) {
		t.Errorf("config made: %v", err)
	}
}

func TestFingerprintFastPath(t *testing.T) {
	b := newBox(t)
	b.desire(b.starCitizen(truckers(proton())))
	b.run(false)
	fp := b.state().Fingerprint
	if fp == "" {
		t.Fatalf("no fingerprint: %s", b.logs.String())
	}
	cfg := filepath.Join(b.root, "config", "config.vdf")
	old := time.Unix(1700000000, 0)
	b.check(os.Chtimes(cfg, old, old))
	b.run(false) // the touch is a change
	b.logs.Reset()
	b.run(false)
	if !strings.Contains(b.logs.String(), "nothing changed") {
		t.Fatalf("no fast path: %s", b.logs.String())
	}
	if fi, _ := os.Stat(cfg); !fi.ModTime().Equal(old) {
		t.Error("the fast path wrote config.vdf")
	}

	// Each input counts: steam.json, a tool, a Steam file.
	for name, change := range map[string]func(){
		"steam.json": func() { b.desire(truckers(proton())) },
		"tool":       func() { b.removeTool(tool) },
		"localconfig": func() {
			b.edit("userdata/127388593/config/localconfig.vdf", func(d []byte) []byte { return append(d, '\n') })
		},
		"unwrap": func() {},
	} {
		change()
		b.logs.Reset()
		b.runWith(Options{Unwrap: name == "unwrap", Budget: 20 * time.Second})
		if strings.Contains(b.logs.String(), "nothing changed") {
			t.Errorf("%s: fast path taken", name)
		}
		b.installTool(tool)
		b.run(false)
	}
}

func TestBudget(t *testing.T) {
	b := newBox(t)
	b.desire(b.starCitizen(truckers(proton())))
	defer func() { stepHook = nil }()

	// A slow step that minds its context: the run stops, says where,
	// and records no fingerprint.
	stepHook = func(ctx context.Context, step string) {
		if step == "launch options" {
			<-ctx.Done()
		}
	}
	began := time.Now()
	b.runWith(Options{Budget: 500 * time.Millisecond})
	if d := time.Since(began); d > 2*time.Second {
		t.Errorf("took %s", d)
	}
	st := b.state()
	if !strings.Contains(st.Error, "out of time") || st.Fingerprint != "" {
		t.Errorf("state error %q fingerprint %q", st.Error, st.Fingerprint)
	}
	if got, _ := b.mapping(0); got.Name != tool {
		t.Errorf("the step before it was undone: %+v", got)
	}
	if o, _ := b.launchOptions(acctA, ets2); strings.Contains(o, steam.Dispatcher) {
		t.Errorf("the slow step ran: %q", o)
	}

	// One that hangs: Run returns at the budget anyway (and vos exits).
	release := make(chan struct{})
	stepHook = func(ctx context.Context, step string) {
		if step == "shortcuts" {
			<-release
		}
	}
	var done <-chan struct{}
	startRun = func(ctx context.Context, o Options) <-chan struct{} {
		done = start(ctx, o)
		return done
	}
	defer func() { startRun = start }()
	began = time.Now()
	b.runWith(Options{Budget: 300 * time.Millisecond})
	if d := time.Since(began); d > 2*time.Second || d < 250*time.Millisecond {
		t.Errorf("Run took %s", d)
	}
	close(release)
	<-done
	if !strings.Contains(b.logs.String(), "out of time after 300ms") {
		t.Errorf("log: %s", b.logs.String())
	}
}

func TestRefusesRoot(t *testing.T) {
	b := newBox(t)
	b.desire(truckers(proton()))
	geteuid = func() int { return 0 }
	before := b.steamFile("config/config.vdf")
	b.run(false)
	if !bytes.Equal(b.steamFile("config/config.vdf"), before) {
		t.Error("config.vdf changed")
	}
	if _, err := os.Stat(StatePath(b.home)); !os.IsNotExist(err) {
		t.Errorf("state written as root: %v", err)
	}
	if !strings.Contains(b.logs.String(), "never as root") {
		t.Errorf("log: %s", b.logs.String())
	}
	var stderr bytes.Buffer
	if code := cli([]string{"prepare"}, &stderr); code != 0 {
		t.Errorf("exit %d", code)
	}
}

func TestCLIArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"prep"}, {"prepare", "--force"}, {"prepare", "--unwrap", "x"}} {
		var stderr bytes.Buffer
		if code := cli(args, &stderr); code != 2 || !strings.Contains(stderr.String(), usage) {
			t.Errorf("%q: exit %d, %q", args, code, stderr.String())
		}
	}
}

func TestParseFailureLeavesFilesUntouched(t *testing.T) {
	b := newBox(t)
	b.desire(truckers(proton()))
	broken := []byte("\"InstallConfigStore\"\n{\n\t\"Software\"\n\t{\n")
	b.write(filepath.Join(b.root, "config", "config.vdf"), broken)
	lcB := "userdata/127388593/config/localconfig.vdf"
	b.edit(lcB, func(d []byte) []byte { return d[:len(d)/2] })
	brokenLC := b.steamFile(lcB)
	b.run(false)

	if !bytes.Equal(b.steamFile("config/config.vdf"), broken) || !bytes.Equal(b.steamFile(lcB), brokenLC) {
		t.Error("a file that does not parse was changed")
	}
	if o, _ := b.launchOptions(acctA, ets2); !strings.HasPrefix(o, steam.Dispatcher) {
		t.Errorf("the other account was not done: %q", o)
	}
	st := b.state()
	if !strings.Contains(st.Error, "config.vdf") || !strings.Contains(st.Error, lcB) || st.Fingerprint != "" {
		t.Errorf("state error %q fingerprint %q", st.Error, st.Fingerprint)
	}

	// A steam.json that does not parse changes nothing.
	c := newBox(t)
	c.write(config.ExtSteamPath(), []byte("{"))
	before := c.steamFile("config/config.vdf")
	c.run(false)
	if !bytes.Equal(c.steamFile("config/config.vdf"), before) || !strings.Contains(c.state().Error, "steam.json") {
		t.Errorf("bad steam.json: %q", c.state().Error)
	}
}

// Each run that changes nothing records why (but those that cannot tell
// whether another run writes the record), and its last line says why.
func TestLeavesSteamAlone(t *testing.T) {
	for name, c := range map[string]struct {
		setup   func(b *box)
		skipped string // "": no record at all
		says    string // in the last line, after "prepare: skipped: "
	}{
		"no steam.json":  {func(b *box) { b.check(os.Remove(config.ExtSteamPath())) }, skipNoDesired, "no steam.json yet"},
		"another set":    {func(b *box) { b.bootReport("5") }, skipOtherSet, `steam.json is for set "4", not this boot's`},
		"no boot report": {func(b *box) { b.check(os.Remove(config.ExtBootPath())) }, skipOtherSet, "not this boot's"},
		"bad steam.json": {func(b *box) { b.write(config.ExtSteamPath(), []byte("{")) }, skipBadDesired, "steam.json does not parse"},
		"Steam running": {func(b *box) {
			proc := filepath.Join(b.dir, "proc", "4242")
			b.write(filepath.Join(proc, "comm"), []byte("steamwebhelper\n"))
		}, skipSteamRunning, "Steam is running"},
		"Steam elsewhere": {func(b *box) {
			link := filepath.Join(b.home, ".steam", "root")
			b.check(os.Remove(link))
			other := filepath.Join(b.dir, "flatpak-steam")
			b.mkdir(other)
			b.check(os.Symlink(other, link))
		}, "", "not ~/.local/share/Steam"},
		// Another run may be writing the record.
		"lock held": {func(b *box) {
			unlock, err := steamlock.Lock(context.Background())
			b.check(err)
			b.t.Cleanup(unlock)
		}, "", "the Steam lock stayed busy"},
		// Before the gaming user's session, /run/user/1000 is not there.
		"no runtime dir": {func(b *box) { b.check(os.RemoveAll(config.GamerRuntimeDir)) }, "", "no runtime directory "},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBox(t)
			b.desire(truckers(proton()))
			c.setup(b)
			before := b.steamFile("config/config.vdf")
			b.runWith(Options{Budget: 400 * time.Millisecond})
			if !bytes.Equal(b.steamFile("config/config.vdf"), before) {
				t.Errorf("config.vdf changed\n%s", b.logs.String())
			}
			if last := b.lastLine(); !strings.HasPrefix(last, "prepare: skipped: ") || !strings.Contains(last, c.says) {
				t.Errorf("last line %q, want it to say %q", last, c.says)
			}
			if c.skipped == "" {
				if _, err := os.Stat(StatePath(b.home)); !os.IsNotExist(err) {
					t.Errorf("state written: %v", err)
				}
				return
			}
			// The accounts are recorded all the same, so vosd does not ask
			// for a Steam restart for them again.
			st := b.state()
			if st.Skipped != c.skipped || len(st.Accounts) != 2 || st.Fingerprint != "" || len(st.Apps) != 0 {
				t.Errorf("record %+v", st)
			}
		})
	}
}

// A run's last line says what it changed, with the dispatcher off too,
// and when there was nothing to change or something failed.
func TestLastLineSaysWhatChanged(t *testing.T) {
	b := newBox(t)
	b.desire(truckers(proton()))
	b.run(false)
	lc := relName(b.root, steam.LocalConfigPath(b.root, acctA))
	if last := b.lastLine(); !strings.HasPrefix(last, "prepare: done; changed config/config.vdf, ") || !strings.Contains(last, lc) {
		t.Errorf("first run: %q", last)
	}
	b.run(false)
	if last := b.lastLine(); last != "prepare: done; nothing changed since the last run" {
		t.Errorf("again: %q", last)
	}

	off := truckers(proton())
	off.Dispatcher = false
	b.desire(off)
	b.run(false)
	if last := b.lastLine(); !strings.HasPrefix(last, "prepare: done (dispatcher off); changed ") || !strings.Contains(last, lc) ||
		strings.Contains(last, "config/config.vdf") {
		t.Errorf("dispatcher off: %q", last)
	}
	cfg := filepath.Join(b.root, "config", "config.vdf")
	old := time.Unix(1700000000, 0)
	b.check(os.Chtimes(cfg, old, old))
	b.run(false)
	if last := b.lastLine(); last != "prepare: done (dispatcher off); nothing needed changing" {
		t.Errorf("nothing to unwrap: %q", last)
	}
	b.write(cfg, []byte("\"InstallConfigStore\"\n{\n"))
	b.run(false)
	if last := b.lastLine(); last != "prepare: done (dispatcher off) with 1 error (above); nothing needed changing" {
		t.Errorf("with a broken config.vdf: %q\n%s", last, b.logs.String())
	}
}

func TestSkipKeepsTheRecord(t *testing.T) {
	b := newBox(t)
	b.desire(truckers(proton()))
	b.run(false)
	st := b.state()
	proc := filepath.Join(b.dir, "proc", "4242")
	b.write(filepath.Join(proc, "comm"), []byte("steam\n"))
	b.run(false)
	skipped := b.state()
	if skipped.Skipped != skipSteamRunning || skipped.Fingerprint != st.Fingerprint || len(skipped.Apps) != len(st.Apps) {
		t.Errorf("record %+v", skipped)
	}
	// The next run that finds nothing to do says so.
	b.check(os.RemoveAll(proc))
	b.logs.Reset()
	b.run(false)
	if !strings.Contains(b.logs.String(), "nothing changed") || b.state().Skipped != "" {
		t.Errorf("skipped %q: %s", b.state().Skipped, b.logs.String())
	}
}

func TestStateRecordsAccounts(t *testing.T) {
	b := newBox(t)
	b.desire(proton())
	b.run(false)
	if got := b.state().Accounts; len(got) != 2 || got[0] != "52079950" || got[1] != "127388593" {
		t.Errorf("accounts %q", got)
	}

	// A loginusers.vdf that cannot be read keeps the accounts recorded,
	// so vosd does not take both for new ones and restart Steam: the run
	// says what failed, and no shortcut's mapping goes meanwhile.
	b.desire(b.starCitizen(proton()))
	b.run(false)
	b.edit("config/loginusers.vdf", func(d []byte) []byte { return d[:len(d)-3] })
	b.desire(proton())
	b.run(false)
	st := b.state()
	if len(st.Accounts) != 2 || st.Accounts[0] != "52079950" || !strings.Contains(st.Error, "loginusers.vdf") {
		t.Errorf("accounts %q, error %q", st.Accounts, st.Error)
	}
	if got, _ := b.mapping(scApp); got != oursApp {
		t.Errorf("shortcut mapping %+v", got)
	}
}
