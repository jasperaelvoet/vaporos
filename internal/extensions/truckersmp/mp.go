package truckersmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/gameproc"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

// `vos ext truckersmp mp ets2|ats` starts multiplayer, from the extension's
// Steam shortcut or from Moonlight (Sunshine's entry), as the gaming user.
// It cannot start the game itself: Steam must, so the game gets its own
// prefix, runtime and overlay, and Steam starts nothing while the
// shortcut that ran this command still counts as running. So it leaves
// the flag for the launch hook and hands the start to a transient user
// unit outside the shortcut, which waits until the shortcut has ended and
// then asks Steam for the game.

// mp is one `vos ext truckersmp mp`; its fields are seams for tests.
type mp struct {
	procs   procFS
	pid     int
	flag    string
	runtime string // XDG_RUNTIME_DIR
	home    string // the home data area
	now     func() time.Time
	libs    func() []string // the Steam libraries
	// handOff starts the transient unit.
	handOff func(ctx context.Context, args []string) error
	tell    func(code string)
}

func newMP() *mp {
	return &mp{
		procs: ownProcs(), pid: os.Getpid(), flag: flagPath(), runtime: runtimeDir(), home: homeDir(),
		now: time.Now, libs: libraries, handOff: systemdRun, tell: tell,
	}
}

// What mp and handoff tell the person when they refuse: they leave vosd
// the code (extensions.WriteMessage), which vosd words with messageText
// (Helper.MessageText), so the person reads only VaporOS's sentences. The
// launch hook and Install refuse with codes of their own the same way
// (extensions.Refuse).
const (
	msgRunning      = "running-"       // and the game's key
	msgNotInstalled = "not-installed-" // and the game's key
	msgNoFiles      = "no-files-"      // and the game's key
	msgLinux        = "linux-"         // and the game's key: the hook, for the Linux build
	msgNeedsUpdate  = "needs-update-"  // and the game's key: no launch options carry the dispatcher
	msgStarting     = "starting"
	msgUpdating     = "updating"
	msgHandOff      = "handoff-failed"
	msgNoSteam      = "steam-silent"
	msgLauncher     = "launcher-failed" // the hook could not copy the injector
	msgSetup        = "setup-failed"    // Install could not copy the injector
)

// messageText is the sentence for one of those codes.
func messageText(code string) (string, bool) {
	switch code {
	case msgStarting:
		return "TruckersMP is already starting. Wait for the game to open.", true
	case msgUpdating:
		return "TruckersMP didn't start because its files are updating. Try again in a few minutes.", true
	case msgHandOff:
		return "TruckersMP didn't start because VaporOS couldn't pass the start on to Steam. Try again.", true
	case msgNoSteam:
		return "TruckersMP didn't start because Steam didn't respond. Try again once Steam is open.", true
	case msgLauncher:
		return "TruckersMP didn't start because VaporOS couldn't set up its launcher. Restart VaporOS and try again.", true
	case msgSetup:
		return "Setting up TruckersMP didn't finish because VaporOS couldn't copy its launcher. Try again, or remove it.", true
	}
	for _, c := range []struct{ prefix, format string }{
		{msgRunning, "TruckersMP didn't start because %s is already running. Quit it, then start TruckersMP again."},
		{msgNotInstalled, "TruckersMP didn't start because %s isn't installed. Install it in Steam, then try again."},
		{msgNoFiles, "TruckersMP didn't start because its files for %s aren't downloaded yet. Try again once its card in VaporOS says it's ready."},
		{msgLinux, "TruckersMP didn't start because %s isn't set to run with Proton. Restart VaporOS and try again."},
		{msgNeedsUpdate, "TruckersMP didn't start because it needs the next VaporOS update. Until then, %s starts from Steam in single-player."},
	} {
		if key, ok := strings.CutPrefix(code, c.prefix); ok {
			if g, ok := gameByKey(key); ok {
				return fmt.Sprintf(c.format, g.short), true
			}
		}
	}
	return "", false
}

// tell shows code's sentence at the control center and in the log.
func tell(code string) {
	text, _ := messageText(code)
	fmt.Fprintln(os.Stderr, "truckersmp:", text)
	if err := extensions.WriteMessage(ID, code, ""); err != nil {
		fmt.Fprintln(os.Stderr, "truckersmp: telling VaporOS:", err)
	}
}

// told tells code and returns it as a refusal with its sentence.
func told(tell func(string), code string) error {
	tell(code)
	text, _ := messageText(code)
	return extensions.Refuse(code, errors.New(text))
}

