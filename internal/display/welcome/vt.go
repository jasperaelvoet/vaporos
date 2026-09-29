package welcome

// vt is the virtual terminal the welcome screen sits on (tty1): the few
// console settings it changes while it owns the display.
type vt interface {
	KeyboardMode() (int, error)
	SetKeyboardMode(mode int) error
	// NoEcho turns off echo (and line editing) on the tty.
	NoEcho() error
	// FlushInput discards input the tty received but nobody read.
	FlushInput() error
	// Graphics puts the VT in KD_GRAPHICS: the kernel console stops drawing.
	Graphics() error
	// LockSwitch locks (or unlocks) VT switching, Alt+Fn and chvt alike.
	LockSwitch(lock bool) error
	Close() error
}

// kOff is K_OFF (linux/kd.h): the VT keyboard produces nothing.
const kOff = 0x04

// claimVT makes the welcome screen the only thing the VT shows or hears,
// and returns the function that gives the keyboard back.
//
//   - The keyboard is off (K_OFF), so keystrokes never reach tty1: n_tty
//     would otherwise echo them into the VT's text buffer, and the kernel
//     console paints that buffer on every monitor as soon as the VT leaves
//     graphics mode. Echo is off and pending input is flushed as well, for
//     keys that arrived before K_OFF.
//   - VT switching is locked, so Alt+Fn cannot show another (text) VT over
//     the welcome screen.
//   - The VT is put in KD_GRAPHICS and left there on exit. Going back to
//     KD_TEXT makes fbcon take the screens over with a forced modeset that
//     ignores DRM master; nothing in VaporOS needs text mode on tty1
//     (gamescope and seatd do not use the VT, and the console is ttyS0).
//
// On exit the input is flushed again, the previous keyboard mode comes back
// and VT switching is unlocked; the tty is closed after that.
func claimVT(v vt, name string, opts *Options) func() {
	prev, err := v.KeyboardMode()
	havePrev := err == nil
	if err != nil {
		opts.logf("vos welcome: keyboard mode of %s: %v", name, err)
	}
	if err := v.SetKeyboardMode(kOff); err != nil {
		opts.logf("vos welcome: K_OFF on %s: %v", name, err)
	}
	if err := v.NoEcho(); err != nil {
		opts.logf("vos welcome: no echo on %s: %v", name, err)
	}
	if err := v.FlushInput(); err != nil {
		opts.logf("vos welcome: flushing %s: %v", name, err)
	}
	if err := v.Graphics(); err != nil {
		opts.logf("vos welcome: KD_GRAPHICS on %s: %v", name, err)
	}
	if err := v.LockSwitch(true); err != nil {
		opts.logf("vos welcome: locking VT switching: %v", err)
	}
	return func() {
		if err := v.FlushInput(); err != nil {
			opts.logf("vos welcome: flushing %s: %v", name, err)
		}
		if havePrev {
			if err := v.SetKeyboardMode(prev); err != nil {
				opts.logf("vos welcome: restoring the keyboard of %s: %v", name, err)
			}
		}
		if err := v.LockSwitch(false); err != nil {
			opts.logf("vos welcome: unlocking VT switching: %v", err)
		}
		v.Close()
	}
}
