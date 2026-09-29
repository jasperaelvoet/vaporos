package welcome

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// withTone is st with another tone.
func withTone(st State, tone brand.State) State {
	st.Tone = tone
	return st
}

// region copies the pixels of r out of img.
func region(img *image.RGBA, r image.Rectangle) []byte {
	var b []byte
	for y := r.Min.Y; y < r.Max.Y; y++ {
		b = append(b, img.Pix[img.PixOffset(r.Min.X, y):img.PixOffset(r.Max.X, y)]...)
	}
	return b
}

// Every tone renders, the QR code stays exact, and a word outside the
// vocabulary looks exactly like neutral.
func TestEveryToneRenders(t *testing.T) {
	fx := loadFixtures(t)
	for _, name := range []string{"ready", "installer-running", "pairing"} {
		neutral := Render(withTone(fx[name], brand.Neutral), 1280, 720)
		for _, tone := range tones {
			st := withTone(fx[name], tone)
			l := computeLayout(st, 1280, 720)
			img := paint(l, true)
			checkQR(t, name+"/"+string(tone), l, img, st.QR)
			if _, known := brand.ParseState(string(tone)); !known && !bytes.Equal(img.Pix, neutral.Pix) {
				t.Errorf("%s: tone %q does not render as neutral", name, tone)
			}
		}
	}
}

// Nothing but the code's two colours inside the QR card, whatever the tone.
// Only the rounded corners, in the quiet zone, show what is behind.
func TestQRCardIsPure(t *testing.T) {
	fx := loadFixtures(t)
	pal := theLook.palette()
	type run struct {
		name string
		sz   image.Point
		tone brand.State
	}
	var runs []run
	for _, tone := range tones {
		for _, name := range []string{"installer-ready", "pairing", "updating"} {
			runs = append(runs, run{name, image.Pt(1024, 768), tone})
		}
	}
	for _, name := range fixtureNames {
		runs = append(runs, run{name, image.Pt(1920, 1080), fx[name].Tone})
	}
	for _, r := range runs {
		l := computeLayout(withTone(fx[r.name], r.tone), r.sz.X, r.sz.Y)
		if l.QR == nil {
			continue
		}
		img := paint(l, true)
		c, rad := l.QRCard, l.QRRadius
		bad := 0
		for y := c.Min.Y; y < c.Max.Y; y++ {
			for x := c.Min.X; x < c.Max.X; x++ {
				if (x < c.Min.X+rad || x >= c.Max.X-rad) && (y < c.Min.Y+rad || y >= c.Max.Y-rad) {
					continue // a rounded corner
				}
				if p := img.RGBAAt(x, y); p != pal.QRDark && p != pal.QRLight {
					bad++
				}
			}
		}
		if bad > 0 {
			t.Errorf("%s@%s tone %q: %d pixels in the QR card are neither QR colour", r.name, sizeName(r.sz), r.tone, bad)
		}
	}
}

// The tone changes the signature, and never the QR code.
func TestToneDrivesSignature(t *testing.T) {
	st := loadFixtures(t)["ready"]
	for _, sz := range []image.Point{{1920, 1080}, {1080, 1920}} {
		a, b := computeLayout(withTone(st, brand.Ready), sz.X, sz.Y), computeLayout(withTone(st, brand.Fault), sz.X, sz.Y)
		if a.Signature.Empty() || a.Signature != b.Signature || a.QRCard != b.QRCard {
			t.Fatalf("%v: signature %v / %v, card %v / %v", sz, a.Signature, b.Signature, a.QRCard, b.QRCard)
		}
		ia, ib := paint(a, true), paint(b, true)
		sa, sb := region(ia, a.Signature), region(ib, b.Signature)
		diff := 0
		for i := 0; i < len(sa); i += 4 {
			if !bytes.Equal(sa[i:i+4], sb[i:i+4]) {
				diff++
			}
		}
		if n := len(sa) / 4; diff*5 < n {
			t.Errorf("%v: only %d of %d signature pixels differ between ready and fault", sz, diff, n)
		}
		// The card's rounded corners show the field behind; the rest of it,
		// quiet zone and code, is the same in every tone.
		c, rad := a.QRCard, a.QRRadius
		for _, r := range []image.Rectangle{{c.Min.Add(image.Pt(rad, 0)), c.Max.Sub(image.Pt(rad, 0))}, {c.Min.Add(image.Pt(0, rad)), c.Max.Sub(image.Pt(0, rad))}} {
			if !bytes.Equal(region(ia, r), region(ib, r)) {
				t.Errorf("%v: the QR card changes with the tone", sz)
			}
		}
	}
}

// The bar shows how far an install or update has got, and nothing else.
func TestProgressBar(t *testing.T) {
	fx := loadFixtures(t)
	for _, name := range []string{"installer-running", "installer-done", "updating"} {
		st := fx[name]
		for _, sz := range []image.Point{{1920, 1080}, {1280, 720}, {1080, 1920}} {
			l := computeLayout(st, sz.X, sz.Y)
			if l.Progress.Empty() || l.Percent != st.Progress {
				t.Fatalf("%s@%s: no bar (%v, %d%%)", name, sizeName(sz), l.Progress, l.Percent)
			}
			img := paint(l, true)
			fill, track := opaque(brand.StyleOf(l.Tone).Color), theLook.palette().Surface2
			y, n := (l.Progress.Min.Y+l.Progress.Max.Y)/2, 0
			for x := l.Progress.Min.X; x < l.Progress.Max.X; x++ {
				if p := img.RGBAAt(x, y); dist(p, fill) < dist(p, track) {
					n++
				}
			}
			if want := barFill(l.Progress, st.Progress); abs(n-want) > 1 {
				t.Errorf("%s@%s: %d px filled, want %d (%d%% of %d)", name, sizeName(sz), n, want, st.Progress, l.Progress.Dx())
			}
		}
	}
	st := fx["updating"]
	for _, c := range []State{withTone(st, brand.Ready), {Status: st.Status, Tone: brand.Updating}, fx["ready"], fx["installer-failed"]} {
		if l := computeLayout(c, 1920, 1080); !l.Progress.Empty() {
			t.Errorf("tone %q, progress %d: bar %v, want none", c.Tone, c.Progress, l.Progress)
		}
	}
}

// While a device waits for its PIN, the line that says where to enter it
// is called out.
func TestAttentionPair(t *testing.T) {
	fx := loadFixtures(t)
	for _, name := range []string{"pairing", "long-unicode"} {
		for _, sz := range pixelSizes {
			l := computeLayout(fx[name], sz.X, sz.Y)
			var detail image.Rectangle
			for _, it := range l.Texts {
				if it.Role == roleDetail {
					detail = it.Rect
				}
			}
			if l.Callout.Empty() || !l.Callout.Overlaps(detail) {
				t.Errorf("%s@%s: callout %v does not cover the detail %v", name, sizeName(sz), l.Callout, detail)
			}
			if l.Callout.Overlaps(l.QRCard) {
				t.Errorf("%s@%s: callout %v overlaps the QR card", name, sizeName(sz), l.Callout)
			}
		}
		st := fx[name]
		st.Attention = ""
		if l := computeLayout(st, 1920, 1080); !l.Callout.Empty() {
			t.Errorf("%s without attention: callout %v", name, l.Callout)
		}
	}
}

func dist(a, b color.RGBA) int {
	return abs(int(a.R)-int(b.R)) + abs(int(a.G)-int(b.G)) + abs(int(a.B)-int(b.B))
}
