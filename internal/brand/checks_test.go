package brand

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func checkMotion(tf *tokensFile) []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	mo := tf.Motion
	for _, k := range []string{"instant", "fast", "base", "slow"} {
		if _, ok := mo.Duration.get(k); !ok {
			bad("motion.duration.%s is required (the control center reads --vos-dur-%s)", k, k)
		}
	}
	for _, k := range mo.Duration.Keys {
		if d := mo.Duration.Vals[k]; !nameRe.MatchString(k) || d < 0 || d > 2000 {
			bad("motion.duration.%s: %d ms is outside 0..2000", k, d)
		}
	}
	if _, ok := mo.Ease.get("standard"); !ok {
		bad("motion.ease.standard is required")
	}
	for _, k := range mo.Ease.Keys {
		e := mo.Ease.Vals[k]
		if !bezierRe.MatchString(e) && !linearRe.MatchString(e) {
			bad("motion.ease.%s: %q is neither cubic-bezier(…) nor linear(…)", k, e)
		}
	}
	for _, k := range mo.Spring.Keys {
		sp := mo.Spring.Vals[k]
		e, ok := mo.Ease.get(k)
		if !ok || !strings.HasPrefix(e, "linear(") {
			bad("motion.spring.%s: needs a linear() ease of the same name", k)
			continue
		}
		if sp.Zeta <= 0 || sp.Zeta >= 1 || sp.Hz <= 0 {
			bad("motion.spring.%s: zeta must be within (0, 1) and hz positive", k)
			continue
		}
		pts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(e, "linear("), ")"), ",")
		n := len(pts) - 1
		w := 2 * math.Pi * sp.Hz
		wd := w * math.Sqrt(1-sp.Zeta*sp.Zeta)
		for i, p := range pts[:n] {
			got, _ := strconv.ParseFloat(strings.TrimSpace(p), 64)
			x := float64(i) / float64(n)
			want := 1 - math.Exp(-sp.Zeta*w*x)*(math.Cos(wd*x)+(sp.Zeta*w/wd)*math.Sin(wd*x))
			if math.Abs(got-want) > 0.002 {
				bad("motion.ease.%s: point %d is %v, the spring (zeta %v) gives %.3f", k, i, got, sp.Zeta, want)
				break
			}
		}
		if strings.TrimSpace(pts[n]) != "1" {
			bad("motion.ease.%s: the last point must be 1", k)
		}
	}
	for _, k := range mo.Pattern.Keys {
		p := mo.Pattern.Vals[k]
		hz := p.Hz
		if p.PeriodMs > 0 {
			hz = math.Max(hz, 1000/float64(p.PeriodMs))
		}
		if hz > 3 {
			bad("motion.pattern.%s: flashes at %.2f Hz, above 3 Hz (WCAG 2.3.1)", k, hz)
		}
		if p.PeriodMs < 0 || p.PeriodMs > 10000 || p.StepMs < 0 || p.StepMs > 2000 {
			bad("motion.pattern.%s: periodMs 0..10000, stepMs 0..2000", k)
		}
	}
	for _, k := range []string{"steady"} {
		if _, ok := mo.Pattern.get(k); !ok {
			bad("motion.pattern.%s is required", k)
		}
	}
	h := mo.Hold
	if h.Ms < 500 || h.Ms > 3000 || h.ReleaseMs < 0 || h.ReleaseMs > 2000 || h.FireMs < 0 || h.FireMs > 2000 || h.NudgeMs < 0 || h.NudgeMs > 10000 {
		bad("motion.hold: ms 500..3000, releaseMs and fireMs 0..2000, nudgeMs 0..10000")
	}
	if s := mo.Scene; s.CoolMs < 0 || s.CoolMs > 10000 || s.HeatMs < 0 || s.HeatMs > 10000 {
		bad("motion.scene: coolMs and heatMs 0..10000")
	}
	return errs
}

