package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// drmClassDir is where the kernel lists DRM connectors; a variable for tests.
var drmClassDir = "/sys/class/drm"

// connectorDir matches a connector entry, card<N>-<name>, not a card or a
// render node.
var connectorDir = regexp.MustCompile(`^card[0-9]+-(.+)$`)

// monitorState reports whether a monitor is attached to any GPU: a DRM
// connector whose status is "connected", other than writeback connectors
// and the ones the kernel command line forces on (video=<C>:e, VaporOS's
// virtual display). known is false while no connector is listed at all,
// before a GPU driver has probed its outputs, when nobody can tell.
func monitorState(dir string, kernelArgs []string) (connected, known bool) {
	forced := forcedConnectors(kernelArgs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false
	}
	for _, e := range entries {
		m := connectorDir.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		name := m[1]
		if strings.HasPrefix(name, "Writeback-") || forced[name] {
			known = true
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "status"))
		if err != nil {
			continue
		}
		known = true
		if strings.TrimSpace(string(b)) == "connected" {
			return true, true
		}
	}
	return false, known
}

// forcedConnectors are the connectors named by video=<C>:…e (or D) kernel
// arguments; such a connector reads "connected" with no monitor behind it.
func forcedConnectors(args []string) map[string]bool {
	out := map[string]bool{}
	for _, a := range args {
		v, ok := strings.CutPrefix(a, "video=")
		if !ok {
			continue
		}
		name, opts, ok := strings.Cut(v, ":")
		if ok && name != "" && (strings.HasSuffix(opts, "e") || strings.HasSuffix(opts, "D")) {
			out[name] = true
		}
	}
	return out
}

// headless is the installer's setup-code waiver: with no monitor attached,
// the code could only be read off a serial console, which a real PC does
// not have, so the installer could not be used at all. It is asked on
// every Setup request, so plugging a monitor in (which starts the welcome
// screen showing the code) turns the code back on at once. Outputs that
// have not been probed yet do not count as headless.
func headless() bool {
	connected, known := monitorState(drmClassDir, config.KernelArgs())
	return known && !connected
}
