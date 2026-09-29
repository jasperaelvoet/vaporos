package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

// serialPath is where the test harness reads VOS-READY (docs/CONTRACTS.md).
var serialPath = "/dev/ttyS0"

// announcer writes the VOS-READY line at start and again whenever what it
// says changes: the first IP (DHCP arriving late, a new lease) or the setup
// code (first-run setup finished).
type announcer struct {
	mode, version string
	code          func() string
	firstIP       func() string
	serial        string
	stdout        io.Writer
	interval      time.Duration
	pokes         chan struct{}
}

func newAnnouncer(live bool, version string, code func() string) *announcer {
	mode := "os"
	if live {
		mode = "installer"
	}
	return &announcer{
		mode:    mode,
		version: version,
		code:    code,
		firstIP: func() string {
			if ips := sysd.LocalIPs(); len(ips) > 0 {
				return ips[0]
			}
			return ""
		},
		serial:   serialPath,
		stdout:   os.Stdout,
		interval: 5 * time.Second,
		pokes:    make(chan struct{}, 1),
	}
}

// poke asks for an immediate re-check (never blocks).
func (a *announcer) poke() {
	select {
	case a.pokes <- struct{}{}:
	default:
	}
}

// run announces until ctx ends, polling the IP every interval.
func (a *announcer) run(ctx context.Context) {
	last := ""
	check := func() {
		line := readyLine(a.mode, a.version, a.firstIP(), a.code())
		if line == last {
			return
		}
		last = line
		a.emit(line)
	}
	check()
	t := time.NewTicker(a.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		case <-a.pokes:
			check()
		}
	}
}

func (a *announcer) emit(line string) {
	fmt.Fprintln(a.stdout, line)
	if a.serial == "" {
		return
	}
	// No serial port (most real PCs) is normal, not worth a log line.
	if err := writeSerial(a.serial, line); err != nil && !os.IsNotExist(err) {
		log.Printf("announce: %s: %v", a.serial, err)
	}
}

// readyLine formats the harness line; empty fields are "-" and no field
// may contain whitespace, which would break the harness's parsing.
func readyLine(mode, version, ip, code string) string {
	field := func(s string) string {
		s = strings.Join(strings.Fields(s), "_")
		if s == "" {
			return "-"
		}
		return s
	}
	return fmt.Sprintf("VOS-READY mode=%s version=%s ip=%s code=%s", field(mode), field(version), field(ip), field(code))
}
