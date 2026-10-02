package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// testHelper is an extension's helper for tests: it records its launch
// hooks and returns fixed Steam parts.
type testHelper struct {
	NopHelper
	id    string
	calls *[]string
	hook  func(ctx context.Context, l *Launch) error
	steam SteamParts
}

func (h testHelper) LaunchHook(ctx context.Context, l *Launch) error {
	if h.calls != nil {
		*h.calls = append(*h.calls, h.id)
	}
	if h.hook != nil {
		return h.hook(ctx, l)
	}
	return nil
}

func (h testHelper) Steam(*Ext) SteamParts { return h.steam }

// withHelper registers h as id's helper for the test.
func withHelper(t *testing.T, id string, h Helper) {
	t.Helper()
	old, had := helpers[id]
	helpers[id] = h
	t.Cleanup(func() {
		if had {
			helpers[id] = old
		} else {
			delete(helpers, id)
		}
	})
}

// launchMessages returns the records `vos ext launch` left for vosd,
// oldest first.
func launchMessages(t *testing.T) []launchRecord {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), messagesRel)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []launchRecord
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		must(t, err)
		r, ok := parseLaunchRecord(b)
		if !ok {
			t.Errorf("%s: not a record: %s", e.Name(), b)
		}
		out = append(out, r)
	}
	return out
}

func writeSteamJSON(t *testing.T, d SteamDesired) {
	t.Helper()
	b, err := json.Marshal(d)
	must(t, err)
	writeFile(t, config.ExtSteamPath(), string(b))
}

// The reaper line Steam puts in front of a game, and the command after it.
var (
	ets2Argv = []string{"/var/home/vapor/.local/share/Steam/ubuntu12_32/reaper", "SteamLaunch", "AppId=227300", "--",
		"/var/home/vapor/.local/share/Steam/ubuntu12_32/steam-launch-wrapper", "--",
		"/usr/share/steam/compatibilitytools.d/proton-cachyos-slr/proton", "waitforexitandrun",
		"/var/mnt/SATA1TB/SteamLibrary/steamapps/common/Euro Truck Simulator 2/bin/win_x64/eurotrucks2.exe"}
	scAppID = ShortcutAppID("star-citizen", "launcher")
)

