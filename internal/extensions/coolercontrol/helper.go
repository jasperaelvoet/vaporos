// Package coolercontrol is the CoolerControl extension's helper
// (docs/CONTRACTS.md "Extensions", CoolerControl): the steps of
// coolercontrold.service's start and stop (`vos ext coolercontrol
// prepare`, `fans snapshot`, `fans restore`), its card's status line, the
// kernel module options its settings set, and putting the fans back when
// it is removed. vosd serves its web UI (internal/extensions, proxy.go).
package coolercontrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/store"
	"github.com/jasperaelvoet/vaporos/internal/sysd"
)

const (
	id   = "coolercontrol"
	unit = "coolercontrold.service"
)

func init() {
	extensions.RegisterHelper(id, newHelper())
	extensions.RegisterCommand(id, "prepare | fans snapshot | fans restore (coolercontrold.service runs these)", cli)
}

// Status words (design/voice.md: plain).
const (
	startingText   = "CoolerControl is starting."
	notRunningText = "CoolerControl isn't running. Restart VaporOS, or remove it."
)

// statusTTL is how long a status check holds: the control center asks for
// it every few seconds.
const statusTTL = 5 * time.Second

// amdgpuFeatureMask is the feature mask the loaded amdgpu runs with. A
// variable for tests.
var amdgpuFeatureMask = "/sys/module/amdgpu/parameters/ppfeaturemask"

// ppOverdrive is amdgpu's PP_OVERDRIVE_MASK: the card's OverDrive
// interface, which its fan curve goes through.
const ppOverdrive = 0x4000

type helper struct {
	extensions.NopHelper

	mu      sync.Mutex
	checked time.Time
	lines   []extensions.StatusLine

	// Seams.
	now       func() time.Time
	mounted   func(id string) bool // by this boot
	wanted    func(id string) bool // still: a removed one's card says so already
	state     func(ctx context.Context, unit string) string
	handshake func(ctx context.Context, upstream string) bool
	systemctl func(ctx context.Context, args ...string) error
}

func newHelper() *helper {
	return &helper{
		now:       time.Now,
		mounted:   mounted,
		wanted:    wanted,
		state:     func(ctx context.Context, u string) string { return sysd.ActiveState(ctx, u, false) },
		handshake: handshake,
		systemctl: sysd.Systemctl,
	}
}

func mounted(id string) bool {
	rep, err := store.LoadBootReport()
	return err == nil && rep.IsMounted(id)
}

func wanted(id string) bool {
	w, err := store.Wanted()
	return err == nil && slices.Contains(w, id)
}

// handshake asks coolercontrold's own server whether it answers.
func handshake(ctx context.Context, upstream string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+upstream+"/handshake", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// upstream is the loopback address vosd proxies its web UI to.
func upstream(x *extensions.Ext) string {
	if x.Desc != nil && x.Desc.Network != nil {
		for _, p := range x.Desc.Network.Ports {
			if p.Mode == "proxied" {
				return p.Upstream
			}
		}
	}
	return "127.0.0.1:11986"
}

// Status says when CoolerControl does not run while it should: its unit
// active and its server answering is no line at all.
func (h *helper) Status(ctx context.Context, x *extensions.Ext) []extensions.StatusLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.now(); !h.checked.IsZero() && t.Sub(h.checked) < statusTTL && !t.Before(h.checked) {
		return h.lines
	}
	h.lines = h.check(ctx, x)
	h.checked = h.now()
	return h.lines
}

func (h *helper) check(ctx context.Context, x *extensions.Ext) []extensions.StatusLine {
	if !h.mounted(x.ID) || !h.wanted(x.ID) {
		return nil
	}
	switch h.state(ctx, unit) {
	case "active":
		if h.handshake(ctx, upstream(x)) {
			return nil
		}
	case "activating", "reloading":
		return []extensions.StatusLine{{Text: startingText}}
	}
	return []extensions.StatusLine{{Text: notRunningText, Tone: "warning"}}
}

// ModuleOptions turns the card's settings into kernel module options,
// which the next restart applies.
func (h *helper) ModuleOptions(x *extensions.Ext) []string {
	var out []string
	if on, _ := x.Settings["gpu_fan_curves"].(bool); on {
		if mask, ok := featureMask(); ok {
			out = append(out, fmt.Sprintf("options amdgpu ppfeaturemask=0x%x", mask))
		}
	}
	if on, _ := x.Settings["it87_conflicts"].(bool); on {
		out = append(out, "options it87 ignore_resource_conflict=1")
	}
	return out
}

// featureMask is amdgpu's running feature mask with OverDrive added. It
// starts from what runs, so this boot's own option renders the same line
// again; never 0xffffffff, which turns on features that are off for a
// reason. No amdgpu, no option.
func featureMask() (uint32, bool) {
	b, err := os.ReadFile(amdgpuFeatureMask)
	if err != nil {
		return 0, false
	}
	cur, err := strconv.ParseUint(strings.TrimSpace(string(b)), 0, 32)
	if err != nil {
		return 0, false
	}
	mask := uint32(cur) | ppOverdrive
	if mask == 0xffffffff {
		return 0, false
	}
	return mask, true
}

// Remove stops CoolerControl now (its ExecStopPost puts the fans back) and
// puts them back once more itself, should that not have run. Purging
// deletes its settings, which vosd also does with the data area.
func (h *helper) Remove(ctx context.Context, x *extensions.Ext, purge bool) error {
	var errs []error
	if h.mounted(x.ID) {
		if err := h.systemctl(ctx, "stop", "--", unit); err != nil {
			errs = append(errs, fmt.Errorf("stopping CoolerControl: %w", err))
		}
	}
	if err := restoreFans(); err != nil {
		errs = append(errs, fmt.Errorf("putting the fans back: %w", err))
	}
	if purge && x.DataDir != "" {
		if err := os.RemoveAll(x.DataDir); err != nil {
			errs = append(errs, err)
		}
	}
	h.mu.Lock()
	h.checked = time.Time{}
	h.mu.Unlock()
	return errors.Join(errs...)
}
