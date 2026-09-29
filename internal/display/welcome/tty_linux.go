//go:build linux

package welcome

import (
	"os"

	"golang.org/x/sys/unix"
)

// platformSupported: Run drives real screens only on Linux.
const platformSupported = true

// VT console modes (linux/kd.h).
const (
	kdSetMode  = 0x4b3a
	kdText     = 0x00
	kdGraphics = 0x01
)

// setGraphics puts the VT in KD_GRAPHICS so the kernel console never
// draws over (or restores itself onto) our framebuffers, and returns the
// function restoring KD_TEXT.
func setGraphics(tty string, opts *Options) func() {
	if tty == "" {
		return func() {}
	}
	f, err := os.OpenFile(tty, os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		opts.logf("vos welcome: %s: %v", tty, err)
		return func() {}
	}
	if err := unix.IoctlSetInt(int(f.Fd()), kdSetMode, kdGraphics); err != nil {
		opts.logf("vos welcome: KD_GRAPHICS on %s: %v", tty, err)
	}
	return func() {
		unix.IoctlSetInt(int(f.Fd()), kdSetMode, kdText)
		f.Close()
	}
}
