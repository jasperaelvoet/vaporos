package welcome

import (
	"embed"
	"image"
	"image/color"
	"path"
	"strings"
	"sync"
	"unicode"

	"github.com/jasperaelvoet/vaporos/internal/brand"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// The TV's faces are the static cuts tools/fonts/fonts.py writes into fonts/
// (TOK §2.7), each next to its family's OFL text, which travels in the binary
// with it. sfnt reads neither WOFF nor variations, so every width and weight
// the screen uses is its own TrueType file.
//
//go:embed fonts/*.ttf fonts/*.txt
var fontFiles embed.FS

// tvFontFiles names the cut in fonts/ for each "<font>/<face>" a TV type role
// (brand.TVType) draws with; brand.TVFontFiles wins once the tokens name them.
var tvFontFiles = map[string]string{
	"display/cold":  "anybody-cold.ttf",
	"display/warm":  "anybody-warm.ttf",
	"display/hot":   "anybody-hot.ttf",
	"ui/regular":    "monasans-regular.ttf",
	"mono/medium":   "martianmono-medium.ttf",
	"mono/semibold": "martianmono-semibold.ttf",
}

// The faces the renderer draws with, parsed once. Go Regular is the
// fallback: it draws the Greek, Cyrillic and rarer Latin letters of a device
// name that the brand cuts leave out.
var (
	fontsOnce    sync.Once
	tvFonts      map[string]*opentype.Font // by "<font>/<face>"
	fontFallback *opentype.Font

	facesMu sync.Mutex
	faces   = map[faceKey]font.Face{}
)

type faceKey struct {
	f  *opentype.Font
	px int
}

func loadFonts() {
	fontsOnce.Do(func() {
		fontFallback = mustParse(goregular.TTF)
		tvFonts = map[string]*opentype.Font{}
		for key := range tvFontFiles {
			b, err := fontFiles.ReadFile(path.Join("fonts", fontFile(key)))
			if err != nil {
				panic("welcome: embedded font: " + err.Error())
			}
			tvFonts[key] = mustParse(b)
		}
	})
}

// fontFile is the file in fonts/ that draws key ("display/warm").
func fontFile(key string) string {
	if f, ok := brand.TVFontFiles[key]; ok {
		return path.Base(f)
	}
	return tvFontFiles[key]
}

// tvFont is the face a TV type role draws with; a role set in the state's
// cut ("state") takes cut, one of the display font's faces.
func tvFont(t brand.TVText, cut string) *opentype.Font {
	loadFonts()
	face := t.Face
	if face == "state" {
		face = cut
	}
	if f, ok := tvFonts[t.Font+"/"+face]; ok {
		return f
	}
	return fontFallback
}

func mustParse(b []byte) *opentype.Font {
	f, err := opentype.Parse(b)
	if err != nil {
		panic("welcome: embedded font: " + err.Error())
	}
	return f
}

// face returns a cached face of f at px pixels (72 DPI: points == pixels).
// Advances and kerning stay fractional: the cuts carry no hints, and a big
// display face spaced on whole pixels looks uneven.
func face(f *opentype.Font, px int) font.Face {
	px = max(px, 6)
	facesMu.Lock()
	defer facesMu.Unlock()
	k := faceKey{f, px}
	if fc, ok := faces[k]; ok {
		return fc
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(px), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic("welcome: font face: " + err.Error())
	}
	faces[k] = fc
	return fc
}

// textItem is one line of text, positioned by its baseline origin. Font is
// its face; runes the face lacks are drawn in fontFallback (runs).
type textItem struct {
	Role  role
	Text  string
	Font  *opentype.Font
	Px    int
	Color color.RGBA
	Dot   image.Point
	Rect  image.Rectangle // ink bounds, for tests
}

// run is a stretch of text drawn in one face.
type run struct {
	f *opentype.Font
	s string
}

// has reports whether f has a glyph for r.
func has(f *opentype.Font, buf *sfnt.Buffer, r rune) bool {
	g, err := f.GlyphIndex(buf, r)
	return err == nil && g != 0
}

// runs splits s into stretches of f and of the fallback face: a rune f
// lacks goes to the fallback, and spaces stay with the face around them.
func runs(f *opentype.Font, s string) []run {
	loadFonts()
	var out []run
	var buf sfnt.Buffer
	var b strings.Builder
	cur := f
	for _, r := range s {
		want := cur
		if !unicode.IsSpace(r) {
			want = f
			if !has(f, &buf, r) {
				want = fontFallback
			}
		}
		if want != cur && b.Len() > 0 {
			out = append(out, run{cur, b.String()})
			b.Reset()
		}
		cur = want
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		out = append(out, run{cur, b.String()})
	}
	return out
}

// inkBounds is the box the glyphs of t cover, in pixels.
func inkBounds(t textItem) image.Rectangle {
	var box fixed.Rectangle26_6
	x := fixed.Int26_6(0)
	for _, r := range runs(t.Font, t.Text) {
		fc := face(r.f, t.Px)
		b, adv := font.BoundString(fc, r.s)
		b.Min.X += x
		b.Max.X += x
		if b.Min.X < b.Max.X {
			if box.Empty() {
				box = b
			} else {
				box = box.Union(b)
			}
		}
		x += adv
	}
	return image.Rect(t.Dot.X+box.Min.X.Floor(), t.Dot.Y+box.Min.Y.Floor(), t.Dot.X+box.Max.X.Ceil(), t.Dot.Y+box.Max.Y.Ceil())
}

// drawable keeps the runes f or the fallback face has a glyph for. Device
// names come from Moonlight clients and can hold anything; a rune neither
// face can draw (CJK or emoji) is dropped rather than drawn as a .notdef
// box, and the spaces around it collapse into one.
func drawable(f *opentype.Font, s string) string {
	loadFonts()
	var b strings.Builder
	var buf sfnt.Buffer
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if !has(f, &buf, r) && !has(fontFallback, &buf, r) {
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// advance is how far the pen moves over text, rounded up to a pixel.
func advance(f *opentype.Font, px int, text string) int {
	return advance26(f, px, text).Ceil()
}

func advance26(f *opentype.Font, px int, text string) fixed.Int26_6 {
	var w fixed.Int26_6
	for _, r := range runs(f, text) {
		w += font.MeasureString(face(r.f, px), r.s)
	}
	return w
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

// drawText draws t onto img, run by run, with kerning inside each run.
func drawText(img *image.RGBA, t textItem) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(t.Color), Dot: fixed.P(t.Dot.X, t.Dot.Y)}
	for _, r := range runs(t.Font, t.Text) {
		d.Face = face(r.f, t.Px)
		d.DrawString(r.s)
	}
}

// ellipsize cuts text short with an ellipsis until it fits maxW pixels at
// px.
func ellipsize(f *opentype.Font, text string, px, maxW int) string {
	if advance(f, px, text) <= maxW {
		return text
	}
	r := []rune(text)
	for len(r) > 0 && advance(f, px, strings.TrimSpace(string(r))+"…") > maxW {
		r = r[:len(r)-1]
	}
	return strings.TrimSpace(string(r)) + "…"
}

// balance wraps text into the fewest lines of at most maxW pixels at px,
// then narrows the lines as far as it can without adding one, like CSS
// text-wrap: balance. Beyond maxLines the rest joins the last line, and
// over says so: the caller cuts that line short.
func balance(f *opentype.Font, text string, px, maxW, maxLines int) (lines []string, over bool) {
	// Each word is measured once; a line is its words and the spaces
	// between them (kerning across a space is too small to matter here).
	words := strings.Fields(text)
	ww := make([]int, len(words))
	for i, w := range words {
		ww[i] = advance26(f, px, w).Ceil()
	}
	sp := advance26(f, px, " ").Ceil()
	wrap := func(w int) []string {
		var lines []string
		start, cur := 0, 0
		for i := range words {
			if i > start && cur+sp+ww[i] > w {
				lines = append(lines, strings.Join(words[start:i], " "))
				start, cur = i, 0
			}
			if i > start {
				cur += sp
			}
			cur += ww[i]
		}
		return append(lines, strings.Join(words[start:], " "))
	}
	best := wrap(maxW)
	lo, hi := maxW*2/5, maxW
	for lo < hi-1 {
		mid := (lo + hi) / 2
		if len(wrap(mid)) <= len(best) {
			hi = mid
		} else {
			lo = mid
		}
	}
	out := wrap(hi)
	if len(out) > len(best) {
		out = best
	}
	if len(out) > maxLines {
		out, over = append(out[:maxLines-1], strings.Join(out[maxLines-1:], " ")), true
	}
	return out, over
}
