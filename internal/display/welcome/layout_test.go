package welcome

import (
	"image"
	"slices"
	"testing"
)

// eachLayout lays out every fixture on every layout size.
func eachLayout(t *testing.T, fn func(name string, sz image.Point, l *layout)) {
	t.Helper()
	fx := loadFixtures(t)
	for _, name := range fixtureNames {
		for _, sz := range layoutSizes {
			fn(name, sz, computeLayout(fx[name], sz.X, sz.Y))
		}
	}
}

// Text, the QR card, the code plate and the progress bar sit inside the
// title-safe area of the screen and inside the scaled reference canvas, so
// an overscanning TV cuts nothing off.
func TestLayoutTitleSafe(t *testing.T) {
	eachLayout(t, func(name string, sz image.Point, l *layout) {
		box := l.Safe.Intersect(l.Content)
		in := func(what string, r image.Rectangle) {
			if !r.Empty() && !r.In(box) {
				t.Errorf("%s@%s: %s %v is outside title-safe %v", name, sizeName(sz), what, r, box)
			}
		}
		for _, it := range l.Texts {
			in(it.Role.String()+" "+quote(it.Text), it.Rect)
		}
		in("QR card", l.QRCard)
		in("code plate", l.Panel)
		in("progress bar", l.Progress)
		in("signature", l.Signature)
		in("handshake mark", l.Handshake)
	})
}

// No text runs into another line, the QR card or its frame, the progress
// bar, the handshake mark or a plate it doesn't belong to: the setup code
// sits inside its plate and the scale's words inside the signature, and
// nothing else touches either.
func TestLayoutNoOverlap(t *testing.T) {
	eachLayout(t, func(name string, sz image.Point, l *layout) {
		at := name + "@" + sizeName(sz)
		for i, a := range l.Texts {
			for _, b := range l.Texts[i+1:] {
				if a.Rect.Overlaps(b.Rect) {
					t.Errorf("%s: %s %s %v overlaps %s %s %v", at, a.Role, quote(a.Text), a.Rect, b.Role, quote(b.Text), b.Rect)
				}
			}
			for what, r := range map[string]image.Rectangle{"the QR card": l.QRCard, "the QR frame": l.QRFrame, "the progress bar": l.Progress, "the handshake mark": l.Handshake, "the mark": l.Mark} {
				if a.Rect.Overlaps(r) && !(what == "the progress bar" && a.Role == roleProgress) {
					t.Errorf("%s: %s %s %v overlaps %s %v", at, a.Role, quote(a.Text), a.Rect, what, r)
				}
			}
			for _, plate := range []struct {
				what  string
				r     image.Rectangle
				roles []role
			}{{"code plate", l.Panel, []role{roleCode, roleCodeLabel}}, {"signature", l.Signature, []role{roleScale, roleScaleLabel}}} {
				on := slices.Contains(plate.roles, a.Role)
				if on && !a.Rect.In(plate.r) || !on && a.Rect.Overlaps(plate.r) {
					t.Errorf("%s: %s %s %v against the %s %v", at, a.Role, quote(a.Text), a.Rect, plate.what, plate.r)
				}
			}
		}
		for what, r := range map[string]image.Rectangle{"signature": l.Signature, "progress bar": l.Progress, "code plate": l.Panel, "handshake mark": l.Handshake, "caption plate": l.Caption, "callout": l.Callout} {
			if r.Overlaps(l.QRFrame) || r.Overlaps(l.QRCard) {
				t.Errorf("%s: the %s %v overlaps the QR card %v or its frame %v", at, what, r, l.QRCard, l.QRFrame)
			}
		}
		// The frame goes around the card; TestQRCardIsPure holds it outside.
		if !l.QRFrame.Empty() && (!l.QRCard.In(l.QRFrame) || l.QRCard == l.QRFrame) {
			t.Errorf("%s: the QR frame %v does not surround the card %v", at, l.QRFrame, l.QRCard)
		}
	})
}

// minPx are the floors at 1920×1080 in pixels, for the fixtures whose lines
// fit without shrinking.
var minPx = map[role]int{
	roleStatus: 48, roleURL: 44, roleCode: 72,
	roleDetail: 28, roleIP: 28, roleCodeLabel: 28, roleCaption: 28,
	roleVersion: 20,
}

// The type is big enough to read from a couch, and the QR code big enough
// to scan from one, on every screen.
func TestLayoutMinimumSizes(t *testing.T) {
	fx := loadFixtures(t)
	for _, name := range []string{"ready", "installer-ready"} {
		l := computeLayout(fx[name], 1920, 1080)
		seen := map[role]bool{}
		for _, it := range l.Texts {
			seen[it.Role] = true
			if min, ok := minPx[it.Role]; ok && it.Px < min {
				t.Errorf("%s: %s %s is %d px, want at least %d", name, it.Role, quote(it.Text), it.Px, min)
			}
		}
		for r := range minPx {
			if !seen[r] && (r != roleCode && r != roleCodeLabel || name == "installer-ready") {
				t.Errorf("%s: no %s line", name, r)
			}
		}
	}
	eachLayout(t, func(name string, sz image.Point, l *layout) {
		if l.QR == nil {
			return
		}
		at := name + "@" + sizeName(sz)
		if l.Module < 3 {
			t.Errorf("%s: module %d px, want at least 3", at, l.Module)
		}
		side, share, want := float64(l.QRCard.Dx()), float64(l.QRCard.Dx())/float64(l.Content.Dy()), 0.45
		if l.Portrait {
			share, want = side/float64(l.Content.Dx()), 0.55
		}
		if share < want {
			t.Errorf("%s: QR card %v is %.0f%% of the canvas, want at least %.0f%%", at, l.QRCard, 100*share, 100*want)
		}
	})
}

func quote(s string) string { return "\"" + s + "\"" }
