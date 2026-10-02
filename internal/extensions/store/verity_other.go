//go:build !linux

package store

import "os"

// Off Linux (the dev Mac) nothing can be sealed; tests fake these.

func sysEnableVerity(f *os.File) error            { return ErrUnsupported }
func sysMeasureVerity(f *os.File) (string, error) { return "", ErrUnsupported }
func sysVerityAttr(f *os.File) (bool, error)      { return false, ErrUnsupported }
