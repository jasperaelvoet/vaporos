//go:build linux || darwin

package update

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// errLocked means another process holds the lock.
var errLocked = errors.New("locked by another process")

// fileLock is an flock(2) on a file under /run/vos. The kernel drops it
// when the process dies, so a crashed update never wedges the next one.
type fileLock struct{ f *os.File }

// lockFile takes an exclusive lock on path. With wait false it fails with
// errLocked instead of blocking.
func lockFile(path string, wait bool) (*fileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) Unlock() {
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
}
