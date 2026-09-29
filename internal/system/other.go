//go:build !unix

package system

import "errors"

// VaporOS is Linux; these keep the package compiling on other systems.

var errUnsupported = errors.New("not supported on this operating system")

func diskUsage(string) (uint64, uint64, error)             { return 0, 0, errUnsupported }
func writeAuthorizedKeys(string, int, int, []string) error { return errUnsupported }
func removeAuthorizedKeys(string) error                    { return errUnsupported }
