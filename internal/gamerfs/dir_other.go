//go:build !unix

package gamerfs

import "os"

func readDirNames(string, []string, int) ([]string, error) { return nil, errUnsupported }
func remove(string, []string) error                        { return errUnsupported }
func openOrCreate(string, []string, os.FileMode, int, int) (*os.File, error) {
	return nil, errUnsupported
}
