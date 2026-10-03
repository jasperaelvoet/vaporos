package extensions

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// gamescopeUnit is the gaming user's unit that runs gamescope and Steam.
const gamescopeUnit = "vos-gamescope.service"

// What an unwrap asks and runs, and how often and how long it waits for
// gamescope's unit to settle; variables for tests.
var (
	gamescopeState = func(ctx context.Context) string { return sysd.ActiveState(ctx, gamescopeUnit, true) }
	prepareAsGamer = func(ctx context.Context) (string, error) {
		return sysd.AsGamer(ctx, vosBinary, "steam", "prepare")
	}
	settleEvery = time.Second
	settleFor   = 30 * time.Second
)

const (
	// prepareWait bounds vosd's run of prepare, which gives itself 5 s.
	prepareWait = 30 * time.Second
	// maxPrepareLines is how much of prepare's output vosd logs: its last
	// lines, the very last saying what it did or why it skipped.
	maxPrepareLines = 10
)

// GoingDown's unwrap must be the last prepare before the restart: once
// goingDown is set, run starts no prepare (which would put back what the
// unwrap took out), and GoingDown takes prepMu to wait for one that runs.
var (
	goingDown atomic.Bool
	prepMu    sync.Mutex
)

// unwrap runs `vos steam prepare` as vapor after the dispatcher turned
// off (steam.json's dispatcher went from true to false), with what it
// asks and runs taken when SyncSteam starts it.
type unwrap struct {
	state        func(context.Context) string
	prepare      func(context.Context) (string, error)
	every, limit time.Duration
}

// run waits for gamescope's unit to settle, looking every u.every for up
// to u.limit. Down (inactive or failed), nothing else will run prepare
// before the box may next boot a VaporOS without `vos ext launch`, whose
// games would not start with it in their launch options: a stopping unit
// may have run its own prepare (ExecStopPost) before steam.json changed.
// So prepare runs here. Active, the unit ran prepare as it started
// (ExecStartPre), or the Steam restart vosd asked for will.
func (u unwrap) run() {
	ctx, cancel := context.WithTimeout(context.Background(), u.limit+prepareWait)
	defer cancel()
	for waited := time.Duration(0); ; waited += u.every {
		st := u.state(ctx)
		switch st {
		case "inactive", "failed":
			prepMu.Lock()
			defer prepMu.Unlock()
			if goingDown.Load() {
				log.Printf("extensions: VaporOS is restarting into a version without extensions; it took them out of Steam itself")
				return
			}
			u.runPrepare(ctx)
			return
		case "active":
			return
		}
		if waited >= u.limit {
			log.Printf("extensions: %s stayed %q for %s after the dispatcher turned off; its next start or stop runs vos steam prepare", gamescopeUnit, st, u.limit)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(u.every):
		}
	}
}

// runPrepare runs prepare and logs what it said it did: it always exits 0,
// whether it unwrapped the launch options or skipped.
func (u unwrap) runPrepare(ctx context.Context) {
	out, err := u.prepare(ctx)
	logPrepare(out)
	if err != nil {
		if inner := errors.Unwrap(err); inner != nil {
			err = inner // the rest is the command line and the output above
		}
		log.Printf("extensions: vos steam prepare as vapor: %v", err)
	}
}

// logPrepare logs prepare's output, each line on one line and at most
// maxPrepareLines of them, the last ones.
func logPrepare(out string) {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = oneLine(l, maxMessageDetail); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		log.Print("extensions: vos steam prepare as vapor said nothing")
		return
	}
	if n := len(lines) - maxPrepareLines; n > 0 {
		log.Printf("extensions: vos steam prepare as vapor: %d earlier lines left out", n)
		lines = lines[n:]
	}
	for _, l := range lines {
		log.Printf("extensions: vos steam prepare as vapor: %s", l)
	}
}
