// Package sysd wraps the few systemd and process operations vosd needs.
// It shells out to systemctl rather than speaking D-Bus: fewer dependencies,
// and every call is easy to reproduce by hand.
package sysd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// waitDelay is how long Run still reads a command's output once the
// command has exited or ctx has ended. A descendant it left behind may hold
// the output pipe open for good; without a bound, ctx could not end Run.
const waitDelay = 2 * time.Second

// Run executes name with args and returns combined, trimmed output.
func Run(ctx context.Context, name string, args ...string) (string, error) {
	return run(exec.CommandContext(ctx, name, args...))
}

func run(cmd *exec.Cmd) (string, error) {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		return s, fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, s)
	}
	return s, nil
}

// groupCommand is exec.CommandContext for a command that forks: it gets its
// own process group, and when ctx ends the whole group is killed, not only
// the process we started.
func groupCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd
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

// ActiveState returns a (user) unit's state as `systemctl is-active` prints
// it ("active", "activating", "deactivating", "inactive", "failed", ...),
// or "" when systemctl cannot tell. A unit waiting out RestartSec is
// "activating": not active, yet about to come back by itself.
func ActiveState(ctx context.Context, unit string, user bool) string {
	args := []string{"is-active", unit}
	if user {
		args = append([]string{"--user", "-M", config.GamerUser + "@"}, args...)
	}
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.WaitDelay = waitDelay
	out, _ := cmd.Output() // exits non-zero for every state but active
	return strings.TrimSpace(string(out))
}

func Reboot(ctx context.Context) error   { return Systemctl(ctx, "reboot") }
func Poweroff(ctx context.Context) error { return Systemctl(ctx, "poweroff") }

// AsGamer runs a command as the gaming user with its runtime dir set.
// runuser forks the command and waits for it; a SIGKILL to runuser alone
// would leave the command running (a gamescopectl stuck on a wedged
// gamescope, say) with our output pipe. `runuser -u` keeps the command in
// runuser's process group (no setsid), so the group kill reaches it.
func AsGamer(ctx context.Context, name string, args ...string) (string, error) {
	uid := strconv.Itoa(config.GamerUID)
	full := append([]string{"-u", config.GamerUser, "--", "env",
		"XDG_RUNTIME_DIR=/run/user/" + uid, "HOME=" + config.GamerHome, name}, args...)
	return run(groupCommand(ctx, "runuser", full...))
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
