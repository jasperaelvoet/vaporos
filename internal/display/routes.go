package display

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
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
	srv.Handle(http.MethodDelete, "/display/modes/{mode}", api.Authed, m.handleRemoveMode)
	srv.Handle(http.MethodPut, "/display/settings", api.Authed, m.handleSettings)
	srv.Handle(http.MethodPut, "/display/screens/{id}", api.Authed, m.handleScreenPut)
	srv.Handle(http.MethodDelete, "/display/screens/{id}", api.Authed, m.handleScreenDelete)
	srv.Handle(http.MethodPost, "/display/hint", api.Authed, m.handleHint)
	srv.Handle(http.MethodGet, "/welcome", api.Local, m.handleWelcome)
}

type connectorInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Physical: a monitor is attached here (connected, and not the virtual
	// connector, which reads connected only because the kernel forces it).
	Physical bool `json:"physical"`
}

// Info is GET /display (docs/CONTRACTS.md).
type Info struct {
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
	Planes int  `json:"planes"`
	HDR    bool `json:"hdr"`
	// Learned is Added plus the modes learned from clients.
	Learned      []string     `json:"learned"`
	Added        []string     `json:"added"`
	Devices      []deviceMode `json:"devices"`
	RebootNeeded bool         `json:"reboot_needed"`
	State        string       `json:"state"`
	// UIScaling is display.ui_scaling; SteamUI how sizing Steam stands
	// (ok, idle, starting, no-debugger, unsupported or off); Screens the
	// devices' screens, most recently seen first (scale.go).
	UIScaling bool         `json:"ui_scaling"`
	SteamUI   string       `json:"steam_ui"`
	Screens   []ScreenView `json:"screens"`
}

