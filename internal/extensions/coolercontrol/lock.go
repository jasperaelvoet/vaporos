package coolercontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockPoll is how often lockArea tries a held lock again.
const lockPoll = 20 * time.Millisecond

func lockPath(d dirs) string { return filepath.Join(d.area, "vaporos.lock") }

// lockArea takes the data area's lock (flock on vaporos.lock), waiting
// until ctx ends. prepare, in the unit's sandbox, and PasswordChanged, in
// vosd, each hold it while they read and write .passwd and vaporos.json,
// so neither puts back what the other just wrote. The data area is the
// one place both can write. The returned function releases it.
func lockArea(ctx context.Context, d dirs) (func(), error) {
	f, err := os.OpenFile(lockPath(d), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
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
