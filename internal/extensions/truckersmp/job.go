package truckersmp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
)

// syncJob is a sync vosd runs, as the gaming user.
type syncJob struct {
	cancel       context.CancelFunc
	done         chan struct{}
	bytes, total int64
	moved        time.Time // when it started or its bytes last grew
}

// startLocked starts a sync in the background (h.mu held).
func (h *Helper) startLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	j := &syncJob{cancel: cancel, done: make(chan struct{}), moved: now()}
	h.job, h.lastRun = j, now()
	log.Printf("truckersmp: syncing the mod's files")
	go func() {
		defer close(j.done)
		defer cancel()
		err := h.runSync(ctx, func(done, total int64) {
			h.mu.Lock()
			if done > j.bytes {
				j.moved = now()
			}
			j.bytes, j.total = done, total
			h.mu.Unlock()
		})
		h.mu.Lock()
		h.job, h.seenAt = nil, time.Time{}
		h.lastErr = ""
		if err != nil {
			h.lastErr = err.Error()
		}
		h.mu.Unlock()
		if err != nil {
			log.Printf("truckersmp: sync: %v", err)
		} else {
			log.Printf("truckersmp: the mod's files are up to date")
		}
	}()
}

// stopSync ends a sync that runs and waits for it, at most wait.
func (h *Helper) stopSync(wait time.Duration) {
	h.mu.Lock()
	j := h.job
	h.mu.Unlock()
	if j == nil {
		return
	}
	j.cancel()
	select {
	case <-j.done:
	case <-time.After(wait):
	}
}

// Busy keeps the PC awake while a sync's bytes arrive (none for
// busyStall: a stalled download lets it idle).
func (h *Helper) Busy() (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if j := h.job; j != nil && now().Sub(j.moved) < busyStall {
		return true, busyReason
	}
	return false, ""
}

// runSyncAsGamer runs `vos ext truckersmp sync` as the gaming user, as
// sysd.AsGamer does, reading its progress lines as they come.
func runSyncAsGamer(ctx context.Context, progress func(done, total int64)) error {
	cmd := exec.CommandContext(ctx, "runuser", "-u", config.GamerUser, "--", "env",
		"XDG_RUNTIME_DIR="+config.GamerRuntimeDir, "HOME="+config.GamerHome, vosBin, "ext", ID, "sync")
	// Its own process group, all of it killed when ctx ends (runuser
	// waits for the command rather than replacing itself).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	var stderr tailBuffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	readProgress(out, progress)
	err = cmd.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == exitSyncRunning:
		return nil // someone else's sync does the work
	}
	return fmt.Errorf("%w: %s", err, lastLine(stderr.String()))
}

// readProgress passes on each {"bytes","total"} line; anything else is
// skipped.
func readProgress(r io.Reader, progress func(done, total int64)) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var p struct {
			Bytes int64 `json:"bytes"`
			Total int64 `json:"total"`
		}
		if json.Unmarshal(sc.Bytes(), &p) == nil && p.Bytes >= 0 && p.Total >= 0 {
			progress(p.Bytes, p.Total)
		}
	}
	io.Copy(io.Discard, r)
}

// tailBuffer keeps the last 4 KiB written to it.
type tailBuffer struct{ b bytes.Buffer }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b.Write(p)
	if t.b.Len() > 4096 {
		keep := append([]byte(nil), t.b.Bytes()[t.b.Len()-4096:]...)
		t.b.Reset()
		t.b.Write(keep)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return t.b.String() }

// installedAndMounted reports whether this boot mounted the extension and
// its helper's Install finished (settings/truckersmp.installed): only
// then does vosd keep the mod's files up to date.
func installedAndMounted() bool {
	rep, err := store.LoadBootReport()
	if err != nil || !rep.IsMounted(ID) {
		return false
	}
	_, err = os.Lstat(filepath.Join(config.ExtSettingsDir(), ID+".installed"))
	return err == nil
}
