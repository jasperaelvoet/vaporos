//go:build !(linux && (amd64 || arm64))

package update

import "os"

// Fallbacks for building and testing off the target (the dev Mac): slots
// are plain files there, and the ESP is a temp dir.

const exclusiveFlag = 0

func dropCache(f *os.File) {}

// diskFree reports "unknown" (-1): the ESP space check is skipped.
func diskFree(path string) (int64, error) { return -1, nil }
