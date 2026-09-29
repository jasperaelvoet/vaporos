package brand

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/vector"
)

// The mark's drawings (Mark, MarkLight, MarkMono, Wordmark, AppIcon and
// AppIconSmall) are generated into mark_gen.go from design/logo.svg, which
// describes each one and the subset it is drawn in. This file is the one
// rasterizer every raster of them goes through: the TV's brand corner and the
// PNG app icons alike, so a raster can never drift from the source.

// Paint is how a path is filled or stroked.
type Paint struct {
	Color   color.NRGBA // used when Current is false; alpha 0 means none
	Current bool        // currentColor: the caller's colour
}

func (p Paint) visible() bool { return p.Current || p.Color.A > 0 }

// Path is one path of a drawing, in viewBox units.
type Path struct {
	Group  string // "tile", "field" or "glyph" in the app icons; "" elsewhere
	Class  string // a role the caller may recolour: "band", "core", "line", "vapor", "os"
	Fill   Paint  // nonzero winding
	Stroke Paint  // round caps and joins
	Width  float32
	Ops    string    // one byte per segment: M, L, Q, C or Z (absolute)
	Pts    []float32 // the segments' coordinates, x then y: 2 for M and L, 4 for Q, 6 for C
}

// Drawing is one drawing of design/logo.svg.
type Drawing struct {
	ViewBox [4]float32 // min-x, min-y, width, height
	Paths   []Path
}

// DrawMark draws the compact mark (the white-hot core in one amber band) into
// r of dst. colors recolours paths by class ("band", "core", "line"), e.g.
// {"line": h0} on the TV's darker ground.
func DrawMark(dst draw.Image, r image.Rectangle, colors map[string]color.Color) {
	Mark.Draw(dst, r, colors)
}

// Draw paints d over dst, scaled uniformly to fit r and centred in it, and
// clipped to r. colors maps a path's class to a colour that replaces its
// paint; "currentColor" gives currentColor paths their colour (bone when
// unset).
func (d *Drawing) Draw(dst draw.Image, r image.Rectangle, colors map[string]color.Color) {
	vw, vh := float64(d.ViewBox[2]), float64(d.ViewBox[3])
	if vw <= 0 || vh <= 0 || r.Empty() {
		return
	}
	s := math.Min(float64(r.Dx())/vw, float64(r.Dy())/vh)
	t := xform{
		s:  s,
		ox: float64(r.Min.X) + (float64(r.Dx())-vw*s)/2 - float64(d.ViewBox[0])*s,
		oy: float64(r.Min.Y) + (float64(r.Dy())-vh*s)/2 - float64(d.ViewBox[1])*s,
	}
	clip := r.Intersect(dst.Bounds())
	for i := range d.Paths {
		d.Paths[i].draw(dst, clip, t, colors)
	}
}

// Group returns the paths of one group ("tile", "field", "glyph") as a
// drawing with the same viewBox.
func (d *Drawing) Group(name string) *Drawing {
	g := &Drawing{ViewBox: d.ViewBox}
	for _, p := range d.Paths {
		if p.Group == name {
			g.Paths = append(g.Paths, p)
		}
	}
	return g
}

// TileColor is the fill of an app icon's tile: the colour a full-bleed icon
// fills its square with.
func (d *Drawing) TileColor() color.NRGBA {
	for _, p := range d.Paths {
		if p.Group == "tile" {
			return p.Fill.Color
		}
	}
	return color.NRGBA{}
}

// RenderIcon rasterizes an app icon (AppIcon or AppIconSmall) at size×size.
// Rounded keeps the tile's rounded corners transparent (the manifest's icons);
// full-bleed fills the square with the tile colour, for platforms that mask
// the icon themselves (iOS, maskable).
func RenderIcon(d *Drawing, size int, fullBleed bool) *image.NRGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	if fullBleed {
		draw.Draw(img, img.Rect, image.NewUniform(d.TileColor()), image.Point{}, draw.Src)
	}
	d.Draw(img, img.Rect, nil)
	out := image.NewNRGBA(img.Rect)
	draw.Draw(out, out.Rect, img, image.Point{}, draw.Src)
	return out
}

// xform maps viewBox units to pixels: x*s + ox, y*s + oy.
type xform struct{ s, ox, oy float64 }

func (t xform) pt(x, y float32) pt {
	return pt{float64(float64(x)*t.s) + t.ox, float64(float64(y)*t.s) + t.oy}
}

