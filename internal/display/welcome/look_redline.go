package welcome

import (
	"image"
	"image/color"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jasperaelvoet/vaporos/internal/brand"
	"golang.org/x/image/font/opentype"
)

// REDLINE (design/tokens.json "redline") shows the box the way a thermal
// camera sees it. The QR card is the white-hot thing you touch, the heat
// around it is the state's temperature (asleep an ember, ready a magenta
// peak, streaming amber), faults are the only cold colour and carry hazard
// tape, and width is temperature too: the status is set in the state's cut
// of Anybody. The words sit on the cool side of the field, the heat scale
// top right reads the state like a camera's overlay, and spot-meter
// brackets frame the card. Everything specific to the direction lives in
// look_redline*.go; its numbers come from brand's generated tokens.
var theLook look = redlineLook{}

type redlineLook struct{}

// redlinePalette is the tokens' TV colours.
var redlinePalette = palette{
	Canvas:   opaque(brand.TVCanvas),
	Canvas2:  opaque(brand.TVCanvas2),
	Surface:  opaque(brand.TVSurface),  // ash: plates
	Surface2: opaque(brand.TVSurface2), // soot: the code plate, the progress track
	Ink:      opaque(brand.TVInk),      // bone
	Ink2:     opaque(brand.TVInk2),     // smoke
	Accent:   opaque(brand.TVAccent),   // white-hot
	QRDark:   opaque(brand.TVQRDark),
	QRLight:  opaque(brand.TVQRLight),
}

func (redlineLook) palette() palette { return redlinePalette }

// redlineGrid is the screen on its reference canvas (1920×1080, or
// 1080×1920 in portrait), in reference units.
type redlineGrid struct {
	margin    float64    // the words' left edge
	brandY    float64    // the top of the mark
	hud       [4]float64 // the heat scale's plate: x, y, w, h
	colTop    float64    // the top of the words
	colW      float64    // the widest they may run
	colBottom float64    // the lowest they may reach
	slot      [3]float64 // the QR slot: x, y, side
	captionY  float64    // the top of the caption's plate
	statusPx  float64
	drop      float64    // a one-line status starts this much lower, level with the card
	addrTop   [2]float64 // the address's top at the least, under a one-line status and under a longer one
	codeTop   float64    // the setup code plate's top at the least
}

func redlineGridFor(portrait bool) redlineGrid {
	if portrait {
		// Heat rises: the card sits at the bottom and the words above it,
		// on the cool side. The slot is wide enough for the card to fill
		// 55% of the width after rounding to whole modules.
		side := max(float64(brand.TVQR.Portrait), 640)
		y := 1064.0
		return redlineGrid{
			margin: 96, brandY: 96, hud: [4]float64{96, 186, 492, 100},
			colTop: 322, colW: 888, colBottom: y - 36 - 30,
			slot: [3]float64{(1080 - side) / 2, y, side}, captionY: y + side + 36 + 16,
			statusPx: float64(brand.TVType.Status.PortraitPx),
		}
	}
	side := float64(brand.TVQR.Landscape)
	return redlineGrid{
		margin: 128, brandY: 104, hud: [4]float64{1330, 76, 492, 100},
		colTop: 206, colW: 1010, colBottom: 1006,
		slot: [3]float64{1232, 272, side}, captionY: 272 + side + 46,
		statusPx: float64(brand.TVType.Status.Px), drop: 66,
		addrTop: [2]float64{612, 668}, codeTop: 796,
	}
}

// statusCut is the display face the status is set in: the state's cut, or
// the pairing modifier's.
func statusCut(l *layout) string {
	if l.Attention == AttentionPair {
		return brand.AttentionPair.Cut
	}
	return brand.StyleOf(l.Tone).Cut
}

