package web

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The PNG icons (iOS home screen, web manifest, /favicon.ico) are rendered
// from static/icon.svg by this generator, so the SVG stays the only source.
// It understands exactly what icon.svg uses: one rounded rect filled with a
// diagonal gradient and round-capped stroked paths (M/C/S/L commands).
//
//	VOS_GEN_ICONS=1 go test ./internal/web -run TestGenerateIcons
func TestGenerateIcons(t *testing.T) {
	if os.Getenv("VOS_GEN_ICONS") == "" {
		t.Skip("set VOS_GEN_ICONS=1 to regenerate static/icon-*.png")
	}
	src, err := os.ReadFile(filepath.Join("static", "icon.svg"))
	if err != nil {
		t.Fatal(err)
	}
	logo, err := parseLogo(string(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []struct {
		name      string
		size      int
		fullBleed bool
	}{
		{"icon-180.png", 180, true}, // iOS masks its own rounded corners
		{"icon-192.png", 192, false},
	} {
		img := logo.render(out.size, out.fullBleed)
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("static", out.name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes", out.name, buf.Len())
	}
}

type pt struct{ x, y float64 }

type stroke struct {
	lines   [][2]pt // flattened segments
	opacity float64
}

type gradStop struct {
	at  float64
	rgb [3]float64
}

type logoShape struct {
	x, y, w, h, rx float64
	stops          []gradStop
	strokeWidth    float64
	strokes        []stroke
}

var (
	reRect   = regexp.MustCompile(`<rect x="([\d.]+)" y="([\d.]+)" width="([\d.]+)" height="([\d.]+)" rx="([\d.]+)"`)
	reStop   = regexp.MustCompile(`<stop offset="([\d.]+)" stop-color="#([0-9a-fA-F]{6})"`)
	reWidth  = regexp.MustCompile(`stroke-width="([\d.]+)"`)
	rePath   = regexp.MustCompile(`<path d="([^"]+)"(?: opacity="([\d.]+)")?`)
	rePathTk = regexp.MustCompile(`[A-Za-z]|[-+]?(?:\d*\.\d+|\d+\.?\d*)(?:[eE][-+]?\d+)?`)
)

func parseLogo(svg string) (*logoShape, error) {
	l := &logoShape{}
	m := reRect.FindStringSubmatch(svg)
	if m == nil {
		return nil, fmt.Errorf("no <rect>")
	}
	f := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	l.x, l.y, l.w, l.h, l.rx = f(m[1]), f(m[2]), f(m[3]), f(m[4]), f(m[5])
	for _, s := range reStop.FindAllStringSubmatch(svg, -1) {
		c, _ := strconv.ParseUint(s[2], 16, 32)
		l.stops = append(l.stops, gradStop{f(s[1]), [3]float64{float64(c >> 16 & 255), float64(c >> 8 & 255), float64(c & 255)}})
	}
	if w := reWidth.FindStringSubmatch(svg); w != nil {
		l.strokeWidth = f(w[1])
	}
	for _, p := range rePath.FindAllStringSubmatch(svg, -1) {
		segs, err := flattenPath(p[1])
		if err != nil {
			return nil, err
		}
		op := 1.0
		if p[2] != "" {
			op = f(p[2])
		}
		l.strokes = append(l.strokes, stroke{segs, op})
	}
	if len(l.stops) < 2 || l.strokeWidth == 0 || len(l.strokes) == 0 {
		return nil, fmt.Errorf("icon.svg is not in the shape the generator expects")
	}
	return l, nil
}

// flattenPath turns SVG path data into line segments.
func flattenPath(d string) ([][2]pt, error) {
	toks := rePathTk.FindAllString(d, -1)
	var (
		out       [][2]pt
		cur, ctl2 pt
		cmd       string
		prevCubic bool
	)
	i := 0
	num := func() (float64, error) {
		if i >= len(toks) {
			return 0, fmt.Errorf("path %q: missing number", d)
		}
		v, err := strconv.ParseFloat(toks[i], 64)
		i++
		return v, err
	}
	pair := func(rel bool) (pt, error) {
		x, err := num()
		if err != nil {
			return pt{}, err
		}
		y, err := num()
		if rel {
			x, y = x+cur.x, y+cur.y
		}
		return pt{x, y}, err
	}
	cubic := func(p0, p1, p2, p3 pt) {
		const n = 32
		prev := p0
		for k := 1; k <= n; k++ {
			t := float64(k) / n
			u := 1 - t
			q := pt{
				u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
				u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
			}
			out = append(out, [2]pt{prev, q})
			prev = q
		}
	}
	for i < len(toks) {
		if c := toks[i]; len(c) == 1 && (c[0] >= 'A' && c[0] <= 'Z' || c[0] >= 'a' && c[0] <= 'z') {
			cmd = c
			i++
		}
		rel := cmd >= "a"
		switch cmd {
		case "M", "m":
			p, err := pair(rel)
			if err != nil {
				return nil, err
			}
			cur = p
			prevCubic = false
			if cmd == "M" {
				cmd = "L"
			} else {
				cmd = "l"
			}
		case "L", "l":
			p, err := pair(rel)
			if err != nil {
				return nil, err
			}
			out = append(out, [2]pt{cur, p})
			cur, prevCubic = p, false
		case "C", "c":
			p1, err1 := pair(rel)
			p2, err2 := pair(rel)
			p3, err3 := pair(rel)
			if err := firstErr(err1, err2, err3); err != nil {
				return nil, err
			}
			cubic(cur, p1, p2, p3)
			cur, ctl2, prevCubic = p3, p2, true
		case "S", "s":
			p1 := cur
			if prevCubic {
				p1 = pt{2*cur.x - ctl2.x, 2*cur.y - ctl2.y}
			}
			p2, err1 := pair(rel)
			p3, err2 := pair(rel)
			if err := firstErr(err1, err2); err != nil {
				return nil, err
			}
			cubic(cur, p1, p2, p3)
			cur, ctl2, prevCubic = p3, p2, true
		default:
			return nil, fmt.Errorf("path %q: unsupported command %q", d, cmd)
		}
	}
	return out, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// render draws the logo at size×size. Coverage comes from exact distances
// (half a pixel of falloff), which antialiases without supersampling.
func (l *logoShape) render(size int, fullBleed bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	// Map the rect onto the canvas when full-bleed, else the whole viewBox.
	vx, vy, vw := 0.0, 0.0, 64.0
	if fullBleed {
		vx, vy, vw = l.x, l.y, l.w
	}
	scale := float64(size) / vw
	hw := l.strokeWidth / 2 * scale
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			// Pixel centre in viewBox units.
			x := vx + (float64(px)+.5)/scale
			y := vy + (float64(py)+.5)/scale
			bg := 1.0
			if !fullBleed {
				bg = clamp01(.5 - l.rectDistance(x, y)*scale)
			}
			if bg == 0 {
				continue
			}
			t := ((x-l.x)/l.w + (y-l.y)/l.h) / 2
			r, g, b := l.gradient(t)
			for _, s := range l.strokes {
				d := math.Inf(1)
				for _, seg := range s.lines {
					d = math.Min(d, segDistance(pt{x, y}, seg[0], seg[1]))
				}
				a := clamp01(.5-(d*scale-hw)) * s.opacity
				r, g, b = r+(255-r)*a, g+(255-g)*a, b+(255-b)*a
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r + .5), uint8(g + .5), uint8(b + .5), uint8(bg*255 + .5)})
		}
	}
	return img
}

