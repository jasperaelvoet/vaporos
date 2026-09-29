package display

// Following the Moonlight client: `vos session begin|end` (Sunshine's
// prep-cmd) reaches Begin and End through the session socket. Begin picks
// the mode, drives gamescope to it (modes.cfg + backend_set_dirty, or a
// restart for an HDR change) and waits until the virtual connector really
// scans it out; unknown client modes are learned into the EDID for the
// next boot.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
	"github.com/jasperaelvoet/vaporos/internal/display/edid"
	"github.com/jasperaelvoet/vaporos/internal/session"
)

// Begin prepares the virtual display for a Moonlight client: it picks the
// closest mode the EDID offers, makes gamescope run with the client's HDR
// setting, switches the mode, waits until the connector really scans it
// out and forces composition. Sunshine waits for this (prep-cmd) before it
// starts capturing.
func (m *Manager) Begin(ctx context.Context, req session.Request) session.Response {
	m.op.Lock()
	defer m.op.Unlock()

	client := strings.TrimSpace(req.Client)
	if client == "" {
		client = "Moonlight"
	}
	asked := clientMode(req)

	m.mu.Lock()
	sess := &sessionInfo{Client: client, App: req.App, Mode: asked.String(), Since: m.now()}
	m.session = sess
	m.holdUntil = time.Time{}
	canGame := m.canGameLocked()
	gpu := m.gpu
	virtual := m.cfg.Display.VirtualConnector
	hdrAllowed := m.cfg.Display.HDR
	m.mu.Unlock()
	log.Printf("display: session begin: %s wants %s hdr=%v (%s)", client, asked, req.HDR, req.App)

	if !canGame {
		m.publishBegin(sess)
		m.poke()
		return session.Response{OK: true, Message: "no supported GPU or virtual display: streaming as is"}
	}

	avail := m.availableModes(gpu, virtual)
	mode, exact := chooseMode(asked, avail)
	m.learn(client, asked, req.HDR, exact)
	hdr := req.HDR && hdrAllowed
	if p := gpu.Profile(); p == nil || !p.VirtualHDR() {
		hdr = false
	}
	m.mu.Lock()
	sess.Mode, sess.HDR = mode.String(), hdr
	m.mu.Unlock()
	m.publishWelcomeLater()

	gsUp := m.h.UnitActive(ctx, GamescopeUnit, true)
	keys := m.writeModesCfg(ctx, virtual, mode, gsUp)
	m.ensureStopped(ctx, WelcomeUnit, false)

	var err error
	switch {
	case !gsUp:
		if err = m.writeGamescopeEnv(virtual, hdr); err == nil {
			err = m.h.StartUnit(ctx, GamescopeUnit, true)
		}
	case m.gsHDR != hdr:
		log.Printf("display: restarting gamescope for hdr=%v", hdr)
		if err = m.writeGamescopeEnv(virtual, hdr); err == nil {
			err = m.h.RestartUnit(ctx, GamescopeUnit, true)
		}
	default:
		if cur, active, serr := m.h.Scanout(gpu.Card, virtual); serr != nil || !active || cur != mode {
			m.nudge(ctx)
		}
	}
	if err == nil && (!gsUp || m.gsHDR != hdr) {
		m.gsHDR = hdr
	}
	m.mu.Lock()
	changed := m.state != StateGaming
	m.state = StateGaming
	m.mu.Unlock()
	if changed {
		m.hub.Publish("display.changed", struct{}{})
	}
	if err != nil {
		log.Printf("display: gamescope: %v", err)
		m.publishBegin(sess)
		return session.Response{OK: false, Mode: mode.String(), HDR: hdr, Message: "gamescope: " + err.Error()}
	}

	ok, msg := m.waitForMode(ctx, gpu.Card, virtual, mode, keys)
	if cerr := m.forceComposite(ctx); cerr != nil {
		log.Printf("display: composite_force: %v", cerr)
		if msg == "" {
			msg = "could not force composition: " + cerr.Error()
		}
	}
	m.publishBegin(sess)
	if ok {
		log.Printf("display: %s ready at %s hdr=%v", virtual, mode, hdr)
	} else {
		log.Printf("display: %s", msg)
	}
	if msg == "" && !exact {
		msg = fmt.Sprintf("%s is not offered yet; using %s (learned for the next boot)", asked, mode)
	}
	return session.Response{OK: ok, Mode: mode.String(), HDR: hdr, Message: msg}
}

func (m *Manager) publishBegin(s *sessionInfo) {
	m.mu.Lock()
	data := map[string]any{"client": s.Client, "mode": s.Mode, "hdr": s.HDR}
	m.mu.Unlock()
	m.hub.Publish("session.begin", data)
	m.publishWelcomeLater()
}