// arrange lays st out: the brand row and the heat scale on top, the words in
// a column on the cool side, the QR card in the heat. The column is set as
// wide and as large as it can be, then narrowed while a line would sit on a
// band too hot to read it on, and scaled down while it runs past the bottom.
func (lk redlineLook) arrange(l *layout, st State) {
	g := redlineGridFor(l.Portrait)
	l.placeQR(g.slot[0], g.slot[1], g.slot[2])
	f := newHeatField(lk.backdrop(l))
	if l.Portrait {
		// Heat rises toward the words from below: keep them above it.
		g.colBottom = f.coolBelow(l, g.margin, g.margin+g.colW, g.colTop+200, g.colBottom)
	}
	base := *l
	colW := g.colW
	var hot []int
	for {
		k := 1.0
		for {
			*l = base
			l.Texts = nil
			bottom := lk.column(l, st, g, colW, k)
			if bottom <= g.colBottom || k < 0.62 {
				break
			}
			k -= 0.06
		}
		// In landscape the heat is beside the words, so a narrower column
		// keeps them off it; in portrait it is below them, and coolBottom
		// has already lifted them.
		hot = f.tooHot(l, redlinePalette.Ink)
		if l.Portrait || len(hot) == 0 || colW < 0.6*g.colW {
			break
		}
		colW -= 40
	}
	for _, i := range hot {
		pad := l.Len(14)
		l.Plates = append(l.Plates, l.Texts[i].Rect.Inset(-pad))
	}
	lk.chrome(l, st, g)
}

