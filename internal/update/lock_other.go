//go:build !(linux || darwin)

package update

import "errors"

var errLocked = errors.New("locked by another process")

// fileLock is a no-op where flock(2) is unavailable; VaporOS only runs on
// Linux, and the dev Mac only runs the tests.
type fileLock struct{}

func lockFile(path string, wait bool) (*fileLock, error) { return &fileLock{}, nil }

func (l *fileLock) Unlock() {}