// run starts multiplayer for g. A refusal is told and is an error.
func (m *mp) run(ctx context.Context, g game) error {
	// Only the launch hook makes the game's start multiplayer, and it runs
	// only while the game's launch options carry the dispatcher: without
	// it the game would start in single-player as if nothing were wrong.
	if on, err := extensions.SteamDispatcher(); err != nil || !on {
		if err != nil {
			fmt.Fprintln(os.Stderr, "truckersmp: steam.json:", err)
		}
		return told(m.tell, msgNeedsUpdate+g.key)
	}
	if running := m.runningGame(); running != "" {
		return told(m.tell, msgRunning+running)
	}
	if _, err := os.Lstat(filepath.Join(m.runtime, "systemd", "transient", gameproc.HandoffUnit)); err == nil {
		return told(m.tell, msgStarting)
	}
	if gameLibrary(m.libs(), g) == "" {
		return told(m.tell, msgNotInstalled+g.key)
	}
	if err := quickCheck(m.home, g); err != nil {
		return told(m.tell, notReady(m.home, g))
	}
	f, err := writeFlag(m.flag, g, m.now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "truckersmp: writing the flag:", err)
		return told(m.tell, msgHandOff)
	}
	shortcut := m.procs.reaperAbove(m.pid)
	args := []string{vosBin, "ext", ID, "handoff", g.key, strconv.FormatUint(shortcut, 10), f.Nonce}
	if err := m.handOff(ctx, args); err != nil {
		fmt.Fprintln(os.Stderr, "truckersmp:", err)
		dropFlag(m.flag, f.Nonce)
		return told(m.tell, msgHandOff)
	}
	return nil
}

// notReady is the code for g's files failing the quick check: not
// downloaded for g yet, or updating. A sync deletes the manifest only
// while it moves files into place, so a manifest without g means no sync
// has fetched g's files; without a manifest, g's core library in MODDIR
// tells that one did.
func notReady(home string, g game) string {
	if m, err := readManifest(home); err == nil && m != nil {
		if m.has(g) {
			return msgUpdating
		}
		return msgNoFiles + g.key
	}
	if _, err := os.Lstat(filepath.Join(home, filesRel, g.coreDLL)); err == nil {
		return msgUpdating
	}
	return msgNoFiles + g.key
}

// runningGame is the key of ETS2 or ATS when Steam runs either:
// TruckersMP needs the game started anew, and Steam would only ask
// whether to start a second one.
func (m *mp) runningGame() string {
	reapers := m.procs.reapers()
	for _, g := range games {
		if slices.Contains(reapers, uint64(g.app)) {
			return g.key
		}
	}
	return ""
}

// systemdRun starts the handoff in the gaming user's manager, so it lives
// on after the shortcut's processes end. Steam's overlay, runtime
// libraries and runtime settings stay out of systemd-run, as they stay out
// of a hook's programs.
func systemdRun(ctx context.Context, args []string) error {
	full := append([]string{"--user", "--collect", "--quiet", "--unit=" + strings.TrimSuffix(gameproc.HandoffUnit, ".service"), "--"}, args...)
	cmd := exec.CommandContext(ctx, "systemd-run", full...)
	cmd.Env = extensions.WithoutSteamEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// handoff is `vos ext truckersmp handoff`, in the transient unit.
type handoff struct {
	procs   procFS
	flag    string
	poll    time.Duration // how often it looks for the shortcut
	settle  time.Duration // after the shortcut ended, before the game starts
	maxWait time.Duration // for the shortcut to end
	sleep   func(ctx context.Context, d time.Duration) error
	launch  func(ctx context.Context, url string) bool
	tell    func(code string)
}

func newHandoff(stderr io.Writer) *handoff {
	return &handoff{
		procs: ownProcs(), flag: flagPath(),
		poll: 250 * time.Millisecond, settle: 2 * time.Second, maxWait: 30 * time.Second,
		sleep: sleepCtx,
		launch: func(ctx context.Context, url string) bool {
			return session.Launch(ctx, url, stderr)
		},
		tell: tell,
	}
}

// run waits for the shortcut's reaper to end (when one started this), a
// moment more for Steam to see it gone, and asks Steam for the game. The
// flag goes when Steam was never asked: no start will take it.
func (h *handoff) run(ctx context.Context, g game, shortcut uint64, nonce string) error {
	if shortcut != 0 {
		h.waitGone(ctx, shortcut)
		if err := h.sleep(ctx, h.settle); err != nil {
			dropFlag(h.flag, nonce)
			return err
		}
	}
	if !h.launch(ctx, "steam://rungameid/"+strconv.FormatUint(uint64(g.app), 10)) {
		dropFlag(h.flag, nonce)
		return told(h.tell, msgNoSteam)
	}
	return nil
}

// waitGone waits up to maxWait until no reaper runs the shortcut.
func (h *handoff) waitGone(ctx context.Context, shortcut uint64) {
	deadline := time.Now().Add(h.maxWait)
	for time.Now().Before(deadline) {
		if !slices.ContainsFunc(h.procs.reapers(), func(a uint64) bool { return sameApp(a, shortcut) }) {
			return
		}
		if h.sleep(ctx, h.poll) != nil {
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
