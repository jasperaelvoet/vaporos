//go:build linux

package welcome

import (
	"os"

	"golang.org/x/sys/unix"
)

// platformSupported: Run drives real screens only on Linux.
const platformSupported = true

// VT ioctls (linux/kd.h, linux/vt.h).
const (
	kdSetMode      = 0x4b3a // KDSETMODE
	kdGraphics     = 0x01   // KD_GRAPHICS
	kdGetKbMode    = 0x4b44 // KDGKBMODE
	kdSetKbMode    = 0x4b45 // KDSKBMODE
	vtLockSwitch   = 0x560b // VT_LOCKSWITCH
	vtUnlockSwitch = 0x560c // VT_UNLOCKSWITCH
)

// setGraphics claims the VT tty (see claimVT) and returns the function
// that lets it go.
func setGraphics(tty string, opts *Options) func() {
	if tty == "" {
		return func() {}
	}
	f, err := os.OpenFile(tty, os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		opts.logf("vos welcome: %s: %v", tty, err)
		return func() {}
	}
	return claimVT(linuxVT{f}, tty, opts)
}

type linuxVT struct{ f *os.File }

func (v linuxVT) fd() int { return int(v.f.Fd()) }

func (v linuxVT) KeyboardMode() (int, error) {
	m, err := unix.IoctlGetUint32(v.fd(), kdGetKbMode)
	return int(m), err
}

func (v linuxVT) SetKeyboardMode(mode int) error { return unix.IoctlSetInt(v.fd(), kdSetKbMode, mode) }

func (v linuxVT) NoEcho() error {
	t, err := unix.IoctlGetTermios(v.fd(), unix.TCGETS)
	if err != nil {
		return err
	}
	t.Lflag &^= unix.ECHO | unix.ECHOE | unix.ECHOK | unix.ECHONL | unix.ECHOCTL | unix.ECHOKE | unix.ICANON
	t.Iflag &^= unix.IXON
	return unix.IoctlSetTermios(v.fd(), unix.TCSETS, t)
}

func (v linuxVT) FlushInput() error { return unix.IoctlSetInt(v.fd(), unix.TCFLSH, unix.TCIFLUSH) }

func (v linuxVT) Graphics() error { return unix.IoctlSetInt(v.fd(), kdSetMode, kdGraphics) }

func (v linuxVT) LockSwitch(lock bool) error {
	req := uint(vtUnlockSwitch)
	if lock {
		req = vtLockSwitch
	}
	return unix.IoctlSetInt(v.fd(), req, 0)
}

func (v linuxVT) Close() error { return v.f.Close() }
