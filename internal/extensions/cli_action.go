package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
)

const actionUsage = "usage: vos ext action ID NAME [--args JSON]"

// actionExitNoAction is `vos ext action`'s exit status for an action the
// extension does not have; vosd answers 404 for it.
const actionExitNoAction = 3

// vosBinary is how vosd runs `vos ext action` as vapor.
const vosBinary = "/usr/bin/vos"

func init() {
	register("action", "ID NAME [--args JSON]: runs one of a running extension's actions here (vosd runs it as vapor)",
		func(args []string) int { return actionCmd(args, os.Stderr) })
}

// actionCmd is `vos ext action`: exit 0 when the action ran, 1 when it
// failed (why, on the last line of stderr), 2 on bad arguments and
// actionExitNoAction for an action the extension does not have.
func actionCmd(args []string, stderr io.Writer) int {
	id, name, raw, err := parseAction(args)
	if err != nil {
		fmt.Fprintln(stderr, actionUsage)
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	err = runAction(ctx, id, name, raw)
	switch {
	case errors.Is(err, ErrNoAction):
		fmt.Fprintln(stderr, err)
		return actionExitNoAction
	case err != nil:
		fmt.Fprintln(stderr, oneLine(err.Error(), maxActionError))
		return 1
	}
	return 0
}

// parseAction reads ID NAME and --args JSON (or --args=JSON), which must
// be an object.
func parseAction(args []string) (id, name string, raw json.RawMessage, err error) {
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--args":
			if i+1 == len(args) {
				return "", "", nil, errors.New("--args needs a value")
			}
			i++
			raw = json.RawMessage(args[i])
		case strings.HasPrefix(a, "--args="):
			raw = json.RawMessage(strings.TrimPrefix(a, "--args="))
		case strings.HasPrefix(a, "-"):
			return "", "", nil, fmt.Errorf("unknown option %s", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return "", "", nil, errors.New("give an extension id and an action name")
	}
	if !manifest.ValidExtensionID(pos[0]) {
		return "", "", nil, fmt.Errorf("%q is not an extension id", pos[0])
	}
	if raw != nil && (!json.Valid(raw) || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{")) {
		return "", "", nil, errors.New("--args must be a JSON object")
	}
	return pos[0], pos[1], raw, nil
}

// runAction runs action name of the mounted extension id through its
// helper, in this process, with its shipped descriptor and settings.
func runAction(ctx context.Context, id, name string, args json.RawMessage) error {
	d, err := Shipped(id)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(d.Actions, func(a descriptor.Action) bool { return a.Name == name })
	if i < 0 {
		return ErrNoAction
	}
	if d.Actions[i].RunAs == "root" && os.Geteuid() != 0 {
		return fmt.Errorf("%s runs as root", d.Actions[i].Label)
	}
	rep, err := store.LoadBootReport()
	if err != nil {
		return err
	}
	if !rep.IsMounted(id) {
		return fmt.Errorf("%s is not running", d.Name)
	}
	return HelperFor(id).Action(ctx, newExt(id, d), name, args)
}

// actionAsGamer runs an action through `vos ext action` as vapor: its
// helper works in vapor's trees as vapor. The action's own words are the
// last line the command printed.
func (s *Service) actionAsGamer(ctx context.Context, id, name string, args json.RawMessage) error {
	cmd := []string{"ext", "action", id, name}
	if len(args) > 0 {
		cmd = append(cmd, "--args", string(args))
	}
	out, err := s.cc.asGamer(ctx, vosBinary, cmd...)
	if err == nil {
		return nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && exit.ExitCode() == actionExitNoAction {
		return ErrNoAction
	}
	log.Printf("extensions: %s: %s as vapor: %v", id, name, err)
	if ctx.Err() != nil {
		return errors.New("it did not finish in time")
	}
	if line := lastLine(out); line != "" {
		return errors.New(line)
	}
	return err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// maxActionError bounds the line vosd shows of a failed action.
const maxActionError = 1000