func TestIdentify(t *testing.T) {
	d := &SteamDesired{Shortcuts: []SteamShortcut{{Owner: "star-citizen", Key: "launcher", Name: "Star Citizen"}}}
	signed := int64(-1066383326) // an id Steam gave the shortcut, kept as a signed int32
	moved := uint32(signed)
	st := &prepareState{Shortcuts: map[string]map[string]preparedShortcut{
		"12345678": {"truckersmp/ets2-mp": {AppID: signed, GameID: itoa(ShortcutGameID(moved))}},
	}}
	for _, c := range []struct {
		name string
		l    launch
		env  []string
		want launchID
	}{
		{"--app", launch{app: 227300, argv: []string{"x"}}, []string{"SteamAppId=1"}, launchID{app: 227300}},
		{"--shortcut", launch{shortcut: "star-citizen", key: "launcher", argv: []string{"x"}}, nil, launchID{owner: "star-citizen", key: "launcher"}},
		{"Steam's reaper line", launch{argv: ets2Argv}, []string{"SteamAppId=270880"}, launchID{app: 227300}},
		{"SteamAppId", launch{argv: []string{"/games/ets2"}}, []string{"HOME=/var/home/vapor", "SteamAppId=270880"}, launchID{app: 270880}},
		{"SteamAppId 0, then SteamGameId", launch{argv: []string{"x"}}, []string{"SteamAppId=0", "SteamGameId=227300"}, launchID{app: 227300}},
		{"a shortcut's game id", launch{argv: []string{"x"}}, []string{"SteamGameId=" + itoa(ShortcutGameID(scAppID))}, launchID{owner: "star-citizen", key: "launcher"}},
		{"a shortcut's app id in the reaper line", launch{argv: []string{"reaper", "SteamLaunch", "AppId=" + itoa(uint64(scAppID)), "--", "x"}}, nil, launchID{owner: "star-citizen", key: "launcher"}},
		{"an id Steam changed, from prepare's record", launch{argv: []string{"x"}}, []string{"SteamGameId=" + itoa(ShortcutGameID(moved))}, launchID{owner: "truckersmp", key: "ets2-mp"}},
		{"someone else's shortcut", launch{argv: []string{"x"}}, []string{"SteamGameId=" + itoa(ShortcutGameID(0x80000001))}, launchID{}},
		{"a 64-bit id that is no shortcut's", launch{argv: []string{"x"}}, []string{"SteamGameId=13866272215130325531"}, launchID{}},
		{"nothing", launch{argv: []string{"x"}}, []string{"SteamAppId=abc"}, launchID{}},
	} {
		if got := identify(c.l, c.env, d, st); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := identify(launch{argv: []string{"x"}}, []string{"SteamGameId=" + itoa(ShortcutGameID(scAppID))}, nil, nil); got != (launchID{}) {
		t.Errorf("without steam.json or a record: %+v", got)
	}
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }

// dispatchBox has truckersmp (hooking ETS2) and star-citizen (a shortcut)
// mounted after proton, with steam.json listing them.
func dispatchBox(t *testing.T) (*env, *[]execCall, *[]string) {
	t.Helper()
	e := newEnv(t)
	calls := fakeExec(t)
	proton := newImage(t, "proton", "", 100, true)
	tmp := newImage(t, "truckersmp", "", 100, false, "proton")
	sc := newImage(t, "star-citizen", "", 100, false, "proton")
	e.catalog(proton, tmp, sc)
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "3", Mounted: mountedAs(proton, tmp, sc)})
	writeSteamJSON(t, SteamDesired{Set: "3", Dispatcher: true,
		Apps: []SteamApp{
			{App: 227300, CompatTool: "proton-cachyos-slr", Hooks: []string{"truckersmp", "coolercontrol"}},
			{App: 270880, CompatTool: "proton-cachyos-slr", Hooks: []string{"truckersmp"}},
		},
		Shortcuts: []SteamShortcut{{Owner: "star-citizen", Key: "launcher", Name: "Star Citizen"}}})
	var hooks []string
	return e, calls, &hooks
}

// What Steam sets for the game alone, which helper programs must not get.
var steamGameEnv = map[string]string{
	"LD_PRELOAD":                     "/var/home/vapor/.local/share/Steam/ubuntu12_64/gameoverlayrenderer.so",
	"LD_LIBRARY_PATH":                "/var/home/vapor/.local/share/Steam/ubuntu12_32/steam-runtime/usr/lib",
	"STEAM_RUNTIME":                  "1",
	"STEAM_RUNTIME_LIBRARY_PATH":     "/var/home/vapor/.local/share/Steam/ubuntu12_32/steam-runtime/lib",
	"PRESSURE_VESSEL_FILESYSTEMS_RO": "/usr/share/steam/compatibilitytools.d",
}

// A helper's own command drops the same variables from what it hands on.
func TestWithoutSteamEnv(t *testing.T) {
	env := []string{"HOME=/var/home/vapor", "STEAM_COMPAT_DATA_PATH=/x"}
	for k, v := range steamGameEnv {
		env = append(env, k+"="+v)
	}
	if got := WithoutSteamEnv(env); !slices.Equal(got, []string{"HOME=/var/home/vapor", "STEAM_COMPAT_DATA_PATH=/x"}) {
		t.Errorf("%q", got)
	}
	if len(env) != 2+len(steamGameEnv) {
		t.Error("env changed in place")
	}
}

