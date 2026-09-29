package brand

import (
	"bytes"
	"fmt"
	"go/format"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The logo and icon outputs, beside the token targets in gen_test.go:
//
//	mark        internal/brand/mark_gen.go, always (the TV draws the mark with it)
//	web-icons   the control center's app icons, manifest and both logo partials,
//	            and internal/web/icons_gen.go from design/icons/*.svg
//	site-icons  the website's favicon, touch and manifest icons, and
//	            src/lib/logo.gen.ts and src/lib/icons.gen.ts
//
// The test names extend TestGenerateDesign and TestDesignOutputsFresh, so the
// usual command (-run TestGenerateDesign) and the freshness check run them too.

// iconTargetOn reports whether an icon target is on. While design/tokens.json
// lists neither icon target, both are on; once it lists one, only the listed
// ones are.
func iconTargetOn(tf *tokensFile, name string) bool {
	if tf.targetOn("web-icons") || tf.targetOn("site-icons") {
		return tf.targetOn(name)
	}
	return true
}

func TestGenerateDesignIcons(t *testing.T) {
	if os.Getenv("VOS_GEN_DESIGN") != "1" {
		t.Skip("set VOS_GEN_DESIGN=1 to regenerate the design outputs")
	}
	tf, raw := loadTokens(t)
	if errs := checkAll(tf, raw); len(errs) > 0 {
		report(t, errs)
		t.Fatal("design/tokens.json is invalid; nothing was written")
	}
	outs, gone, err := iconOutputs(tf)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		p := filepath.Join(repoRoot, filepath.FromSlash(o.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, o.body, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%-10s %s (%d bytes)", o.target, o.path, len(o.body))
	}
	for _, g := range gone {
		if err := os.Remove(filepath.Join(repoRoot, filepath.FromSlash(g))); err == nil {
			t.Logf("removed    %s (no longer generated)", g)
		}
	}
}

func TestDesignOutputsFreshIcons(t *testing.T) {
	tf, _ := loadTokens(t)
	outs, gone, err := iconOutputs(tf)
	if err != nil {
		t.Fatalf("%v — fix it, then run %s", err, regenerate)
	}
	for _, o := range outs {
		got, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(o.path)))
		if err != nil {
			t.Errorf("missing: %s — run %s", o.path, regenerate)
			continue
		}
		if strings.HasSuffix(o.path, ".png") {
			if msg := samePNG(got, o.body); msg != "" {
				t.Errorf("stale: %s — run %s\n  %s", o.path, regenerate, msg)
			}
			continue
		}
		if !bytes.Equal(got, o.body) {
			line, have, want := firstDiff(got, o.body)
			t.Errorf("stale: %s — run %s\n  first difference on line %d:\n  committed: %q\n  generated: %q",
				o.path, regenerate, line, have, want)
		}
	}
	for _, g := range gone {
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(g))); err == nil {
			t.Errorf("stale: %s is no longer generated — run %s, which removes it", g, regenerate)
		}
	}
}

// samePNG compares two PNGs by their pixels: the size exactly, and at most
// 0.5% of pixels off by more than 2 in a channel. Deflate output and float
// rounding may differ between machines; the picture may not.
func samePNG(a, b []byte) string {
	ia, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		return "committed file does not decode: " + err.Error()
	}
	ib, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return "generated file does not decode: " + err.Error()
	}
	if ia.Bounds() != ib.Bounds() {
		return fmt.Sprintf("size %v, generated %v", ia.Bounds().Size(), ib.Bounds().Size())
	}
	r := ia.Bounds()
	off := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ca := color.NRGBAModel.Convert(ia.At(x, y)).(color.NRGBA)
			cb := color.NRGBAModel.Convert(ib.At(x, y)).(color.NRGBA)
			if far(ca.R, cb.R) || far(ca.G, cb.G) || far(ca.B, cb.B) || far(ca.A, cb.A) {
				off++
			}
		}
	}
	if off*200 > r.Dx()*r.Dy() {
		return fmt.Sprintf("%d of %d pixels differ", off, r.Dx()*r.Dy())
	}
	return ""
}

func far(a, b uint8) bool { return int(a) > int(b)+2 || int(b) > int(a)+2 }

// iconTarget is one generator of icon outputs. A target may have several
// (web-icons has the icons, the partials and the UI icon set).
type iconTarget struct {
	name string // "mark", "web-icons" or "site-icons"
	gen  func(tf *tokensFile, syms map[string]*logoSym) (outs []output, gone []string, err error)
}

// iconTargets is filled by each target's file.
var iconTargets = []iconTarget{{"mark", markTarget}}

// iconOutputs renders every icon output of the targets that are on, and
// lists the optional files that are not generated (so they can be removed).
func iconOutputs(tf *tokensFile) (outs []output, gone []string, err error) {
	b, err := os.ReadFile(filepath.Join(repoRoot, logoPath))
	if err != nil {
		return nil, nil, err
	}
	list, err := parseLogo(b)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", logoPath, err)
	}
	syms := map[string]*logoSym{}
	for i := range list {
		syms[list[i].ID] = &list[i]
	}
	for _, d := range logoDrawings {
		if syms[d.id] == nil {
			return nil, nil, fmt.Errorf("%s has no drawing %q", logoPath, d.id)
		}
	}
	for _, id := range []string{"icon", "icon-small"} {
		if syms[id].drawing().TileColor().A != 255 {
			return nil, nil, fmt.Errorf(`%s: drawing %q needs a <g class="tile"> with an opaque fill`, logoPath, id)
		}
	}
	for _, it := range iconTargets {
		if it.name != "mark" && !iconTargetOn(tf, it.name) {
			continue
		}
		o, g, err := it.gen(tf, syms)
		if err != nil {
			return nil, nil, err
		}
		outs, gone = append(outs, o...), append(gone, g...)
	}
	return outs, gone, nil
}

