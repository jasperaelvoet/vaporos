package display

// Following the client's HDR. gamescope outputs HDR while its hdr_enabled
// convar is on and the display supports HDR (the virtual EDID says it
// does). --hdr-enabled only sets the convar at start: Steam then writes its
// own per-display "Enable HDR" setting into the GAMESCOPE_DISPLAY_HDR_ENABLED
// root property, which gamescope copies into the convar, so an SDR stream
// came out as HDR (washed out) whenever Steam's setting said on. So Begin
// sets both halves to the session's HDR, live, with no restart of gamescope
// or Steam, and the watchdog puts them back while the session lasts, as it
// does for composite_force.

import (
	"context"
	"log"
	"time"
)

// hdrWatch is the HDR watchdog's memory (guarded by Manager.mu).
type hdrWatch struct {
	why      string // last reason logged
	loggedAt time.Time
}

// setHDRWithin turns gamescope's HDR output on or off: the convar
// (`gamescopectl hdr_enabled 1|0`) and the root property Steam writes.
func (m *Manager) setHDRWithin(ctx context.Context, on bool, wait time.Duration) error {
	v := "0"
	if on {
		v = "1"
	}
	return m.setConvarWithin(ctx, wait, "hdr_enabled", hdrEnabledProp, v)
}

// checkHDR is one watchdog round: during a session on gamescope, when the
// root property is unset or not the session's HDR (Steam wrote its own
// setting, at its start or on a display change), it sets HDR again. It
// returns why it acted ("" when it did not).
func (m *Manager) checkHDR(ctx context.Context) string {
	m.mu.Lock()
	s := m.session
	watched := s != nil && m.state == StateGaming && m.canGameLocked()
	var want bool
	if watched {
		want = s.HDR
	}
	m.mu.Unlock()
	if !watched {
		return ""
	}
	// Begin sets HDR itself once gamescope is there.
	if !m.op.TryLock() {
		return ""
	}
	m.op.Unlock()

	out, err := m.h.Xprop(ctx, getRootPropArgs(hdrEnabledProp)...)
	if err != nil {
		return ""
	}
	var why string
	switch v, set := parseXpropCardinal(out, hdrEnabledProp); {
	case !set:
		why = hdrEnabledProp + " is unset"
	case (v != 0) != want:
		why = hdrEnabledProp + " is " + onOff(v != 0) + " for a stream with HDR " + onOff(want)
	default:
		return ""
	}
	err = m.setHDRWithin(ctx, want, 0)

	m.mu.Lock()
	now := m.now()
	logIt := why != m.hdr.why || now.Sub(m.hdr.loggedAt) >= compositeLogEvery
	if logIt {
		m.hdr.why, m.hdr.loggedAt = why, now
	}
	m.mu.Unlock()
	if logIt {
		if err != nil {
			log.Printf("display: setting HDR %s again (%s): %v", onOff(want), why, err)
		} else {
			log.Printf("display: set HDR %s again (%s)", onOff(want), why)
		}
	}
	return why
}