func TestDispatchRunsTheHooks(t *testing.T) {
	_, calls, hooks := dispatchBox(t)
	for k, v := range steamGameEnv {
		t.Setenv(k, v)
	}
	t.Setenv("STEAM_COMPAT_DATA_PATH", "/var/mnt/SATA1TB/SteamLibrary/steamapps/compatdata/227300")
	stripSteamEnv = stripSteamEnvForReal
	var helperSaw string
	withHelper(t, "truckersmp", testHelper{id: "truckersmp", calls: hooks, hook: func(ctx context.Context, l *Launch) error {
		if l.App != 227300 || l.Shortcut != "" || !slices.Equal(l.Argv, ets2Argv) {
			return errors.New("wrong launch")
		}
		// A helper program the hook starts runs without Steam's overlay,
		// libraries and runtime; what else Steam set it keeps.
		out, err := exec.CommandContext(ctx, "sh", "-c", `printf %s "$LD_PRELOAD$LD_LIBRARY_PATH$STEAM_RUNTIME$STEAM_RUNTIME_LIBRARY_PATH$PRESSURE_VESSEL_FILESYSTEMS_RO|$STEAM_COMPAT_DATA_PATH"`).Output()
		helperSaw = string(out)
		l.Argv = append(l.Argv[:len(l.Argv)-1], "/var/home/vapor/.local/share/vaporos/ext/truckersmp/truckersmp-cli.exe")
		l.Env = append(l.Env, "TRUCKERSMP=1")
		return err
	}})
	withHelper(t, "coolercontrol", testHelper{id: "coolercontrol", calls: hooks})
	t.Setenv("SteamAppId", "227300")

	rc, msg := runLaunch(append([]string{"--app", "227300"}, ets2Argv...)...)
	if rc != 1 || len(*calls) != 1 { // 1: the faked exec "fails"
		t.Fatalf("exit %d: %s", rc, msg)
	}
	c := (*calls)[0]
	if !slices.Equal(*hooks, []string{"truckersmp"}) {
		t.Errorf("hooks run: %q (coolercontrol is not mounted)", *hooks)
	}
	if c.path != ets2Argv[0] || c.argv[len(c.argv)-1] != "/var/home/vapor/.local/share/vaporos/ext/truckersmp/truckersmp-cli.exe" {
		t.Errorf("exec %+v", c)
	}
	if !slices.Contains(c.env, "TRUCKERSMP=1") {
		t.Errorf("the hook's variable is missing: %q", c.env)
	}
	for k, v := range steamGameEnv {
		if !slices.Contains(c.env, k+"="+v) {
			t.Errorf("the game lost Steam's %s: %q", k, c.env)
		}
	}
	if helperSaw != "|/var/mnt/SATA1TB/SteamLibrary/steamapps/compatdata/227300" {
		t.Errorf("a helper program got %q", helperSaw)
	}

	// Steam's reaper line names the app when the token is gone.
	*hooks, *calls = nil, nil
	runLaunch(ets2Argv...)
	if !slices.Equal(*hooks, []string{"truckersmp"}) || len(*calls) != 1 {
		t.Errorf("hooks %q, calls %d", *hooks, len(*calls))
	}
}

func TestDispatchHookOrder(t *testing.T) {
	_, calls, hooks := dispatchBox(t)
	writeSteamJSON(t, SteamDesired{Set: "3", Apps: []SteamApp{{App: 227300, Hooks: []string{"star-citizen", "truckersmp", "proton"}}}})
	for _, id := range []string{"proton", "truckersmp", "star-citizen"} {
		withHelper(t, id, testHelper{id: id, calls: hooks})
	}
	runLaunch("--app", "227300", "/games/ets2")
	if !slices.Equal(*hooks, []string{"proton", "truckersmp", "star-citizen"}) || len(*calls) != 1 {
		t.Errorf("hooks %q (want catalog order), calls %d", *hooks, len(*calls))
	}
}

func TestDispatchShortcut(t *testing.T) {
	e, calls, hooks := dispatchBox(t)
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "star-citizen.json"), starCitizenDesc)
	var got *Launch
	withHelper(t, "star-citizen", testHelper{id: "star-citizen", calls: hooks, hook: func(_ context.Context, l *Launch) error {
		got = l
		l.Env = append(l.Env, "STEAM_COMPAT_DATA_PATH=/var/mnt/SATA1TB/VaporOS/star-citizen")
		return nil
	}})
	runLaunch("--shortcut", "star-citizen/launcher", "/usr/share/steam/compatibilitytools.d/proton-cachyos-slr/proton", "waitforexitandrun", "/x/RSI Launcher-Setup.exe")
	if got == nil || got.Shortcut != "star-citizen/launcher" || got.App != 0 || len(*calls) != 1 {
		t.Fatalf("launch %+v, calls %+v", got, *calls)
	}
	if !slices.Contains((*calls)[0].env, "STEAM_COMPAT_DATA_PATH=/var/mnt/SATA1TB/VaporOS/star-citizen") {
		t.Errorf("env %q", (*calls)[0].env)
	}

	// Found by its game id too, and refused while its extension is not
	// mounted, never started without its hook.
	*hooks = nil
	t.Setenv("SteamGameId", itoa(ShortcutGameID(scAppID)))
	runLaunch("/usr/bin/true")
	if !slices.Equal(*hooks, []string{"star-citizen"}) || len(*calls) != 2 {
		t.Errorf("hooks %q, calls %d", *hooks, len(*calls))
	}
	e.report(store.BootReport{Mode: store.ModeOff, Reason: store.ReasonSkipOnce})
	if rc, msg := runLaunch("/usr/bin/true"); rc != 1 || len(*calls) != 2 || !strings.Contains(msg, "star-citizen: not-mounted: shortcut star-citizen/launcher") {
		t.Errorf("exit %d, calls %d: %s", rc, len(*calls), msg)
	}
	if recs := launchMessages(t); len(recs) != 1 || recs[0].Code != codeNotMounted || recs[0].ID != "star-citizen" {
		t.Errorf("records %+v", recs)
	}
}

