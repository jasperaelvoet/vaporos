//go:build linux

package drm

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// WatchHotplug listens for kernel DRM uevents (connector plugged or
// unplugged, EDID changed, card added) and signals the returned channel,
// coalescing bursts. The channel closes when ctx ends.
func WatchHotplug(ctx context.Context) (<-chan struct{}, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return nil, err
	}
	// Group 1 is the kernel's own broadcast; udevd re-broadcasts on group 2.
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	// A non-blocking fd wrapped in an *os.File goes through the runtime
	// poller, so closing it wakes the blocked Read below.
	f := os.NewFile(uintptr(fd), "uevent")
	ch := make(chan struct{}, 1)
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	go func() {
		defer close(ch)
		buf := make([]byte, 16<<10)
		for {
			n, err := f.Read(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// ENOBUFS: the kernel dropped events because we were slow.
				// Treat it as "something changed" and keep going.
				if errors.Is(err, unix.ENOBUFS) {
					notify(ch)
					continue
				}
				return
			}
			if u, ok := ParseUevent(buf[:n]); ok && u.IsDRM() {
				notify(ch)
			}
		}
	}()
	return ch, nil
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
