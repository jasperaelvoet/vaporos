package drm

import (
	"bytes"
	"strings"
)

// Uevent is one kernel uevent: the "action@devpath" header plus its
// KEY=value environment.
type Uevent struct {
	Action  string
	DevPath string
	Env     map[string]string
}

// IsDRM reports whether the event concerns a DRM device: connector hotplug
// ("change" with HOTPLUG=1) and cards appearing or going away.
func (u Uevent) IsDRM() bool { return u.Env["SUBSYSTEM"] == "drm" }

// ParseUevent decodes a NETLINK_KOBJECT_UEVENT datagram from the kernel:
// NUL-separated "action@devpath", then KEY=value pairs. Messages from
// udevd (which start with "libudev") are rejected; we only listen to the
// kernel so hotplug works before or without udev.
func ParseUevent(b []byte) (Uevent, bool) {
	parts := bytes.Split(b, []byte{0})
	if len(parts) == 0 {
		return Uevent{}, false
	}
	action, devpath, ok := strings.Cut(string(parts[0]), "@")
	if !ok || action == "" || devpath == "" {
		return Uevent{}, false
	}
	u := Uevent{Action: action, DevPath: devpath, Env: map[string]string{}}
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(string(p), "="); ok && k != "" {
			u.Env[k] = v
		}
	}
	return u, true
}
