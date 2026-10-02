package truckersmp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

func TestSteamParts(t *testing.T) {
	newBox(t)
	h := newHelper()
	x := testExt()
	d, err := descriptor.Load(filepath.Join("..", "..", "..", "extensions", ID, "extension.json"))
	if err != nil {
		t.Fatal(err)
	}
	x.Desc = d
	p := h.Steam(x)
	want := extensions.SteamParts{
		Shortcuts: map[string]extensions.ShortcutTarget{
			"ets2": {Exe: "/usr/bin/vos", StartDir: homeDir(), Args: []string{"ext", "truckersmp", "mp", "ets2"}},
			"ats":  {Exe: "/usr/bin/vos", StartDir: homeDir(), Args: []string{"ext", "truckersmp", "mp", "ats"}},
		},
		SunshineApps: []extensions.SunshineApp{
			{Name: "TruckersMP (ETS2)", Detached: []string{"/usr/bin/vos ext truckersmp mp ets2"}},
			{Name: "TruckersMP (ATS)", Detached: []string{"/usr/bin/vos ext truckersmp mp ats"}},
		},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("got\n%+v\nwant\n%+v", p, want)
	}

	// The branch asked for; anything else in the file is ignored.
	write(t, branchPath(x.DataDir), `{"at":"2026-10-02T20:00:00Z","apps":{"227300":"temporary_1_61","440":"temporary_1_1","270880":"public; x"}}`)
	if p := h.Steam(x); !reflect.DeepEqual(p.Beta, map[uint32]string{227300: "temporary_1_61"}) {
		t.Errorf("beta %v", p.Beta)
	}
}

// branchHelper is a helper whose version API says TruckersMP supports
// ETS2 1.61 and ATS 1.61.
func branchHelper() *Helper {
	h := newHelper()
	h.fetchAPI = func(context.Context) (*versionInfo, error) {
		return &versionInfo{Name: "0.7.7.9", SupportedETS2: "1.61.1.1s", SupportedATS: "1.61.3.1s"}, nil
	}
	return h
}

func TestSwitchBranch(t *testing.T) {
	b := newBox(t)
	h := branchHelper()
	x := testExt()
	ctx := context.Background()
	run := func(name string) error { return h.Action(ctx, x, name, nil) }

	if err := run("switch-branch"); err == nil || !strings.Contains(err.Error(), "Neither ETS2 nor ATS is installed") {
		t.Fatalf("nothing installed: %v", err)
	}
	b.install(b.disk, games[0])
	if err := run("switch-branch"); err == nil || !strings.Contains(err.Error(), "Start ETS2 once") {
		t.Fatalf("version unknown: %v", err)
	}
	b.log(b.disk, games[0], "1.61.1.1s")
	if err := run("switch-branch"); err == nil || err.Error() != "ETS2 already runs a version TruckersMP supports." {
		t.Fatalf("supported already: %v", err)
	}

	// Steam updated ETS2 past TruckersMP; ATS is where TruckersMP is.
	b.log(b.disk, games[0], "1.62.0.5s")
	b.install(b.steam, games[1])
	b.log(b.steam, games[1], "1.61.3.1s")
	if err := run("switch-branch"); err != nil {
		t.Fatal(err)
	}
	if got := readBranch(x.DataDir); !reflect.DeepEqual(got, map[uint32]string{227300: "temporary_1_61"}) {
		t.Fatalf("branch %v", got)
	}
	// On the branch, ETS2 runs 1.61 again; a second switch for ATS keeps
	// ETS2 where it is.
	b.log(b.disk, games[0], "1.61.1.1s")
	b.log(b.steam, games[1], "1.62.0.1s")
	if err := run("switch-branch"); err != nil {
		t.Fatal(err)
	}
	if got := readBranch(x.DataDir); !reflect.DeepEqual(got, map[uint32]string{227300: "temporary_1_61", 270880: "temporary_1_61"}) {
		t.Fatalf("branch %v", got)
	}
	if fi, err := os.Stat(branchPath(x.DataDir)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("branch.json %v %v", fi, err)
	}

	if err := run("latest-branch"); err != nil {
		t.Fatal(err)
	}
	if got := readBranch(x.DataDir); got != nil {
		t.Errorf("after latest-branch: %v", got)
	}
	if err := run("latest-branch"); err != nil {
		t.Errorf("twice: %v", err)
	}

	// TruckersMP out of reach.
	h = newHelper()
	h.fetchAPI = func(context.Context) (*versionInfo, error) { return nil, errors.New("offline") }
	if err := h.Action(ctx, x, "switch-branch", nil); err == nil || !strings.Contains(err.Error(), "Couldn't reach TruckersMP") {
		t.Errorf("offline: %v", err)
	}
	if err := h.Action(ctx, x, "dance", nil); !errors.Is(err, extensions.ErrNoAction) {
		t.Errorf("unknown action: %v", err)
	}
}

