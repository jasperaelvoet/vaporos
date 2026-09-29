package storage

import (
	"fmt"
	"strings"
)

// unitNameMax is systemd's UNIT_NAME_MAX minus the terminating NUL.
const unitNameMax = 255

// EscapePath returns what `systemd-escape --path p` prints: the unit-name
// form of an absolute path, as systemd derives mount unit names from
// Where=. The rules (systemd's unit_name_path_escape) are:
//   - collapse repeated slashes and drop "." components and a trailing slash;
//   - reject ".." components (systemd refuses non-normalized paths);
//   - "/" becomes "-";
//   - strip the leading slash, then map "/" to "-", keep [A-Za-z0-9:_.],
//     and write every other byte (including "-" and "\") as \xNN in
//     lowercase hex; a leading "." is escaped too, so no unit starts with one.
func EscapePath(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("escape %q: not an absolute path", p)
	}
	var parts []string
	for _, c := range strings.Split(p, "/") {
		switch c {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("escape %q: path is not normalized", p)
		}
		parts = append(parts, c)
	}
	if len(parts) == 0 {
		return "-", nil
	}
	return escapeString(strings.Join(parts, "/")), nil
}

func escapeString(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c == '.' && i == 0:
			b.WriteString(`\x2e`)
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '.':
			b.WriteByte(c)
		default:
			b.WriteString(`\x`)
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

// MountUnitName returns the .mount unit systemd uses for mountpoint.
func MountUnitName(mountpoint string) (string, error) {
	e, err := EscapePath(mountpoint)
	if err != nil {
		return "", err
	}
	name := e + ".mount"
	if len(name) > unitNameMax {
		return "", fmt.Errorf("mount unit name for %q is too long", mountpoint)
	}
	return name, nil
}

// DeviceUnitName returns the .device unit for a device node path such as
// /dev/disk/by-uuid/<uuid>.
func DeviceUnitName(dev string) (string, error) {
	e, err := EscapePath(dev)
	if err != nil {
		return "", err
	}
	name := e + ".device"
	if len(name) > unitNameMax {
		return "", fmt.Errorf("device unit name for %q is too long", dev)
	}
	return name, nil
}
