package extensions

import (
	"context"
	"errors"
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

// steamOnly reports whether a variable of Steam's environment is for the
// game alone: Steam's overlay (LD_PRELOAD), its runtime's libraries
// (LD_LIBRARY_PATH) and the runtime's and its container's settings, which
// would load Steam's libraries into VaporOS's programs or send them into
// the container.
func steamOnly(key string) bool {
	return key == "LD_PRELOAD" || key == "LD_LIBRARY_PATH" ||
		strings.HasPrefix(key, "STEAM_RUNTIME") || strings.HasPrefix(key, "PRESSURE_VESSEL")
}

// WithoutSteamEnv is env less the steamOnly variables: what a helper's
// own command that Steam started (TruckersMP's mp) hands the programs it
// starts, as a hook's programs get.
func WithoutSteamEnv(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return steamOnly(k)
	})
}

// stripSteamEnv takes the steamOnly variables out of this process's
// environment, which every helper program a hook starts inherits; the
// launch itself keeps Steam's environment. A variable for tests.
var stripSteamEnv = func() {
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); steamOnly(k) {
			os.Unsetenv(k)
		}
	}
}

// dispatch runs l: the hooks of what it starts, then the command. A
// refusal leaves a record for vosd (refuseLaunch), unless Steam stopped
// the launch itself (ctx ended while a hook ran): nobody waits for it then.
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
			detail := "this boot did not mount it"
			if repErr != nil {
				detail = repErr.Error()
			}
			return refuseLaunch(stderr, launchRecord{Code: codeNotMounted, ID: id.owner,
				Detail: fmt.Sprintf("shortcut %s/%s: %s", id.owner, id.key, detail)})
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
		stripSteamEnv()
	}
	for _, h := range hooks {
		err := HelperFor(h).LaunchHook(ctx, run)
		if ctx.Err() != nil {
			// Also when the hook itself returned nil: nothing more runs.
			return stopped(stderr, h, err)
		}
		if err == nil && len(run.Argv) == 0 {
			err = errors.New("its hook left nothing to run")
		}
		if err == nil {
			continue
		}
		return refuseLaunch(stderr, launchRecord{Code: codeHookFailed, ID: h, Detail: err.Error()})
	}
	if ctx.Err() != nil {
		return stopped(stderr, "", nil)
	}
	return execLaunch(run, stderr)
}

// stopped ends a launch Steam stopped (SIGTERM or SIGINT): nobody waits
// for that game any more, so vosd is told nothing and nothing is run.
func stopped(stderr io.Writer, hook string, err error) int {
	msg := "vos ext launch: Steam stopped the launch"
	if hook != "" {
		msg = "vos ext launch: " + hook + ": Steam stopped the launch"
	}
	if err != nil {
		msg += ": " + err.Error()
	}
	fmt.Fprintln(stderr, msg)
	return 1
}

// refuseLaunch tells Steam's log why a launch does not start, and vosd
// (writeMessage), which tells the person at the control center in its
// own words.
func refuseLaunch(stderr io.Writer, r launchRecord) int {
	fmt.Fprintf(stderr, "vos ext launch: %s: %s: %s\n", r.ID, r.Code, r.Detail)
	if err := writeMessage(r); err != nil {
		fmt.Fprintf(stderr, "vos ext launch: telling VaporOS: %v\n", err)
	}
	return 1
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