// rectDistance is the signed distance from (x,y) to the rounded rect's edge.
func (l *logoShape) rectDistance(x, y float64) float64 {
	cx, cy := l.x+l.w/2, l.y+l.h/2
	qx := math.Abs(x-cx) - (l.w/2 - l.rx)
	qy := math.Abs(y-cy) - (l.h/2 - l.rx)
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return outside + math.Min(math.Max(qx, qy), 0) - l.rx
}

func (l *logoShape) gradient(t float64) (float64, float64, float64) {
	t = clamp01(t)
	s := l.stops
	for i := 1; i < len(s); i++ {
		if t <= s[i].at {
			k := (t - s[i-1].at) / (s[i].at - s[i-1].at)
			return lerp(s[i-1].rgb[0], s[i].rgb[0], k), lerp(s[i-1].rgb[1], s[i].rgb[1], k), lerp(s[i-1].rgb[2], s[i].rgb[2], k)
		}
	}
	last := s[len(s)-1].rgb
	return last[0], last[1], last[2]
}

func segDistance(p, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	t := 0.0
	if l2 := dx*dx + dy*dy; l2 > 0 {
		t = clamp01(((p.x-a.x)*dx + (p.y-a.y)*dy) / l2)
	}
	return math.Hypot(p.x-(a.x+t*dx), p.y-(a.y+t*dy))
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func TestIconPNGs(t *testing.T) {
	for name, size := range map[string]int{"icon-180.png": 180, "icon-192.png": 192} {
		b, err := content.ReadFile("static/" + name)
		if err != nil {
			t.Fatalf("%s: %v (regenerate with VOS_GEN_ICONS=1)", name, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.Width != size || cfg.Height != size {
			t.Errorf("%s is %dx%d, want %dx%d", name, cfg.Width, cfg.Height, size, size)
		}
	}
}

func TestFlattenPath(t *testing.T) {
	segs, err := flattenPath("M0 0l10 0c0 5 5 10 10 10s5 5 10 10")
	if err != nil {
		t.Fatal(err)
	}
	if got := segs[0]; got != [2]pt{{0, 0}, {10, 0}} {
		t.Errorf("first segment = %v", got)
	}
	end := segs[len(segs)-1][1]
	if math.Abs(end.x-30) > 1e-9 || math.Abs(end.y-20) > 1e-9 {
		t.Errorf("path ends at %v, want (30,20)", end)
	}
	if _, err := flattenPath("M0 0 Q1 1 2 2"); err == nil {
		t.Error("unsupported command accepted")
	}
}
