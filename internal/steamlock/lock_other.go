//go:build !unix

package steamlock

import (
	"errors"
	"os"
)

func tryLock(*os.File) (bool, error) {
	return false, errors.New("steamlock: not supported on this operating system")
}
