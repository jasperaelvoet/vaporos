package welcome

import (
	"image"
	"image/color"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// The faces the renderer draws with, parsed once. Go Regular also stays as
// the fallback for device names in Latin Extended, Greek and Cyrillic when a
// direction's own faces arrive in fonts/.
var (
	fontsOnce                       sync.Once
	fontRegular, fontBold, fontMono *opentype.Font

	facesMu sync.Mutex
	faces   = map[faceKey]font.Face{}
)

type faceKey struct {
	f  *opentype.Font
	px int
}

func loadFonts() {
	fontsOnce.Do(func() {
		fontRegular = mustParse(goregular.TTF)
		fontBold = mustParse(gobold.TTF)
		fontMono = mustParse(gomonobold.TTF)
	})
}

func mustParse(b []byte) *opentype.Font {
	f, err := opentype.Parse(b)
	if err != nil {
		panic("welcome: embedded font: " + err.Error())
	}
	return f
}

// face returns a cached face of f at px pixels (72 DPI: points == pixels).
func face(f *opentype.Font, px int) font.Face {
	px = max(px, 6)
	facesMu.Lock()
	defer facesMu.Unlock()
	k := faceKey{f, px}
	if fc, ok := faces[k]; ok {
		return fc
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(px), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic("welcome: font face: " + err.Error())
	}
	faces[k] = fc
	return fc
}

// textItem is one line of text, positioned by its baseline origin.
type textItem struct {
	Role  role
	Text  string
	Font  *opentype.Font
	Px    int
	Color color.RGBA
	Dot   image.Point
	Rect  image.Rectangle // ink bounds, for tests
}

// inkBounds is the box the glyphs of t cover, in pixels.
func inkBounds(t textItem) image.Rectangle {
	b, _ := font.BoundString(face(t.Font, t.Px), t.Text)
	return image.Rect(t.Dot.X+b.Min.X.Floor(), t.Dot.Y+b.Min.Y.Floor(), t.Dot.X+b.Max.X.Ceil(), t.Dot.Y+b.Max.Y.Ceil())
}

// advance is how far the pen moves over text, rounded up to a pixel.
func advance(f *opentype.Font, px int, text string) int {
	return font.MeasureString(face(f, px), text).Ceil()
}

// fitSize shrinks px until text fits in maxW pixels, down to a floor of
// 8 px.
func fitSize(f *opentype.Font, text string, px, maxW int) int {
	for px > 8 {
		w := advance(f, px, text)
		if w <= maxW {
			break
		}
		px = max(8, int(float64(px)*float64(maxW)/float64(w)))
		if advance(f, px, text) <= maxW {
			break
		}
		px--
	}
	return px
}

// fit shrinks text to fit maxW pixels (fitSize); a line too long even at
// the floor is cut short with an ellipsis, so it never runs into the QR
// card or off a small panel.
func fit(f *opentype.Font, text string, px, maxW int) (string, int) {
	px = fitSize(f, text, px, maxW)
	if advance(f, px, text) <= maxW {
		return text, px
	}
	r := []rune(strings.TrimSpace(text))
	for len(r) > 0 && advance(f, px, strings.TrimSpace(string(r))+"…") > maxW {
		r = r[:len(r)-1]
	}
	if len(r) == 0 {
		return "", px
	}
	return strings.TrimSpace(string(r)) + "…", px
}

// drawText draws t onto img, with kerning.
func drawText(img *image.RGBA, t textItem) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(t.Color), Face: face(t.Font, t.Px), Dot: fixed.P(t.Dot.X, t.Dot.Y)}
	d.DrawString(t.Text)
}
