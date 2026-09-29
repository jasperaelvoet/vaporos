package welcome

import (
	"context"
	"flag"
	"image"
	"image/png"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// Options configure Run.
type Options struct {
	StatePath string // /run/vos/welcome.json
	TTY       string // VT claimed while running, see claimVT ("" = none)
	// VirtualConnector returns the configured virtual connector ("DP-1"),
	// re-read on every rescan; nil or "" means none.
	VirtualConnector func() string
	Logf             func(format string, args ...any)
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// Main is `vos welcome`. With --png it renders the current state to an
// image and exits (handy on the dev Mac); otherwise it drives the screens
// until SIGTERM.
func Main(args []string, statePath string, virtual func() string) int {
	fs := flag.NewFlagSet("vos welcome", flag.ContinueOnError)
	state := fs.String("state", statePath, "welcome state file")
	out := fs.String("png", "", "render to this PNG file instead of the screens")
	size := fs.String("size", "1920x1080", "image size for --png")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logger := log.New(os.Stderr, "", 0)
	if *out != "" {
		m, err := edid.ParseMode(*size)
		if err != nil {
			logger.Printf("vos welcome: %v", err)
			return 2
		}
		st, err := Load(*state)
		if err != nil {
			st = Placeholder
		}
		if err := writePNG(*out, Render(st, m.W, m.H)); err != nil {
			logger.Printf("vos welcome: %v", err)
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	err := Run(ctx, Options{StatePath: *state, TTY: "/dev/tty1", VirtualConnector: virtual, Logf: logger.Printf})
	if err != nil {
		logger.Printf("vos welcome: %v", err)
		return 1
	}
	return 0
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// stateChanged loads the state file and reports whether it differs from cur.
// A missing or unreadable file keeps whatever is shown.
func stateChanged(path string, cur State, have bool) (State, bool) {
	st, err := Load(path)
	if err != nil {
		if !have {
			return Placeholder, true
		}
		return cur, false
	}
	if have && st == cur {
		return cur, false
	}
	return st, true
}