// column sets the status, the detail, the progress bar, the address with
// the handshake mark and the setup code at scale k in a column colW wide,
// and returns where it ends, in reference units.
func (redlineLook) column(l *layout, st State, g redlineGrid, colW, k float64) float64 {
	p := redlinePalette
	x0 := g.margin
	ty := brand.TVType
	y := g.colTop

	// The status, in the state's cut, one line if it can be.
	if st.Status != "" {
		fs := tvFont(ty.Status, statusCut(l))
		lines := statusLines(fs, drawable(fs, st.Status), l.Len(g.statusPx*k), l.Len(colW))
		if len(lines) == 1 {
			y += g.drop * k
		}
		for i, ln := range lines {
			ln.text, ln.px = fit(fs, ln.text, ln.px, l.Len(colW)) // a word too long even at the floor
			px := float64(ln.px) / l.S
			y += px * 0.95
			t := textItem{Role: roleStatus, Text: ln.text, Font: fs, Px: ln.px, Color: p.Ink, Dot: image.Pt(l.X(x0), l.Y(y))}
			if i > 0 { // keep the ascenders clear of the descenders above
				if gap := l.Texts[len(l.Texts)-1].Rect.Max.Y + l.Len(6*k) - inkBounds(t).Min.Y; gap > 0 {
					y += float64(gap) / l.S
					t.Dot.Y = l.Y(y)
				}
			}
			l.place(t)
			y += px * 0.14
		}
	}
	multi := st.Status != "" && len(l.Texts) > 1

	// The detail, in balanced lines.
	if st.Detail != "" {
		fd := tvFont(ty.Detail, "")
		px := l.Len(float64(ty.Detail.Px) * k)
		y += 78 * k
		first := len(l.Texts)
		lines, over := balance(fd, drawable(fd, st.Detail), px, l.Len(min(900, colW)), 4)
		for i, s := range lines {
			if over && i == len(lines)-1 {
				s = ellipsize(fd, s, px, l.Len(min(900, colW)))
			}
			l.addText(roleDetail, s, fd, float64(ty.Detail.Px)*k, p.Ink2, x0, y, min(900, colW))
			y += 54 * k
		}
		if l.Attention == AttentionPair && len(l.Texts) > first {
			r := l.Texts[first].Rect
			for _, t := range l.Texts[first+1:] {
				r = r.Union(t.Rect)
			}
			l.Callout = image.Rect(r.Min.X-l.Len(24), r.Min.Y-l.Len(18), r.Max.X+l.Len(24), r.Max.Y+l.Len(20))
		}
		y -= 54 * k
	}

	// A running install or update: a bar under the detail.
	if l.Percent > 0 && (l.Tone == brand.Installing || l.Tone == brand.Updating) {
		y += 36 * k
		l.Progress = image.Rect(l.X(x0), l.Y(y), l.X(x0+min(colW, 720)), l.Y(y+14*k))
		y += 14 * k
	}

	// The address: at its place, or lower when the words need the room,
	// with the handshake mark in front of it.
	top := y + 64*k
	if k == 1 {
		top = max(top, g.addrTop[b2i(multi)])
	}
	if st.URL != "" {
		ux := x0
		if st.Hostname != "" {
			side := 7 * float64(max(1, l.Len(16*k)))
			l.HandshakeMark = brand.HandshakeFor(st.Hostname)
			l.Handshake = image.Rect(l.X(x0), l.Y(top+10*k), l.X(x0)+int(side), l.Y(top+10*k)+int(side))
			ux = x0 + side/l.S + 36*k
			y = top + 10*k + side/l.S
		}
		umax := colW - (ux - x0)
		fu := tvFont(ty.URL, "")
		scheme, host := splitScheme(st.URL)
		px := fitSize(fu, drawable(fu, scheme+host), l.Len(float64(ty.URL.Px)*k), l.Len(umax))
		base := top + 62*k
		if scheme != "" {
			i := l.place(textItem{Role: roleURL, Text: scheme, Font: fu, Px: px, Color: p.Ink2, Dot: image.Pt(l.X(ux), l.Y(base))})
			sw := advance(fu, px, scheme)
			h, hpx := fit(fu, drawable(fu, host), px, l.Len(umax)-sw)
			l.place(textItem{Role: roleURL, Text: h, Font: fu, Px: hpx, Color: p.Ink, Dot: l.Texts[i].Dot.Add(image.Pt(sw, 0))})
		} else {
			h, hpx := fit(fu, drawable(fu, host), px, l.Len(umax))
			l.place(textItem{Role: roleURL, Text: h, Font: fu, Px: hpx, Color: p.Ink, Dot: image.Pt(l.X(ux), l.Y(base))})
		}
		y = max(y, base+0.24*float64(px)/l.S)
		if st.IPURL != "" && st.IPURL != st.URL {
			fo, fi := tvFont(ty.IPPrefix, ""), tvFont(ty.IP, "")
			ib := top + 126*k
			opx := l.Len(float64(ty.IPPrefix.Px) * k)
			i := l.place(textItem{Role: roleIP, Text: "or", Font: fo, Px: opx, Color: p.Ink2, Dot: image.Pt(l.X(ux), l.Y(ib))})
			ow := advance(fo, opx, "or ")
			ip, ipx := fit(fi, drawable(fi, st.IPURL), l.Len(float64(ty.IP.Px)*k), l.Len(umax)-ow)
			l.place(textItem{Role: roleIP, Text: ip, Font: fi, Px: ipx, Color: p.Ink, Dot: l.Texts[i].Dot.Add(image.Pt(ow, 0))})
			y = max(y, ib+0.24*float64(ipx)/l.S)
		}
	}

	// The setup code, on a soot plate with a white-hot rim.
	if st.Code != "" {
		py := top + 184*k
		if k == 1 {
			py = max(py, g.codeTop)
		}
		if st.URL == "" {
			py = y + 48*k
		}
		fl, fc := tvFont(ty.CodeLabel, ""), tvFont(ty.Code, "")
		label := l.addText(roleCodeLabel, brand.TVLabels.CodeLabel, fl, float64(ty.CodeLabel.Px)*k, p.Ink2, x0+36*k, py+54*k, colW-72*k)
		code := l.addText(roleCode, st.Code, fc, float64(ty.Code.Px)*k, p.Ink, x0+36*k, py+164*k, colW-72*k)
		w := max(l.Texts[label].Rect.Max.X, l.Texts[code].Rect.Max.X) - l.X(x0) + l.Len(36*k)
		l.Panel = image.Rect(l.X(x0), l.Y(py), l.X(x0)+w, l.Y(py+198*k))
		y = py + 198*k
	}
	return y
}

