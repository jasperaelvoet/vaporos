package display

// Keeping gamescope compositing. Stock Sunshine's KMS capture follows the
// CRTC's primary plane. When gamescope scans a game out directly on more
// than one plane (the spike PC showed a 3840x2160 primary plus a scaled
// 1920x1080 overlay on the same CRTC), the stream loses part of the picture
// or freezes. The composite_force convar makes gamescope composite every
// frame into one plane, but the convar alone does not stick: gamescope
// copies the X root property GAMESCOPE_COMPOSITE_FORCE into it whenever
// that property changes, and Steam writes 0 there. So vosd sets both halves
// after every gamescope start and every modeset, and a watchdog puts them
// back while a stream may be running.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/display/edid"
)

// compositeWatch is the watchdog's memory (guarded by Manager.mu).
type compositeWatch struct {
	mode     edid.Mode // last mode seen on the virtual connector
	why      string    // last reason logged
	loggedAt time.Time
}

// compositeLogEvery rate-limits the watchdog's log line for a reason that
// persists (a gamescope that keeps two planes despite the convar).
const compositeLogEvery = time.Minute

// forceComposite makes gamescope composite into one plane: the convar
// (`gamescopectl composite_force 1`) and the X root property (`xprop -root
// -f GAMESCOPE_COMPOSITE_FORCE 32c -set GAMESCOPE_COMPOSITE_FORCE 1`). A
// fresh gamescope takes a moment to answer, so it retries for up to
// m.composeWait.
func (m *Manager) forceComposite(ctx context.Context) error {
	return m.forceCompositeWithin(ctx, m.composeWait)
}

// forceCompositeWithin tries both halves until both took or wait passes.
// Once one half took, gamescope is up: the other gets about a second more
// (Xwayland comes up just after the control socket), not the whole wait.
// It never reads the convar back: `gamescopectl composite_force` without a
// value prints nothing.
func (m *Manager) forceCompositeWithin(ctx context.Context, wait time.Duration) error {
	deadline := m.now().Add(wait)
	var ctlErr, xErr error
	ctlDone, xDone, cut := false, false, false
	for {
		if !ctlDone {
			_, ctlErr = m.h.Gamescopectl(ctx, "composite_force", "1")
			ctlDone = ctlErr == nil
		}
		if !xDone {
			_, xErr = m.h.Xprop(ctx, setCompositeForceArgs...)
			xDone = xErr == nil
		}
		if ctlDone && xDone {
			return nil
		}
		now := m.now()
		if (ctlDone || xDone) && !cut {
			cut = true
			if d := now.Add(time.Second); d.Before(deadline) {
				deadline = d
			}
		}
		if !now.Before(deadline) || !sleepCtx(ctx, 500*time.Millisecond) {
			break
		}
	}
	var errs []error
	if !ctlDone {
		errs = append(errs, fmt.Errorf("gamescopectl composite_force: %w", ctlErr))
	}
	if !xDone {
		errs = append(errs, fmt.Errorf("xprop %s: %w", compositeForceProp, xErr))
	}
	return errors.Join(errs...)
}

// noteCompositeMode tells the watchdog which mode was just set up (with
// composition forced), so it does not mistake that modeset for one of
// gamescope's own.
func (m *Manager) noteCompositeMode(mode edid.Mode) {
	m.mu.Lock()
	m.composite.mode = mode
	m.mu.Unlock()
}

// watchComposite runs checkComposite every m.compositeEvery until ctx ends.
func (m *Manager) watchComposite(ctx context.Context) {
	if m.compositeEvery <= 0 {
		return
	}
	t := time.NewTicker(m.compositeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.checkComposite(ctx)
		}
	}
}

// compositeWatchedLocked reports whether composition must be kept forced
// now: gamescope drives the virtual connector, and either a session is
// active or gamescope runs headless for streaming (no monitor), where a
// stream may start at any moment.
func (m *Manager) compositeWatchedLocked() bool {
	if m.state != StateGaming || !m.canGameLocked() {
		return false
	}
	return m.session != nil || !m.physicalConnectedLocked()
}

// checkComposite is one watchdog round. When the virtual connector's CRTC
// scans out more than one fb-backed plane, the root property does not read
// 1, or gamescope changed the mode by itself, it forces composition again;
// both writes are idempotent, and the checks are one DRM plane query and
// one xprop. It returns why it acted ("" when it did not).
func (m *Manager) checkComposite(ctx context.Context) string {
	virtual := m.virtual()
	m.mu.Lock()
	watched := m.compositeWatchedLocked()
	card := m.gpu.Card
	if !watched {
		m.composite.mode = edid.Mode{}
	}
	m.mu.Unlock()
	if !watched {
		return ""
	}
	// A session or the policy is switching units right now; it forces
	// composition itself once gamescope is there.
	if !m.op.TryLock() {
		return ""
	}
	m.op.Unlock()

	why := m.compositeTrouble(ctx, card, virtual)
	if why == "" {
		return ""
	}
	err := m.forceCompositeWithin(ctx, 0)

	m.mu.Lock()
	now := m.now()
	logIt := why != m.composite.why || now.Sub(m.composite.loggedAt) >= compositeLogEvery
	if logIt {
		m.composite.why, m.composite.loggedAt = why, now
	}
	m.mu.Unlock()
	if logIt {
		if err != nil {
			log.Printf("display: forcing composition again (%s): %v", why, err)
		} else {
			log.Printf("display: forced composition again (%s)", why)
		}
	}
	return why
}

// compositeTrouble lists what says composition is not forced (any more).
// What cannot be observed does not count.
func (m *Manager) compositeTrouble(ctx context.Context, card, virtual string) string {
	var why []string
	if mode, active, err := m.h.Scanout(card, virtual); err == nil && active {
		m.mu.Lock()
		last := m.composite.mode
		m.composite.mode = mode
		m.mu.Unlock()
		if last != (edid.Mode{}) && last != mode {
			why = append(why, "gamescope switched to "+mode.String())
		}
	}
	if n, err := m.h.Planes(card, virtual); err == nil && n > 1 {
		why = append(why, fmt.Sprintf("%d planes scan out on %s", n, virtual))
	}
	if out, err := m.h.Xprop(ctx, getCompositeForceArgs...); err == nil {
		switch v, set := parseXpropCardinal(out, compositeForceProp); {
		case !set:
			why = append(why, compositeForceProp+" is unset")
		case v == 0:
			why = append(why, compositeForceProp+" is 0")
		}
	}
	return strings.Join(why, ", ")
}
