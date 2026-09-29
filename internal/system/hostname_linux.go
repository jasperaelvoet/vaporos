//go:build linux

package system

import "syscall"

// setKernelHostname sets the running kernel's hostname, the fallback when
// hostnamectl is missing or systemd-hostnamed is unavailable.
func setKernelHostname(name string) error { return syscall.Sethostname([]byte(name)) }
