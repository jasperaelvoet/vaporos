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
	Focus image.Rectangle // what the background centres on, usually the QR card
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