func TestDispatchRefuses(t *testing.T) {
	e, calls, hooks := dispatchBox(t)
	writeFile(t, filepath.Join(config.ExtDescriptorsDir, "truckersmp.json"), `{"schema":1,"id":"truckersmp","name":"TruckersMP",
		"summary":"Multiplayer for ETS2 and ATS.","category":"app","upstream":{"name":"TruckersMP","url":"https://truckersmp.com","license":"MIT"}}`)
	withHelper(t, "truckersmp", testHelper{id: "truckersmp", calls: hooks, hook: func(context.Context, *Launch) error {
		return errors.New("the TruckersMP files are out of date. Update them on the TruckersMP card")
	}})
	rc, msg := runLaunch("--app", "227300", "/games/ets2")
	if rc != 1 || len(*calls) != 0 || !strings.Contains(msg, "truckersmp: hook-failed: the TruckersMP files are out of date") {
		t.Fatalf("exit %d, calls %d: %s", rc, len(*calls), msg)
	}
	want := launchRecord{Code: codeHookFailed, ID: "truckersmp", Detail: "the TruckersMP files are out of date. Update them on the TruckersMP card"}
	if recs := launchMessages(t); len(recs) != 1 || recs[0] != want {
		t.Fatalf("records %+v", recs)
	}

	// A hook that leaves nothing to run refuses too.
	withHelper(t, "truckersmp", testHelper{id: "truckersmp", hook: func(_ context.Context, l *Launch) error {
		l.Argv = nil
		return nil
	}})
	if rc, _ := runLaunch("--app", "227300", "/games/ets2"); rc != 1 || len(*calls) != 0 {
		t.Fatalf("exit %d, calls %d", rc, len(*calls))
	}
	if recs := launchMessages(t); len(recs) != 2 || recs[1].Code != codeHookFailed || recs[1].Detail != "its hook left nothing to run" {
		t.Fatalf("records %+v", recs)
	}

	// An app no mounted extension hooks just runs, whatever steam.json says.
	sc := newImage(t, "star-citizen", "", 100, false, "proton")
	e.report(store.BootReport{Mode: store.ModeEnabled, Set: "4", Mounted: mountedAs(sc)})
	if rc, _ := runLaunch("--app", "227300", "/games/ets2"); rc != 1 || len(*calls) != 1 {
		t.Fatalf("exit %d, calls %d", rc, len(*calls))
	}
	// So does any app without steam.json.
	os.Remove(config.ExtSteamPath())
	if rc, _ := runLaunch("--app", "227300", "/games/ets2"); rc != 1 || len(*calls) != 2 {
		t.Fatalf("exit %d, calls %d", rc, len(*calls))
	}
}

