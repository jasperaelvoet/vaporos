package brand

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"
)

// The rasterizer on shapes whose coverage is known.
func TestRasterizer(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	d := &Drawing{ViewBox: [4]float32{0, 0, 20, 20}, Paths: []Path{
		// A filled square from 2 to 8, then a 4-wide stroke along y = 14.
		{Fill: Paint{Color: color.NRGBA{255, 0, 0, 255}}, Ops: "MLLLZ", Pts: []float32{2, 2, 8, 2, 8, 8, 2, 8}},
		{Stroke: Paint{Current: true}, Width: 4, Ops: "ML", Pts: []float32{4, 14, 16, 14}},
	}}
	d.Draw(img, img.Rect, map[string]color.Color{"currentColor": color.NRGBA{0, 0, 255, 255}})
	at := func(x, y int) color.RGBA { return img.RGBAAt(x, y) }
	for _, c := range []struct {
		x, y int
		want color.RGBA
	}{
		{2, 2, color.RGBA{255, 0, 0, 255}}, {7, 7, color.RGBA{255, 0, 0, 255}}, {8, 8, color.RGBA{}}, {1, 5, color.RGBA{}},
		{10, 12, color.RGBA{0, 0, 255, 255}}, {10, 15, color.RGBA{0, 0, 255, 255}}, {10, 11, color.RGBA{}}, {10, 16, color.RGBA{}},
		{3, 14, color.RGBA{0, 0, 255, 255}}, // the round cap reaches 2 past the end
		{0, 14, color.RGBA{}},
	} {
		if got := at(c.x, c.y); got != c.want {
			t.Errorf("pixel (%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
	// Scaled into a larger rectangle, the drawing keeps its proportions and is centred.
	big := image.NewRGBA(image.Rect(0, 0, 60, 40))
	d.Draw(big, big.Rect, nil)
	if got := big.RGBAAt(10+2*2, 2*2); got.R != 255 {
		t.Errorf("scaled square missing at (14,4): %v", got)
	}
	if got := big.RGBAAt(5, 20); got.A != 0 {
		t.Errorf("letterbox painted at (5,20): %v", got)
	}
}

// TestMaskableSafeZone: platforms may crop a maskable icon to the circle of
// radius 0.4 × size, so every pixel of the glyph lies inside it, and the
// full-bleed icon has no transparent pixel for a mask to reveal.
func TestMaskableSafeZone(t *testing.T) {
	const size = 512
	glyph := RenderIcon(AppIcon.Group("glyph"), size, false)
	if len(AppIcon.Group("glyph").Paths) == 0 {
		t.Fatal(`AppIcon has no <g class="glyph">`)
	}
	limit := 0.4*size + 1
	out, reach := 0, 0.0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if glyph.NRGBAAt(x, y).A == 0 {
				continue
			}
			d := math.Hypot(float64(x)+0.5-size/2, float64(y)+0.5-size/2)
			reach = math.Max(reach, d)
			if d > limit {
				out++
			}
		}
	}
	if out > 0 {
		t.Errorf("%d glyph pixels lie outside the safe circle (radius %.1f); the glyph reaches %.1f", out, limit, reach)
	}
	full := RenderIcon(AppIcon, size, true)
	for i := 3; i < len(full.Pix); i += 4 {
		if full.Pix[i] != 255 {
			t.Fatalf("the full-bleed icon has a transparent pixel at %d", i/4)
		}
	}
	t.Logf("the glyph reaches %.1f px from the centre; the safe circle is %.1f", reach, limit)
}

// The chrome marks stay compact: the one amber band and the core, nothing
// that reads as a neon logo at 16 px.
func TestMarkIsCompact(t *testing.T) {
	var bands []string
	for _, p := range Mark.Paths {
		if p.Class == "band" {
			bands = append(bands, fmt.Sprint(p.Stroke.Color))
		}
	}
	if len(bands) != 1 {
		t.Errorf("the compact mark has %d bands (%v), want one", len(bands), bands)
	}
	if MarkMono.Paths[0].Stroke.Current != true || len(MarkMono.Paths) != 1 {
		t.Error("the one-colour mark is one currentColor path")
	}
}

// TestIconEdgeIsOneColour: the app icons' outermost opaque pixels are all
// the tile's colour, rounded and full-bleed. An isotherm that crosses the
// tile's edge leaves a colder wedge in a corner, and on light browser chrome
// that wedge reads as a square black corner.
func TestIconEdgeIsOneColour(t *testing.T) {
	const size = 512
	for _, ic := range []struct {
		name string
		d    *Drawing
	}{{"icon", AppIcon}, {"icon-small", AppIconSmall}} {
		tile := ic.d.TileColor()
		img := RenderIcon(ic.d, size, false)
		opaque := func(x, y int) bool {
			return x >= 0 && y >= 0 && x < size && y < size && img.NRGBAAt(x, y).A == 255
		}
		off := 0
		var first image.Point
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if !opaque(x, y) {
					continue
				}
				edge := false
				for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
					if !opaque(x+d[0], y+d[1]) {
						edge = true
						break
					}
				}
				if edge && img.NRGBAAt(x, y) != tile {
					if off == 0 {
						first = image.Pt(x, y)
					}
					off++
				}
			}
		}
		if off > 0 {
			t.Errorf("%s: %d edge pixels are not the tile's colour %v, the first at %v (%v)", ic.name, off, tile, first, img.NRGBAAt(first.X, first.Y))
		}
		full := RenderIcon(ic.d, size, true)
		for i := 0; i < size; i++ {
			for _, p := range []image.Point{{i, 0}, {i, size - 1}, {0, i}, {size - 1, i}} {
				if c := full.NRGBAAt(p.X, p.Y); c != tile {
					t.Fatalf("%s full-bleed: the border pixel %v is %v, not the tile's colour %v", ic.name, p, c, tile)
				}
			}
		}
	}
}
