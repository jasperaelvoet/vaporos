package welcome

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// REDLINE's background is a thermal image of the box: heat rises off the QR
// card, the thing you point your phone at, and falls off toward the words,
// seen through Inferno in posterised bands with hairline isotherms. The
// numbers are the tokens' (brand.TVLook.Params, brand.StyleOf(tone).Reach
// and .Peak); everything is laid out in reference units times the screen's
// scale, so a 4K screen shows the same picture as a 720p one.

// lookParam is a numeric parameter of the TV look, or def when the tokens
// leave it out.
func lookParam(name string, def float64) float64 {
	if v, ok := brand.TVLook.Params[name]; ok {
		return v
	}
	return def
}

// heatField is the heat at every point of one screen in one tone. Its
// inputs are the backdrop's, so it is what the background cache caches.
type heatField struct {
	s, ox, oy float64 // reference units to pixels, and the reference canvas's origin
	h         float64 // the screen's height in pixels
	portrait  bool

	// The hot core: the QR slot plus a margin, a rounded rectangle.
	cx, cy, hw, hh, r float64

	reach, peak float64 // falloff length in pixels; heat at the core's edge
	cold        bool    // off the scale: drawn with the cold map
	cool        float64 // how hard the side toward the words flattens the heat
	warp        float64 // how far the air bends the isotherms, in pixels
	ambient     float64 // the faint heat of the room, rising toward the top
}

// newHeatField sets up the field for b: centred on b.Focus (the QR slot),
// as hot and as far-reaching as the tone, hotter still while a device waits
// for its PIN, and a small round bloom while there is no card yet.
func newHeatField(b backdrop) heatField {
	g := newGeometry(b.W, b.H)
	st := brand.StyleOf(b.Tone)
	f := heatField{
		s: g.S, ox: g.OX, oy: g.OY, h: float64(b.H), portrait: g.Portrait,
		reach: st.Reach, peak: st.Peak, cold: st.ColdMap,
		cool:    lookParam("cool", 2.2),
		warp:    lookParam("warp", 70) * g.S,
		ambient: lookParam("ambient", 0.05),
	}
	if p := brand.AttentionPair; b.Pair && !f.cold && p.Peak > f.peak {
		f.reach, f.peak = p.Reach, p.Peak
	}
	f.reach = max(1, f.reach*g.S)
	f.cx = float64(b.Focus.Min.X+b.Focus.Max.X) / 2
	f.cy = float64(b.Focus.Min.Y+b.Focus.Max.Y) / 2
	if b.Bloom {
		// No card yet (starting): a round bloom of heat where it will be.
		f.hw, f.hh, f.r = 60*g.S, 60*g.S, 60*g.S
		return f
	}
	f.hw = float64(b.Focus.Dx())/2 + 20*g.S
	f.hh = float64(b.Focus.Dy())/2 + 20*g.S
	f.r = 16 * g.S
	return f
}

// at is the heat at pixel (x, y), 0 to just under 1: an exponential falloff
// from a signed distance to the core, stretched upward because heat rises
// (in landscape), pressed flat toward the words (left in landscape, up in
// portrait: the cool side), and bent by two octaves of value noise in reference units, so
// the picture is the same at every size.
func (f *heatField) at(x, y float64) float64 {
	s := f.s
	qx, qy := math.Abs(x-f.cx)-(f.hw-f.r), math.Abs(y-f.cy)-(f.hh-f.r)
	d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - f.r
	// Heat rises: it reaches further up than down. In portrait the words
	// are above the card, so there it only falls off faster below.
	if up := (f.cy - y) / (540 * s); up > 0 && !f.portrait {
		d /= 1 + 0.55*up
	} else if up < 0 {
		d *= 1 - 0.3*up
	}
	var side float64
	if f.portrait {
		side = math.Max(0, (f.cy-f.hh)-y) / (300 * s)
	} else {
		side = math.Max(0, (f.cx-f.hw)-x) / (300 * s)
	}
	d *= 1 + f.cool*math.Min(side, 2.5)
	n := fbm((x-f.ox)/s*0.0032, (y-f.oy)/s*0.0032)*2 - 1
	d = math.Max(0, d+n*f.warp*math.Min(1, d/(120*s)+0.35))
	h := f.peak*math.Exp(-d/f.reach) + f.ambient*(1-y/f.h)*(0.6+0.4*n)
	return min(0.999, max(0, h))
}

// heatBands is how many isotherms the ramp is cut into.
func heatBands() int { return max(2, int(lookParam("bands", 12))) }

// ramp is a 256-entry lookup table of a colour ramp, in sRGB bytes.
type ramp [256][3]float64

