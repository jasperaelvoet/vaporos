package brand

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// regenerate is the command every stale-output message prints.
const regenerate = "VOS_GEN_DESIGN=1 go test ./internal/brand -run TestGenerateDesign"

// output is one generated file, by repo-relative path.
type output struct {
	target, path string
	body         []byte
}

// TestGenerateDesign validates design/tokens.json and writes every enabled
// target. It is the only writer of the generated files.
//
//	VOS_GEN_DESIGN=1 go test ./internal/brand -run TestGenerateDesign
func TestGenerateDesign(t *testing.T) {
	if os.Getenv("VOS_GEN_DESIGN") != "1" {
		t.Skip("set VOS_GEN_DESIGN=1 to regenerate the design outputs")
	}
	tf, raw := loadTokens(t)
	if errs := checkAll(tf, raw); len(errs) > 0 {
		report(t, errs)
		t.Fatal("design/tokens.json is invalid; nothing was written")
	}
	outs, err := generate(tf, raw)
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
		t.Logf("%-5s %s (%d bytes)", o.target, o.path, len(o.body))
	}
}

// TestDesignOutputsFresh regenerates in memory and compares with what is
// committed, so a tokens.json edit without a regeneration fails.
func TestDesignOutputsFresh(t *testing.T) {
	tf, raw := loadTokens(t)
	outs, err := generate(tf, raw)
	if err != nil {
		t.Fatalf("design/tokens.json: %v — fix it, then run %s", err, regenerate)
	}
	for _, o := range outs {
		got, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(o.path)))
		if err != nil {
			t.Errorf("missing: %s — run %s", o.path, regenerate)
			continue
		}
		if !bytes.Equal(got, o.body) {
			line, have, want := firstDiff(got, o.body)
			t.Errorf("stale: %s — run %s\n  first difference on line %d:\n  committed: %q\n  generated: %q",
				o.path, regenerate, line, have, want)
		}
	}
}

func firstDiff(a, b []byte) (int, string, string) {
	la, lb := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < max(len(la), len(lb)); i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y || i >= len(la) || i >= len(lb) {
			return i + 1, clip(x), clip(y)
		}
	}
	return 0, "", ""
}

func clip(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// generate renders every enabled target.
func generate(tf *tokensFile, raw []byte) ([]output, error) {
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])[:12]
	var outs []output
	if tf.targetOn("tv") {
		src, err := goTokens(tf, sha)
		if err != nil {
			return nil, err
		}
		outs = append(outs, output{"tv", "internal/brand/tokens_gen.go", src})
	}
	if tf.targetOn("web") {
		css, err := cssTokens(tf, sha, cssOpts{
			header: "/* Code generated from design/tokens.json by go test ./internal/brand (VOS_GEN_DESIGN=1). DO NOT EDIT.\n" +
				"   The control center's Tailwind v4 input: styles/app.css imports it. Direction: " + tf.Direction + ". */",
			modes:     modeDefaults(tf.Modes.Web, tf.Theme.Default),
			fontFaces: true,
			heatVars:  true,
		})
		if err != nil {
			return nil, err
		}
		outs = append(outs, output{"web", "internal/web/styles/tokens.css", []byte(css)})
		vec, err := heatVectors(tf)
		if err != nil {
			return nil, err
		}
		outs = append(outs, output{"web", "design/heat-vectors.json", vec})
	}
	if tf.targetOn("site") {
		site, err := siteOutputs(tf, sha)
		if err != nil {
			return nil, err
		}
		outs = append(outs, site...)
	}
	return outs, nil
}