// chrome places what sits outside the column: the brand row, the heat
// scale, the spot-meter brackets and the caption under the card.
func (redlineLook) chrome(l *layout, st State, g redlineGrid) {
	p := redlinePalette
	ty := brand.TVType
	x0 := g.margin

	// The compact mark, then the wordmark in two cuts ("Vapor" hot in
	// bone, "OS" cold in smoke), then the version.
	l.Mark = image.Rect(l.X(x0), l.Y(g.brandY), l.X(x0)+l.Len(58), l.Y(g.brandY)+l.Len(58))
	bx, by := x0+80, g.brandY+46
	room := g.colW - 80
	title := st.Title
	if title == "" {
		title = brand.AppName
	}
	fh := tvFont(ty.Wordmark, "")
	head, accent := title, ""
	if h, ok := strings.CutSuffix(title, "OS"); ok && h != "" {
		head, accent = h, "OS"
	}
	i := l.addText(roleWordmark, head, fh, float64(ty.Wordmark.Px), p.Ink, bx, by, room)
	end := l.Texts[i].Dot.X + advance(fh, l.Texts[i].Px, l.Texts[i].Text)
	if accent != "" {
		fa := tvFont(ty.WordmarkAccent, "")
		dot := image.Pt(end+l.Len(float64(ty.Wordmark.Px)*0.07), l.Texts[i].Dot.Y)
		j := l.place(textItem{Role: roleWordmark, Text: accent, Font: fa, Px: l.Texts[i].Px, Color: p.Ink2, Dot: dot})
		end = dot.X + advance(fa, l.Texts[j].Px, accent)
	}
	if v := st.Version; v != "" {
		if brand.TVLabels.VersionPrefix != "" {
			v = brand.TVLabels.VersionPrefix + " " + v
		}
		if st.Mode == "installer" && brand.TVLabels.InstallerPrefix != "" {
			v = brand.TVLabels.InstallerPrefix + " " + v
		}
		vx := float64(end-l.X(0))/l.S + 28
		l.addText(roleVersion, v, tvFont(ty.Version, ""), float64(ty.Version.Px), p.Ink2, vx, by, x0+g.colW-vx)
	}

	// The heat scale: the ramp from asleep to streaming, and a marker at
	// the state's heat with its word; a fault sits below the scale.
	hx, hy, hw, hh := g.hud[0], g.hud[1], g.hud[2], g.hud[3]
	l.Signature = image.Rect(l.X(hx), l.Y(hy), l.X(hx+hw), l.Y(hy+hh))
	barX, barW := hx+28, hw-56
	fe := tvFont(ty.Scale, "")
	l.addText(roleScale, brand.TVLabels.ScaleCold, fe, float64(ty.Scale.Px), p.Ink2, barX, hy+90, barW/2)
	hot := l.addText(roleScale, brand.TVLabels.ScaleHot, fe, float64(ty.Scale.Px), p.Ink2, barX, hy+90, barW/2)
	l.shift(hot, l.X(barX+barW)-l.Texts[hot].Dot.X-advance(fe, l.Texts[hot].Px, l.Texts[hot].Text))
	if word, t := scaleMark(l); word != "" {
		fw := tvFont(ty.ScaleLabel, "")
		i := l.addText(roleScaleLabel, word, fw, float64(ty.ScaleLabel.Px), p.Ink, barX, hy+36, barW+24)
		lw := float64(advance(fw, l.Texts[i].Px, word)) / l.S
		x := min(max(barX+t*barW-lw/2, barX-12), barX+barW-lw)
		l.shift(i, l.X(x)-l.Texts[i].Dot.X)
	}

	// Spot-meter brackets 30 units outside the slot, and the caption on
	// its plate under them.
	if l.QR == nil {
		return
	}
	l.QRFrame = l.QRSlot.Inset(-l.Len(30))
	fc := tvFont(ty.Caption, "")
	sx, side := g.slot[0], g.slot[2]
	i = l.addText(roleCaption, brand.TVLabels.QRCaption, fc, float64(ty.Caption.Px), p.Ink, sx, g.captionY+43, side-48)
	w := advance(fc, l.Texts[i].Px, l.Texts[i].Text)
	l.shift(i, (l.QRSlot.Min.X+l.QRSlot.Max.X)/2-w/2-l.Texts[i].Dot.X)
	pad := l.Len(24)
	l.Caption = image.Rect(l.Texts[i].Dot.X-pad, l.Y(g.captionY), l.Texts[i].Dot.X+w+pad, l.Y(g.captionY+62))
}

// scaleMark is the word the heat scale's marker carries and where on the
// scale it sits, 0 (asleep) to 1 (white-hot).
func scaleMark(l *layout) (string, float64) {
	st := brand.StyleOf(l.Tone)
	if l.Attention == AttentionPair && !st.ColdMap {
		st = brand.AttentionPair
	}
	return st.Signage, min(max(st.Heat, 0), 1)
}

