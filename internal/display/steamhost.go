package display

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// steamPipeRel is Steam's command pipe, relative to the gaming user's home.
const steamPipeRel = ".steam/steam.pipe"

// steamEnvKeys are what `steam -shutdown` takes from the running Steam's
// environment, as `vos session launch` does for a URL: how to reach
// gamescope's X and Wayland servers and the session bus. sysd.AsGamer
// sets XDG_RUNTIME_DIR and HOME itself.
var steamEnvKeys = []string{"DISPLAY", "WAYLAND_DISPLAY", "GAMESCOPE_WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS"}

func (h *realHost) SteamPID() int {
	return gamerProbe().SteamClient(filepath.Join(config.GamerHome, steamPipeRel))
}

func (h *realHost) ShutdownSteam(ctx context.Context, pid int) error {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	env := gamerProbe().Environ(pid, steamEnvKeys...)
	args := make([]string, 0, len(env)+2)
	for _, k := range steamEnvKeys {
		if v, ok := env[k]; ok {
			args = append(args, k+"="+v)
		}
	}
	_, err := sysd.AsGamer(ctx, "env", append(args, "steam", "-shutdown")...)
	return err
}

func (h *realHost) UnitStarted(ctx context.Context, unit string, user bool) (job, main time.Time) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	args := []string{"show", "--timestamp=us+utc", "-p", "InactiveExitTimestamp", "-p", "ExecMainStartTimestamp", unit}
	if user {
		args = append([]string{"--user", "-M", config.GamerUser + "@"}, args...)
	}
	out, err := sysd.Run(ctx, "systemctl", args...)
	if err != nil {
		return time.Time{}, time.Time{}
	}
	return parseUnitTimes(out)
}

// unitTimeLayout is how `systemctl show --timestamp=us+utc` prints a time.
const unitTimeLayout = "Mon 2006-01-02 15:04:05.000000 MST"

// parseUnitTimes reads the InactiveExitTimestamp (the start job) and
// ExecMainStartTimestamp lines of `systemctl show`; one that is empty,
// "n/a" or malformed is the zero time.
func parseUnitTimes(out string) (job, main time.Time) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		t, err := time.Parse(unitTimeLayout, v)
		if err != nil {
			continue
		}
		switch k {
		case "InactiveExitTimestamp":
			job = t
		case "ExecMainStartTimestamp":
			main = t
		}
	}
	return job, main
}