// encodePNG writes img at best compression: RGBA, or paletted when that is
// smaller. A drawing of flat colours has few colours but for its
// anti-aliased edges, so the palette keeps the 256 most frequent colours and
// maps every other pixel to the nearest of them. That is deterministic, and
// invisible at icon sizes.
func encodePNG(img *image.NRGBA) []byte {
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	var full bytes.Buffer
	if err := enc.Encode(&full, img); err != nil {
		panic(err)
	}
	var small bytes.Buffer
	if err := enc.Encode(&small, paletted(img)); err != nil {
		panic(err)
	}
	if small.Len() < full.Len() {
		return small.Bytes()
	}
	return full.Bytes()
}

// paletted maps img onto its 256 most frequent colours (ties broken by
// value), each other colour going to the nearest palette entry.
func paletted(img *image.NRGBA) *image.Paletted {
	norm := func(i int) color.NRGBA {
		c := color.NRGBA{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
		if c.A == 0 {
			return color.NRGBA{}
		}
		return c
	}
	count := map[color.NRGBA]int{}
	for i := 0; i < len(img.Pix); i += 4 {
		count[norm(i)]++
	}
	cs := make([]color.NRGBA, 0, len(count))
	for c := range count {
		cs = append(cs, c)
	}
	key := func(c color.NRGBA) uint32 { return uint32(c.R)<<24 | uint32(c.G)<<16 | uint32(c.B)<<8 | uint32(c.A) }
	sort.Slice(cs, func(i, j int) bool {
		if count[cs[i]] != count[cs[j]] {
			return count[cs[i]] > count[cs[j]]
		}
		return key(cs[i]) < key(cs[j])
	})
	cs = cs[:min(len(cs), 256)]
	pal := make(color.Palette, len(cs))
	index := map[color.NRGBA]uint8{}
	for i, c := range cs {
		pal[i] = c
		index[c] = uint8(i)
	}
	near := func(c color.NRGBA) uint8 {
		if k, ok := index[c]; ok {
			return k
		}
		best, bd := 0, -1
		for i, p := range cs {
			dr, dg, db, da := int(c.R)-int(p.R), int(c.G)-int(p.G), int(c.B)-int(p.B), int(c.A)-int(p.A)
			if d := dr*dr + dg*dg + db*db + 2*da*da; bd < 0 || d < bd {
				best, bd = i, d
			}
		}
		index[c] = uint8(best)
		return uint8(best)
	}
	out := image.NewPaletted(img.Rect, pal)
	for i, j := 0, 0; i < len(img.Pix); i, j = i+4, j+1 {
		out.Pix[j] = near(norm(i))
	}
	return out
}

// ---- mark_gen.go

func goF32(v float32) string { return strconv.FormatFloat(float64(v), 'f', -1, 32) }

func goPaint(p Paint) string {
	switch {
	case p.Current:
		return "Paint{Current: true}"
	case p.Color.A == 0:
		return ""
	}
	return fmt.Sprintf("Paint{Color: nrgba(0x%02x%02x%02x%02x)}", p.Color.R, p.Color.G, p.Color.B, p.Color.A)
}

func markTarget(_ *tokensFile, syms map[string]*logoSym) ([]output, []string, error) {
	src, err := markGo(syms)
	if err != nil {
		return nil, nil, err
	}
	return []output{{"mark", "internal/brand/mark_gen.go", src}}, nil, nil
}

func markGo(syms map[string]*logoSym) ([]byte, error) {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("// Code generated by go test ./internal/brand -run TestGenerateDesign (VOS_GEN_DESIGN=1) from design/logo.svg. DO NOT EDIT.\n\n")
	w("package brand\n\n")
	for _, ld := range logoDrawings {
		s := syms[ld.id]
		w("// %s is design/logo.svg's %q: %s.\n", ld.goName, ld.id, ld.doc)
		w("var %s = &Drawing{ViewBox: [4]float32{%s, %s, %s, %s}, Paths: []Path{\n", ld.goName,
			goF32(s.ViewBox[0]), goF32(s.ViewBox[1]), goF32(s.ViewBox[2]), goF32(s.ViewBox[3]))
		for _, lp := range s.Paths {
			p := lp.Path
			var f []string
			if p.Group != "" {
				f = append(f, fmt.Sprintf("Group: %q", p.Group))
			}
			if p.Class != "" {
				f = append(f, fmt.Sprintf("Class: %q", p.Class))
			}
			if g := goPaint(p.Fill); g != "" {
				f = append(f, "Fill: "+g)
			}
			if g := goPaint(p.Stroke); g != "" {
				f = append(f, "Stroke: "+g, "Width: "+goF32(p.Width))
			}
			f = append(f, fmt.Sprintf("Ops: %q", p.Ops))
			w("\t{%s, Pts: []float32{", strings.Join(f, ", "))
			for i, v := range p.Pts {
				if i%12 == 0 {
					w("\n\t\t")
				} else {
					w(" ")
				}
				w("%s,", goF32(v))
			}
			w("\n\t}},\n")
		}
		w("}}\n\n")
	}
	return format.Source([]byte(b.String()))
}