// splitScheme cuts "http://" or "https://" off an address.
func splitScheme(u string) (scheme, rest string) {
	for _, s := range []string{"http://", "https://"} {
		if r, ok := strings.CutPrefix(u, s); ok {
			return s, r
		}
	}
	return "", u
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// statusLine is one line of the status at its own size.
type statusLine struct {
	text string
	px   int
}

// statusLines sets a status at px in maxW pixels. It stays one line if it
// fits at 80% of px or more. Otherwise a status that leads with a verb
// ("Streaming to Living room TV") keeps the verb at full size over the rest
// at half, as the phone sets its state word, and anything else becomes two
// balanced lines of one size.
func statusLines(f *opentype.Font, text string, px, maxW int) []statusLine {
	one := fitSize(f, text, px, maxW)
	if float64(one) >= 0.8*float64(px) || !strings.Contains(text, " ") {
		return []statusLine{{text, one}}
	}
	if verb, rest, _ := strings.Cut(text, " "); leadVerb(verb) {
		return []statusLine{{verb, fitSize(f, verb, px, maxW)}, {rest, fitSize(f, rest, px/2, maxW)}}
	}
	words := strings.Fields(text)
	best, bestW := 1, math.MaxInt
	for i := 1; i < len(words); i++ {
		w := max(advance(f, px, strings.Join(words[:i], " ")), advance(f, px, strings.Join(words[i:], " ")))
		if w < bestW {
			best, bestW = i, w
		}
	}
	a, b := strings.Join(words[:best], " "), strings.Join(words[best:], " ")
	size := min(fitSize(f, a, px, maxW), fitSize(f, b, px, maxW))
	return []statusLine{{a, size}, {b, size}}
}

// leadVerb reports whether a status's first word is a verb that can stand
// on its own line: a present participle ("Streaming", "Downloading",
// "Installing", "Waiting") or "Restart".
func leadVerb(w string) bool {
	lw := strings.ToLower(w)
	return lw == "restart" || utf8.RuneCountInString(lw) > 4 && strings.HasSuffix(lw, "ing")
}

// backdrop is everything the field depends on: the screen, the tone, a
// waiting pairing, the QR slot and whether a card is in it (never the code
// itself, so a new percentage or a new address reuses the cached field).
func (redlineLook) backdrop(l *layout) backdrop {
	return backdrop{W: l.W, H: l.H, Tone: l.Tone, Pair: l.Attention == AttentionPair, Focus: l.QRSlot, Bloom: l.QR == nil}
}

// background paints the heat field, and hazard tape along the top in a
// fault.
func (redlineLook) background(img *image.RGBA, b backdrop) {
	f := newHeatField(b)
	f.paint(img)
	if f.cold {
		tape(img, f.s)
	}
}

// decorate paints the plates, the pairing callout, the heat scale, the
// setup code's plate, the progress bar, the brackets, the caption's plate,
// the mark and the handshake mark. Nothing here reaches into l.QRCard.
func (redlineLook) decorate(img *image.RGBA, l *layout) {
	p := redlinePalette
	s := l.S
	plate := lookParam("plateAlpha", 0.86)
	bone := p.Ink
	for _, r := range l.Plates {
		blendRound(img, r, 10*s, p.Surface, lookParam("textPlateAlpha", 0.9))
	}
	if c := l.Callout; !c.Empty() {
		blendRound(img, c, 12*s, p.Surface, plate)
		brackets(img, c, l.Len(26), max(2, l.Len(4)), p.Accent)
	}

	// The heat scale.
	if hud := l.Signature; !hud.Empty() {
		blendRound(img, hud, 12*s, p.Surface, plate)
		blendRing(img, hud, 12*s, 1, bone, 0.14)
		bar := image.Rect(hud.Min.X+l.Len(28), hud.Min.Y+l.Len(56), hud.Max.X-l.Len(28), hud.Min.Y+l.Len(56)+l.Len(12))
		blendRound(img, bar.Inset(-1), 2*s+1, bone, 0.28)
		for x := bar.Min.X; x < bar.Max.X; x++ {
			c := heatRamp[(x-bar.Min.X)*255/max(1, bar.Dx()-1)]
			fillRect(img, image.Rect(x, bar.Min.Y, x+1, bar.Max.Y), color.RGBA{clamp8(c[0]), clamp8(c[1]), clamp8(c[2]), 0xff})
		}
		_, t := scaleMark(l)
		tip := p.Accent
		if brand.StyleOf(l.Tone).ColdMap {
			tip, t = opaque(brand.Palette.Cold), 0
		}
		marker(img, float64(bar.Min.X)+t*float64(bar.Dx()), float64(hud.Min.Y)+44*s, 8*s, 10*s, tip)
	}

	// The setup code: soot inside a white-hot rim.
	if r := l.Panel; !r.Empty() {
		blendRound(img, r, 14*s, p.Accent, 1)
		blendRound(img, r.Inset(max(2, l.Len(3))), 11*s, p.Surface2, 1)
	}

	// Progress: a soot track and the state's colour, heating to white-hot
	// at its leading edge.
	if r := l.Progress; !r.Empty() {
		fillBar(img, r, l.Percent, p.Surface2, opaque(brand.StyleOf(l.Tone).Color))
		w := barFill(r, l.Percent)
		fill := image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y)
		tail := min(w, l.Len(56))
		for x := fill.Max.X - tail; x < fill.Max.X; x++ {
			t := float64(x-(fill.Max.X-tail)+1) / float64(tail)
			for y := fill.Min.Y; y < fill.Max.Y; y++ {
				cov := math.Min(1, math.Max(0, 0.5-roundDist(fill, float64(r.Dy())/2, x, y)))
				blendPx(img, x, y, p.Accent, cov*t*t)
			}
		}
	}

	// Spot-meter brackets around the card, and the caption's plate.
	if !l.QRFrame.Empty() {
		brackets(img, l.QRFrame, l.Len(56), max(2, l.Len(6)), p.Accent)
	}
	if c := l.Caption; !c.Empty() {
		blendRound(img, c, float64(c.Dy())/2, p.Surface, plate)
		blendRing(img, c, float64(c.Dy())/2, 1, bone, 0.14)
	}

	// The compact mark, on the field's darkest ground.
	if !l.Mark.Empty() {
		brand.DrawMark(img, l.Mark, map[string]color.Color{"line": brand.TVCanvas})
	}

	// The handshake: the hostname's 5×5 cells on its ground, as the phone
	// shows it after the scan, in a hairline frame.
	if r := l.Handshake; !r.Empty() {
		hs := l.HandshakeMark
		fillRect(img, r.Inset(-max(1, l.Len(3))), opaque(brand.TVLine))
		fillRect(img, r, opaque(hs.Ground))
		cell := r.Dx() / 7
		for y, row := range hs.Cells {
			for x, on := range row {
				if on {
					at := r.Min.Add(image.Pt((x+1)*cell, (y+1)*cell))
					fillRect(img, image.Rectangle{at, at.Add(image.Pt(cell, cell))}, opaque(hs.On))
				}
			}
		}
	}
}

