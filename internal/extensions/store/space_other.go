//go:build !linux

package store

// Off Linux (the dev Mac) the free space is not known; tests fake it.
func statFree(string) (int64, error) { return -1, nil }
