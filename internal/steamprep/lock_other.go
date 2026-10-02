//go:build !unix

package steamprep

import (
	"errors"
	"os"
)

func tryLock(*os.File) (bool, error) {
	return false, errors.New("the Steam lock needs flock")
}
