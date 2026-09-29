package welcome

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"sync"
	"sync/atomic"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// look is a direction's TV still: its colours, its grid and the layers only
// it draws. Exactly one is compiled in (theLook, in look_<direction>.go);
// layout.go and paint.go hold what every look shares.
type look interface {
	// palette returns the look's colours. paint takes the QR card's two
	// from it and nothing else.
	palette() palette
	// arrange lays st out on l: the texts, the QR card and the regions.
	arrange(l *layout, st State)
	// backdrop names everything the background depends on. Render caches
	// the background on it, so it must not hold more than that.
	backdrop(l *layout) backdrop
	// background paints the full-bleed layer for b into dst.
	background(dst *image.RGBA, b backdrop)
	// decorate paints the signature, the plates, the frames and the
	// progress bar over the background, never inside l.QRCard.
	decorate(dst *image.RGBA, l *layout)
}

// palette is a look's colours, named after the tokens' TV colour roles
// (brand.TVCanvas, brand.TVInk and so on) so that a look built on the tokens
// fills it field for field.
type palette struct {
	Canvas, Canvas2   color.RGBA // the background
	Surface, Surface2 color.RGBA // plates and tracks
	Ink, Ink2         color.RGBA // text, secondary text
	Accent            color.RGBA
	QRDark, QRLight   color.RGBA // the QR card: exactly these two, nothing else inside it
}

// rgb unpacks 0xRRGGBB into an opaque colour.
func rgb(v uint32) color.RGBA {
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

// opaque converts a token colour; the tokens' TV and state colours are opaque.
func opaque(c color.NRGBA) color.RGBA { return color.RGBA{R: c.R, G: c.G, B: c.B, A: 0xff} }

// Render draws the welcome screen for st at w×h pixels. It shares font
// faces between calls, so one goroutine renders at a time.
func Render(st State, w, h int) *image.RGBA {
	return paint(computeLayout(st, w, h), true)
}

// paint draws l in layers, each over the last:
//  1. the background, full-bleed, from the cache;
//  2. the look's decoration: signature, plates, frames, progress bar;
//  3. the QR card, in the palette's two QR colours;
//  4. the text, unless text is false (the contrast test measures the
//     pixels under each line that way).
func paint(l *layout, text bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, l.W, l.H))
	copy(img.Pix, background(theLook.backdrop(l)).Pix)
	theLook.decorate(img, l)
	pal := theLook.palette()
	paintQR(img, l, pal.QRDark, pal.QRLight)
	if text {
		for _, t := range l.Texts {
			drawText(img, t)
		}
	}
	return img
}

// paintQR fills the card in light, then each dark module with draw.Src, so
// the code is exactly two colours.
func paintQR(img *image.RGBA, l *layout, dark, light color.RGBA) {
	if l.QR == nil {
		return
	}
	fillRounded(img, l.QRCard, l.QRRadius, light)
	src := image.NewUniform(dark)
	for y := 0; y < l.QR.Size; y++ {
		for x := 0; x < l.QR.Size; x++ {
			if l.QR.Black(x, y) {
				r := image.Rect(0, 0, l.Module, l.Module).Add(l.QRAt.Add(image.Pt(x*l.Module, y*l.Module)))
				draw.Draw(img, r, src, image.Point{}, draw.Src)
			}
		}
	}
}

// fillBar draws a progress bar: a rounded track, then a fill from its left
// end over percent of its width (barFill).
func fillBar(img *image.RGBA, track image.Rectangle, percent int, trackC, fillC color.RGBA) {
	if track.Empty() {
		return
	}
	fillRounded(img, track, track.Dy()/2, trackC)
	if w := barFill(track, percent); w > 0 {
		fill := track
		fill.Max.X = fill.Min.X + w
		fillRounded(img, fill, fill.Dy()/2, fillC)
	}
}

// barFill is the width of the fill: round(percent/100 × the track's width).
func barFill(track image.Rectangle, percent int) int {
	return int(math.Round(float64(min(max(percent, 0), 100)) / 100 * float64(track.Dx())))
}

// backdrop is everything a look's background may depend on. It is the
// background cache's key, so it stays small and comparable.
type backdrop struct {
	W, H  int
	Tone  brand.State     // zero when the look's background ignores the tone
	Pair  bool            // a device waits for its PIN, when the look's background shows it
	Focus image.Rectangle // what the background centres on, usually the QR card's slot
	Bloom bool            // no card in Focus yet (starting): a look may centre a smaller shape there
}

// The background cache: the bgCacheSize most recently used backgrounds, and
// no more than bgCacheBytes of them. A status-only change (install
// progress, a download percentage) then copies the background instead of
// painting it again.
const (
	bgCacheSize  = 4
	bgCacheBytes = 96 << 20 // two 4K backgrounds and two 1080p ones
)

type bgKey struct {
	backdrop
	tokens string // brand.TokensSHA: new tokens, new backgrounds
}

var (
	bgMu      sync.Mutex
	bgEntries []bgEntry // most recently used first
	bgPainted atomic.Int64
)

type bgEntry struct {
	key bgKey
	img *image.RGBA
}

