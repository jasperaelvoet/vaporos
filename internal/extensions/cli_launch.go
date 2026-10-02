package extensions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

const launchUsage = "usage: vos ext launch [--app N | --shortcut ID/KEY] [--] CMD [ARGS...]"

func init() {
	register("launch", "[--app N | --shortcut ID/KEY] [--] CMD [ARGS...]: Steam's launch dispatcher; runs CMD",
		func(args []string) int { return launchCmd(args, os.Stderr) })
}

// execve replaces this process with the command; a variable for tests.
var execve = syscall.Exec

// shortcutKeyRe is a descriptor's steam.shortcuts key.
var shortcutKeyRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// launch is a parsed `vos ext launch`.
type launch struct {
	app      uint32 // Steam's app id, 0 when not given
	shortcut string // the extension of --shortcut ID/KEY, "" when not given
	key      string
	argv     []string // CMD and ARGS, untouched
}

// parseLaunch reads the dispatcher's own options up to "--" or the first
// other word, which starts the command: Steam's %command% and the user's
// launch options after it are never read as options.
func parseLaunch(args []string) (launch, error) {
	var l launch
	seen := false
	i := 0
	for ; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		if args[i] == "--" {
			i++
			break
		}
		if name != "--app" && name != "--shortcut" {
			break
		}
		if !hasVal {
			if i+1 == len(args) {
				return l, fmt.Errorf("%s needs a value", name)
			}
			i++
			val = args[i]
		}
		if seen {
			return l, errors.New("--app and --shortcut go once, and not together")
		}
		seen = true
		if name == "--app" {
			n, err := strconv.ParseUint(val, 10, 32)
			if err != nil || n == 0 {
				return l, fmt.Errorf("--app %q is not a Steam app id", val)
			}
			l.app = uint32(n)
			continue
		}
		id, key, ok := strings.Cut(val, "/")
		if !ok || !manifest.ValidExtensionID(id) || !shortcutKeyRe.MatchString(key) {
			return l, fmt.Errorf("--shortcut %q is not ID/KEY", val)
		}
		l.shortcut, l.key = id, key
	}
	l.argv = args[i:]
	if len(l.argv) == 0 {
		return l, errors.New("no command to run")
	}
	return l, nil
}

// launchCmd is `vos ext launch` (docs/CONTRACTS.md "Binary"): it runs
// the launch hooks of what starts, then execs CMD (dispatch.go). A hook
// may wait on the network or the disk, so SIGTERM and SIGINT (Steam
// stopping the game) end its context.
func launchCmd(args []string, stderr io.Writer) int {
	l, err := parseLaunch(args)
	if err != nil {
		fmt.Fprintf(stderr, "vos ext launch: %v\n%s\n", err, launchUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return dispatch(ctx, l, os.Environ(), stderr)
}
