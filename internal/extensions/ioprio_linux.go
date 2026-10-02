package extensions

import "golang.org/x/sys/unix"

// From linux/ioprio.h; x/sys/unix has the syscall but not these.
const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
)

// idleIO puts the calling thread's I/O in the idle class: the disk serves
// it only when nothing else wants it. The caller locks itself to the
// thread.
func idleIO() error {
	_, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, 0, ioprioClassIdle<<ioprioClassShift)
	if errno != 0 {
		return errno
	}
	return nil
}
