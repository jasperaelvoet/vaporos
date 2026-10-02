// Package steamprep is `vos steam prepare`: before every Steam start, as
// the gaming user, it brings Steam's own files in line with what the
// mounted extensions ask for (/var/lib/vos/ext/steam.json): compatibility
// tools, launch options that go through `vos ext launch`, shortcuts and
// their art, and queued branch switches. It changes only what VaporOS
// owns and keeps a record of what was there before. vosd never runs it;
// see "Steam" under Extensions in docs/CONTRACTS.md.
package steamprep

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// Budget bounds a whole run: gamescope, and Steam with it, waits for it.
const Budget = 5 * time.Second

// Options are one run's inputs; tests point them into a temp dir.
type Options struct {
	Home    string // the gaming user's home
	ProcDir string // to see whether Steam runs
	Unwrap  bool
	Budget  time.Duration
	Log     *log.Logger
}

// Variables for tests.
var (
	geteuid = os.Geteuid
	getuid  = os.Getuid
	// stepHook runs before each step, with the run's context.
	stepHook func(ctx context.Context, step string)
	startRun = start
)

const usage = "usage: vos steam prepare [--unwrap]"

// CLI runs `vos steam <command>`. Prepare always exits 0: a Steam that
// starts with its files as they were beats one that does not start.
func CLI(args []string) int {
	return cli(args, os.Stderr)
}

func cli(args []string, stderr io.Writer) int {
	logger := log.New(stderr, "vos steam: ", 0)
	if len(args) == 0 || args[0] != "prepare" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	o := Options{ProcDir: "/proc", Budget: Budget, Log: logger}
	for _, a := range args[1:] {
		if a != "--unwrap" {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		o.Unwrap = true
	}
	var err error
	if o.Home, err = os.UserHomeDir(); err != nil {
		logger.Printf("prepare: skipped: %v", err)
		return 0
	}
	Run(context.Background(), o)
	return 0
}

// Run prepares Steam's files within o.Budget. It returns at the budget
// even when a step hangs; the caller then exits, which ends that step.
func Run(ctx context.Context, o Options) {
	hard := time.NewTimer(o.Budget)
	defer hard.Stop()
	select {
	case <-startRun(ctx, o):
	case <-hard.C:
		o.Log.Printf("prepare: stopped, out of time after %s; Steam starts with what is done", o.Budget)
	}
}

// start runs prepare in the background and closes the channel when it
// is done. The work stops itself a tenth before the budget, so it can
// still record how far it got.
func start(ctx context.Context, o Options) <-chan struct{} {
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(ctx, o.Budget-o.Budget/10)
	go func() {
		defer close(done)
		defer cancel()
		prepare(ctx, o)
	}()
	return done
}