type pt struct{ x, y float64 }

func (p *Path) paint(pa Paint, colors map[string]color.Color) color.Color {
	if c, ok := colors[p.Class]; ok && p.Class != "" {
		return c
	}
	if pa.Current {
		if c, ok := colors["currentColor"]; ok {
			return c
		}
		return Palette.Bone
	}
	return pa.Color
}

func (p *Path) draw(dst draw.Image, clip image.Rectangle, t xform, colors map[string]color.Color) {
	if p.Fill.visible() {
		if m := p.fillMask(t, clip); m != nil {
			draw.DrawMask(dst, m.Rect, image.NewUniform(p.paint(p.Fill, colors)), image.Point{}, m, m.Rect.Min, draw.Over)
		}
	}
	if p.Stroke.visible() && p.Width > 0 {
		if m := strokeMask(p.polylines(t), float64(p.Width)*t.s/2, clip); m != nil {
			draw.DrawMask(dst, m.Rect, image.NewUniform(p.paint(p.Stroke, colors)), image.Point{}, m, m.Rect.Min, draw.Over)
		}
	}
}

// segments calls fn for every segment of p in pixels. For M, L and Z it gets
// the end point; for Q and C the control points too (in order).
func (p *Path) segments(t xform, fn func(op byte, q []pt)) {
	var buf [3]pt
	k := 0
	for i := 0; i < len(p.Ops); i++ {
		op := p.Ops[i]
		n := 0
		switch op {
		case 'M', 'L':
			n = 1
		case 'Q':
			n = 2
		case 'C':
			n = 3
		}
		for j := 0; j < n; j++ {
			buf[j] = t.pt(p.Pts[k], p.Pts[k+1])
			k += 2
		}
		fn(op, buf[:n])
	}
}

func (p *Path) bounds(t xform) (lo, hi pt) {
	lo, hi = pt{math.Inf(1), math.Inf(1)}, pt{math.Inf(-1), math.Inf(-1)}
	p.segments(t, func(_ byte, q []pt) {
		for _, v := range q {
			lo.x, lo.y = math.Min(lo.x, v.x), math.Min(lo.y, v.y)
			hi.x, hi.y = math.Max(hi.x, v.x), math.Max(hi.y, v.y)
		}
	})
	return lo, hi
}

// box is the pixel rectangle around lo..hi grown by pad, clipped.
func box(lo, hi pt, pad float64, clip image.Rectangle) image.Rectangle {
	if lo.x > hi.x {
		return image.Rectangle{}
	}
	return image.Rect(int(math.Floor(lo.x-pad)), int(math.Floor(lo.y-pad)),
		int(math.Ceil(hi.x+pad)), int(math.Ceil(hi.y+pad))).Intersect(clip)
}

// fillMask rasterizes p's fill with exact area coverage. Every subpath is
// closed, as SVG closes it for filling.
func (p *Path) fillMask(t xform, clip image.Rectangle) *image.Alpha {
	lo, hi := p.bounds(t)
	b := box(lo, hi, 1, clip)
	if b.Empty() {
		return nil
	}
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	z.DrawOp = draw.Src
	fx := func(v pt) (float32, float32) { return float32(v.x - float64(b.Min.X)), float32(v.y - float64(b.Min.Y)) }
	open := false
	p.segments(t, func(op byte, q []pt) {
		switch op {
		case 'M':
			if open {
				z.ClosePath()
			}
			z.MoveTo(fx(q[0]))
			open = true
		case 'L':
			z.LineTo(fx(q[0]))
		case 'Q':
			x1, y1 := fx(q[0])
			x2, y2 := fx(q[1])
			z.QuadTo(x1, y1, x2, y2)
		case 'C':
			x1, y1 := fx(q[0])
			x2, y2 := fx(q[1])
			x3, y3 := fx(q[2])
			z.CubeTo(x1, y1, x2, y2, x3, y3)
		case 'Z':
			z.ClosePath()
			open = false
		}
	})
	if open {
		z.ClosePath()
	}
	m := image.NewAlpha(image.Rect(0, 0, b.Dx(), b.Dy()))
	z.Draw(m, m.Rect, image.Opaque, image.Point{})
	m.Rect = b // same pixels, placed where they belong
	return m
}

// flatTolerance is how far, in pixels, a flattened curve may stray.
const flatTolerance = 0.08

