package display

import (
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/display/welcome"
)

// Routes registers /display/* and GET /welcome.
func (m *Manager) Routes(srv *api.Server) {
	srv.Handle(http.MethodGet, "/display", api.Authed, m.handleGet)
	srv.Handle(http.MethodPost, "/display/modes", api.Authed, m.handleAddMode)
	srv.Handle(http.MethodPut, "/display/settings", api.Authed, m.handleSettings)
	srv.Handle(http.MethodGet, "/welcome", api.Local, m.handleWelcome)
}

type connectorInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Physical: a monitor is attached here (connected, and not the virtual
	// connector, which reads connected only because the kernel forces it).
	Physical bool `json:"physical"`
}

// displayInfo is GET /display (docs/CONTRACTS.md).
type displayInfo struct {
	Profile          string          `json:"profile"`
	VirtualConnector string          `json:"virtual_connector"`
	Connectors       []connectorInfo `json:"connectors"`
	// AvailableConnectors are the free ports PUT /display/settings accepts
	// as the new virtual connector: disconnected DP and HDMI connectors of
	// a supported GPU, other than the current one.
	AvailableConnectors []string `json:"available_connectors"`
	Modes               []string `json:"modes"`
	Current             *string  `json:"current"`
	// Planes counts the fb-backed planes on the virtual connector's CRTC:
	// 1 while gamescope composites (what Sunshine's KMS capture needs), more
	// when it scans out directly, 0 when nothing is shown or observable.
	Planes       int      `json:"planes"`
	HDR          bool     `json:"hdr"`
	Learned      []string `json:"learned"`
	RebootNeeded bool     `json:"reboot_needed"`
	State        string   `json:"state"`
}

func (m *Manager) info() displayInfo {
	dc := m.displayConfig()
	virtual, hdr := dc.VirtualConnector, dc.HDR
	m.mu.Lock()
	gpu := m.gpu
	state := m.state
	if m.session != nil {
		state = StateStreaming
	}
	conns := slices.Clone(m.conns)
	m.mu.Unlock()

	d := displayInfo{
		Profile:             "none",
		VirtualConnector:    virtual,
		Connectors:          []connectorInfo{},
		AvailableConnectors: []string{},
		Modes:               []string{},
		HDR:                 hdr,
		Learned:             m.learnedModes(),
		RebootNeeded:        m.rebootNeededNow(),
		State:               state,
	}
	if p := gpu.Profile(); p != nil && p.Supported() {
		d.Profile = p.Name()
	}
	for _, c := range conns {
		if c.Type == "Writeback" {
			continue
		}
		d.Connectors = append(d.Connectors, connectorInfo{Name: c.Name, Status: c.Status, Physical: c.Connected() && c.Name != virtual})
		if gpu.Supported && c.Name != virtual && c.Status == "disconnected" && isCandidateType(c.Type) {
			d.AvailableConnectors = append(d.AvailableConnectors, c.Name)
		}
	}
	if virtual != "" && gpu.Supported {
		modes := m.availableModes(gpu, virtual)
		sortModes(modes)
		for _, md := range modes {
			d.Modes = append(d.Modes, md.String())
		}
		if cur, active, err := m.h.Scanout(gpu.Card, virtual); err == nil && active {
			s := cur.String()
			d.Current = &s
		}
		if n, err := m.h.Planes(gpu.Card, virtual); err == nil {
			d.Planes = n
		}
	}
	return d
}

// learnedModes are the extra modes beyond the catalogue (configured and
// learned from clients).
func (m *Manager) learnedModes() []string {
	seen := map[edid.Mode]bool{}
	for _, c := range edid.Catalogue {
		seen[c] = true
	}
	var modes []edid.Mode
	for _, md := range m.extraModes() {
		if !seen[md] && edid.Check(md) == nil {
			seen[md] = true
			modes = append(modes, md)
		}
	}
	sortModes(modes)
	out := []string{}
	for _, md := range modes {
		out = append(out, md.String())
	}
	return out
}

