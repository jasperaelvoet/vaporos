package extensions

import (
	"context"
	"encoding/json"
	"errors"
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

// launchMessages returns the texts `vos ext launch` left for vosd, oldest
// first.
func launchMessages(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), messagesRel)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		var m message
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		must(t, err)
		must(t, json.Unmarshal(b, &m))
		if m.Level != "warning" {
			t.Errorf("%s: level %q", e.Name(), m.Level)
		}
		out = append(out, m.Text)
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

func TestDispatchRunsTheHooks(t *testing.T) {
	_, calls, hooks := dispatchBox(t)
	t.Setenv("LD_PRELOAD", "/var/home/vapor/.local/share/Steam/ubuntu12_64/gameoverlayrenderer.so")
	stripPreload = func() { os.Unsetenv("LD_PRELOAD") }
	var helperSaw string
	withHelper(t, "truckersmp", testHelper{id: "truckersmp", calls: hooks, hook: func(ctx context.Context, l *Launch) error {
		if l.App != 227300 || l.Shortcut != "" || !slices.Equal(l.Argv, ets2Argv) {
			return errors.New("wrong launch")
		}
		// A helper program the hook starts runs without Steam's overlay.
		out, err := exec.CommandContext(ctx, "sh", "-c", `printf %s "$LD_PRELOAD"`).Output()
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
	if !slices.Contains(c.env, "TRUCKERSMP=1") || !slices.Contains(c.env, "LD_PRELOAD=/var/home/vapor/.local/share/Steam/ubuntu12_64/gameoverlayrenderer.so") {
		t.Errorf("the game lost Steam's environment: %q", c.env)
	}
	if helperSaw != "" {
		t.Errorf("a helper program got LD_PRELOAD=%q", helperSaw)
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
	if rc, msg := runLaunch("/usr/bin/true"); rc != 1 || len(*calls) != 2 || !strings.Contains(msg, "Star Citizen did not start") {
		t.Errorf("exit %d, calls %d: %s", rc, len(*calls), msg)
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
	if rc != 1 || len(*calls) != 0 || !strings.Contains(msg, "TruckersMP did not start: the TruckersMP files are out of date") {
		t.Fatalf("exit %d, calls %d: %s", rc, len(*calls), msg)
	}
	if texts := launchMessages(t); len(texts) != 1 || texts[0] != "TruckersMP did not start: the TruckersMP files are out of date. Update them on the TruckersMP card" {
		t.Fatalf("messages %q", texts)
	}

	// A hook that leaves nothing to run refuses too.
	withHelper(t, "truckersmp", testHelper{id: "truckersmp", hook: func(_ context.Context, l *Launch) error {
		l.Argv = nil
		return nil
	}})
	if rc, _ := runLaunch("--app", "227300", "/games/ets2"); rc != 1 || len(*calls) != 0 {
		t.Fatalf("exit %d, calls %d", rc, len(*calls))
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
