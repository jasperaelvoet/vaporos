package welcome

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"rsc.io/qr"
)

// Palette: a deep indigo night with a soft blue accent, readable from a
// couch on any panel.
var (
	colBgTop    = color.RGBA{0x0b, 0x0f, 0x1e, 0xff}
	colBgBottom = color.RGBA{0x1b, 0x12, 0x35, 0xff}
	colText     = color.RGBA{0xf4, 0xf5, 0xfa, 0xff}
	colMuted    = color.RGBA{0xa3, 0xa9, 0xc4, 0xff}
	colAccent   = color.RGBA{0x8e, 0xa2, 0xff, 0xff}
	colPanel    = color.RGBA{0x24, 0x28, 0x4a, 0xff}
	colQRDark   = color.RGBA{0x0b, 0x0f, 0x1e, 0xff}
	colQRLight  = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

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
	Text  string
	Font  *opentype.Font
	Px    int
	Color color.RGBA
	Dot   image.Point
	Rect  image.Rectangle // ink bounds, for tests
}

// layout is everything Render draws, computed without touching pixels so
// tests can check positions against the rendered image.
type layout struct {
	W, H    int
	Texts   []textItem
	Bar     image.Rectangle // accent bar under the wordmark
	Panel   image.Rectangle // behind the setup code (empty if none)
	QR      *qr.Code
	QRCard  image.Rectangle // white card including the quiet zone
	QRAt    image.Point     // top-left pixel of module (0,0)
	Module  int             // pixels per module
	GlowAt  image.Point
	GlowRad int
}

// Render draws the welcome screen for st at w x h.
func Render(st State, w, h int) *image.RGBA {
	return draw1(computeLayout(st, w, h))
}

func computeLayout(st State, w, h int) *layout {
	loadFonts()
	l := &layout{W: w, H: h}
	portrait := h > w
	refW, refH := 1920.0, 1080.0
	if portrait {
		refW, refH = 1080.0, 1920.0
	}
	s := math.Min(float64(w)/refW, float64(h)/refH)
	ox := (float64(w) - refW*s) / 2
	oy := (float64(h) - refH*s) / 2
	X := func(v float64) int { return int(math.Round(ox + v*s)) }
	Y := func(v float64) int { return int(math.Round(oy + v*s)) }
	S := func(v float64) int { return max(1, int(math.Round(v*s))) }

	// The QR card: right half in landscape, top centre in portrait.
	if st.QR != "" {
		if code, err := qr.Encode(st.QR, qr.M); err == nil {
			l.QR = code
		}
	}
	margin := 128.0
	var cardX, cardY, cardSize float64
	textMax := refW - 2*margin
	if portrait {
		margin = 96
		cardSize = 640
		cardX, cardY = (refW-cardSize)/2, 300
		textMax = refW - 2*margin
	} else {
		cardSize = 560
		cardX, cardY = refW-margin-cardSize, (refH-cardSize)/2
		if l.QR != nil {
			textMax = cardX - 64 - margin
		}
	}
	if l.QR != nil {
		size := S(cardSize)
		// Quiet zone of 4 modules on every side, as the QR spec asks.
		l.Module = max(1, size/(l.QR.Size+8))
		inner := l.Module * (l.QR.Size + 8)
		cx, cy := X(cardX)+(size-inner)/2, Y(cardY)+(size-inner)/2
		l.QRCard = image.Rect(cx, cy, cx+inner, cy+inner)
		l.QRAt = image.Pt(cx+4*l.Module, cy+4*l.Module)
		l.GlowAt = image.Pt((l.QRCard.Min.X+l.QRCard.Max.X)/2, (l.QRCard.Min.Y+l.QRCard.Max.Y)/2)
		l.GlowRad = S(cardSize * 0.95)
	}

	// add lays out one line of text at reference coordinates (x, baseline
	// y), shrinking it to fit maxW, and returns its index in l.Texts.
	add := func(text string, f *opentype.Font, px float64, c color.RGBA, x, y, maxW float64) int {
		size := fitSize(f, text, S(px), S(maxW))
		l.Texts = append(l.Texts, textItem{Text: text, Font: f, Px: size, Color: c, Dot: image.Pt(X(x), Y(y))})
		i := len(l.Texts) - 1
		l.measure(i)
		return i
	}

	title := st.Title
	if title == "" {
		title = "VaporOS"
	}
	// Wordmark, with the "OS" of "VaporOS" in the accent colour.
	wmY := 190.0
	if portrait {
		wmY = 200
	}
	if head, ok := strings.CutSuffix(title, "OS"); ok && head != "" {
		i := add(head, fontBold, 76, colText, margin, wmY, textMax)
		t := l.Texts[i]
		adv := font.MeasureString(face(fontBold, t.Px), head).Ceil()
		l.Texts = append(l.Texts, textItem{Text: "OS", Font: fontBold, Px: t.Px, Color: colAccent, Dot: t.Dot.Add(image.Pt(adv, 0))})
		l.measure(len(l.Texts) - 1)
	} else {
		add(title, fontBold, 76, colText, margin, wmY, textMax)
	}
	l.Bar = image.Rect(X(margin), Y(wmY+32), X(margin)+S(96), Y(wmY+32)+S(6))

	// Text column: status and detail, the addresses, then the setup code.
	y := 370.0
	if portrait {
		y = cardY + cardSize + 170
	}
	if st.Status != "" {
		add(st.Status, fontBold, 60, colText, margin, y, textMax)
	}
	if st.Detail != "" {
		add(st.Detail, fontRegular, 34, colMuted, margin, y+58, textMax)
	}
	y += 190
	if st.URL != "" {
		add(st.URL, fontBold, 58, colText, margin, y, textMax)
		y += 62
	}
	if st.IPURL != "" && st.IPURL != st.URL {
		add(st.IPURL, fontRegular, 40, colAccent, margin, y, textMax)
	}
	if st.Code != "" {
		label := add("Setup code", fontRegular, 30, colMuted, margin, y+102, textMax)
		code := add(st.Code, fontMono, 84, colText, margin, y+192, textMax-48)
		pad := S(24)
		lr, cr := l.Texts[label].Rect, l.Texts[code].Rect
		l.Panel = image.Rect(cr.Min.X-pad, lr.Min.Y-pad, max(cr.Max.X, lr.Max.X)+pad, cr.Max.Y+pad)
		// Indent both lines so the panel's edge sits on the margin.
		dx := X(margin) - l.Panel.Min.X
		l.shift(label, dx)
		l.shift(code, dx)
		l.Panel = l.Panel.Add(image.Pt(dx, 0))
	}
	if l.QR != nil {
		i := add("Scan to open", fontRegular, 28, colMuted, 0, cardY+cardSize+56, cardSize)
		// Centre the caption under the card.
		r := l.Texts[i].Rect
		l.shift(i, (l.QRCard.Min.X+l.QRCard.Max.X)/2-(r.Min.X+r.Max.X)/2)
	}
	if st.Version != "" {
		v := st.Version
		if !strings.HasPrefix(v, "VaporOS") {
			v = "VaporOS " + v
		}
		add(v, fontRegular, 24, colMuted, margin, refH-64, textMax)
	}
	return l
}