// sortModes orders by area, then refresh, largest first.
func sortModes(modes []edid.Mode) {
	sort.SliceStable(modes, func(i, j int) bool {
		if modes[i].Area() != modes[j].Area() {
			return modes[i].Area() > modes[j].Area()
		}
		if modes[i].W != modes[j].W {
			return modes[i].W > modes[j].W
		}
		return modes[i].Refresh > modes[j].Refresh
	})
}

func (m *Manager) handleGet(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, m.info())
}

// handleAddMode adds a mode to config.display.extra_modes and the learned
// EDID. The kernel offers it after the next boot.
func (m *Manager) handleAddMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	md, err := edid.ParseMode(req.Mode)
	if err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := edid.Check(md); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if !slices.Contains(m.displayConfig().ExtraModes, md.String()) {
		if err := m.cfg.Mutate(func(c *config.Config) {
			if !slices.Contains(c.Display.ExtraModes, md.String()) {
				c.Display.ExtraModes = append(c.Display.ExtraModes, md.String())
			}
		}); err != nil {
			api.Error(w, http.StatusInternalServerError, "saving config: %v", err)
			return
		}
	}
	if _, err := m.regenerateEDID(); err != nil {
		api.Error(w, http.StatusInternalServerError, "writing EDID: %v", err)
		return
	}
	m.hub.Publish("display.changed", struct{}{})
	api.WriteJSON(w, http.StatusOK, map[string]bool{"reboot_needed": m.rebootNeededNow()})
}

// handleSettings changes HDR and, optionally, the virtual connector (which
// rewrites the machine kernel cmdline and needs a reboot).
func (m *Manager) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HDR              *bool   `json:"hdr"`
		VirtualConnector *string `json:"virtual_connector"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	setHDR := func(c *config.Config) {
		if req.HDR != nil {
			c.Display.HDR = *req.HDR
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.VirtualConnector != nil && *req.VirtualConnector != m.virtual() {
		c := *req.VirtualConnector
		if !m.gpu.Supported {
			api.Error(w, http.StatusConflict, "no supported GPU for a virtual display")
			return
		}
		if !m.connectorUsableLocked(c) {
			api.Error(w, http.StatusBadRequest, "%q is not a DP or HDMI connector of %s", c, m.gpu.Name)
			return
		}
		if err := m.setVirtualLocked(c, setHDR); err != nil {
			api.Error(w, http.StatusInternalServerError, "%v", err)
			return
		}
	} else if req.HDR != nil {
		if err := m.cfg.Mutate(setHDR); err != nil {
			api.Error(w, http.StatusInternalServerError, "saving config: %v", err)
			return
		}
	}
	m.hub.Publish("display.changed", struct{}{})
	api.OK(w)
}

// connectorUsableLocked reports whether c exists on the GPU and may carry
// the virtual display.
func (m *Manager) connectorUsableLocked(c string) bool {
	for _, conn := range m.h.Connectors(m.gpu.cardName) {
		if conn.Name == c {
			return isCandidateType(conn.Type)
		}
	}
	return false
}

// handleWelcome is GET /welcome: what the welcome screen shows, minus the
// setup code. The route is open to any local process, and Steam and every
// game run locally; the code must reach only people who see the screen.
func (m *Manager) handleWelcome(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, withoutSetupCode(m.welcomeState()))
}

// withoutSetupCode drops the setup code from a welcome state, including
// the copy in the QR code's URL.
func withoutSetupCode(st welcome.State) welcome.State {
	st.Code = ""
	if i := strings.Index(st.QR, "/setup?"); i >= 0 {
		st.QR = st.QR[:i+1]
	}
	return st
}

// CLIWelcome is `vos welcome`.
func CLIWelcome(args []string) int {
	return welcome.Main(args, config.WelcomeStatePath(), welcomeVirtualConnector)
}

// welcomeVirtualConnector tells `vos welcome` which connector gets the
// 1080p splash: the configured one, else the one the kernel forces on.
func welcomeVirtualConnector() string {
	if cfg, err := config.Load(); err == nil && cfg.Display.VirtualConnector != "" {
		return cfg.Display.VirtualConnector
	}
	return forcedConnector(config.KernelArgs())
}
