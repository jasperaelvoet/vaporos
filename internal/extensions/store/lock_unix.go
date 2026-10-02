//go:build linux || darwin

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLock(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) {
			return errLockHeld
		}
		return err
	}
}

func unlockFile(f *os.File) { unix.Flock(int(f.Fd()), unix.LOCK_UN) }
