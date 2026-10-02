package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// Where the CLI writes; tests capture it.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// errUsage means the command line was wrong (exit status 2).
var errUsage = errors.New("usage")

// CLI runs update, rollback, status, health, sign, keygen or index.
func CLI(cmd string, args []string) int {
	var err error
	switch cmd {
	case "update":
		err = cliUpdate(args)
	case "rollback":
		err = cliRollback(args)
	case "status":
		err = cliStatus(args)
	case "health":
		return cliHealth(args)
	case "sign":
		err = cliSign(args)
	case "keygen":
		err = cliKeygen(args)
	case "index":
		err = cliIndex(args)
	default:
		fmt.Fprintf(stderr, "vos: unknown command %q\n", cmd)
		return 2
	}
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 2
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	return 1
}

func newFlags(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet("vos "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: vos %s %s\n", name, usage)
		fs.PrintDefaults()
	}
	return fs
}

// parseArgs parses flags anywhere among the arguments (the flag package
// stops at the first positional one) and returns the positional ones.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, errUsage
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// printer writes the bash vos's "==> step" / "    detail" lines.
type printer struct{ w io.Writer }

func (p printer) say(format string, a ...any)  { fmt.Fprintf(p.w, "==> "+format+"\n", a...) }
func (p printer) info(format string, a ...any) { fmt.Fprintf(p.w, "    "+format+"\n", a...) }

func cliUpdate(args []string) error {
	fs := newFlags("update", "[--from SRC] [--channel C] [--force] [--allow-downgrade] [--check] [--stage-only | --reboot] [--json-progress]")
	from := fs.String("from", "", "source: oci://registry/repo[:tag], http(s)://host/dir/, a directory, or live (default: config.json)")
	channel := fs.String("channel", "", "channel, i.e. the OCI tag (default: config.json, then the image's own)")
	force := fs.Bool("force", false, "also accept the running version, older versions and versions that failed before")
	allowDowngrade := fs.Bool("allow-downgrade", false, "accept a version older than the running one")
	check := fs.Bool("check", false, "only report whether an update is available")
	stageOnly := fs.Bool("stage-only", false, "stage the update for the next boot without rebooting (the default)")
	reboot := fs.Bool("reboot", false, "reboot into the update once it is staged")
	jsonProgress := fs.Bool("json-progress", false, "print progress as JSON lines on stdout")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 || (*stageOnly && *reboot) {
		fs.Usage()
		return errUsage
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "warning: %v\n", err)
	}
	opts := Options{From: *from, Channel: *channel, Force: *force, AllowDowngrade: *allowDowngrade}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out := printer{stdout}
	if *jsonProgress {
		out = printer{stderr} // stdout carries the JSON
	}
	if src, err := sourceFor(cfg, opts); err == nil {
		out.say("Checking %s", src)
	}
	if *check {
		return cliCheck(ctx, cfg, opts, out, *jsonProgress)
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root (sudo vos update)")
	}

	img := bootedImage()
	prog := newCLIProgress(*jsonProgress)
	opts.Progress = prog.show
	opts.Accepted = func(m *manifest.Manifest, slot string) {
		out.info("running: %s (slot %s)", img.Version, config.BootedSlot())
		out.info("new:     %s -> slot %s", m.Version, slot)
	}
	res, err := Stage(ctx, cfg, opts)
	prog.clear()
	switch {
	case errors.Is(err, ErrUpToDate):
		out.say("Already up to date (%s).", img.Version)
		return nil
	case errors.Is(err, ErrHeld):
		out.say("Not installing %s: %v.", res.Manifest.Version, err)
		return nil
	case errors.Is(err, ErrAlreadyStaged):
		out.say("VaporOS %s is already staged in slot %s.", res.Manifest.Version, res.Slot)
	case err != nil:
		if *jsonProgress {
			prog.emit(Progress{Phase: "error", Error: err.Error()})
		}
		return err
	default:
		out.say("VaporOS %s is ready in slot %s.", res.Manifest.Version, res.Slot)
	}
	if *reboot {
		out.info("Rebooting.")
		return sysd.Reboot(ctx)
	}
	out.info("Reboot to switch. If it fails to start three times, %s comes back on its own.", img.Version)
	return nil
}

func cliCheck(ctx context.Context, cfg *config.Config, opts Options, out printer, asJSON bool) error {
	res, err := Check(ctx, cfg, opts)
	if err != nil {
		return err
	}
	if asJSON {
		reason := ""
		if res.Reason != nil {
			reason = res.Reason.Error()
		}
		return json.NewEncoder(stdout).Encode(map[string]any{
			"booted": bootedImage().Version, "offered": res.Manifest.Version,
			"available": res.Available, "reason": reason,
		})
	}
	if res.Available != nil {
		out.say("Update available: %s (%s).", res.Available.Version, humanBytes(res.Available.Size))
	} else {
		out.say("No update: %v.", res.Reason)
	}
	return nil
}

// cliProgress shows Progress: JSON lines, a line rewritten in place on a
// terminal, or a line per phase and per 10% in a log.
type cliProgress struct {
	json  bool
	tty   bool
	shown bool // a rewritable line is on screen
	phase string
	pct   int
}

func newCLIProgress(asJSON bool) *cliProgress {
	c := &cliProgress{json: asJSON}
	if f, ok := stderr.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			c.tty = true
		}
	}
	return c
}

var phaseLabels = map[string]string{
	"check": "checking", "download": "downloading", "write": "writing",
	"verify": "verifying", "install": "installing", "done": "done",
}

