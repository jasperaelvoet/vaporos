package welcome

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

type fakeVT struct {
	calls  []string
	kbMode int
	getErr error
}

func (v *fakeVT) KeyboardMode() (int, error) {
	v.calls = append(v.calls, "get-kb")
	return v.kbMode, v.getErr
}
func (v *fakeVT) SetKeyboardMode(m int) error {
	v.calls = append(v.calls, fmt.Sprintf("kb=%d", m))
	v.kbMode = m
	return nil
}
func (v *fakeVT) NoEcho() error     { v.calls = append(v.calls, "no-echo"); return nil }
func (v *fakeVT) FlushInput() error { v.calls = append(v.calls, "flush"); return nil }
func (v *fakeVT) Graphics() error   { v.calls = append(v.calls, "graphics"); return nil }
func (v *fakeVT) LockSwitch(lock bool) error {
	v.calls = append(v.calls, fmt.Sprintf("lock=%v", lock))
	return nil
}
func (v *fakeVT) Close() error { v.calls = append(v.calls, "close"); return nil }

// TestClaimVT: while the welcome runs, keystrokes never reach tty1 and
// Alt+Fn cannot switch VTs; on exit the keyboard comes back (after a
// flush) but the VT stays in graphics mode, so the kernel console never
// paints its text buffer on the monitors.
func TestClaimVT(t *testing.T) {
	const kUnicode = 0x03
	v := &fakeVT{kbMode: kUnicode}
	release := claimVT(v, "/dev/tty1", &Options{})
	want := []string{"get-kb", "kb=4", "no-echo", "flush", "graphics", "lock=true"}
	if !slices.Equal(v.calls, want) {
		t.Errorf("claim = %v, want %v", v.calls, want)
	}
	v.calls = nil
	release()
	want = []string{"flush", "kb=3", "lock=false", "close"}
	if !slices.Equal(v.calls, want) {
		t.Errorf("release = %v, want %v", v.calls, want)
	}
	if v.kbMode != kUnicode {
		t.Errorf("keyboard mode = %d", v.kbMode)
	}

	// Without the old mode, the keyboard stays off rather than guessing.
	v = &fakeVT{getErr: errors.New("ENOTTY")}
	var logged []string
	release = claimVT(v, "/dev/tty1", &Options{Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }})
	release()
	if v.kbMode != kOff || slices.Contains(v.calls, "kb=0") || len(logged) != 1 {
		t.Errorf("calls %v, log %v", v.calls, logged)
	}
}
