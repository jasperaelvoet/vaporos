// Package display owns everything about screens: GPU detection and vendor
// profiles, DRM connector state (pure-Go ioctls), the EDID generator, the
// welcome renderer (`vos welcome`), gamescope control, and vosd's display
// policy (welcome vs gamescope, following the Sunshine client's mode).
// See docs/CONTRACTS.md.
package display

import (
	"context"
	"fmt"
	"os"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

// GPUInfo describes the primary GPU.
type GPUInfo struct {
	Vendor    string `json:"vendor"` // "amd" | "intel" | "nvidia" | "virtual" | ""
	Name      string `json:"name"`
	Driver    string `json:"driver"`
	Card      string `json:"card"` // /dev/dri/cardN
	Supported bool   `json:"supported"`
}

// Probe inspects /sys/class/drm and returns the primary GPU.
func Probe() GPUInfo { return GPUInfo{} }

// ChooseVirtualConnector picks the connector the virtual display uses on
// this machine: a disconnected DP (else HDMI) on a supported GPU; "" if none.
func ChooseVirtualConnector() (string, error) { return "", nil }

// MachineCmdlineFor returns the machine kernel args for a virtual connector.
func MachineCmdlineFor(connector string) string { return "" }

type Manager struct{ cfg *config.Config }

func NewManager(cfg *config.Config) *Manager { return &Manager{cfg: cfg} }

// SetSetupCode sets the code the welcome screen shows ("" for none).
func (m *Manager) SetSetupCode(code string) {}

// Routes registers /display/* and GET /welcome.
func (m *Manager) Routes(srv *api.Server) {}

// Run applies the display policy until ctx ends: hotplug, welcome vs
// gamescope, welcome.json, and the session socket.
func (m *Manager) Run(ctx context.Context) {}

func (m *Manager) Begin(ctx context.Context, req session.Request) session.Response {
	return session.Response{OK: true}
}
func (m *Manager) End(ctx context.Context) {}

// Streaming reports whether a Moonlight session is active.
func (m *Manager) Streaming() (bool, string) { return false, "" }

// CLIWelcome is `vos welcome`.
func CLIWelcome(args []string) int {
	fmt.Fprintln(os.Stderr, "vos welcome: not implemented")
	return 1
}

// CLIEdid is `vos edid generate|decode`.
func CLIEdid(args []string) int {
	fmt.Fprintln(os.Stderr, "vos edid: not implemented")
	return 1
}