// publishWelcomeLater refreshes welcome.json from the Run loop (Begin may
// hold op for a while; the loop does the file write).
func (m *Manager) publishWelcomeLater() { m.poke() }

// End is `vos session end`: the client left. On a machine with a monitor
// the welcome screen returns after the idle grace period.
func (m *Manager) End(ctx context.Context) {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	was := m.session
	m.session = nil
	m.holdUntil = m.now().Add(m.returnDelay)
	m.mu.Unlock()
	if was != nil {
		log.Printf("display: session end: %s after %s", was.Client, m.now().Sub(was.Since).Round(time.Second))
	}
	m.hub.Publish("session.end", struct{}{})
	m.poke()
}

// clientMode turns a session request into a mode, defaulting what Sunshine
// did not say.
func clientMode(req session.Request) edid.Mode {
	m := edid.Mode{W: req.Width, H: req.Height, Refresh: req.FPS}
	if m.W <= 0 || m.H <= 0 {
		m.W, m.H = 1920, 1080
	}
	if m.Refresh <= 0 {
		m.Refresh = 60
	}
	return m
}

// chooseMode picks what to show for a client asking for want, from the
// modes the virtual connector offers:
//  1. exactly want;
//  2. else the largest mode with the same aspect ratio that fits inside
//     want, at the highest refresh not above want's (or, failing that, the
//     lowest refresh of that size);
//  3. else 1920x1080 at min(want's refresh, 60), else its best refresh;
//  4. else the first (preferred) mode offered.
func chooseMode(want edid.Mode, avail []edid.Mode) (edid.Mode, bool) {
	if len(avail) == 0 {
		avail = edid.Catalogue
	}
	if slices.Contains(avail, want) && !(want.W == 4096 && want.H == 2160) {
		return want, true
	}
	usable := make([]edid.Mode, 0, len(avail))
	for _, m := range avail {
		switch {
		case m.W == 4096 && m.H == 2160: // gamescope ignores it
		case m == failSafeMode: // only there for CTA-861 conformance
		default:
			usable = append(usable, m)
		}
	}
	var size edid.Mode
	for _, m := range usable {
		if edid.SameAspect(m, want) && m.W <= want.W && m.H <= want.H && m.Area() > size.Area() {
			size = m
		}
	}
	if size.W > 0 {
		return bestRate(usable, size.W, size.H, want.Refresh), false
	}
	if m := bestRate(usable, 1920, 1080, min(want.Refresh, 60)); m.W > 0 {
		return m, false
	}
	if len(usable) > 0 {
		return usable[0], false
	}
	return edid.Preferred, false
}

// failSafeMode is VGA 640x480@60, which every EDID lists as an established
// timing; it is never a sensible stand-in for a client's mode.
var failSafeMode = edid.Mode{W: 640, H: 480, Refresh: 60}

// bestRate picks, among modes of size w x h, the highest refresh <= rate,
// else the lowest above it. The zero Mode means none of that size.
func bestRate(avail []edid.Mode, w, h, rate int) edid.Mode {
	var below, above edid.Mode
	for _, m := range avail {
		if m.W != w || m.H != h {
			continue
		}
		if m.Refresh <= rate {
			if m.Refresh > below.Refresh {
				below = m
			}
		} else if above.W == 0 || m.Refresh < above.Refresh {
			above = m
		}
	}
	if below.W > 0 {
		return below
	}
	return above
}

// availableModes lists what the virtual connector offers: the kernel's
// live list, else the connector's EDID, else the EDID files, else the
// built-in catalogue.
func (m *Manager) availableModes(gpu GPUInfo, virtual string) []edid.Mode {
	if modes := m.h.ConnectorModes(gpu.Card, virtual); len(modes) > 0 {
		return modes
	}
	if b := m.virtualEDID(gpu, virtual); b != nil {
		if info, err := edid.Decode(b); err == nil {
			return info.Modes()
		}
	}
	return edid.Catalogue
}

// virtualEDID returns the EDID the virtual connector uses: sysfs, else the
// learned file, else the image's.
func (m *Manager) virtualEDID(gpu GPUInfo, virtual string) []byte {
	for _, c := range m.h.Connectors(gpu.cardName) {
		if c.Name == virtual {
			if b := c.EDID(); b != nil {
				return b
			}
		}
	}
	for _, p := range []string{config.LearnedEDIDPath(), config.ImageEDIDPath} {
		if b, err := os.ReadFile(p); err == nil && len(b) >= 128 {
			return b
		}
	}
	return nil
}

