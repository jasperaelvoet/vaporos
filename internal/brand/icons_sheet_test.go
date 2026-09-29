package brand

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// TestGenerateDesignSheet draws the logo contact sheet for review: the app
// icon at 16, 24, 32, 180 and 512 px on dark and light browser chrome, the
// marks at chrome sizes, the TV's brand corner at 1:1 on a 1920×1080 frame
// and the phone's app bar and tab bar at 2×. Everything is drawn from
// design/logo.svg with the rasterizer the outputs use.
//
//	VOS_GEN_DESIGN=sheet [VOS_GEN_OUT=<dir>] go test ./internal/brand -run TestGenerateDesign
//
// It writes <dir>/contact-sheet.png (default .build/design, which git ignores).
func TestGenerateDesignSheet(t *testing.T) {
	if os.Getenv("VOS_GEN_DESIGN") != "sheet" {
		t.Skip("set VOS_GEN_DESIGN=sheet to draw the logo contact sheet")
	}
	syms := map[string]*Drawing{}
	for _, s := range readLogo(t) {
		syms[s.ID] = s.drawing()
	}
	ft, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face := func(px float64) font.Face {
		f, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	small, label := face(13), face(16)

	const W, H = 1560, 1660
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	fill := func(r image.Rectangle, c color.Color) {
		draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
	}
	text := func(f font.Face, x, y int, c color.Color, s string) {
		(&font.Drawer{Dst: img, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}).DrawString(s)
	}
	fill(img.Rect, Palette.Ash)

	// The app icon on browser chrome, as a tab, a bookmark and a home screen see it.
	chrome := []struct {
		name   string
		bg, fg color.NRGBA
	}{
		{"dark browser chrome", nrgba(0x202124ff), nrgba(0xe8eaedff)},
		{"light browser chrome", nrgba(0xf1f3f4ff), nrgba(0x3c4043ff)},
	}
	for i, ch := range chrome {
		y0 := 20 + i*610
		fill(image.Rect(20, y0, W-20, y0+590), ch.bg)
		text(label, 40, y0+30, ch.fg, "App icon on "+ch.name)
		x := 40
		for _, ic := range []struct {
			size      int
			d         *Drawing
			fullBleed bool
			note      string
		}{
			{16, syms["icon-small"], false, "16 favicon (icon.svg)"},
			{24, syms["icon-small"], false, "24"},
			{32, syms["icon-small"], false, "32"},
			{180, syms["icon"], true, "180 apple-touch (full-bleed)"},
			{512, syms["icon"], false, "512 manifest"},
		} {
			top := y0 + 50 + 512 - ic.size
			r := image.Rect(x, top, x+ic.size, top+ic.size)
			var src image.Image = RenderIcon(ic.d, ic.size, ic.fullBleed)
			draw.Draw(img, r, src, image.Point{}, draw.Over)
			text(small, x, y0+50+512+22, ch.fg, ic.note)
			x += max(ic.size, len(ic.note)*7) + 36
		}
	}

	// The marks at chrome sizes on ash, and the light-ground mark on white-hot.
	y0 := 1240
	text(label, 40, y0+4, Palette.Smoke, "Marks at 16, 20, 24, 32 and 64 px; mono; on white-hot; the lockup")
	x := 40
	for _, s := range []int{16, 20, 24, 32, 64} {
		syms["mark"].Draw(img, image.Rect(x, y0+84-s, x+s, y0+84), nil)
		text(small, x, y0+104, Palette.Dim, strconv.Itoa(s))
		x += s + 28
	}
	syms["mono"].Draw(img, image.Rect(x, y0+60, x+24, y0+84), nil)
	text(small, x, y0+104, Palette.Dim, "mono")
	x += 70
	fill(image.Rect(x-12, y0+30, x+100, y0+96), HeatRamp[9])
	syms["mark-light"].Draw(img, image.Rect(x, y0+52, x+32, y0+84), nil)
	syms["mark-light"].Draw(img, image.Rect(x+48, y0+60, x+72, y0+84), nil)
	x += 140
	syms["mark"].Draw(img, image.Rect(x, y0+44, x+40, y0+84), nil)
	lockW := int(float32(28) * syms["wordmark"].ViewBox[2] / syms["wordmark"].ViewBox[3])
	syms["wordmark"].Draw(img, image.Rect(x+50, y0+54, x+50+lockW, y0+82), nil)

	// The TV's brand corner at 1:1: the 1920×1080 layout puts the mark at
	// (96, 96), 58 px, and the 46 px wordmark 80 px to its right.
	y0 = 1370
	text(label, 40, y0, Palette.Smoke, "TV at 1920×1080, 1:1 crop")
	tv := image.Rect(40, y0+14, 40+720, y0+14+260)
	fill(tv, TVCanvas)
	fill(image.Rect(tv.Min.X, tv.Min.Y+170, tv.Max.X, tv.Max.Y), TVCanvas2)
	syms["mark"].Draw(img, image.Rect(tv.Min.X+96, tv.Min.Y+96, tv.Min.X+96+58, tv.Min.Y+96+58), map[string]color.Color{"line": TVCanvas})
	wh := 35 // the 46 px cut's box: cap height 33 of 36 units
	ww := int(float32(wh) * syms["wordmark"].ViewBox[2] / syms["wordmark"].ViewBox[3])
	syms["wordmark"].Draw(img, image.Rect(tv.Min.X+176, tv.Min.Y+109, tv.Min.X+176+ww, tv.Min.Y+109+wh), nil)

	// The phone at 2×: the app bar's lockup, and the tab bar's icons beside
	// the mark's weight.
	text(label, 800, y0, Palette.Smoke, "Phone app bar and tab bar, 390 pt wide at 2×")
	ph := image.Rect(800, y0+14, 800+780, y0+14+260)
	fill(ph, Palette.Ash)
	fill(image.Rect(ph.Min.X, ph.Min.Y, ph.Max.X, ph.Min.Y+112), Palette.Soot)
	syms["mark"].Draw(img, image.Rect(ph.Min.X+32, ph.Min.Y+32, ph.Min.X+32+48, ph.Min.Y+32+48), nil)
	bw := int(float32(36) * syms["wordmark"].ViewBox[2] / syms["wordmark"].ViewBox[3])
	syms["wordmark"].Draw(img, image.Rect(ph.Min.X+96, ph.Min.Y+38, ph.Min.X+96+bw, ph.Min.Y+38+36), nil)
	fill(image.Rect(ph.Min.X, ph.Max.Y-120, ph.Max.X, ph.Max.Y), Palette.Soot)
	fill(image.Rect(ph.Min.X, ph.Max.Y-121, ph.Max.X, ph.Max.Y-120), Palette.Line)
	tab := face(22)
	for i, tb := range []struct{ icon, name string }{{"home", "Home"}, {"phone", "Devices"}, {"monitor", "Screen"}, {"sliders", "System"}} {
		c := Palette.Smoke
		if i == 0 {
			c = Palette.Bone
		}
		cx := ph.Min.X + 97 + i*195
		ic, err := sheetIcon(tb.icon)
		if err != nil {
			t.Fatal(err)
		}
		ic.Draw(img, image.Rect(cx-24, ph.Max.Y-104, cx+24, ph.Max.Y-56), map[string]color.Color{"currentColor": c})
		w := font.MeasureString(tab, tb.name).Ceil()
		text(tab, cx-w/2, ph.Max.Y-24, c, tb.name)
	}

	out := os.Getenv("VOS_GEN_OUT")
	if out == "" {
		out = filepath.Join(repoRoot, ".build", "design")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(out, "contact-sheet.png")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", p)
}

// sheetIcon turns design/icons/<name>.svg into a drawing stroked in
// currentColor at the control center's width, for the tab-bar crop. It knows
// the shapes the icons use: paths, lines, rects (with rx) and circles.
func sheetIcon(name string) (*Drawing, error) {
	b, err := os.ReadFile(filepath.Join(repoRoot, iconsDir, name+".svg"))
	if err != nil {
		return nil, err
	}
	inner, err := iconInner(b)
	if err != nil {
		return nil, err
	}
	d := &Drawing{ViewBox: [4]float32{0, 0, 24, 24}}
	attr := func(tag, k string) float64 {
		i := strings.Index(tag, " "+k+`="`)
		if i < 0 {
			return 0
		}
		v := tag[i+len(k)+3:]
		f, _ := strconv.ParseFloat(v[:strings.IndexByte(v, '"')], 64)
		return f
	}
	for _, tag := range strings.SplitAfter(inner, "/>") {
		if tag = strings.TrimSpace(tag); tag == "" {
			continue
		}
		var data string
		switch {
		case strings.HasPrefix(tag, "<path"):
			i := strings.Index(tag, ` d="`) + 4
			data = tag[i : i+strings.IndexByte(tag[i:], '"')]
		case strings.HasPrefix(tag, "<line"):
			data = fmt.Sprintf("M%g %gL%g %g", attr(tag, "x1"), attr(tag, "y1"), attr(tag, "x2"), attr(tag, "y2"))
		case strings.HasPrefix(tag, "<circle"):
			cx, cy, r := attr(tag, "cx"), attr(tag, "cy"), attr(tag, "r")
			data = roundRect(cx-r, cy-r, 2*r, 2*r, r)
		case strings.HasPrefix(tag, "<rect"):
			data = roundRect(attr(tag, "x"), attr(tag, "y"), attr(tag, "width"), attr(tag, "height"), attr(tag, "rx"))
		default:
			return nil, fmt.Errorf("%s: the sheet cannot draw %s", name, tag)
		}
		ops, pts, err := parsePathData(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		d.Paths = append(d.Paths, Path{Stroke: Paint{Current: true}, Width: 2, Ops: ops, Pts: pts})
	}
	return d, nil
}

// roundRect is path data for a rectangle with corner radius r (cubic corners).
func roundRect(x, y, w, h, r float64) string {
	r = min(r, w/2, h/2)
	k := r * 0.5523
	return fmt.Sprintf("M%g %gH%gC%g %g %g %g %g %gV%gC%g %g %g %g %g %gH%gC%g %g %g %g %g %gV%gC%g %g %g %g %g %gZ",
		x+r, y, x+w-r, x+w-r+k, y, x+w, y+r-k, x+w, y+r,
		y+h-r, x+w, y+h-r+k, x+w-r+k, y+h, x+w-r, y+h,
		x+r, x+r-k, y+h, x, y+h-r+k, x, y+h-r,
		y+r, x, y+r-k, x+r-k, y, x+r, y)
}
