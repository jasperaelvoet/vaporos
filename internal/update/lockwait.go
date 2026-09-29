package update

import (
	"context"
	"errors"
	"time"
)

var (
	// lockPatience is how long Stage and Rollback retry the update lock.
	// updateLocked probes it by taking it for an instant, and a probe must
	// not read as another update; a real one holds it for minutes.
	lockPatience = 200 * time.Millisecond
	// probePatience rides out another probe in updateLocked, so two probes
	// never both report an update.
	probePatience = 20 * time.Millisecond
	lockRetry     = 5 * time.Millisecond
)

// takeUpdateLock takes the update lock, retrying for patience (0: until ctx
// ends). It fails with ErrBusy once patience runs out.
func takeUpdateLock(ctx context.Context, patience time.Duration) (*fileLock, error) {
	var deadline <-chan time.Time
	if patience > 0 {
		t := time.NewTimer(patience)
		defer t.Stop()
		deadline = t.C
	}
	for {
		l, err := lockFile(updateLockPath(), false)
		if !errors.Is(err, errLocked) {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, ErrBusy
		case <-time.After(lockRetry):
		}
	}
}