func (c *cliProgress) show(p Progress) {
	if c.json {
		c.emit(p)
		return
	}
	label := phaseLabels[p.Phase]
	if label == "" {
		label = p.Phase
	}
	size := ""
	if p.Total > 1 {
		size = humanBytes(p.Bytes) + " / " + humanBytes(p.Total)
	}
	if c.tty {
		fmt.Fprintf(stderr, "\r\033[K    %-12s %3d%%  %s", label, p.Percent, size)
		c.shown = true
		return
	}
	if p.Phase != c.phase || p.Percent/10 != c.pct/10 {
		fmt.Fprintf(stderr, "    %-12s %3d%%  %s\n", label, p.Percent, size)
		c.phase, c.pct = p.Phase, p.Percent
	}
}

func (c *cliProgress) clear() {
	if c.shown {
		fmt.Fprint(stderr, "\r\033[K")
		c.shown = false
	}
}

func (c *cliProgress) emit(p Progress) { json.NewEncoder(stdout).Encode(p) }

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func cliRollback(args []string) error {
	fs := newFlags("rollback", "[--force]")
	force := fs.Bool("force", false, "roll back even to a version that failed to start before")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fs.Usage()
		return errUsage
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root (sudo vos rollback)")
	}
	v, err := Rollback(*force)
	if err != nil {
		return err
	}
	printer{stdout}.say("Next boot uses slot %s (%s). Reboot to roll back.", config.OtherSlot(config.BootedSlot()), v)
	return nil
}

func cliStatus(args []string) error {
	fs := newFlags("status", "[--json]")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fs.Usage()
		return errUsage
	}
	s := GetStatus()
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	printStatus(stdout, s)
	return nil
}

// printStatus keeps the bash vos's layout ("slot a:  <ver>  <- running"),
// which scripts/dev.sh matches.
func printStatus(w io.Writer, s *Status) {
	fmt.Fprintf(w, "VaporOS %s\n", s.Version)
	p := printer{w}
	if s.Mode == "live" {
		p.info("mode:    live (nothing is saved)")
		return
	}
	if s.Channel != "" {
		p.info("channel: %s", s.Channel)
	}
	booted := s.BootedSlot
	if booted == "" {
		booted = "?"
	}
	p.info("booted:  slot %s", booted)
	if s.Slots == nil {
		p.info("(%s; run as root for slot details)", s.ESPError)
	}
	for _, slot := range []string{"a", "b"} {
		if s.Slots == nil {
			break
		}
		ss := s.Slots[slot]
		if ss == nil {
			p.info("slot %s:  empty", slot)
			continue
		}
		line := fmt.Sprintf("slot %s:  %s", slot, ss.Version)
		if ss.Running {
			line += "  <- running"
		}
		switch {
		case ss.Counting && ss.Bootable:
			line += fmt.Sprintf("  (on trial: %d tries left)", ss.TriesLeft)
		case ss.Counting:
			line += "  (out of tries: boots only as a last resort)"
		}
		p.info("%s", line)
	}
	if s.Staged != nil {
		p.info("staged:  %s in slot %s (reboot to switch)", s.Staged.Version, s.Staged.Slot)
	}
	if s.Available != nil {
		p.info("update:  %s available", s.Available.Version)
	}
	if s.Held != nil {
		p.info("held:    %s and older (rolled back from; not reinstalled automatically)", s.Held.Version)
	}
	if len(s.Failed) > 0 {
		p.info("failed:  %s", strings.Join(s.Failed, " "))
	}
	if s.LastError != "" {
		p.info("error:   %s", s.LastError)
	}
}

func cliHealth(args []string) int {
	fs := newFlags("health", "")
	if pos, err := parseArgs(fs, args); err != nil || len(pos) > 0 {
		return 2
	}
	logf := func(format string, a ...any) { fmt.Fprintf(stdout, format+"\n", a...) }
	if config.IsLive() {
		logf("health: live system, nothing to check")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	return runHealth(ctx, systemHealthEnv(), logf)
}

func cliIndex(args []string) error {
	fs := newFlags("index", "IMAGE")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return errUsage
	}
	out, err := IndexFile(pos[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\n", out)
	return nil
}

func cliSign(args []string) error {
	fs := newFlags("sign", "--key FILE|env:VAR MANIFEST")
	key := fs.String("key", "", "the private key: a file, or env:VAR")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if *key == "" || len(pos) != 1 {
		fs.Usage()
		return errUsage
	}
	file := pos[0]
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	// Never sign what `vos update` would reject anyway.
	if _, err := manifest.Parse(b); err != nil {
		return err
	}
	priv, err := manifest.LoadPrivateKey(*key)
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(file+".sig", manifest.Sign(b, priv), 0o644); err != nil {
		return err
	}
	pub := strings.TrimSpace(manifest.EncodePublicKey(priv.Public().(ed25519.PublicKey)))
	fmt.Fprintf(stdout, "signed %s (key %s)\n", file, pub)
	return nil
}

func cliKeygen(args []string) error {
	fs := newFlags("keygen", "--out PREFIX")
	out := fs.String("out", "", "write PREFIX.key (private, mode 0600) and PREFIX.pub")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if *out == "" || len(pos) > 0 {
		fs.Usage()
		return errUsage
	}
	keyPath, pubPath := *out+".key", *out+".pub"
	// Never overwrite a key: a lost release key cannot be recovered.
	for _, p := range []string{keyPath, pubPath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s already exists", p)
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := writeNew(keyPath, manifest.EncodePrivateKey(priv), 0o600); err != nil {
		return err
	}
	if err := writeNew(pubPath, manifest.EncodePublicKey(pub), 0o644); err != nil {
		os.Remove(keyPath)
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (keep it secret) and %s\npublic key: %s", keyPath, pubPath, manifest.EncodePublicKey(pub))
	return nil
}

// writeNew creates path, failing if it exists.
func writeNew(path, content string, perm fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}