// Info builds GET /display, which GET /status shares. It reads sysfs,
// config and clients.json and asks DRM about the virtual connector only.
func (m *Manager) Info() Info {
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
	clients, _ := LoadClients(config.ClientsPath())

	d := Info{
		Profile:             "none",
		VirtualConnector:    virtual,
		Connectors:          []connectorInfo{},
		AvailableConnectors: []string{},
		Modes:               []string{},
		HDR:                 hdr,
		Learned:             m.learnedModes(),
		Added:               beyondCatalogue(m.configuredModes()),
		Devices:             clients.devices(),
		RebootNeeded:        m.rebootNeededNow(),
		State:               state,
		UIScaling:           dc.UIScaling,
		SteamUI:             m.steamUIView(dc.UIScaling),
		Screens:             m.screenViews(),
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
func (m *Manager) learnedModes() []string { return beyondCatalogue(m.extraModes()) }

// beyondCatalogue keeps the valid modes the catalogue lacks, once each,
// sorted as GET /display lists modes.
func beyondCatalogue(extra []edid.Mode) []string {
	seen := map[edid.Mode]bool{}
	for _, c := range edid.Catalogue {
		seen[c] = true
	}
	var modes []edid.Mode
	for _, md := range extra {
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
	api.WriteJSON(w, http.StatusOK, m.Info())
}

// handleAddMode adds a mode to config.display.extra_modes and the learned
// EDID. The kernel offers it right away where it can, else after the next
// boot; reboot_needed says which.
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
	if err := m.addMode(md); err != nil {
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	m.hub.Publish("display.changed", struct{}{})
	api.WriteJSON(w, http.StatusOK, map[string]bool{"reboot_needed": m.rebootNeededNow()})
}

func (m *Manager) addMode(md edid.Mode) error {
	m.edidMu.Lock()
	defer m.edidMu.Unlock()
	if !slices.Contains(m.displayConfig().ExtraModes, md.String()) {
		if err := m.cfg.Mutate(func(c *config.Config) {
			if !slices.Contains(c.Display.ExtraModes, md.String()) {
				c.Display.ExtraModes = append(c.Display.ExtraModes, md.String())
			}
		}); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
	}
	if _, err := m.regenerateEDID(m.idle()); err != nil {
		return fmt.Errorf("writing EDID: %w", err)
	}
	return nil
}

// handleRemoveMode is DELETE /display/modes/{mode}: it forgets a mode that
// was added by hand or learned from clients, and rewrites the learned EDID.
// A client that asks for the mode again teaches it again.
func (m *Manager) handleRemoveMode(w http.ResponseWriter, r *http.Request) {
	md, err := edid.ParseMode(r.PathValue("mode"))
	if err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	found, err := m.removeMode(md)
	if err != nil {
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if !found {
		api.Error(w, http.StatusNotFound, "%s is neither an added nor a learned mode", md)
		return
	}
	m.hub.Publish("display.changed", struct{}{})
	api.WriteJSON(w, http.StatusOK, map[string]bool{"reboot_needed": m.rebootNeededNow()})
}

// removeMode drops md from display.extra_modes and from every clients.json
// entry that asked for it. It reports whether either held it.
func (m *Manager) removeMode(md edid.Mode) (bool, error) {
	m.edidMu.Lock()
	defer m.edidMu.Unlock()
	same := func(s string) bool {
		p, err := edid.ParseMode(s)
		return err == nil && p == md
	}
	added := slices.ContainsFunc(m.displayConfig().ExtraModes, same)
	if added {
		if err := m.cfg.Mutate(func(c *config.Config) {
			c.Display.ExtraModes = slices.DeleteFunc(c.Display.ExtraModes, same)
		}); err != nil {
			return false, fmt.Errorf("saving config: %w", err)
		}
	}
	// An unreadable clients.json holds nothing to remove; learn replaces it.
	clients, err := LoadClients(config.ClientsPath())
	learned := err == nil && clients.forget(md)
	if learned {
		if err := clients.Save(config.ClientsPath()); err != nil {
			return false, fmt.Errorf("saving clients: %w", err)
		}
	}
	if !added && !learned {
		return false, nil
	}
	if _, err := m.regenerateEDID(m.idle()); err != nil {
		return true, fmt.Errorf("writing EDID: %w", err)
	}
	return true, nil
}

// handleSettings changes HDR, interface scaling and, optionally, the
// virtual connector (which rewrites the machine kernel cmdline and needs a
// reboot).
func (m *Manager) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HDR              *bool   `json:"hdr"`
		VirtualConnector *string `json:"virtual_connector"`
		UIScaling        *bool   `json:"ui_scaling"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	set := func(c *config.Config) {
		if req.HDR != nil {
			c.Display.HDR = *req.HDR
		}
		if req.UIScaling != nil {
			c.Display.UIScaling = *req.UIScaling
		}
	}
	was := m.displayConfig().UIScaling
	status, msg := m.applySettings(req.VirtualConnector, req.HDR != nil || req.UIScaling != nil, set)
	if now := m.displayConfig().UIScaling; now != was {
		m.scalingChanged(now)
	}
	if status != 0 {
		api.Error(w, status, "%s", msg)
		return
	}
	m.hub.Publish("display.changed", struct{}{})
	api.OK(w)
}

// applySettings saves the settings (set) and a new virtual connector conn,
// under the display lock. It returns an HTTP status and message when it
// refused or failed (0 when all went well).
func (m *Manager) applySettings(conn *string, change bool, set func(*config.Config)) (int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if conn != nil && *conn != m.virtual() {
		c := *conn
		if !m.gpu.Supported {
			return http.StatusConflict, "no supported GPU for a virtual display"
		}
		if !m.connectorUsableLocked(c) {
			return http.StatusBadRequest, fmt.Sprintf("%q is not a DP or HDMI connector of %s", c, m.gpu.Name)
		}
		if err := m.setVirtualLocked(c, set); err != nil {
			return http.StatusInternalServerError, err.Error()
		}
	} else if change {
		if err := m.cfg.Mutate(set); err != nil {
			return http.StatusInternalServerError, "saving config: " + err.Error()
		}
	}
	return 0, ""
}

// screenViews lists the screens for GET /display: screens.json's, with the
// session's own in place of its stored one (or first, when the device
// cannot be told apart).
func (m *Manager) screenViews() []ScreenView {
	m.mu.Lock()
	var live *ScreenView
	if s := m.session; s != nil && s.plan != nil {
		v := s.screenView()
		live = &v
	}
	m.mu.Unlock()
	m.screensMu.Lock()
	defer m.screensMu.Unlock()
	return m.screensLocked().Views(live, m.now())
}

// streamingScreen is the session streaming to screen id, and a copy of its
// screen; nil, nil when that screen does not stream.
func (m *Manager) streamingScreen(id string) (*sessionInfo, *screenPlan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session
	if id == "" || s == nil || s.plan == nil || s.plan.id != id {
		return nil, nil
	}
	p := *s.plan
	return s, &p
}

// replan gives the session streaming now a changed screen, which the
// scaler then applies. It returns the screen as GET /display lists it.
func (m *Manager) replan(sess *sessionInfo, p screenPlan) (ScreenView, bool) {
	m.mu.Lock()
	if m.session != sess {
		m.mu.Unlock()
		return ScreenView{}, false
	}
	sess.plan = &p
	v := sess.screenView()
	if m.want != nil && m.want.sess == sess {
		m.renewWantLocked()
	}
	m.mu.Unlock()
	m.kickScale()
	return v, true
}

// handleScreenPut is PUT /display/screens/{id}: the user's kind, size and
// steam_auto for a screen. It answers once screens.json has them; while
// the screen streams, the scaler applies them right away.
func (m *Manager) handleScreenPut(w http.ResponseWriter, r *http.Request) {
	var e ScreenEdit
	if err := api.ReadJSON(r, &e); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	id := r.PathValue("id")
	sess, plan := m.streamingScreen(id)
	var inEffect Kind
	vetoed := false
	if plan != nil {
		inEffect, vetoed = plan.kind, plan.vetoed
	}
	m.screensMu.Lock()
	sc, err := m.screensLocked().Edit(id, e, inEffect, vetoed)
	if err == nil {
		if serr := m.saveScreensLocked(); serr != nil {
			err = fmt.Errorf("saving screens: %w", serr)
		}
	}
	m.screensMu.Unlock()
	switch {
	case errors.Is(err, ErrNoScreen):
		api.Error(w, http.StatusNotFound, "%v", err)
		return
	case errors.Is(err, ErrBadKind), errors.Is(err, ErrBadSize):
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	case err != nil:
		api.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	view := sc.View(id)
	if plan != nil {
		np := *plan
		np.size, np.steamAuto = normSize(sc.Size), sc.SteamAuto
		np.infer(sc.Kind)
		if v, ok := m.replan(sess, np); ok {
			view = v
		}
	}
	m.hub.Publish("display.changed", struct{}{})
	api.WriteJSON(w, http.StatusOK, view)
}

// handleScreenDelete is DELETE /display/screens/{id}: it forgets a screen;
// the device's browser hints stay. A device streaming now starts afresh
// right away, as its next session would.
func (m *Manager) handleScreenDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, plan := m.streamingScreen(id)
	m.screensMu.Lock()
	s := m.screensLocked()
	found := s.Delete(id)
	var np screenPlan
	var err error
	if found {
		if plan != nil {
			np = *plan
			np.size, np.steamAuto, np.savedScale, np.savedDPI = 1, false, 0, 0
			np.sig.History = nil
			np.panel = PanelFor(np.sig.Hint, np.asked, nil)
			np.infer("")
			s.Touch(id, np.name, np.mac, np.ip, np.asked, np.lastSeen)
			s.Update(id, func(sc *Screen) { sc.Guess, sc.GuessFrom = np.guess, np.guessFrom })
		}
		err = m.saveScreensLocked()
	}
	m.screensMu.Unlock()
	switch {
	case !found:
		api.Error(w, http.StatusNotFound, "%v", ErrNoScreen)
		return
	case err != nil:
		api.Error(w, http.StatusInternalServerError, "saving screens: %v", err)
		return
	}
	if plan != nil {
		m.replan(sess, np)
	}
	m.hub.Publish("display.changed", struct{}{})
	api.OK(w)
}

// handleHint is POST /display/hint: what a browser says about its own
// screen, kept as a hint for the device it runs on (its MAC, else its
// IPv4 address) with the kind its User-Agent gives; the User-Agent itself
// is not kept. From loopback, or an address several devices share, it
// keeps nothing.
func (m *Manager) handleHint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		W     int     `json:"w"`
		H     int     `json:"h"`
		DPR   float64 `json:"dpr"`
		Touch int     `json:"touch"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	switch {
	case req.W < 1 || req.W > maxPanelDim || req.H < 1 || req.H > maxPanelDim:
		api.Error(w, http.StatusBadRequest, "w and h must be between 1 and 20000")
		return
	case !(req.DPR >= 0.5 && req.DPR <= 8):
		api.Error(w, http.StatusBadRequest, "dpr must be between 0.5 and 8")
		return
	case req.Touch < 0 || req.Touch > 20:
		api.Error(w, http.StatusBadRequest, "touch must be between 0 and 20")
		return
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		if a := ap.Addr().WithZone("").Unmap(); !a.IsLoopback() && !a.IsUnspecified() {
			h := Hint{
				Kind: ClassifyBrowser(r.UserAgent(), req.W, req.H, req.DPR, req.Touch),
				W:    req.W, H: req.H, DPR: req.DPR, Touch: req.Touch, At: m.now().UTC(),
			}
			mac := m.h.NeighbourMAC(a)
			m.screensMu.Lock()
			if m.screensLocked().PutHint(mac, a.String(), h) {
				m.saveScreensLocked()
			}
			m.screensMu.Unlock()
		}
	}
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
