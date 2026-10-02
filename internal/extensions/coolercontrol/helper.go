// Package coolercontrol is the CoolerControl extension's helper
// (docs/CONTRACTS.md "Extensions", CoolerControl): the steps of
// coolercontrold.service's start and stop (`vos ext coolercontrol
// prepare`, `fans snapshot`, `fans restore`), its card's status line, the
// kernel module options its settings set, a new admin password, and
// putting the fans back when it is removed. vosd serves its web UI
// (internal/extensions, proxy.go).
package coolercontrol

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
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

// What ModuleOptions reads: the feature mask the loaded amdgpu runs with,
// and the PCI devices. Variables for tests.
var (
	amdgpuFeatureMask = "/sys/module/amdgpu/parameters/ppfeaturemask"
	pciDevicesDir     = "/sys/bus/pci/devices"
)

const (
	// ppOverdrive is amdgpu's PP_OVERDRIVE_MASK: the card's OverDrive
	// interface, which its fan curve goes through.
	ppOverdrive = 0x4000
	// amdgpuDefaultMask is the kernel's own ppfeaturemask (amdgpu_drv.c).
	amdgpuDefaultMask = 0xfff7bfff
)

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
	booted    func() []string // the module option lines this boot started with
}

func newHelper() *helper {
	return &helper{
		now:       time.Now,
		mounted:   mounted,
		wanted:    wanted,
		state:     func(ctx context.Context, u string) string { return sysd.ActiveState(ctx, u, false) },
		handshake: handshake,
		systemctl: sysd.Systemctl,
		booted:    bootedOptions,
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
	return "127.0.0.1:" + daemonPort
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
		if mask, ok := h.featureMask(); ok {
			out = append(out, fmt.Sprintf("options amdgpu ppfeaturemask=0x%x", mask))
		}
	}
	if on, _ := x.Settings["it87_conflicts"].(bool); on {
		out = append(out, "options it87 ignore_resource_conflict=1")
	}
	return out
}

// featureMask is the mask amdgpu is to boot with: the running one with
// OverDrive added, so this boot's own option renders the same line again.
// Before amdgpu has loaded (vosd may ask first) it is the booted set's,
// else the kernel's default with OverDrive when the PC has an AMD
// graphics card: a line that came and went with the driver would make a
// new set, and a restart, each time. Never 0xffffffff, which turns on
// features that are off for a reason.
func (h *helper) featureMask() (uint32, bool) {
	if b, err := os.ReadFile(amdgpuFeatureMask); err == nil {
		return withOverdrive(strings.TrimSpace(string(b)))
	}
	for _, l := range h.booted() {
		f := strings.Fields(l)
		if len(f) == 3 && f[0] == "options" && f[1] == "amdgpu" && strings.HasPrefix(f[2], "ppfeaturemask=") {
			return withOverdrive(strings.TrimPrefix(f[2], "ppfeaturemask="))
		}
	}
	if amdDisplay() {
		return withOverdrive(strconv.Itoa(amdgpuDefaultMask))
	}
	return 0, false
}

func withOverdrive(s string) (uint32, bool) {
	cur, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return 0, false
	}
	mask := uint32(cur) | ppOverdrive
	return mask, mask != 0xffffffff
}

// bootedOptions are the module options this boot started with: the
// initramfs's /run/modprobe.d/vos-ext.conf, then the booted set's own
// modprobe.conf, which has them also when the extension did not mount.
func bootedOptions() []string {
	var out []string
	if b, err := os.ReadFile(filepath.Join(config.ModprobeRunDir, "vos-ext.conf")); err == nil {
		out = strings.Split(string(b), "\n")
	}
	if rep, err := store.LoadBootReport(); err == nil && rep.Set != "" {
		if set, err := store.ReadSet(rep.Set); err == nil {
			out = append(out, set.Options...)
		}
	}
	return out
}

// amdDisplay reports whether the PC has an AMD display controller: PCI
// vendor 0x1002, class 0x03xxxx.
func amdDisplay() bool {
	devs, _ := filepath.Glob(filepath.Join(pciDevicesDir, "*"))
	for _, d := range devs {
		vendor, err := os.ReadFile(filepath.Join(d, "vendor"))
		if err != nil || strings.TrimSpace(string(vendor)) != "0x1002" {
			continue
		}
		if class, err := os.ReadFile(filepath.Join(d, "class")); err == nil && strings.HasPrefix(strings.TrimSpace(string(class)), "0x03") {
			return true
		}
	}
	return false
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

// PasswordChanged gives CoolerControl a new VaporOS admin password at once
// (coolercontrold reads .passwd again when its mtime changes), on the
// terms of each start's prepare: only while .passwd is still the copy
// VaporOS made. Without a .passwd, the next start writes one. It waits
// for a start's prepare to finish with the password.
func (h *helper) PasswordChanged(ctx context.Context, x *extensions.Ext) error {
	d := areaDirs(x.DataDir)
	unlock, err := lockArea(ctx, d)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // no data area: CoolerControl never started
	}
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(filepath.Join(d.config, ".passwd")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	st := loadState(d)
	return copyPassword(d, &st)
}
