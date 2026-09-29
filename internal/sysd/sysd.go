// Package sysd wraps the few systemd and process operations vosd needs.
// It shells out to systemctl rather than speaking D-Bus: fewer dependencies,
// and every call is easy to reproduce by hand.
package sysd

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// Run executes name with args and returns combined, trimmed output.
func Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
	}
	return s, nil
}

func Systemctl(ctx context.Context, args ...string) error {
	_, err := Run(ctx, "systemctl", args...)
	return err
}

// UserSystemctl runs systemctl against the gaming user's manager.
func UserSystemctl(ctx context.Context, args ...string) error {
	_, err := Run(ctx, "systemctl", append([]string{"--user", "-M", config.GamerUser + "@"}, args...)...)
	return err
}

// IsActive reports whether a (user) unit is active.
func IsActive(ctx context.Context, unit string, user bool) bool {
	args := []string{"is-active", "--quiet", unit}
	if user {
		args = append([]string{"--user", "-M", config.GamerUser + "@"}, args...)
	}
	return exec.CommandContext(ctx, "systemctl", args...).Run() == nil
}

func Reboot(ctx context.Context) error   { return Systemctl(ctx, "reboot") }
func Poweroff(ctx context.Context) error { return Systemctl(ctx, "poweroff") }

// AsGamer runs a command as the gaming user with its runtime dir set.
func AsGamer(ctx context.Context, name string, args ...string) (string, error) {
	uid := strconv.Itoa(config.GamerUID)
	full := append([]string{"-u", config.GamerUser, "--", "env",
		"XDG_RUNTIME_DIR=/run/user/" + uid, "HOME=" + config.GamerHome, name}, args...)
	return Run(ctx, "runuser", full...)
}

// WaitFor polls f every interval until it returns true or timeout passes.
func WaitFor(ctx context.Context, timeout, interval time.Duration, f func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if f() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
}

// LocalIPs returns the machine's global-scope IPv4 addresses (then IPv6),
// skipping loopback, link-local and container bridges.
func LocalIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var v4, v6 []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if strings.HasPrefix(ifc.Name, "docker") || strings.HasPrefix(ifc.Name, "veth") || strings.HasPrefix(ifc.Name, "br-") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLoopback() {
				continue
			}
			if ipn.IP.To4() != nil {
				v4 = append(v4, ipn.IP.String())
			} else {
				v6 = append(v6, ipn.IP.String())
			}
		}
	}
	return append(v4, v6...)
}
