package truckersmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const usage = `usage: vos ext truckersmp <command>, as the gaming user
  sync                       download the TruckersMP mod's files for the installed games
  mp ets2|ats                start multiplayer through Steam
  handoff ets2|ats ID NONCE  (the transient unit mp starts) wait for shortcut ID to end, then start the game
  setup                      copy the injector into the home data area
  copy-profiles              copy the Linux builds' profiles into the Proton prefixes`

// Exit codes besides 0, 1 and 2 (bad arguments): the sync failures the
// card words by itself.
const (
	exitSyncRunning = 3 // another sync holds the lock
	exitUnchecked   = 4 // errUnchecked
	exitNoSpace     = 5 // a spaceError, after a {"short":N} line
)

// cli is `vos ext truckersmp`. Every command works in the gaming user's
// files, so none runs as root.
func cli(args []string) int {
	return runCLI(args, os.Stdout, os.Stderr)
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "vos ext truckersmp: runs as the gaming user, not as root")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	switch cmd, rest := args[0], args[1:]; {
	case cmd == "sync" && len(rest) == 0:
		err := newSyncer(stdout).run(ctx)
		var space *spaceError
		switch {
		case errors.Is(err, errSyncRunning):
			fmt.Fprintln(stderr, err)
			return exitSyncRunning
		case errors.Is(err, errUnchecked):
			fmt.Fprintln(stderr, err)
			return exitUnchecked
		case errors.As(err, &space):
			fmt.Fprintf(stdout, "{\"short\":%d}\n", space.short)
			fmt.Fprintln(stderr, err)
			return exitNoSpace
		}
		return report(stderr, err)
	case cmd == "mp" && len(rest) == 1:
		g, ok := gameByKey(rest[0])
		if !ok {
			break
		}
		if err := newMP().run(ctx, g); err != nil {
			return 1
		}
		return 0
	case cmd == "handoff" && len(rest) == 3:
		g, ok := gameByKey(rest[0])
		shortcut, err := strconv.ParseUint(rest[1], 10, 64)
		if !ok || err != nil || !nonceRe.MatchString(rest[2]) {
			break
		}
		if err := newHandoff(stderr).run(ctx, g, shortcut, rest[2]); err != nil {
			return 1
		}
		return 0
	case cmd == "setup" && len(rest) == 0:
		return report(stderr, ensureInjector(injectorSource(), filepath.Join(homeDir(), binRel)))
	case cmd == "copy-profiles" && len(rest) == 0:
		r := copyProfiles(homeDir(), libraries(), stderr)
		for _, c := range r.copied {
			fmt.Fprintln(stdout, "copied", c)
		}
		return report(stderr, r.err())
	}
	fmt.Fprintln(stderr, usage)
	return 2
}

// report prints err as the command's one line of output for vosd.
func report(stderr io.Writer, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, err)
	return 1
}

// syncTimeout bounds one sync run by vosd (the first downloads about 640
// MiB).
const syncTimeout = 4 * time.Hour
