package display

// Sunshine's KMS monitor index. Stock Sunshine maps output_name to the
// n-th lit plane (cursor planes left out, across /dev/dri/card*) once, when
// it starts, and checks capture against that number before a stream's prep
// command runs. When gamescope scans out on two planes at that moment (at
// boot, before vosd forces composition) and on one later, the number names
// no plane and every stream fails with error 503 until Sunshine restarts.
// CaptureIndex gives the sunshine service the number a fresh Sunshine would
// pick now, so it can tell (see docs/CONTRACTS.md, Units).

import "github.com/jasperaelvoet/vaporos/internal/display/drm"

// kmsgrabIndex is Sunshine's index for the virtual connector's CRTC, given
// the lit planes of the virtual connector's card and whether another card
// lights any. It only answers when that is unambiguous and settled: one lit
// plane on the CRTC (gamescope composites), and no other card lighting
// anything (Sunshine walks the cards in readdir order).
func kmsgrabIndex(lit []drm.Plane, crtc uint32, otherCardsLit bool) (int, bool) {
	if crtc == 0 || otherCardsLit {
		return 0, false
	}
	index, on := 0, 0
	for i, p := range lit {
		if p.CRTCID == crtc {
			index, on = i, on+1
		}
	}
	if on != 1 {
		return 0, false
	}
	return index, true
}

// CaptureIndex is the KMS monitor index a Sunshine started now would map
// the virtual connector to; ok is false while gamescope does not drive it
// settled on one plane, while the display is switching, or when it cannot
// be observed.
func (m *Manager) CaptureIndex() (int, bool) {
	m.mu.Lock()
	gaming := m.state == StateGaming && m.canGameLocked()
	card := m.gpu.Card
	m.mu.Unlock()
	if !gaming {
		return 0, false
	}
	if !m.op.TryLock() {
		return 0, false
	}
	m.op.Unlock()
	return m.h.KMSGrabIndex(card, m.virtual())
}
