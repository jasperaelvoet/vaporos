package steamprep

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockName is the Steam lock in the gaming user's runtime directory:
// prepare and vosd's library registration hold flock(LOCK_EX) on it while
// they edit Steam's files.
const LockName = "vos-steam.lock"

const lockPoll = 50 * time.Millisecond

// lock takes the Steam lock in runtimeDir, waiting until ctx ends.
// Closing the descriptor, or the process ending, releases it.
func lock(ctx context.Context, runtimeDir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(runtimeDir, LockName),
		os.O_RDONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		ok, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if ok {
			return func() { f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
