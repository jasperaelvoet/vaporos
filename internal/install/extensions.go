package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// The new system's extensions (docs/CONTRACTS.md "Extensions"): its core
// images are fetched and sealed into its store while installing, as the
// pending set, so its first boot tries them and `vos health` promotes them.
// Only as far as the network allows: an offline install still works, and
// vosd fetches them once the system is online.

var (
	// seedTimeout caps the fetch, so a slow network costs at most this.
	seedTimeout = 20 * time.Minute
	// seedReportEvery is how often progress is reported while bytes come
	// in: the percent alone moves only twice in the configure step.
	seedReportEvery = time.Second
)

// seedRun runs name with args, calling stdout and stderr with each line
// the command prints there. Tests fake it.
var seedRun = runLines

// seedExtensions runs the new image's `vos ext fetch --seed` against the
// new system's state directory: a separate process, because the store's
// paths are the process's. It reports under the configure step and only
// fails the install when the install itself is cancelled.
func (in *installer) seedExtensions(ctx context.Context) error {
	repair := in.opts.Mode == ModeRepair
	if repair {
		in.resetExtensions()
	}
	if len(in.man.Extensions) == 0 && !repair {
		return nil
	}
	args := []string{"ext", "fetch", "--state-dir", filepath.Join(in.rootDir, config.StateDir),
		"--from", in.seedSource(), "--version", in.man.Version, "--seed"}
	if repair {
		args = append(args, "--repair")
	}
	const what = "Adding extensions"
	in.report(StepConfigure, 96, "%s", what)
	sctx, cancel := context.WithTimeout(ctx, seedTimeout)
	defer cancel()
	logLine := func(line string) { in.env.logf("install: vos ext fetch: %s", line) }
	var (
		reported bool
		lastDone int64
		lastAt   time.Time
	)
	err := seedRun(sctx, filepath.Join(in.rootDir, "usr", "bin", "vos"), args, func(line string) {
		var p struct{ Bytes, Total int64 }
		if json.Unmarshal([]byte(line), &p) != nil || p.Total <= 0 {
			logLine(line)
			return
		}
		// The bytes cover the whole run. The first line always shows, then
		// a new percent, the end, or new bytes once seedReportEvery passed.
		done := min(max(p.Bytes, 0), p.Total)
		pct, now := 96+int(2*done/p.Total), time.Now()
		if reported && (done == lastDone || pct == in.percent && done != p.Total && now.Sub(lastAt) < seedReportEvery) {
			return
		}
		reported, lastDone, lastAt = true, done, now
		in.report(StepConfigure, pct, "%s (%s of %s)", what, humanBytes(done), humanBytes(p.Total))
	}, logLine)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		in.env.logf("install: extensions not added now (%v); VaporOS adds them once it is online", err)
	}
	return nil
}

// seedSource is where the images come from: the install's own source, or,
// for the live medium (which carries none), the new system's
// config.update.source; `--version` picks this image there.
func (in *installer) seedSource() string {
	switch in.src.spec.kind {
	case srcDir:
		return in.src.spec.dir
	case srcHTTP, srcOCI:
		return in.src.spec.raw
	}
	cfg := config.Defaults()
	if err := config.ReadJSON(filepath.Join(in.rootDir, config.ConfigPath()), cfg); err != nil {
		in.env.logf("install: config.json: %v", err)
	}
	if cfg.Update.Source != "" {
		return cfg.Update.Source
	}
	return config.DefaultUpdateSrc
}

// resetExtensions drops what a repaired system must not carry over: slot
// b's catalog (the slot was wiped), the enabled set, the set waiting for its
// trial and the failed sets; wanted stays. `vos ext fetch --repair` does
// the same and then seeds the core extensions as a pending trial. Doing it
// here first means a repair that cannot run it (offline) boots with no
// extension rather than the old set, and vosd proposes them again later.
func (in *installer) resetExtensions() {
	for _, p := range []string{filepath.Join(config.ExtSlotsDir(), "b.json"), config.ExtEnabledLink(),
		config.ExtPendingLink(), config.ExtFailedPath()} {
		if err := os.Remove(filepath.Join(in.rootDir, p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			in.env.logf("install: %v", err)
		}
	}
}

// runLines runs a command, handing over its output a line at a time.
func runLines(ctx context.Context, name string, args []string, stdout, stderr func(line string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, errOut := &lineWriter{fn: stdout}, &lineWriter{fn: stderr}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	out.flush()
	errOut.flush()
	return err
}

// lineWriter calls fn for every line written to it.
type lineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.fn(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 64<<10 {
		w.flush()
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.fn(string(w.buf))
		w.buf = nil
	}
}