// rampOf interpolates stops evenly over 0..255.
func rampOf(stops []color.NRGBA) (out ramp) {
	for i := range out {
		t := float64(i) / 255 * float64(len(stops)-1)
		k := min(len(stops)-2, int(t))
		u := t - float64(k)
		a, b := stops[k], stops[k+1]
		out[i] = [3]float64{
			float64(a.R) + (float64(b.R)-float64(a.R))*u,
			float64(a.G) + (float64(b.G)-float64(a.G))*u,
			float64(a.B) + (float64(b.B)-float64(a.B))*u,
		}
	}
	return out
}

var (
	heatRamp = rampOf(brand.HeatRamp) // Inferno, h0 to white-hot
	coldRamp = rampOf(brand.ColdRamp) // the fault map, below the scale
)

// bandColors is the flat colour of each isotherm: the ramp at the middle of
// the band.
func bandColors(cold bool) [][3]float64 {
	lut := &heatRamp
	if cold {
		lut = &coldRamp
	}
	n := heatBands()
	out := make([][3]float64, n)
	for b := range out {
		out[b] = lut[(b*2+1)*255/(2*n)]
	}
	return out
}

// bandOf is the isotherm heat h falls in.
func bandOf(h float64) int { return min(heatBands()-1, max(0, int(h*float64(heatBands())))) }

// paint draws the field into img: the heat on a grid of G pixels, and every
// pixel's heat and its gradient interpolated from the four grid points
// around it, so there is no full-resolution heat buffer. Per pixel: the
// band's colour, a contour line about 1.5 px wide at any size (the distance
// to the band edge over the gradient), a vignette and grain. Rows run in
// parallel.
func (f *heatField) paint(img *image.RGBA) {
	G := max(2, int(lookParam("grid", 8)))
	W, H := img.Rect.Dx(), img.Rect.Dy()
	N := heatBands()
	gw, gh := W/G+2, H/G+2
	grid := make([]float64, gw*gh)
	parallel(gh, func(j0, j1 int) {
		for j := j0; j < j1; j++ {
			for i := 0; i < gw; i++ {
				grid[j*gw+i] = f.at(float64(i*G), float64(j*G))
			}
		}
	})
	bands := bandColors(f.cold)
	vx := make([]float64, W)
	for x := range vx {
		v := (float64(x) - float64(W)/2) / (float64(W) / 2)
		vx[x] = v * v
	}
	line := math.Max(1, f.s) // contour line width in pixels
	inLine, lineOff := 1/(1.1*line), 0.6*line
	lineDark := lookParam("lineDark", 0.42)
	grain := 2 * lookParam("grain", 3.5)
	fN, invG := float64(N), 1/float64(G)
	parallel(H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			j := y / G
			v := float64(y-j*G) * invG
			fy := (float64(y) - float64(H)/2) / (float64(H) / 2)
			vy := fy * fy
			row := img.Pix[y*img.Stride : y*img.Stride+W*4]
			gr := grid[j*gw:]
			for x := 0; x < W; x++ {
				i := x / G
				u := float64(x-i*G) * invG
				g00, g10, g01, g11 := gr[i], gr[i+1], gr[i+gw], gr[i+gw+1]
				top, bot := g00+(g10-g00)*u, g01+(g11-g01)*u
				h := (top + (bot-top)*v) * fN
				dx := ((g10-g00)*(1-v) + (g11-g01)*v) * (fN * invG)
				dy := (bot - top) * (fN * invG)
				b := int(h)
				fr := h - float64(b)
				b = min(b, N-1)
				ln := 0.0
				if b > 0 || fr > 0.5 {
					m := min(fr, 1-fr)
					e := max(0, m/(math.Sqrt(dx*dx+dy*dy)+1e-6)-lineOff)
					ln = max(0, 1-e*inLine)
				}
				k := (1 - lineDark*ln) * (1 - 0.11*(vx[x]+vy))
				gn := (hash(x+911, y+373) - 0.5) * grain
				c := &bands[b]
				o := x * 4
				row[o] = clamp8(c[0]*k + gn)
				row[o+1] = clamp8(c[1]*k + gn)
				row[o+2] = clamp8(c[2]*k + gn)
				row[o+3] = 0xff
			}
		}
	})
}

// tape draws hazard tape along the top edge: fault is off the scale, and
// the cold stripes say so without flashing. The stripes run at 45° with
// anti-aliased edges.
func tape(img *image.RGBA, s float64) {
	th := int(math.Round(24 * s))
	per := 52 * s
	cold, dark := opaque(brand.Palette.Cold), opaque(brand.HeatRamp[0])
	for y := 0; y < min(th, img.Rect.Dy()); y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < img.Rect.Dx(); x++ {
			t := math.Mod(float64(x+y)+1, per)
			d := math.Min(math.Min(t, math.Abs(t-per/2)), per-t) / math.Sqrt2 // to the nearest edge
			a := math.Min(1, 0.5+d)
			if t > per/2 {
				a = 1 - a
			}
			o := x * 4
			for c, v := range [3]uint8{cold.R, cold.G, cold.B} {
				w := [3]uint8{dark.R, dark.G, dark.B}[c]
				row[o+c] = uint8(float64(w) + (float64(v)-float64(w))*a + 0.5)
			}
			row[o+3] = 0xff
		}
	}
}