// Steam stopping a launch while a hook waits (SIGTERM ends the context) is
// no refusal: nobody waits for that game any more, so vosd is told nothing.
func TestDispatchStoppedBySteam(t *testing.T) {
	_, calls, hooks := dispatchBox(t)
	ctx, cancel := context.WithCancel(context.Background())
	withHelper(t, "truckersmp", testHelper{id: "truckersmp", calls: hooks, hook: func(ctx context.Context, _ *Launch) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}})
	l, err := parseLaunch([]string{"--app", "227300", "/games/ets2"})
	must(t, err)
	var stderr strings.Builder
	if rc := dispatch(ctx, l, nil, &stderr); rc != 1 || len(*calls) != 0 || !strings.Contains(stderr.String(), "Steam stopped the launch") {
		t.Fatalf("exit %d, calls %d: %s", rc, len(*calls), stderr.String())
	}
	if recs := launchMessages(t); len(recs) != 0 {
		t.Fatalf("records %+v", recs)
	}

	// A hook that returns nil after Steam stopped the launch: no later
	// hook runs, and neither does the game.
	ctx, cancel = context.WithCancel(context.Background())
	writeSteamJSON(t, SteamDesired{Set: "3", Apps: []SteamApp{{App: 227300, Hooks: []string{"proton", "truckersmp"}}}})
	withHelper(t, "proton", testHelper{id: "proton", calls: hooks, hook: func(context.Context, *Launch) error {
		cancel()
		return nil
	}})
	*hooks = nil
	stderr.Reset()
	if rc := dispatch(ctx, l, nil, &stderr); rc != 1 || len(*calls) != 0 || !slices.Equal(*hooks, []string{"proton"}) ||
		!strings.Contains(stderr.String(), "proton: Steam stopped the launch") {
		t.Fatalf("exit %d, calls %d, hooks %q: %s", rc, len(*calls), *hooks, stderr.String())
	}
	if recs := launchMessages(t); len(recs) != 0 {
		t.Fatalf("records %+v", recs)
	}

	// Stopped before any hook ran: no exec either.
	stderr.Reset()
	if rc := dispatch(ctx, launch{argv: []string{"/games/other"}}, nil, &stderr); rc != 1 || len(*calls) != 0 {
		t.Fatalf("exit %d, calls %d: %s", rc, len(*calls), stderr.String())
	}
}

// A hook's error of any length still reaches vosd: the record's detail is
// cut to what vosd logs, so the file stays within what it reads.
func TestDispatchLongError(t *testing.T) {
	e, _, hooks := dispatchBox(t)
	s, _ := e.service()
	got := capture(s)
	long := strings.Repeat("<&\" ", 2500) // 15 KB, longer still as JSON
	withHelper(t, "truckersmp", testHelper{id: "truckersmp", calls: hooks, hook: func(context.Context, *Launch) error {
		return errors.New(long)
	}})
	if rc, _ := runLaunch("--app", "227300", "/games/ets2"); rc != 1 {
		t.Fatalf("exit %d", rc)
	}
	recs := launchMessages(t)
	if len(recs) != 1 || recs[0].Code != codeHookFailed || len([]rune(recs[0].Detail)) != maxMessageDetail {
		t.Fatalf("records %+v", recs)
	}
	s.pollMessages()
	if len(*got) != 1 || (*got)[0].topic != "system.message" {
		t.Fatalf("published %+v", *got)
	}
}

// wordyHook is a hook whose helper words its own refusals.
type wordyHook struct{ testHelper }

func (wordyHook) MessageText(code string) (string, bool) { return wordsHelper{}.MessageText(code) }

// A hook's Refusal with a code its helper words is recorded with that
// code; one it has no words for is a plain hook-failed.
func TestDispatchHookRefusal(t *testing.T) {
	_, calls, hooks := dispatchBox(t)
	for _, tc := range []struct{ code, want string }{{"starting", "starting"}, {"pwned", codeHookFailed}, {codeNotMounted, codeHookFailed}} {
		withHelper(t, "truckersmp", wordyHook{testHelper: testHelper{id: "truckersmp", calls: hooks, hook: func(context.Context, *Launch) error {
			return fmt.Errorf("multiplayer: %w", Refuse(tc.code, errors.New("a handoff unit is loaded")))
		}}})
		if rc, _ := runLaunch("--app", "227300", "/games/ets2"); rc != 1 || len(*calls) != 0 {
			t.Fatalf("%s: exit %d, calls %d", tc.code, rc, len(*calls))
		}
		recs := launchMessages(t)
		if r := recs[len(recs)-1]; r.Code != tc.want || r.ID != "truckersmp" || !strings.Contains(r.Detail, "a handoff unit is loaded") {
			t.Fatalf("%s: record %+v", tc.code, r)
		}
	}
}