// measure sets the ink bounds of text item i.
func (l *layout) measure(i int) {
	t := &l.Texts[i]
	b, _ := font.BoundString(face(t.Font, t.Px), t.Text)
	t.Rect = image.Rect(t.Dot.X+b.Min.X.Floor(), t.Dot.Y+b.Min.Y.Floor(), t.Dot.X+b.Max.X.Ceil(), t.Dot.Y+b.Max.Y.Ceil())
}

// shift moves text item i right by dx pixels.
func (l *layout) shift(i, dx int) {
	l.Texts[i].Dot.X += dx
	l.Texts[i].Rect = l.Texts[i].Rect.Add(image.Pt(dx, 0))
}

// fitSize shrinks px until text fits in maxW pixels.
func fitSize(f *opentype.Font, text string, px, maxW int) int {
	for px > 8 {
		w := font.MeasureString(face(f, px), text).Ceil()
		if w <= maxW {
			break
		}
		px = max(8, int(float64(px)*float64(maxW)/float64(w)))
		if font.MeasureString(face(f, px), text).Ceil() <= maxW {
			break
		}
		px--
	}
	return px
}

func draw1(l *layout) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, l.W, l.H))
	paintBackground(img, l)
	fillRounded(img, l.Bar, l.Bar.Dy()/2, colAccent)
	if !l.Panel.Empty() {
		fillRounded(img, l.Panel, l.Panel.Dy()/8, colPanel)
	}
	if l.QR != nil {
		fillRounded(img, l.QRCard, l.Module, colQRLight) // radius 1 module keeps the quiet zone whole
		dark := image.NewUniform(colQRDark)
		for y := 0; y < l.QR.Size; y++ {
			for x := 0; x < l.QR.Size; x++ {
				if l.QR.Black(x, y) {
					r := image.Rect(0, 0, l.Module, l.Module).Add(l.QRAt.Add(image.Pt(x*l.Module, y*l.Module)))
					draw.Draw(img, r, dark, image.Point{}, draw.Src)
				}
			}
		}
	}
	for _, t := range l.Texts {
		d := font.Drawer{Dst: img, Src: image.NewUniform(t.Color), Face: face(t.Font, t.Px), Dot: fixed.P(t.Dot.X, t.Dot.Y)}
		d.DrawString(t.Text)
	}
	return img
}

// paintBackground fills a vertical gradient with a soft accent glow
// behind the QR card.
func paintBackground(img *image.RGBA, l *layout) {
	w, h := l.W, l.H
	r2 := float64(l.GlowRad) * float64(l.GlowRad)
	for y := 0; y < h; y++ {
		t := float64(y) / math.Max(1, float64(h-1))
		br := lerp(colBgTop.R, colBgBottom.R, t)
		bg := lerp(colBgTop.G, colBgBottom.G, t)
		bb := lerp(colBgTop.B, colBgBottom.B, t)
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		dy := float64(y - l.GlowAt.Y)
		for x := 0; x < w; x++ {
			r, g, b := br, bg, bb
			if l.GlowRad > 0 {
				dx := float64(x - l.GlowAt.X)
				if d := (dx*dx + dy*dy) / r2; d < 1 {
					k := (1 - d) * (1 - d) * 0.22
					r += (float64(colAccent.R) - r) * k
					g += (float64(colAccent.G) - g) * k
					b += (float64(colAccent.B) - b) * k
				}
			}
			i := x * 4
			row[i], row[i+1], row[i+2], row[i+3] = uint8(r), uint8(g), uint8(b), 0xff
		}
	}
}

func lerp(a, b uint8, t float64) float64 { return float64(a) + (float64(b)-float64(a))*t }

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
