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
	// handOff starts the transient unit.
	handOff func(ctx context.Context, args []string) error
	tell    func(text string)
}

func newMP() *mp {
	return &mp{
		procs: ownProcs(), pid: os.Getpid(), flag: flagPath(), runtime: runtimeDir(), home: homeDir(),
		now: time.Now, handOff: systemdRun, tell: tell,
	}
}

// tell shows text at the control center and in the log.
func tell(text string) {
	fmt.Fprintln(os.Stderr, "truckersmp:", text)
	if err := extensions.WriteMessage(text); err != nil {
		fmt.Fprintln(os.Stderr, "truckersmp: telling VaporOS:", err)
	}
}

// run starts multiplayer for g. A refusal is told and is an error.
func (m *mp) run(ctx context.Context, g game) error {
	if running := m.runningGame(); running != "" {
		return m.refuse(fmt.Sprintf("TruckersMP didn't start because %s is already running. Quit it, then start TruckersMP again.", running))
	}
	if _, err := os.Lstat(filepath.Join(m.runtime, "systemd", "transient", gameproc.HandoffUnit)); err == nil {
		return m.refuse("TruckersMP is already starting. Wait for the game to open.")
	}
	if err := quickCheck(m.home, g); err != nil {
		return m.refuse("TruckersMP didn't start because its files are updating. Try again in a few minutes.")
	}
	f, err := writeFlag(m.flag, g, m.now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "truckersmp: writing the flag:", err)
		return m.refuse(handOffFailed)
	}
	shortcut := m.procs.reaperAbove(m.pid)
	args := []string{vosBin, "ext", ID, "handoff", g.key, strconv.FormatUint(shortcut, 10), f.Nonce}
	if err := m.handOff(ctx, args); err != nil {
		fmt.Fprintln(os.Stderr, "truckersmp:", err)
		dropFlag(m.flag, f.Nonce)
		return m.refuse(handOffFailed)
	}
	return nil
}

const handOffFailed = "TruckersMP didn't start because VaporOS couldn't pass the start on to Steam. Try again."

func (m *mp) refuse(text string) error {
	m.tell(text)
	return errors.New(text)
}

// runningGame names ETS2 or ATS when Steam runs either: TruckersMP needs
// the game started anew, and Steam would only ask whether to start a
// second one.
func (m *mp) runningGame() string {
	reapers := m.procs.reapers()
	for _, g := range games {
		if slices.Contains(reapers, uint64(g.app)) {
			return g.short
		}
	}
	return ""
}

// systemdRun starts the handoff in the gaming user's manager, so it lives
// on after the shortcut's processes end. Steam's overlay (LD_PRELOAD) and
// runtime libraries (LD_LIBRARY_PATH) stay out of systemd-run.
func systemdRun(ctx context.Context, args []string) error {
	full := append([]string{"--user", "--collect", "--quiet", "--unit=" + strings.TrimSuffix(gameproc.HandoffUnit, ".service"), "--"}, args...)
	cmd := exec.CommandContext(ctx, "systemd-run", full...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "LD_PRELOAD=") || strings.HasPrefix(kv, "LD_LIBRARY_PATH=")
	})
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
	tell    func(text string)
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
		text := "TruckersMP didn't start because Steam didn't respond. Try again once Steam is open."
		h.tell(text)
		return errors.New(text)
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