// background returns the look's background for b. The result is shared:
// callers copy it and never draw on it.
func background(b backdrop) *image.RGBA {
	k := bgKey{b, brand.TokensSHA}
	bgMu.Lock()
	for i, e := range bgEntries {
		if e.key == k {
			copy(bgEntries[1:i+1], bgEntries[:i])
			bgEntries[0] = e
			bgMu.Unlock()
			return e.img
		}
	}
	bgMu.Unlock()

	img := image.NewRGBA(image.Rect(0, 0, b.W, b.H))
	theLook.background(img, b)
	bgPainted.Add(1)

	bgMu.Lock()
	defer bgMu.Unlock()
	bgEntries = append([]bgEntry{{k, img}}, bgEntries...)
	size := 0
	for i, e := range bgEntries {
		size += len(e.img.Pix)
		if i > 0 && (i >= bgCacheSize || size > bgCacheBytes) {
			clear(bgEntries[i:])
			bgEntries = bgEntries[:i]
			break
		}
	}
	return img
}

// fillRounded fills r with radius-rad corners, anti-aliased by 4x4
// supersampling at the corners only.
func fillRounded(img *image.RGBA, r image.Rectangle, rad int, c color.RGBA) {
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	rad = min(rad, r.Dx()/2, r.Dy()/2)
	fr := float64(rad)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			// Distance into the corner square, if in one.
			cx, cy := -1.0, -1.0
			switch {
			case x < r.Min.X+rad:
				cx = float64(r.Min.X + rad)
			case x >= r.Max.X-rad:
				cx = float64(r.Max.X - rad)
			}
			switch {
			case y < r.Min.Y+rad:
				cy = float64(r.Min.Y + rad)
			case y >= r.Max.Y-rad:
				cy = float64(r.Max.Y - rad)
			}
			cov := 1.0
			if cx >= 0 && cy >= 0 {
				n := 0
				for sy := 0; sy < 4; sy++ {
					for sx := 0; sx < 4; sx++ {
						px := float64(x) + (float64(sx)+0.5)/4
						py := float64(y) + (float64(sy)+0.5)/4
						if (px-cx)*(px-cx)+(py-cy)*(py-cy) <= fr*fr {
							n++
						}
					}
				}
				cov = float64(n) / 16
			}
			if cov <= 0 {
				continue
			}
			i := img.PixOffset(x, y)
			p := img.Pix[i : i+4 : i+4]
			p[0] = uint8(float64(p[0]) + (float64(c.R)-float64(p[0]))*cov)
			p[1] = uint8(float64(p[1]) + (float64(c.G)-float64(p[1]))*cov)
			p[2] = uint8(float64(p[2]) + (float64(c.B)-float64(p[2]))*cov)
			p[3] = 0xff
		}
	}
}

// fillRect paints r in c.
func fillRect(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	r = r.Intersect(img.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.Pix[img.PixOffset(r.Min.X, y):img.PixOffset(r.Max.X, y)]
		for i := 0; i < len(row); i += 4 {
			row[i], row[i+1], row[i+2], row[i+3] = c.R, c.G, c.B, 0xff
		}
	}
}

// blendPx mixes c into the pixel at (x, y) by a.
func blendPx(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	if !(image.Point{x, y}).In(img.Rect) || a <= 0 {
		return
	}
	o := img.PixOffset(x, y)
	p := img.Pix[o : o+4 : o+4]
	p[0] = uint8(float64(p[0]) + (float64(c.R)-float64(p[0]))*a + 0.5)
	p[1] = uint8(float64(p[1]) + (float64(c.G)-float64(p[1]))*a + 0.5)
	p[2] = uint8(float64(p[2]) + (float64(c.B)-float64(p[2]))*a + 0.5)
	p[3] = 0xff
}

// roundDist is the signed distance from the centre of pixel (x, y) to the
// edge of r with corners of radius rad: negative inside.
func roundDist(r image.Rectangle, rad float64, x, y int) float64 {
	cx, cy := float64(r.Min.X+r.Max.X)/2, float64(r.Min.Y+r.Max.Y)/2
	hw, hh := float64(r.Dx())/2, float64(r.Dy())/2
	rad = min(rad, hw, hh)
	qx, qy := math.Abs(float64(x)+0.5-cx)-(hw-rad), math.Abs(float64(y)+0.5-cy)-(hh-rad)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - rad
}

// blendRound fills r, its corners rounded by rad, with c at opacity a,
// anti-aliased by the distance to its edge.
func blendRound(img *image.RGBA, r image.Rectangle, rad float64, c color.RGBA, a float64) {
	for y := max(r.Min.Y, img.Rect.Min.Y); y < min(r.Max.Y, img.Rect.Max.Y); y++ {
		for x := max(r.Min.X, img.Rect.Min.X); x < min(r.Max.X, img.Rect.Max.X); x++ {
			blendPx(img, x, y, c, math.Min(1, math.Max(0, 0.5-roundDist(r, rad, x, y)))*a)
		}
	}
}

// blendRing draws the inner w pixels of r's rounded edge in c at opacity a.
func blendRing(img *image.RGBA, r image.Rectangle, rad, w float64, c color.RGBA, a float64) {
	for y := max(r.Min.Y, img.Rect.Min.Y); y < min(r.Max.Y, img.Rect.Max.Y); y++ {
		for x := max(r.Min.X, img.Rect.Min.X); x < min(r.Max.X, img.Rect.Max.X); x++ {
			d := roundDist(r, rad, x, y)
			if d < -w-1 {
				continue
			}
			cov := math.Min(1, math.Max(0, 0.5-d)) - math.Min(1, math.Max(0, 0.5-(d+w)))
			blendPx(img, x, y, c, cov*a)
		}
	}
}
