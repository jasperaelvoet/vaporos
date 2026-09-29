package welcome

import (
	"image"
	"image/color"
	"math"

	"github.com/jasperaelvoet/vaporos/internal/brand"
	"golang.org/x/image/font/opentype"
	"rsc.io/qr"
)

// role says what a text item is, so the tests can hold each one to its
// rules (minimum size, contrast, title-safe) whatever grid the look uses.
type role uint8

const (
	roleWordmark  role = iota + 1 // the product name, when it is set as text
	roleStatus                    // the one sentence that says what is going on
	roleDetail                    // the line under it
	roleURL                       // http://vapor.local
	roleIP                        // the same address by IP
	roleCodeLabel                 // "Setup code"
	roleCode                      // the setup code itself
	roleCaption                   // "Scan to open", under the QR card
	roleVersion                   // the image version
	roleProgress                  // a label on the progress bar
)

var roleNames = [...]string{"", "wordmark", "status", "detail", "url", "ip", "codeLabel", "code", "caption", "version", "progress"}

func (r role) String() string {
	if int(r) < len(roleNames) {
		return roleNames[r]
	}
	return "role?"
}

// geometry maps the reference canvas (brand.TVReference: 1920×1080, or
// 1080×1920 when the screen is taller than wide) onto a W×H screen. The
// canvas is scaled uniformly by S and centred, so other aspect ratios get
// bands of background.
type geometry struct {
	W, H       int
	Portrait   bool
	RefW, RefH float64
	S          float64 // reference units to pixels
	OX, OY     float64 // where the reference canvas starts, in pixels

	Screen  image.Rectangle // the whole buffer; only backgrounds and full-width signatures use it
	Safe    image.Rectangle // Screen inset by brand.TVSafeInset on every side (title-safe)
	Content image.Rectangle // the scaled reference canvas
}

func newGeometry(w, h int) geometry {
	g := geometry{W: w, H: h, Portrait: h > w}
	ref := brand.TVReference.Landscape
	if g.Portrait {
		ref = brand.TVReference.Portrait
	}
	g.RefW, g.RefH = float64(ref[0]), float64(ref[1])
	g.S = math.Min(float64(w)/g.RefW, float64(h)/g.RefH)
	g.OX = (float64(w) - g.RefW*g.S) / 2
	g.OY = (float64(h) - g.RefH*g.S) / 2
	g.Screen = image.Rect(0, 0, w, h)
	ix, iy := int(math.Ceil(float64(w)*brand.TVSafeInset)), int(math.Ceil(float64(h)*brand.TVSafeInset))
	g.Safe = image.Rect(ix, iy, w-ix, h-iy)
	g.Content = image.Rect(g.X(0), g.Y(0), g.X(g.RefW), g.Y(g.RefH))
	return g
}

// X and Y map reference coordinates to pixels.
func (g geometry) X(v float64) int { return int(math.Round(g.OX + v*g.S)) }
func (g geometry) Y(v float64) int { return int(math.Round(g.OY + v*g.S)) }

// Len maps a reference length to pixels, at least one.
func (g geometry) Len(v float64) int { return max(1, int(math.Round(v*g.S))) }

// layout is everything Render draws, computed without touching pixels so
// the tests can check positions against the rendered image. Rectangles are
// in pixels, and an empty one is not drawn. The look fills it (arrange);
// which regions it uses is up to the look.
type layout struct {
	geometry
	Tone      brand.State // the state's tone; a word outside the vocabulary is neutral ("")
	Attention string      // "pair" while a device waits for its PIN
	Percent   int         // the state's progress, clamped to 0..100

	Texts []textItem

	Mark      image.Rectangle // the vector mark
	Signature image.Rectangle // the look's state element (the draft: the bar under the wordmark)
	Progress  image.Rectangle // the progress track; empty when there is no bar
	Callout   image.Rectangle // the attention highlight, under the line it points at
	Panel     image.Rectangle // the plate behind the setup code
	QR        *qr.Code
	QRCard    image.Rectangle // the light card, quiet zone included
	QRRadius  int             // the card's corner radius: its corners show what is behind
	QRAt      image.Point     // top-left pixel of module (0,0)
	Module    int             // pixels per module
	QRFrame   image.Rectangle // decoration around the card, outside it
}

// computeLayout lays st out on a w×h screen with the compiled-in look.
func computeLayout(st State, w, h int) *layout {
	loadFonts()
	tone, _ := brand.ParseState(string(st.Tone))
	l := &layout{geometry: newGeometry(w, h), Tone: tone, Attention: st.Attention, Percent: min(max(st.Progress, 0), 100)}
	if st.QR != "" {
		if code, err := qr.Encode(st.QR, qr.M); err == nil {
			l.QR = code
		}
	}
	theLook.arrange(l, st)
	return l
}

// placeQR puts the code in a square of side size reference units at (x, y).
// The module is the largest whole number of pixels that fits the code and
// its quiet zone of brand.TVQR.QuietModules modules on every side; the card
// is a whole number of modules, centred in the square, with corners rounded
// by one module so the quiet zone stays whole.
func (l *layout) placeQR(x, y, size float64) {
	if l.QR == nil {
		return
	}
	quiet := brand.TVQR.QuietModules
	side := l.Len(size)
	n := l.QR.Size + 2*quiet
	l.Module = max(1, side/n)
	inner := l.Module * n
	cx, cy := l.X(x)+(side-inner)/2, l.Y(y)+(side-inner)/2
	l.QRCard = image.Rect(cx, cy, cx+inner, cy+inner)
	l.QRAt = image.Pt(cx+quiet*l.Module, cy+quiet*l.Module)
	l.QRRadius = l.Module
}

// addText lays out one line at reference (x, baseline y) in f at px
// reference pixels, shrunk until it fits maxW reference units, and returns
// its index in l.Texts.
func (l *layout) addText(r role, text string, f *opentype.Font, px float64, c color.RGBA, x, y, maxW float64) int {
	size := fitSize(f, text, l.Len(px), l.Len(maxW))
	return l.place(textItem{Role: r, Text: text, Font: f, Px: size, Color: c, Dot: image.Pt(l.X(x), l.Y(y))})
}

// place adds t as it is, measures its ink and returns its index.
func (l *layout) place(t textItem) int {
	t.Rect = inkBounds(t)
	l.Texts = append(l.Texts, t)
	return len(l.Texts) - 1
}

// shift moves text item i right by dx pixels.
func (l *layout) shift(i, dx int) {
	l.Texts[i].Dot.X += dx
	l.Texts[i].Rect = l.Texts[i].Rect.Add(image.Pt(dx, 0))
}
