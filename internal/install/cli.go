package install

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// listFlag collects a repeated flag (--library UUID --library UUID).
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type cliArgs struct {
	opts Options
	yes  bool
}

// parseCLI parses `vos install` flags. --user is accepted and ignored so
// older scripts keep working: there is no Unix account to create any more.
func parseCLI(args []string, stderr io.Writer) (cliArgs, error) {
	var c cliArgs
	var libs listFlag
	var user string
	fs := flag.NewFlagSet("vos install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.opts.Disk, "disk", "", "target disk (/dev/sda, /dev/nvme0n1, ...)")
	fs.StringVar(&c.opts.Mode, "mode", ModeErase, "erase: wipe the whole disk; repair: keep the data partition")
	fs.StringVar(&c.opts.Hostname, "hostname", "", "machine name (default "+defaultHostname+")")
	fs.StringVar(&c.opts.Password, "password", "", "web admin password (default: set it in the web UI on first boot)")
	fs.StringVar(&c.opts.Timezone, "timezone", "", "tz database name, e.g. Europe/Brussels (default UTC)")
	fs.StringVar(&c.opts.Source, "source", "", "image source: a directory, an http(s) URL or oci://registry/repo (default: the live medium)")
	fs.StringVar(&c.opts.Channel, "channel", "", "channel (tag) for an oci:// source without one (default: the live image's)")
	fs.Var(&libs, "library", "filesystem UUID of a Steam library disk to adopt (repeatable)")
	fs.BoolVar(&c.yes, "yes", false, "do not ask for confirmation (required when not on a terminal)")
	fs.StringVar(&user, "user", "", "ignored; the gaming user is built in and the web admin is 'admin'")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: vos install --disk D [--mode erase|repair] [--hostname H] [--password P]\n"+
			"                   [--timezone Z] [--source SRC [--channel C]] [--library UUID]... [--yes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if user != "" {
		fmt.Fprintln(stderr, "vos install: --user is ignored: the gaming user 'vapor' is built in and the web admin is 'admin'")
	}
	c.opts.Libraries = libs
	return c, nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// CLI implements `vos install --disk D --hostname H --password P
// --timezone Z [--source SRC [--channel C]] [--library UUID]...
// [--mode erase|repair] [--yes]`.
func CLI(args []string) int {
	log.SetFlags(0)
	c, err := parseCLI(args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vos install:", err)
		return 2
	}
	interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	in := bufio.NewReader(os.Stdin)
	if !c.yes && !interactive {
		fmt.Fprintln(os.Stderr, "vos install: refusing to write a disk without confirmation; pass --yes")
		return 2
	}
	if c.opts.Disk == "" {
		if !interactive {
			fmt.Fprintln(os.Stderr, "vos install: --disk is required")
			return 2
		}
		c.opts.Disk = askDisk(in, os.Stdout)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	p := &cliProgress{w: os.Stdout}
	inst, err := newInstaller(defaultEnv(), c.opts, p.report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vos install:", err)
		return 2
	}
	defer inst.close()
	if err := inst.prepare(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "vos install:", err)
		return 1
	}
	fmt.Println()
	for _, line := range inst.summary() {
		fmt.Println("    " + line)
	}
	fmt.Println()
	if !c.yes {
		word := "ERASE"
		if inst.opts.Mode == ModeRepair {
			word = "REPAIR"
			fmt.Printf("VaporOS on %s will be reinstalled; the data partition is kept.\n", devName(inst.disk))
		} else {
			fmt.Printf("Everything on %s will be erased.\n", devName(inst.disk))
		}
		fmt.Printf("Type %s to continue: ", word)
		if answer, _ := in.ReadString('\n'); strings.TrimSpace(answer) != word {
			fmt.Fprintln(os.Stderr, "vos install: aborted")
			return 1
		}
	}
	if err := inst.execute(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "vos install:", err)
		return 1
	}
	fmt.Println("    Remove the installation medium and reboot.")
	return 0
}

// askDisk lists the disks the installer could use and reads a choice.
func askDisk(in *bufio.Reader, out io.Writer) string {
	mounts, _ := readMountinfo()
	live := liveDisks(mounts)
	fmt.Fprintln(out, "Disks:")
	for _, g := range sysfsDisks() {
		if live[g.name] {
			continue
		}
		fmt.Fprintf(out, "    %-16s %10s  %s\n", devName(g.name), humanBytes(sizeBytes(g.name)), sysModel(g.name))
	}
	fmt.Fprint(out, "Install to which disk? ")
	answer, _ := in.ReadString('\n')
	return strings.TrimSpace(answer)
}

// summary describes a prepared install for the confirmation prompt.
func (in *installer) summary() []string {
	disk := fmt.Sprintf("%s (%s", devName(in.disk), humanBytes(sizeBytes(in.disk)))
	if m := sysModel(in.disk); m != "" {
		disk += ", " + m
	}
	version := fmt.Sprintf("%s from %s", in.man.Version, in.src)
	if in.unsigned {
		version += " (UNSIGNED debug build)"
	}
	lines := []string{
		"Disk:       " + disk + ")",
		"Version:    " + version,
	}
	if in.opts.Mode == ModeRepair {
		lines = append(lines, "Mode:       repair (slot a and the ESP are rewritten, data is kept)")
	} else {
		lines = append(lines, fmt.Sprintf("Layout:     2 x %d GiB slots, the rest for data", in.slotMiB>>10))
	}
	if in.opts.Hostname != "" {
		lines = append(lines, "Hostname:   "+in.opts.Hostname)
	}
	if in.opts.Timezone != "" {
		lines = append(lines, "Timezone:   "+in.opts.Timezone)
	}
	switch {
	case in.opts.Password != "":
		lines = append(lines, "Web admin:  password set")
	case in.opts.Mode == ModeErase:
		lines = append(lines, "Web admin:  set a password in the web UI on first boot")
	}
	for _, l := range in.libraries {
		lines = append(lines, fmt.Sprintf("Library:    %s -> %s", orElse(l.Label, l.UUID), l.Mountpoint))
	}
	if in.connector != "" {
		lines = append(lines, "Display:    virtual display on "+in.connector)
	} else {
		lines = append(lines, "Display:    no supported GPU; no virtual display")
	}
	return lines
}

// cliProgress prints progress lines. Byte counters ("Writing slot a (1.2
// GiB of 1.9 GiB)") repeat with the same text before the parenthesis; they
// are printed every 5% only.
type cliProgress struct {
	w       io.Writer
	kind    string
	percent int
}

func (p *cliProgress) report(step string, percent int, message string) {
	kind, _, _ := strings.Cut(message, " (")
	if kind == p.kind && percent < p.percent+5 && percent != 100 {
		return
	}
	p.kind, p.percent = kind, percent
	fmt.Fprintf(p.w, "==> [%3d%%] %s\n", percent, message)
}