// brackets draws a spot meter's four corner brackets on the edge of r, arm
// pixels long and t thick.
func brackets(img *image.RGBA, r image.Rectangle, arm, t int, c color.RGBA) {
	arm = min(arm, r.Dx()/2, r.Dy()/2)
	for _, q := range [4][2]int{{r.Min.X, r.Min.Y}, {r.Max.X - arm, r.Min.Y}, {r.Min.X, r.Max.Y - t}, {r.Max.X - arm, r.Max.Y - t}} {
		fillRect(img, image.Rect(q[0], q[1], q[0]+arm, q[1]+t), c)
	}
	for _, q := range [4][2]int{{r.Min.X, r.Min.Y}, {r.Max.X - t, r.Min.Y}, {r.Min.X, r.Max.Y - arm}, {r.Max.X - t, r.Max.Y - arm}} {
		fillRect(img, image.Rect(q[0], q[1], q[0]+t, q[1]+arm), c)
	}
}

// marker draws the scale's marker: a triangle pointing down at the ramp,
// its tip at (x, y+h), half as wide as hw at the top.
func marker(img *image.RGBA, x, y, hw, h float64, c color.RGBA) {
	for py := int(y); py < int(math.Ceil(y+h)); py++ {
		half := hw * (1 - (float64(py)+0.5-y)/h)
		for px := int(x - hw - 1); px <= int(x+hw+1); px++ {
			d := math.Abs(float64(px)+0.5-x) - half
			blendPx(img, px, py, c, math.Min(1, math.Max(0, 0.5-d)))
		}
	}
}
