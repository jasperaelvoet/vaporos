package welcome

import (
	"image"
	"math"
	"strings"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// The draft look is the welcome screen as it was before the redesign: a
// deep indigo night with a soft blue accent, readable from a couch on any
// panel. It stays pixel for pixel until the chosen direction's look
// (look_<direction>.go, built on brand.TV*) replaces this file. Everything
// specific to it lives here: palette, type scale, grid, background and
// decoration.
var theLook look = draftLook{}

type draftLook struct{}

// draftPalette holds today's colours under the tokens' TV role names.
var draftPalette = palette{
	Canvas:   rgb(0x0b0f1e), // the background's top
	Canvas2:  rgb(0x1b1235), // the background's bottom
	Surface:  rgb(0x24284a), // the setup code plate
	Surface2: rgb(0x24284a),
	Ink:      rgb(0xf4f5fa),
	Ink2:     rgb(0xa3a9c4),
	Accent:   rgb(0x8ea2ff),
	QRDark:   rgb(0x0b0f1e),
	QRLight:  rgb(0xffffff),
}

// draftType is today's type scale in reference pixels, under the roles of
// brand.TVType.
var draftType = struct {
	Wordmark, Status, Detail, URL, IP, CodeLabel, Code, Caption, Version float64
}{Wordmark: 76, Status: 60, Detail: 34, URL: 58, IP: 40, CodeLabel: 30, Code: 84, Caption: 28, Version: 24}

// draftCard is the QR card's side in reference pixels: right of the text in
// landscape, above it in portrait.
func draftCard(portrait bool) float64 {
	if portrait {
		return 640
	}
	return 560
}

func (draftLook) palette() palette { return draftPalette }

// arrange is today's grid: the wordmark, the status and detail, the
// addresses and the setup code in a column on the left, the QR card on the
// right; in portrait the card on top and the column under it.
func (draftLook) arrange(l *layout, st State) {
	p := draftPalette
	margin := 128.0
	cardSize := draftCard(l.Portrait)
	var cardX, cardY float64
	textMax := l.RefW - 2*margin
	if l.Portrait {
		margin = 96
		cardX, cardY = (l.RefW-cardSize)/2, 300
		textMax = l.RefW - 2*margin
	} else {
		cardX, cardY = l.RefW-margin-cardSize, (l.RefH-cardSize)/2
		if l.QR != nil {
			textMax = cardX - 64 - margin
		}
	}
	l.placeQR(cardX, cardY, cardSize)

	title := st.Title
	if title == "" {
		title = "VaporOS"
	}
	// Wordmark, with the "OS" of "VaporOS" in the accent colour.
	wmY := 190.0
	if l.Portrait {
		wmY = 200
	}
	if head, ok := strings.CutSuffix(title, "OS"); ok && head != "" {
		t := l.Texts[l.addText(roleWordmark, head, fontBold, draftType.Wordmark, p.Ink, margin, wmY, textMax)]
		l.place(textItem{Role: roleWordmark, Text: "OS", Font: fontBold, Px: t.Px, Color: p.Accent, Dot: t.Dot.Add(image.Pt(advance(fontBold, t.Px, head), 0))})
	} else {
		l.addText(roleWordmark, title, fontBold, draftType.Wordmark, p.Ink, margin, wmY, textMax)
	}
	// The signature: an accent bar under the wordmark.
	l.Signature = image.Rect(l.X(margin), l.Y(wmY+32), l.X(margin)+l.Len(96), l.Y(wmY+32)+l.Len(6))

	// Text column: status and detail, the addresses, then the setup code.
	y := 370.0
	if l.Portrait {
		y = cardY + cardSize + 170
	}
	if st.Status != "" {
		l.addText(roleStatus, st.Status, fontBold, draftType.Status, p.Ink, margin, y, textMax)
	}
	if st.Detail != "" {
		l.addText(roleDetail, st.Detail, fontRegular, draftType.Detail, p.Ink2, margin, y+58, textMax)
	}
	y += 190
	if st.URL != "" {
		l.addText(roleURL, st.URL, fontBold, draftType.URL, p.Ink, margin, y, textMax)
		y += 62
	}
	if st.IPURL != "" && st.IPURL != st.URL {
		l.addText(roleIP, st.IPURL, fontRegular, draftType.IP, p.Accent, margin, y, textMax)
	}
	if st.Code != "" {
		label := l.addText(roleCodeLabel, brand.TVLabels.CodeLabel, fontRegular, draftType.CodeLabel, p.Ink2, margin, y+102, textMax)
		code := l.addText(roleCode, st.Code, fontMono, draftType.Code, p.Ink, margin, y+192, textMax-48)
		pad := l.Len(24)
		lr, cr := l.Texts[label].Rect, l.Texts[code].Rect
		l.Panel = image.Rect(cr.Min.X-pad, lr.Min.Y-pad, max(cr.Max.X, lr.Max.X)+pad, cr.Max.Y+pad)
		// Indent both lines so the panel's edge sits on the margin.
		dx := l.X(margin) - l.Panel.Min.X
		l.shift(label, dx)
		l.shift(code, dx)
		l.Panel = l.Panel.Add(image.Pt(dx, 0))
	}
	if l.QR != nil {
		i := l.addText(roleCaption, brand.TVLabels.QRCaption, fontRegular, draftType.Caption, p.Ink2, 0, cardY+cardSize+56, cardSize)
		// Centre the caption under the card.
		r := l.Texts[i].Rect
		l.shift(i, (l.QRCard.Min.X+l.QRCard.Max.X)/2-(r.Min.X+r.Max.X)/2)
	}
	if st.Version != "" {
		v := st.Version
		if !strings.HasPrefix(v, "VaporOS") {
			v = "VaporOS " + v
		}
		l.addText(roleVersion, v, fontRegular, draftType.Version, p.Ink2, margin, l.RefH-64, textMax)
	}
}

// The draft background ignores the tone: it depends on the size and on
// where the QR card sits.
func (draftLook) backdrop(l *layout) backdrop {
	return backdrop{W: l.W, H: l.H, Focus: l.QRCard}
}

// background fills a vertical gradient with a soft accent glow behind the
// QR card (Focus).
func (draftLook) background(img *image.RGBA, b backdrop) {
	p := draftPalette
	w, h := b.W, b.H
	var glowAt image.Point
	glowRad := 0
	if !b.Focus.Empty() {
		g := newGeometry(w, h)
		glowAt = image.Pt((b.Focus.Min.X+b.Focus.Max.X)/2, (b.Focus.Min.Y+b.Focus.Max.Y)/2)
		glowRad = g.Len(draftCard(g.Portrait) * 0.95)
	}
	r2 := float64(glowRad) * float64(glowRad)
	for y := 0; y < h; y++ {
		t := float64(y) / math.Max(1, float64(h-1))
		br := lerp(p.Canvas.R, p.Canvas2.R, t)
		bg := lerp(p.Canvas.G, p.Canvas2.G, t)
		bb := lerp(p.Canvas.B, p.Canvas2.B, t)
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		dy := float64(y - glowAt.Y)
		for x := 0; x < w; x++ {
			r, g, b := br, bg, bb
			if glowRad > 0 {
				dx := float64(x - glowAt.X)
				if d := (dx*dx + dy*dy) / r2; d < 1 {
					k := (1 - d) * (1 - d) * 0.22
					r += (float64(p.Accent.R) - r) * k
					g += (float64(p.Accent.G) - g) * k
					b += (float64(p.Accent.B) - b) * k
				}
			}
			i := x * 4
			row[i], row[i+1], row[i+2], row[i+3] = uint8(r), uint8(g), uint8(b), 0xff
		}
	}
}

func lerp(a, b uint8, t float64) float64 { return float64(a) + (float64(b)-float64(a))*t }

// decorate draws the accent bar under the wordmark and the setup code plate.
func (draftLook) decorate(img *image.RGBA, l *layout) {
	p := draftPalette
	fillRounded(img, l.Signature, l.Signature.Dy()/2, p.Accent)
	if !l.Panel.Empty() {
		fillRounded(img, l.Panel, l.Panel.Dy()/8, p.Surface)
	}
}
