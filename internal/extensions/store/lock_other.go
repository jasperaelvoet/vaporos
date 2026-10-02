//go:build !(linux || darwin)

package store

import "os"

// Without flock(2) the lock is a no-op; VaporOS only runs on Linux.

func tryLock(f *os.File) error { return nil }

func unlockFile(f *os.File) {}
