package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// lockPoll is how often Lock retries a held lock.
const lockPoll = 20 * time.Millisecond

// errLockHeld means another holder has the lock right now.
var errLockHeld = errors.New("extension store lock is held")

// Lock takes /run/vos/ext.lock (flock), retrying until ctx ends. The kernel
// drops it when the process dies, so a crash never wedges the store. The
// returned function releases it.
func Lock(ctx context.Context) (unlock func(), err error) {
	path := config.ExtLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := tryLock(f)
		if err == nil {
			var once sync.Once
			return func() { once.Do(func() { unlockFile(f); f.Close() }) }, nil
		}
		if !errors.Is(err, errLockHeld) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
