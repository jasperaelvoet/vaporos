package extensions

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/gameproc"
)

// The dispatcher: `vos ext launch` stands in front of every Steam launch
// VaporOS wraps (docs/CONTRACTS.md "Binary" and "Extensions", Steam). It
// finds which app or shortcut is starting, runs the launch hooks of the
// mounted extensions that hook it, and execs the command. It runs as the
// gaming user, inside Steam.

// launchID is what a launch starts: a Steam app, or an extension's
// shortcut (owner/key).
type launchID struct {
	app        uint32
	owner, key string
}

// identify reads the launch's identity from, in order, --app or
// --shortcut, Steam's reaper line at the start of the command (AppId=),
// and Steam's SteamAppId and SteamGameId variables. A shortcut's app id
// (top bit set) or game id names an extension's shortcut when steam.json
// or prepare's record knows it; otherwise the launch is nobody's.
func identify(l launch, env []string, d *SteamDesired, st *prepareState) launchID {
	if l.app != 0 {
		return launchID{app: l.app}
	}
	if l.shortcut != "" {
		return launchID{owner: l.shortcut, key: l.key}
	}
	n, ok := gameproc.ReaperApp(l.argv)
	if !ok {
		n = envUint(env, "SteamAppId")
	}
	if n == 0 {
		n = envUint(env, "SteamGameId")
	}
	var appid uint32
	switch {
	case n == 0:
		return launchID{}
	case n > math.MaxUint32:
		if !isShortcutGameID(n) {
			return launchID{}
		}
		appid = uint32(n >> 32)
	case n < 0x80000000:
		return launchID{app: uint32(n)}
	default:
		appid = uint32(n)
	}
	if d != nil {
		for _, sc := range d.Shortcuts {
			if ShortcutAppID(sc.Owner, sc.Key) == appid {
				return launchID{owner: sc.Owner, key: sc.Key}
			}
		}
	}
	if owner, key, ok := st.shortcutByAppID(appid); ok {
		return launchID{owner: owner, key: key}
	}
	return launchID{}
}

func envUint(env []string, key string) uint64 {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// appHooks returns the extensions whose hooks steam.json lists for app
// and this boot mounted, in the boot report's (catalog) order.
func appHooks(d *SteamDesired, rep *store.BootReport, app uint32) []string {
	var listed []string
	for _, a := range d.Apps {
		if a.App == app {
			listed = a.Hooks
		}
	}
	var out []string
	for _, m := range rep.Mounted {
		if slices.Contains(listed, m.ID) && !slices.Contains(out, m.ID) {
			out = append(out, m.ID)
		}
	}
	return out
}

// stripPreload takes LD_PRELOAD (Steam's overlay) out of this process's
// environment, which every helper program a hook starts inherits; the
// launch itself keeps Steam's environment. A variable for tests.
var stripPreload = func() { os.Unsetenv("LD_PRELOAD") }

// dispatch runs l: the hooks of what it starts, then the command.
func dispatch(ctx context.Context, l launch, env []string, stderr io.Writer) int {
	d, _ := readSteamDesired() // nil without one: no app is hooked
	var st *prepareState
	if l.app == 0 && l.shortcut == "" {
		st = readPrepareState()
	}
	id := identify(l, env, d, st)
	rep, repErr := store.LoadBootReport()
	var hooks []string
	switch {
	case id.owner != "":
		if repErr != nil || !rep.IsMounted(id.owner) {
			return refuseLaunch(stderr, fmt.Sprintf("%s did not start because its extension is not active right now (%s/%s). Check Extensions in VaporOS.",
				displayName(id.owner), id.owner, id.key))
		}
		hooks = []string{id.owner}
	case id.app != 0 && d != nil && repErr == nil:
		hooks = appHooks(d, rep, id.app)
	}
	run := &Launch{App: id.app, Argv: slices.Clone(l.argv), Env: slices.Clone(env)}
	if id.owner != "" {
		run.Shortcut = id.owner + "/" + id.key
	}
	if len(hooks) > 0 {
		stripPreload()
	}
	for _, h := range hooks {
		if err := HelperFor(h).LaunchHook(ctx, run); err != nil {
			return refuseLaunch(stderr, fmt.Sprintf("%s did not start: %v", displayName(h), err))
		}
		if len(run.Argv) == 0 {
			return refuseLaunch(stderr, fmt.Sprintf("%s did not start: its hook left nothing to run.", displayName(h)))
		}
	}
	return execLaunch(run, stderr)
}

// refuseLaunch tells the person at the control center (writeMessage) and
// Steam's log why a launch does not start.
func refuseLaunch(stderr io.Writer, text string) int {
	fmt.Fprintf(stderr, "vos ext launch: %s\n", text)
	if err := writeMessage(text); err != nil {
		fmt.Fprintf(stderr, "vos ext launch: telling VaporOS: %v\n", err)
	}
	return 1
}

// displayName is the extension's name from its shipped descriptor.
func displayName(id string) string {
	if d, err := Shipped(id); err == nil {
		return d.Name
	}
	return id
}

// execLaunch replaces this process with the launch's command.
func execLaunch(run *Launch, stderr io.Writer) int {
	path := run.Argv[0]
	if !filepath.IsAbs(path) {
		var err error
		if path, err = exec.LookPath(path); err != nil {
			fmt.Fprintf(stderr, "vos ext launch: %v\n", err)
			return 1
		}
	}
	err := execve(path, run.Argv, run.Env)
	fmt.Fprintf(stderr, "vos ext launch: %s: %v\n", path, err)
	return 1
}