// learn records the client's mode and, when the EDID lacks it, adds it to
// the learned EDID for the next boot.
func (m *Manager) learn(client string, asked edid.Mode, hdr, exact bool) {
	clients, err := LoadClients(config.ClientsPath())
	if err != nil {
		log.Printf("display: %v (starting a new clients.json)", err)
	}
	clients.Record(client, ClientMode{W: asked.W, H: asked.H, FPS: asked.Refresh, HDR: hdr, LastSeen: m.now().UTC()})
	if err := clients.Save(config.ClientsPath()); err != nil {
		log.Printf("display: saving clients: %v", err)
	}
	if exact {
		return
	}
	if err := edid.Check(asked); err != nil {
		log.Printf("display: cannot learn %v", err)
		return
	}
	if _, err := m.regenerateEDID(); err != nil {
		log.Printf("display: learned EDID: %v", err)
	}
}

// extraModes are the configured extra modes, then learned client modes.
func (m *Manager) extraModes() []edid.Mode {
	m.mu.Lock()
	cfgModes := slices.Clone(m.cfg.Display.ExtraModes)
	m.mu.Unlock()
	var out []edid.Mode
	for _, s := range cfgModes {
		if md, err := edid.ParseMode(s); err == nil {
			out = append(out, md)
		}
	}
	clients, _ := LoadClients(config.ClientsPath())
	return append(out, clients.Modes()...)
}

// regenerateEDID rewrites /var/lib/vos/firmware/edid/vaporos.bin from the
// catalogue plus extra modes. It reports whether the file changed (and so
// a reboot is needed for the kernel to offer the new modes).
func (m *Manager) regenerateEDID() (bool, error) {
	res, err := edid.Generate(m.extraModes())
	if err != nil {
		return false, err
	}
	for _, e := range res.Skipped {
		log.Printf("display: EDID: skipped %v", e)
	}
	path := config.LearnedEDIDPath()
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if bytes.Equal(old, res.EDID) {
		return false, nil
	}
	if old == nil {
		if img, err := os.ReadFile(config.ImageEDIDPath); err == nil && bytes.Equal(img, res.EDID) {
			return false, nil // nothing beyond what the image already offers
		}
	}
	if err := config.WriteFileAtomic(path, res.EDID, 0o644); err != nil {
		return false, err
	}
	m.mu.Lock()
	m.rebootNeeded = true
	m.mu.Unlock()
	log.Printf("display: learned EDID now offers %d modes (applies after a reboot)", len(res.Modes))
	m.hub.Publish("display.changed", struct{}{})
	return true, nil
}

// rebootNeededNow reports whether the running kernel lags the machine's
// display configuration: machine cmdline args missing from /proc/cmdline,
// or a learned EDID that differs from what the virtual connector uses.
func (m *Manager) rebootNeededNow() bool {
	m.mu.Lock()
	flag, live, gpu, virtual := m.rebootNeeded, m.live, m.gpu, m.cfg.Display.VirtualConnector
	m.mu.Unlock()
	if flag {
		return true
	}
	if live {
		return false
	}
	args := map[string]bool{}
	for _, a := range config.KernelArgs() {
		args[a] = true
	}
	for _, a := range strings.Fields(readMachineCmdline()) {
		if !args[a] {
			return true
		}
	}
	learned, err := os.ReadFile(config.LearnedEDIDPath())
	if err != nil || virtual == "" {
		return false
	}
	for _, c := range m.h.Connectors(gpu.cardName) {
		if c.Name == virtual {
			if cur := c.EDID(); cur != nil && !bytes.Equal(cur, learned) {
				return true
			}
		}
	}
	return false
}

// writeModesCfg stores mode as the saved mode of the virtual display in
// gamescope's modes.cfg and returns the display keys it used.
func (m *Manager) writeModesCfg(ctx context.Context, virtual string, mode edid.Mode, running bool) []string {
	keys := m.displayKeys(ctx, virtual, running)
	path := ModesCfgPath()
	old, _ := os.ReadFile(path)
	if err := m.writeGamerFile(path, updateModesCfg(old, keys, mode), 0o644); err != nil {
		log.Printf("display: modes.cfg: %v", err)
	}
	return keys
}

// displayKeys returns gamescope's name for the virtual display: what a
// running gamescope reports, and what it will compute from the EDID.
func (m *Manager) displayKeys(ctx context.Context, virtual string, running bool) []string {
	var keys []string
	if running {
		if k := m.reportedKey(ctx, virtual); k != "" {
			keys = append(keys, k)
		}
	}
	m.mu.Lock()
	gpu := m.gpu
	m.mu.Unlock()
	if b := m.virtualEDID(gpu, virtual); b != nil {
		if k, err := gamescopeKeyForEDID(b); err == nil && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		keys = append(keys, pnpName(edid.PNPID)+" "+edid.ModelName)
	}
	return keys
}