// parallel splits [0, n) into one band per CPU.
func parallel(n int, fn func(a, b int)) {
	p := min(runtime.GOMAXPROCS(0), n)
	if p <= 1 {
		fn(0, n)
		return
	}
	var wg sync.WaitGroup
	step := (n + p - 1) / p
	for a := 0; a < n; a += step {
		wg.Add(1)
		go func(a, b int) { defer wg.Done(); fn(a, b) }(a, min(n, a+step))
	}
	wg.Wait()
}

// hash is deterministic noise in [0, 1] for an integer point; vnoise and
// fbm build smooth value noise from it. They match the concept's tv.html
// bit for bit (uint32 wraparound is Math.imul's).
func hash(x, y int) float64 {
	h := uint32(int32(x))*374761393 + uint32(int32(y))*668265263
	h = (h ^ (h >> 13)) * 1274126177
	return float64(h^(h>>16)) / 4294967295
}

func vnoise(x, y float64) float64 {
	ix, iy := math.Floor(x), math.Floor(y)
	fx, fy := x-ix, y-iy
	sx, sy := fx*fx*(3-2*fx), fy*fy*(3-2*fy)
	a, b := hash(int(ix), int(iy)), hash(int(ix)+1, int(iy))
	c, d := hash(int(ix), int(iy)+1), hash(int(ix)+1, int(iy)+1)
	return a + (b-a)*sx + (c-a)*sy + (a-b-c+d)*sx*sy
}

func fbm(x, y float64) float64 {
	return vnoise(x, y)*0.62 + vnoise(x*2.03+17.1, y*2.03+9.2)*0.38
}

func clamp8(v float64) uint8 {
	v += 0.5
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}

// coolBelow is how far down, in reference units, the columns x0..x1 stay
// cool from y0 on: the first row above y1 where a sample sits on a band
// past maxTextBand-1, less a margin, or y1. Heat rises toward the words
// in portrait, and this is where they have to stop.
func (f *heatField) coolBelow(l *layout, x0, x1, y0, y1 float64) float64 {
	limit := int(lookParam("maxTextBand", 2)) - 1
	for y := y0; y < y1; y += 8 {
		for x := x0; x <= x1; x += 16 {
			if bandOf(f.at(float64(l.X(x)), float64(l.Y(y)))) > limit {
				return y - 16
			}
		}
	}
	return y1
}

// tooHot lists the lines on the field that sit on a band too hot to read
// them on: hotter than the tokens' maxTextBand allows (one band more for
// bone), or too light for the line's contrast floor (7:1 for the status and
// the address, 4.5:1 otherwise). Lines on plates are not on the field.
func (f *heatField) tooHot(l *layout, bone color.RGBA) []int {
	var out []int
	step := max(4, l.Len(20)) // the field changes over hundreds of units; the pixel test checks between
	for i, t := range l.Texts {
		switch t.Role {
		case roleCode, roleCodeLabel, roleCaption, roleScale, roleScaleLabel:
			continue
		case roleDetail:
			if !l.Callout.Empty() {
				continue
			}
		}
		limit := hottestBand(t, f.cold, bone)
		r := t.Rect.Inset(-max(4, l.Len(8)))
		hot := false
		for y := r.Min.Y; y <= r.Max.Y+step-1 && !hot; y += step {
			for x := r.Min.X; x <= r.Max.X+step-1; x += step {
				if bandOf(f.at(float64(min(x, r.Max.X)), float64(min(y, r.Max.Y)))) > limit {
					hot = true
					break
				}
			}
		}
		if hot {
			out = append(out, i)
		}
	}
	return out
}

// hottestBand is the hottest isotherm t may sit on: bone text may sit one
// band hotter than the rest.
func hottestBand(t textItem, cold bool, bone color.RGBA) int {
	need := 4.5
	if t.Role == roleStatus || t.Role == roleURL || t.Role == roleCode {
		need = 7
	}
	ceiling := int(lookParam("maxTextBand", 2))
	if t.Color == bone {
		ceiling++
	}
	grain := lookParam("grain", 3.5)
	best := -1
	for b, c := range bandColors(cold) {
		lit := color.RGBA{clamp8(c[0] + grain), clamp8(c[1] + grain), clamp8(c[2] + grain), 0xff}
		if b > ceiling || contrastRatio(t.Color, lit) < need*1.04 {
			break
		}
		best = b
	}
	return best
}

// contrastRatio is WCAG 2's contrast between two opaque colours.
func contrastRatio(a, b color.RGBA) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

func relLuminance(c color.RGBA) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}