// polylines flattens p into polylines in pixels under t, one per subpath;
// a closed subpath ends where it started.
func (p *Path) polylines(t xform) [][]pt {
	var out [][]pt
	var cur []pt
	var start pt
	p.segments(t, func(op byte, q []pt) {
		switch op {
		case 'M':
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur, start = []pt{q[0]}, q[0]
		case 'L':
			cur = append(cur, q[0])
		case 'Q':
			a := cur[len(cur)-1]
			n := steps(dev(a, q[0], q[1]) / 4)
			for i := 1; i <= n; i++ {
				s := float64(i) / float64(n)
				u := 1 - s
				cur = append(cur, pt{
					float64(u*u*a.x) + float64(2*u*s*q[0].x) + float64(s*s*q[1].x),
					float64(u*u*a.y) + float64(2*u*s*q[0].y) + float64(s*s*q[1].y),
				})
			}
		case 'C':
			a := cur[len(cur)-1]
			n := steps(0.75 * math.Max(dev(a, q[0], q[1]), dev(q[0], q[1], q[2])))
			for i := 1; i <= n; i++ {
				s := float64(i) / float64(n)
				u := 1 - s
				cur = append(cur, pt{
					float64(u*u*u*a.x) + float64(3*u*u*s*q[0].x) + float64(3*u*s*s*q[1].x) + float64(s*s*s*q[2].x),
					float64(u*u*u*a.y) + float64(3*u*u*s*q[0].y) + float64(3*u*s*s*q[1].y) + float64(s*s*s*q[2].y),
				})
			}
		case 'Z':
			if len(cur) > 0 {
				cur = append(cur, start)
				out = append(out, cur)
			}
			cur = nil
		}
	})
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// dev is |a - 2b + c|, the second difference that bounds a curve's
// distance from its chord.
func dev(a, b, c pt) float64 { return math.Hypot(a.x-2*b.x+c.x, a.y-2*b.y+c.y) }

// steps is how many line segments keep a curve with second difference d
// within flatTolerance.
func steps(d float64) int {
	n := int(math.Ceil(math.Sqrt(d / flatTolerance)))
	return max(1, min(n, 512))
}

// strokeMask covers every pixel within half-width h of the polylines, with a
// one-pixel ramp at the edge: round caps and joins, and the union of
// overlapping parts, come out exact.
func strokeMask(polys [][]pt, h float64, clip image.Rectangle) *image.Alpha {
	lo, hi := pt{math.Inf(1), math.Inf(1)}, pt{math.Inf(-1), math.Inf(-1)}
	for _, pl := range polys {
		for _, v := range pl {
			lo.x, lo.y = math.Min(lo.x, v.x), math.Min(lo.y, v.y)
			hi.x, hi.y = math.Max(hi.x, v.x), math.Max(hi.y, v.y)
		}
	}
	b := box(lo, hi, h+1, clip)
	if b.Empty() {
		return nil
	}
	cov := make([]float64, b.Dx()*b.Dy())
	seg := func(a, c pt) {
		sb := box(pt{math.Min(a.x, c.x), math.Min(a.y, c.y)}, pt{math.Max(a.x, c.x), math.Max(a.y, c.y)}, h+1, b)
		dx, dy := c.x-a.x, c.y-a.y
		l2 := float64(dx*dx) + float64(dy*dy)
		for y := sb.Min.Y; y < sb.Max.Y; y++ {
			py := float64(y) + 0.5
			row := cov[(y-b.Min.Y)*b.Dx() : (y-b.Min.Y+1)*b.Dx()]
			for x := sb.Min.X; x < sb.Max.X; x++ {
				px := float64(x) + 0.5
				u := 0.0
				if l2 > 0 {
					u = math.Max(0, math.Min(1, (float64((px-a.x)*dx)+float64((py-a.y)*dy))/l2))
				}
				v := h + 0.5 - math.Hypot(px-(a.x+float64(u*dx)), py-(a.y+float64(u*dy)))
				if i := x - b.Min.X; v > row[i] {
					row[i] = math.Min(v, 1)
				}
			}
		}
	}
	for _, pl := range polys {
		if len(pl) == 1 {
			seg(pl[0], pl[0])
		}
		for i := 1; i < len(pl); i++ {
			seg(pl[i-1], pl[i])
		}
	}
	m := image.NewAlpha(b)
	for i, v := range cov {
		if v > 0 {
			m.Pix[i] = uint8(math.Round(v * 255))
		}
	}
	return m
}