// fmtNum prints a number the shortest way that round-trips.
func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// camel turns a kebab-case token name into a Go identifier part.
func camel(s string) string {
	var b strings.Builder
	for _, p := range strings.Split(s, "-") {
		if p != "" {
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}

// goColor is an nrgba(0xRRGGBBAA) call, so a value is easy to find in tokens.json.
func goColor(h string) string {
	c, err := parseHex(h)
	if err != nil {
		panic(err)
	}
	b := func(v float64) int { return int(math.Round(v * 255)) }
	return fmt.Sprintf("nrgba(0x%02x%02x%02x%02x)", b(c.r), b(c.g), b(c.b), b(c.a))
}

// mustHex resolves a value the validation has already accepted.
func (tf *tokensFile) mustHex(v string) string {
	h, err := tf.hexOf(v)
	if err != nil {
		panic(err)
	}
	return h
}

func goStrings(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = strconv.Quote(s)
	}
	return "[]string{" + strings.Join(q, ", ") + "}"
}

func goStyle(tf *tokensFile, st stateTok, indent string) string {
	l := st.Look
	cold := ""
	if l.Map == "cold" {
		cold = " ColdMap: true,"
	}
	return fmt.Sprintf("{\n%[1]s\tLabel: %[2]q, Color: %[3]s, Ink: %[4]s, Soft: %[5]s,\n"+
		"%[1]s\tMotion: %[6]q, Reduced: %[7]q, Signage: %[8]q,\n"+
		"%[1]s\tHeat: %[9]s, Cut: %[10]q, From: %[11]s,%[12]s Reach: %[13]s, Peak: %[14]s,\n%[1]s}",
		indent, st.Label, goColor(tf.mustHex(st.Color.pick("tv"))), goColor(tf.mustHex(st.Ink.pick("tv"))),
		goColor(tf.mustHex(st.Soft.pick("tv"))), st.Motion, st.Reduced, st.Signage,
		fmtNum(l.Heat), l.Cut, fmtNum(l.From), cold, fmtNum(l.Reach), fmtNum(l.Peak))
}

// goTokens writes internal/brand/tokens_gen.go. Colours are the tv mode's
// (which falls back to dark), since the TV is the Go consumer that paints.
func goTokens(tf *tokensFile, sha string) ([]byte, error) {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("// Code generated by go test ./internal/brand -run TestGenerateDesign (VOS_GEN_DESIGN=1) from design/tokens.json. DO NOT EDIT.\n\n")
	w("package brand\n\nimport \"image/color\"\n\n")
	w("// TokensSHA is the first 12 hex digits of the SHA-256 of design/tokens.json.\nconst TokensSHA = %q\n\n", sha)
	w("// Direction is the visual direction design/tokens.json carries.\nconst Direction = %q\n\n", tf.Direction)

	dark, err := tf.roleHex("canvas", "dark")
	if err != nil {
		return nil, err
	}
	light := dark
	if h, err := tf.roleHex("canvas", "light"); err == nil {
		light = h
	}
	w("// Theme colours for <meta name=\"theme-color\">. Without a light mode both are the dark canvas.\n")
	w("const (\n\tThemeColorDark = %q\n\tThemeColorLight = %q\n)\n\n", dark, light)
	w("// The product's name and one-line description (web manifest, site metadata).\n")
	w("const (\n\tAppName = %q\n\tAppShortName = %q\n\tAppDescription = %q\n)\n\n", tf.App.Name, tf.App.ShortName, tf.App.Description)

	w("// Palette holds the direction's named colours.\nvar Palette = struct {\n")
	for _, k := range tf.Palette.Keys {
		w("\t%s color.NRGBA\n", camel(k))
	}
	w("}{\n")
	for _, k := range tf.Palette.Keys {
		p := tf.Palette.Vals[k]
		w("\t%s: %s, // %s\n", camel(k), goColor(p.Hex), p.Use)
	}
	w("}\n\n")
	for _, k := range tf.Ramp.Keys {
		r := tf.Ramp.Vals[k]
		w("// %sRamp is %s0 to %s%d, %s.\nvar %sRamp = []color.NRGBA{\n", camel(k), r.Prefix, r.Prefix, len(r.Stops)-1, r.Use, camel(k))
		for _, h := range r.Stops {
			w("\t%s,\n", goColor(h))
		}
		w("}\n\n")
	}

	w("var stateStyles = map[State]Style{\n")
	for _, k := range tf.State.Keys {
		w("\t%s: %s,\n", camel(k), goStyle(tf, tf.State.Vals[k], "\t"))
	}
	w("}\n\n")
	w("var neutralStyle = Style%s\n\n", goStyle(tf, tf.Neutral, ""))
	for _, k := range tf.Attention.Keys {
		w("// Attention%s is the %s modifier on top of a state.\nvar Attention%s = Style%s\n\n", camel(k), k, camel(k), goStyle(tf, tf.Attention.Vals[k], ""))
	}

	w("// TV colours: every colour role in the tv mode, and the QR card.\nvar (\n")
	for _, role := range tf.Color.Keys {
		h, err := tf.roleHex(role, "tv")
		if err != nil {
			return nil, err
		}
		w("\tTV%s = %s\n", camel(role), goColor(h))
	}
	w("\tTVQRDark = %s\n\tTVQRLight = %s\n)\n\n", goColor(tf.mustHex(tf.TV.QR.Dark)), goColor(tf.mustHex(tf.TV.QR.Light)))

	tv := tf.TV
	w("// TVSafeInset is the title-safe inset, as a fraction of each side.\nconst TVSafeInset = %s\n\n", fmtNum(tv.SafeInset))
	w("// TVReference is the reference canvas the TV lays out on before scaling.\n")
	w("var TVReference = struct{ Landscape, Portrait [2]int }{[2]int{%d, %d}, [2]int{%d, %d}}\n\n",
		tv.Reference.Landscape[0], tv.Reference.Landscape[1], tv.Reference.Portrait[0], tv.Reference.Portrait[1])
	w("// TVType is the TV's type scale in reference pixels.\nvar TVType = TVTypes{\n")
	for _, r := range tv.Type.roles() {
		v := r.v
		extra := ""
		if v.PortraitPx != 0 {
			extra += fmt.Sprintf(", PortraitPx: %d", v.PortraitPx)
		}
		if v.Tracking != 0 {
			extra += ", Tracking: " + fmtNum(v.Tracking)
		}
		if v.Upper {
			extra += ", Upper: true"
		}
		w("\t%s: TVText{Font: %q, Face: %q, Px: %d%s},\n", r.field, v.Font, v.Face, v.Px, extra)
	}
	w("}\n\n")
	lb := tf.Labels.TV
	w("// TVLabels is the copy the TV renderer owns.\nvar TVLabels = TVLabelSet{CodeLabel: %q, QRCaption: %q, VersionPrefix: %q, InstallerPrefix: %q, Starting: %q, ScaleCold: %q, ScaleHot: %q}\n\n",
		lb.CodeLabel, lb.QRCaption, lb.VersionPrefix, lb.InstallerPrefix, lb.Starting, lb.ScaleCold, lb.ScaleHot)
	w("// TVQR sizes the QR card.\nvar TVQR = TVQRCard{Landscape: %d, Portrait: %d, QuietModules: %d}\n\n", tv.QR.Card.Landscape, tv.QR.Card.Portrait, tv.QR.QuietModules)
	w("// TVLook names the TV look and its parameters.\nvar TVLook = TVLookSet{Background: %q, Signature: %q, QRFrame: %q, Params: map[string]float64{\n",
		tv.Look.Background, tv.Look.Signature, tv.Look.QRFrame)
	for _, k := range tv.Look.Params.Keys {
		w("\t%q: %s,\n", k, fmtNum(tv.Look.Params.Vals[k]))
	}
	w("}}\n\n")

	w("// Fonts lists each font role's family, fallbacks and static faces.\nvar Fonts = map[string]FontRole{\n")
	var tvFiles []string
	for _, r := range tf.Font.Keys {
		f := tf.Font.Vals[r]
		w("\t%q: {Family: %q, Fallback: %s, Features: %q, Faces: map[string]Face{", r, f.Family, goStrings(f.Fallback), f.Features)
		for i, k := range f.Faces.Keys {
			fc := f.Faces.Vals[k]
			if i > 0 {
				w(", ")
			}
			w("%q: {Wdth: %d, Wght: %d}", k, fc.Wdth, fc.Wght)
			if fc.TV != "" {
				tvFiles = append(tvFiles, fmt.Sprintf("\t%q: %q,\n", r+"/"+k, fc.TV))
			}
		}
		w("}},\n")
	}
	w("}\n\n")
	w("// TVFontFiles maps \"<role>/<face>\" to its TTF in internal/display/welcome/fonts.\nvar TVFontFiles = map[string]string{\n%s}\n\n", strings.Join(tvFiles, ""))

	w("var (\n\thandshakeOn = []color.NRGBA{")
	for i, v := range tf.Handshake.On {
		if i > 0 {
			w(", ")
		}
		w("%s", goColor(tf.mustHex(v)))
	}
	w("}\n\thandshakeGround = []color.NRGBA{")
	for i, v := range tf.Handshake.Ground {
		if i > 0 {
			w(", ")
		}
		w("%s", goColor(tf.mustHex(v)))
	}
	w("}\n)\n")

	return format.Source([]byte(b.String()))
}

// rampAt samples a ramp at t in [0, 1], linearly between stops. The explicit
// float64 conversion stops the compiler fusing a multiply-add, so arm64 and
// amd64 print the same tables.
func rampAt(stops []string, t float64) [3]float64 {
	t = math.Max(0, math.Min(1, t))
	f := t * float64(len(stops)-1)
	k := min(len(stops)-2, int(f))
	u := f - float64(k)
	a, _ := parseHex(stops[k])
	c, _ := parseHex(stops[k+1])
	lerp := func(x, y float64) float64 { return x + float64((y-x)*u) }
	return [3]float64{lerp(a.r, c.r), lerp(a.g, c.g), lerp(a.b, c.b)}
}

// filterTables builds the heat fields' discrete tables (the painter's, and
// once feComponentTransfer's): bands isotherms, each sub entries wide; the
// first entry of every band but the coolest is darkened by line, which
// draws a contour on the band's cool edge.
func filterTables(stops []string, bands, sub int, line float64) [3]string {
	var ch [3][]string
	for b := 0; b < bands; b++ {
		c := rampAt(stops, (float64(b)+0.5)/float64(bands))
		for s := 0; s < sub; s++ {
			k := 1.0
			if b > 0 && s == 0 {
				k = line
			}
			for i := range 3 {
				ch[i] = append(ch[i], fmt.Sprintf("%.3f", c[i]*k))
			}
		}
	}
	return [3]string{strings.Join(ch[0], " "), strings.Join(ch[1], " "), strings.Join(ch[2], " ")}
}

// heatVectors writes design/heat-vectors.json: each map's ramp and its
// discrete table (r, g and b, as feFuncR/G/B tableValues had them), so the
// painter's tables (static/js/heatmap.js, checked by
// internal/web/jstest/heatmap.test.mjs) cannot drift from these.
func heatVectors(tf *tokensFile) ([]byte, error) {
	type mapVec struct {
		Stops []string  `json:"stops"`
		Table [3]string `json:"table"`
	}
	tu := tf.Filter.Turbulence
	doc := struct {
		Note  string            `json:"note"`
		Air   string            `json:"air"`
		Bands string            `json:"bands"`
		Maps  map[string]mapVec `json:"maps"`
	}{
		Note: "Generated from design/tokens.json by go test ./internal/brand (VOS_GEN_DESIGN=1). DO NOT EDIT.",
		Air:  fmt.Sprintf("%s %d %d %s", tu.BaseFrequency, tu.Octaves, tu.Seed, fmtNum(tu.Scale)),
		Maps: map[string]mapVec{},
	}
	for _, id := range tf.Filter.Maps.Keys {
		m := tf.Filter.Maps.Vals[id]
		r, _ := tf.Ramp.get(m.Ramp)
		doc.Bands = fmt.Sprintf("%d %d %s", m.Bands, m.Sub, cssNum(m.Line))
		doc.Maps[m.Ramp] = mapVec{Stops: r.Stops, Table: filterTables(r.Stops, m.Bands, m.Sub, m.Line)}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// cssNum prints a number without a leading zero before the point (".7").
func cssNum(v float64) string {
	s := fmtNum(v)
	if strings.HasPrefix(s, "0.") {
		return s[1:]
	}
	if strings.HasPrefix(s, "-0.") {
		return "-" + s[2:]
	}
	return s
}