func checkTV(tf *tokensFile) []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	tv := tf.TV
	if tv.SafeInset < 0.035 || tv.SafeInset > 0.1 {
		bad("tv.safeInset %v is outside 0.035..0.1", tv.SafeInset)
	}
	if r := tv.Reference; r.Landscape[0] <= r.Landscape[1] || r.Portrait[0] >= r.Portrait[1] {
		bad("tv.reference: landscape must be wider than tall, portrait taller than wide")
	}
	for _, r := range tv.Type.roles() {
		v := r.v
		if v.Px < 16 || v.Px > 200 || v.PortraitPx != 0 && (v.PortraitPx < 16 || v.PortraitPx > 200) {
			bad("tv.type.%s: px must be within 16..200", r.key)
		}
		f, ok := tf.Font.get(v.Font)
		if !ok {
			bad("tv.type.%s: font %q is not a font role", r.key, v.Font)
			continue
		}
		if _, ok := f.Faces.get(v.Face); !ok && (v.Face != "state" || v.Font != "display") {
			bad("tv.type.%s: face %q is not a face of %s (\"state\" is allowed for display)", r.key, v.Face, v.Font)
		}
	}
	dark, err1 := tf.hexOf(tv.QR.Dark)
	light, err2 := tf.hexOf(tv.QR.Light)
	if err := errors.Join(err1, err2); err != nil {
		bad("tv.qr: %v", err)
	} else {
		d, _ := parseHex(dark)
		l, _ := parseHex(light)
		luma := func(c rgba) float64 { return 255 * (0.299*c.r + 0.587*c.g + 0.114*c.b) }
		if d.a != 1 || l.a != 1 || luma(d) >= 64 || luma(l) <= 192 || contrastRatio(d, l) < 12 {
			bad("tv.qr: dark needs luma < 64, light luma > 192, both opaque, and a ratio of at least 12:1")
		}
	}
	if tv.QR.QuietModules < 4 || tv.QR.Card.Landscape < 200 || tv.QR.Card.Portrait < 200 {
		bad("tv.qr: quietModules ≥ 4 and cards ≥ 200 px")
	}
	if tv.Look.Background == "" || tv.Look.Signature == "" || tv.Look.QRFrame == "" {
		bad("tv.look: background, signature and qrFrame are required")
	}
	lb := tf.Labels.TV
	if lb.CodeLabel == "" || lb.QRCaption == "" || lb.Starting == "" || lb.InstallerPrefix == "" || lb.ScaleCold == "" || lb.ScaleHot == "" {
		bad("labels.tv: every label but versionPrefix is required")
	}
	return errs
}

// checkFonts validates the font roles and any files F3 has listed.
func checkFonts(tf *tokensFile) []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	for _, r := range fontRoles {
		if _, ok := tf.Font.get(r); !ok {
			bad("font.%s is required", r)
		}
	}
	for _, r := range tf.Font.Keys {
		f := tf.Font.Vals[r]
		if f.Family == "" || len(f.Fallback) == 0 || len(f.Faces.Keys) == 0 {
			bad("font.%s: family, fallback and at least one face are required", r)
		}
		for _, k := range f.Faces.Keys {
			fc := f.Faces.Vals[k]
			if !nameRe.MatchString(k) || fc.Wdth < 25 || fc.Wdth > 200 || fc.Wght < 100 || fc.Wght > 1000 {
				bad("font.%s.faces.%s: needs a kebab-case name, wdth 25..200 and wght 100..1000", r, k)
			}
			if fc.Web != "" {
				if !strings.HasPrefix(fc.Web, "internal/web/static/fonts/") || !strings.HasSuffix(fc.Web, ".woff2") {
					bad("font.%s.faces.%s: web must be a .woff2 under internal/web/static/fonts/", r, k)
				} else if b, err := os.ReadFile(filepath.Join(repoRoot, fc.Web)); err != nil || !bytes.HasPrefix(b, []byte("wOF2")) {
					bad("font.%s.faces.%s: %s is missing or not WOFF2", r, k, fc.Web)
				}
			}
			if fc.TV != "" {
				errs = append(errs, checkTVFont(fmt.Sprintf("font.%s.faces.%s", r, k), fc.TV)...)
			}
		}
	}
	return errs
}

// checkTVFont reads the sfnt table directory: a static TrueType or CFF font
// with no fvar table, and an OFL licence beside it.
func checkTVFont(where, file string) []error {
	dir := filepath.Join(repoRoot, "internal", "display", "welcome", "fonts")
	b, err := os.ReadFile(filepath.Join(dir, filepath.Base(file)))
	if err != nil || len(b) < 12 {
		return []error{fmt.Errorf("%s: %s is missing from internal/display/welcome/fonts", where, file)}
	}
	var errs []error
	switch string(b[:4]) {
	case "\x00\x01\x00\x00", "true", "OTTO":
	default:
		errs = append(errs, fmt.Errorf("%s: %s is not a TrueType or OpenType font", where, file))
	}
	n := int(b[4])<<8 | int(b[5])
	for i := 0; i < n && 12+16*i+4 <= len(b); i++ {
		if string(b[12+16*i:12+16*i+4]) == "fvar" {
			errs = append(errs, fmt.Errorf("%s: %s is a variable font; the TV needs static instances", where, file))
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "OFL*.txt")); len(m) == 0 {
		errs = append(errs, fmt.Errorf("%s: no OFL licence beside %s", where, file))
	}
	return errs
}
