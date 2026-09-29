package welcome

import (
	"image"
	"math"
	"slices"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// REDLINE's direction test (TOK §2.6): the heat is read through a ramp
// that only ever gets lighter, the tone sets the temperature (asleep an
// ember, ready a magenta peak, streaming amber, and the rest in between in
// the tokens' order), a fault is drawn cold with hazard tape and never with
// the heat ramp, and no line of text sits on a band too hot to read it on.

// fieldOf is the background of st at w×h, and the field behind it.
func fieldOf(st State, w, h int) (*image.RGBA, *layout, heatField) {
	l := computeLayout(st, w, h)
	b := theLook.backdrop(l)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	theLook.background(img, b)
	return img, l, newHeatField(b)
}

// ringHeat is the hottest point just outside the QR slot, where each tone
// peaks.
func ringHeat(l *layout, f *heatField) float64 {
	r := l.QRSlot.Inset(-l.Len(24))
	hot := 0.0
	for i := 0; i <= 40; i++ {
		t := float64(i) / 40
		for _, p := range [4][2]float64{
			{float64(r.Min.X) + t*float64(r.Dx()), float64(r.Min.Y)},
			{float64(r.Min.X) + t*float64(r.Dx()), float64(r.Max.Y)},
			{float64(r.Min.X), float64(r.Min.Y) + t*float64(r.Dy())},
			{float64(r.Max.X), float64(r.Min.Y) + t*float64(r.Dy())},
		} {
			hot = max(hot, f.at(p[0], p[1]))
		}
	}
	return hot
}

func TestDirectionRedline(t *testing.T) {
	if brand.Direction != "redline" {
		t.Skipf("tokens are %q", brand.Direction)
	}
	// The lookup tables only ever get lighter, so heat reads in greyscale
	// and to colour-blind eyes.
	for name, lut := range map[string]*ramp{"heat": &heatRamp, "cold": &coldRamp} {
		prev := -1.0
		for i, c := range lut {
			y := 0.2126*lin(c[0]) + 0.7152*lin(c[1]) + 0.0722*lin(c[2])
			if y < prev-1e-9 {
				t.Errorf("%s ramp gets darker at %d: %.5f after %.5f", name, i, y, prev)
			}
			prev = y
		}
	}

	st := loadFixtures(t)["ready"]
	peak := map[brand.State]float64{}
	for _, tone := range append(slices.Clone(brand.States), brand.Neutral) {
		img, l, f := fieldOf(withTone(st, tone), 1280, 720)
		peak[tone] = ringHeat(l, &f)
		var warm, cold, amber, magenta, bright, tape int
		for y := 0; y < img.Rect.Dy(); y++ {
			for x := 0; x < img.Rect.Dx(); x++ {
				c := img.RGBAAt(x, y)
				r, g, b := int(c.R), int(c.G), int(c.B)
				if y < l.Len(20) && abs(r-0x7f)+abs(g-0xd6)+abs(b-0xff) < 24 {
					tape++
				}
				if r > b+24 {
					warm++
				}
				if b > r+60 && g > 60 {
					cold++
				}
				if r > 230 && g > 140 {
					amber++
				}
				if r > 140 && g < 90 && b > 70 {
					magenta++
				}
				if r > 90 || g > 60 {
					bright++
				}
			}
		}
		at := "tone " + string(tone)
		switch tone {
		case brand.Fault:
			if warm > 0 || cold == 0 || tape == 0 {
				t.Errorf("%s: %d warm pixels, %d cold, %d of tape; want the cold map and hazard tape only", at, warm, cold, tape)
			}
		default:
			if cold > 0 || tape > 0 {
				t.Errorf("%s: %d cold pixels, %d of tape; only a fault is cold", at, cold, tape)
			}
		}
		switch tone {
		case brand.Ready:
			if magenta == 0 || amber > 0 {
				t.Errorf("%s: %d magenta pixels, %d amber; ready peaks at magenta", at, magenta, amber)
			}
		case brand.Streaming:
			if amber < 10000 {
				t.Errorf("%s: %d amber pixels; streaming runs amber to white-hot", at, amber)
			}
		case brand.Asleep:
			if bright > 0 {
				t.Errorf("%s: %d pixels brighter than an ember", at, bright)
			}
		}
	}
	// The temperatures climb in the tokens' order.
	order := []brand.State{brand.Asleep, brand.Installing, brand.Ready, brand.RestartNeeded, brand.Updating, brand.Streaming}
	for i := 1; i < len(order); i++ {
		if peak[order[i]] <= peak[order[i-1]] {
			t.Errorf("%s peaks at %.2f, not hotter than %s at %.2f", order[i], peak[order[i]], order[i-1], peak[order[i-1]])
		}
	}
	// A device waiting for its PIN heats the ready field.
	pair := withTone(st, brand.Ready)
	pair.Attention = AttentionPair
	_, l, f := fieldOf(pair, 1280, 720)
	if h := ringHeat(l, &f); h <= peak[brand.Ready] {
		t.Errorf("pairing peaks at %.2f, ready at %.2f", h, peak[brand.Ready])
	}
}

// On a landscape screen every fixture's words, in every tone, sit on the
// cool side without a plate: the column narrows instead.
func TestWordsOnTheCoolSide(t *testing.T) {
	fx := loadFixtures(t)
	for _, name := range fixtureNames {
		for _, tone := range append(slices.Clone(brand.States), brand.Neutral) {
			for _, sz := range []image.Point{{1920, 1080}, {1280, 720}} {
				l := computeLayout(withTone(fx[name], tone), sz.X, sz.Y)
				if len(l.Plates) > 0 {
					t.Errorf("%s/%s@%s: %d lines needed a plate", name, tone, sizeName(sz), len(l.Plates))
				}
				f := newHeatField(theLook.backdrop(l))
				if hot := f.tooHot(l, redlinePalette.Ink); len(hot) > 0 {
					t.Errorf("%s/%s@%s: %s %q sits on a band past %d", name, tone, sizeName(sz), l.Texts[hot[0]].Role, l.Texts[hot[0]].Text, hottestBand(l.Texts[hot[0]], f.cold, redlinePalette.Ink))
				}
			}
		}
	}
}

// Width is temperature: the status is set in the state's cut, and a long
// status that leads with a verb keeps the verb at full size over the rest
// at half.
func TestStatusCutAndBreak(t *testing.T) {
	fx := loadFixtures(t)
	for _, c := range []struct {
		name  string
		cut   string
		lines []string
	}{
		{"ready", "warm", []string{"Ready to stream"}},
		{"installer-ready", "cold", []string{"Ready to install"}},
		{"streaming", "hot", []string{"Streaming", "to Living room TV"}},
		{"updating", "warm", []string{"Downloading", "update 20261003.0915"}},
		{"offline", "warm", []string{"Waiting", "for the network"}},
		{"no-gpu", "warm", []string{"No supported", "graphics card"}},
		{"pairing", "warm", []string{"Jasper’s iPhone", "wants to pair"}},
	} {
		l := computeLayout(fx[c.name], 1920, 1080)
		var got []textItem
		for _, it := range l.Texts {
			if it.Role == roleStatus {
				got = append(got, it)
			}
		}
		if len(got) != len(c.lines) {
			t.Errorf("%s: %d status lines, want %q", c.name, len(got), c.lines)
			continue
		}
		for i, it := range got {
			if it.Text != c.lines[i] || it.Font != tvFont(brand.TVType.Status, c.cut) {
				t.Errorf("%s: line %d is %q, want %q in the %s cut", c.name, i, it.Text, c.lines[i], c.cut)
			}
		}
		if full := brand.TVType.Status.Px; leadVerb(c.lines[0]) && len(got) == 2 && (got[1].Px > full/2 || got[1].Px >= got[0].Px || got[0].Px < full*4/5) {
			t.Errorf("%s: %q at %d px under %q at %d px, want the verb near %d px over half that", c.name, got[1].Text, got[1].Px, got[0].Text, got[0].Px, full)
		}
		if !leadVerb(c.lines[0]) && len(got) == 2 && got[0].Px != got[1].Px {
			t.Errorf("%s: balanced lines at %d and %d px", c.name, got[0].Px, got[1].Px)
		}
	}
}

// The handshake mark next to the address is the hostname's (brand's
// vectors), drawn cell for cell in its two colours, and never comes from
// the setup code.
func TestHandshakeMark(t *testing.T) {
	fx := loadFixtures(t)
	for _, name := range []string{"ready", "installer-ready"} {
		st := fx[name]
		for _, sz := range []image.Point{{1920, 1080}, {1080, 1920}, {1280, 720}} {
			l := computeLayout(st, sz.X, sz.Y)
			hs := brand.HandshakeFor(st.Hostname)
			if l.Handshake.Empty() || l.HandshakeMark != hs || l.Handshake.Dx() != l.Handshake.Dy() || l.Handshake.Dx()%7 != 0 {
				t.Fatalf("%s@%s: handshake %v %+v", name, sizeName(sz), l.Handshake, l.HandshakeMark)
			}
			var url image.Rectangle
			for _, it := range l.Texts {
				if it.Role == roleURL {
					url = url.Union(it.Rect)
				}
			}
			if l.Handshake.Max.X >= url.Min.X || l.Handshake.Max.Y < url.Min.Y || l.Handshake.Min.Y > url.Max.Y {
				t.Errorf("%s@%s: handshake %v is not beside the address %v", name, sizeName(sz), l.Handshake, url)
			}
			img := paint(l, true)
			cell := l.Handshake.Dx() / 7
			for r, row := range hs.Cells {
				for c, on := range row {
					want := hs.Ground
					if on {
						want = hs.On
					}
					p := l.Handshake.Min.Add(image.Pt((c+1)*cell+cell/2, (r+1)*cell+cell/2))
					if got := img.RGBAAt(p.X, p.Y); got != opaque(want) {
						t.Errorf("%s@%s: cell %d,%d is %v, want %v", name, sizeName(sz), r, c, got, want)
					}
				}
			}
		}
		other := st
		other.Code = "ZZZZ-9999"
		if computeLayout(other, 1920, 1080).HandshakeMark != computeLayout(st, 1920, 1080).HandshakeMark {
			t.Errorf("%s: the setup code changed the handshake", name)
		}
	}
	if l := computeLayout(Placeholder, 1920, 1080); !l.Handshake.Empty() {
		t.Errorf("the placeholder has a handshake %v", l.Handshake)
	}
}

// lin maps an sRGB channel value (0..255, fractional) to linear light.
func lin(v float64) float64 {
	s := v / 255
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}