// reportedKey asks gamescope which display it drives.
func (m *Manager) reportedKey(ctx context.Context, virtual string) string {
	out, err := m.h.Gamescopectl(ctx)
	if err != nil {
		return ""
	}
	info, ok := parseGamescopectl(out)
	if !ok || (info.Connector != "" && info.Connector != virtual) {
		return ""
	}
	return info.Key()
}

// nudge makes gamescope re-read modes.cfg and modeset (atomic, no restart).
func (m *Manager) nudge(ctx context.Context) {
	if _, err := m.h.Gamescopectl(ctx, "backend_set_dirty"); err == nil {
		return
	}
	if err := m.h.Xprop(ctx, "-root", "-f", "GAMESCOPE_DISPLAY_MODE_NUDGE", "32c", "-set", "GAMESCOPE_DISPLAY_MODE_NUDGE", "1"); err != nil {
		log.Printf("display: could not nudge gamescope: %v", err)
	}
}

// forceComposite makes gamescope composite every frame into one plane, so
// Sunshine's KMS capture never loses the picture to direct scanout.
func (m *Manager) forceComposite(ctx context.Context) error {
	deadline := m.now().Add(m.composeWait)
	for {
		_, err := m.h.Gamescopectl(ctx, "composite_force", "1")
		if err == nil {
			return nil
		}
		if m.now().After(deadline) || ctx.Err() != nil {
			break
		}
		if !sleepCtx(ctx, 500*time.Millisecond) {
			break
		}
	}
	return m.h.Xprop(ctx, "-root", "-f", "GAMESCOPE_COMPOSITE_FORCE", "32c", "-set", "GAMESCOPE_COMPOSITE_FORCE", "1")
}

// waitForMode polls the virtual connector's CRTC (read-only DRM) until it
// scans out want with a framebuffer for m.settle, or m.modeTimeout passes.
func (m *Manager) waitForMode(ctx context.Context, card, virtual string, want edid.Mode, keys []string) (bool, string) {
	start := m.now()
	deadline := start.Add(m.modeTimeout)
	var stable time.Time
	rechecked := false
	var last edid.Mode
	for {
		cur, active, scanErr := m.h.Scanout(card, virtual)
		now := m.now()
		switch {
		case scanErr != nil:
			// DRM cannot be observed (no card access): settle for gamescope
			// answering its control socket.
			for !now.After(deadline) {
				if _, err := m.h.Gamescopectl(ctx); err == nil {
					return true, "mode not verified: " + scanErr.Error()
				}
				if !sleepCtx(ctx, time.Second) {
					return false, ctx.Err().Error()
				}
				now = m.now()
			}
			return false, "gamescope did not come up"
		case active && cur == want:
			if stable.IsZero() {
				stable = now
			}
			if now.Sub(stable) >= m.settle {
				return true, ""
			}
		default:
			stable = time.Time{}
			last = cur
			if !rechecked && now.Sub(start) >= m.recheckAfter && m.h.UnitActive(ctx, GamescopeUnit, true) {
				// gamescope may name the display differently than we
				// computed: ask it, rewrite modes.cfg if so, nudge again.
				rechecked = true
				if k := m.reportedKey(ctx, virtual); k != "" && !slices.Contains(keys, k) {
					log.Printf("display: gamescope calls the display %q", k)
					m.writeModesCfg(ctx, virtual, want, true)
				}
				m.nudge(ctx)
			}
		}
		if now.After(deadline) {
			return false, fmt.Sprintf("%s still shows %s instead of %s after %s", virtual, last, want, m.modeTimeout)
		}
		if !sleepCtx(ctx, m.poll) {
			return false, ctx.Err().Error()
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// writeGamescopeEnv writes the unit's EnvironmentFile if it changed.
func (m *Manager) writeGamescopeEnv(output string, hdr bool) error {
	if output == "" {
		return errors.New("no virtual connector configured")
	}
	// logind mounts the runtime dir when the user manager starts; creating
	// it ourselves would leave our file hidden under that mount.
	if _, err := os.Stat(UserRuntimeDir); err != nil {
		return fmt.Errorf("gamescope env: %s not there yet (user manager not running?)", UserRuntimeDir)
	}
	data := gamescopeEnv{Output: output, HDR: hdr}.render()
	path := GamescopeEnvPath()
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := m.writeGamerFile(path, data, 0o644); err != nil {
		return fmt.Errorf("gamescope env: %w", err)
	}
	return nil
}

// writeGamerFile writes a file the gaming user owns, creating missing
// parent directories owned by the gaming user as well.
func (m *Manager) writeGamerFile(path string, data []byte, perm os.FileMode) error {
	if err := m.mkdirGamer(filepath.Dir(path)); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(path, data, perm); err != nil {
		return err
	}
	return m.h.OwnByGamer(path)
}

func (m *Manager) mkdirGamer(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := m.mkdirGamer(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return m.h.OwnByGamer(dir)
}