// Install copies the injector as the gaming user and starts the first
// sync; Remove stops a sync that runs and forgets the branch.
func TestInstallAndRemove(t *testing.T) {
	newBox(t)
	h := newHelper()
	var ran [][]string
	h.asGamer = func(ctx context.Context, name string, args ...string) (string, error) {
		ran = append(ran, append([]string{name}, args...))
		return "", nil
	}
	started := make(chan struct{})
	stopped := make(chan error, 1)
	h.runSync = func(ctx context.Context, progress func(done, total int64)) error {
		close(started)
		<-ctx.Done()
		stopped <- ctx.Err()
		return ctx.Err()
	}
	x := testExt()
	if err := h.Install(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ran, [][]string{{"/usr/bin/vos", "ext", "truckersmp", "setup"}}) {
		t.Errorf("ran %q", ran)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no sync started")
	}
	write(t, branchPath(x.DataDir), `{"apps":{"227300":"temporary_1_61"}}`)
	if err := h.Remove(context.Background(), x, false); err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err == nil {
		t.Error("the sync was not stopped")
	}
	if _, err := os.Stat(branchPath(x.DataDir)); !os.IsNotExist(err) {
		t.Error("branch.json stays")
	}

	// A setup that fails is the card's reason.
	h.asGamer = func(ctx context.Context, name string, args ...string) (string, error) {
		return "runuser: something\nopen /usr/lib/vos/ext/truckersmp/truckersmp-cli.exe: no such file", errors.New("exit status 1")
	}
	if err := h.Install(context.Background(), x); err == nil || !strings.Contains(err.Error(), "launcher could not be copied") {
		t.Errorf("err %v", err)
	}
}

func TestCopyProfilesAction(t *testing.T) {
	newBox(t)
	h := newHelper()
	var ran []string
	h.asGamer = func(ctx context.Context, name string, args ...string) (string, error) {
		ran = append([]string{name}, args...)
		return "copied ETS2: 4A6F\nATS hasn't started with Proton yet. Start it once in Steam, then copy the profiles again.", errors.New("exit status 1")
	}
	err := h.Action(context.Background(), testExt(), "copy-profiles", nil)
	if err == nil || err.Error() != "ATS hasn't started with Proton yet. Start it once in Steam, then copy the profiles again." {
		t.Errorf("err %v", err)
	}
	if !slices.Equal(ran, []string{"/usr/bin/vos", "ext", "truckersmp", "copy-profiles"}) {
		t.Errorf("ran %q", ran)
	}
}

func TestCLI(t *testing.T) {
	newBox(t)
	var out, errb bytes.Buffer
	for _, args := range [][]string{nil, {"mp"}, {"mp", "ets3"}, {"handoff", "ets2", "x", "y"}, {"sync", "now"}, {"dance"}} {
		out.Reset()
		errb.Reset()
		if code := runCLI(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage") {
			t.Errorf("%q: %d %q", args, code, errb.String())
		}
	}
	if code := runCLI([]string{"setup"}, &out, &errb); code != 0 {
		t.Fatalf("setup: %d %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(homeDir(), binRel)); err != nil {
		t.Error("setup copied no injector")
	}
}

// The real runner reads the sync's progress lines; a fake runuser stands
// in for the command.
func TestRunSyncAsGamerReadsProgress(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "runuser"), "#!/bin/sh\necho '{\"bytes\":1,\"total\":4}'\necho '{\"bytes\":4,\"total\":4}'\necho 'it broke' >&2\nexit 1\n")
	os.Chmod(filepath.Join(dir, "runuser"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var got []int64
	err := runSyncAsGamer(context.Background(), func(done, total int64) { got = append(got, done, total) })
	if err == nil || !strings.HasSuffix(err.Error(), "it broke") {
		t.Errorf("err %v", err)
	}
	if !slices.Equal(got, []int64{1, 4, 4, 4}) {
		t.Errorf("progress %v", got)
	}
	write(t, filepath.Join(dir, "runuser"), "#!/bin/sh\necho 'another sync of the TruckersMP files is running' >&2\nexit 3\n")
	if err := runSyncAsGamer(context.Background(), func(int64, int64) {}); err != nil {
		t.Errorf("a sync already running: %v", err)
	}
}
