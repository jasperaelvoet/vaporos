//go:build !linux

package system

import "errors"

// setKernelHostname is Linux-only; elsewhere (development on macOS) the
// file write alone has to do.
func setKernelHostname(string) error {
	return errors.New("setting the kernel hostname is only supported on Linux")
}
