// Package steamlock is the lock every writer of the gaming user's Steam
// files holds (docs/CONTRACTS.md "Extensions", Steam): `vos steam prepare`,
// as the gaming user, and vosd's library registration, as root, take
// flock(LOCK_EX) on /run/user/1000/vos-steam.lock while they read and
// rewrite Steam's files, so neither replaces a file the other is editing.
package steamlock

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/gamerfs"
)

// Name is the lock file in the gaming user's runtime directory.
const Name = "vos-steam.lock"

// RuntimeDir is the gaming user's /run/user/<uid> (a variable for tests).
var RuntimeDir = "/run/user/" + strconv.Itoa(config.GamerUID)

// poll is how often Lock tries again while someone else holds the lock.
const poll = 50 * time.Millisecond

// Lock takes the lock, waiting until ctx ends. The file is opened through
// gamerfs, since the gaming user owns the directory, and made empty, the
// gaming user's, mode 0600 when it is missing.
func Lock(ctx context.Context) (unlock func(), err error) {
	uid := -1
	if os.Geteuid() == 0 {
		uid = config.GamerUID
	}
	f, err := gamerfs.OpenOrCreate(RuntimeDir, Name, 0o600, uid, uid)
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
			// Closing the descriptor releases the lock.
			return func() { f.Close() }, nil
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}
