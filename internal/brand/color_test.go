package brand

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// rgba is a colour with alpha, channels 0..1.
type rgba struct{ r, g, b, a float64 }

func parseHex(h string) (rgba, error) {
	if !hexRe.MatchString(h) {
		return rgba{}, fmt.Errorf("%q is not lowercase #rrggbb or #rrggbbaa", h)
	}
	v, _ := strconv.ParseUint(h[1:7], 16, 32)
	c := rgba{float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255, 1}
	if len(h) == 9 {
		a, _ := strconv.ParseUint(h[7:9], 16, 8)
		c.a = float64(a) / 255
	}
	return c, nil
}

// over composites c over an opaque background.
func (c rgba) over(bg rgba) rgba {
	mix := func(x, y float64) float64 { return x*c.a + float64(y*(1-c.a)) }
	return rgba{mix(c.r, bg.r), mix(c.g, bg.g), mix(c.b, bg.b), 1}
}

// luminance is WCAG 2.x relative luminance.
func (c rgba) luminance() float64 {
	lin := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.r) + 0.7152*lin(c.g) + 0.0722*lin(c.b)
}

func contrastRatio(a, b rgba) float64 {
	la, lb := a.luminance(), b.luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// named returns every palette and ramp stop name with its hex value.
func (tf *tokensFile) named() map[string]string {
	m := map[string]string{}
	for _, k := range tf.Palette.Keys {
		m[k] = tf.Palette.Vals[k].Hex
	}
	for _, k := range tf.Ramp.Keys {
		r := tf.Ramp.Vals[k]
		for i, h := range r.Stops {
			m[r.Prefix+strconv.Itoa(i)] = h
		}
	}
	return m
}

// hexOf resolves a colour value (a palette or ramp name, or a hex literal).
func (tf *tokensFile) hexOf(v string) (string, error) {
	if strings.HasPrefix(v, "#") {
		if !hexRe.MatchString(v) {
			return "", fmt.Errorf("%q is not lowercase #rrggbb or #rrggbbaa", v)
		}
		return v, nil
	}
	if h, ok := tf.named()[v]; ok {
		return h, nil
	}
	return "", fmt.Errorf("%q names no palette colour or ramp stop", v)
}

// pick returns a per-mode value: tv falls back to dark.
func (m modeVal) pick(mode string) string {
	switch mode {
	case "light":
		return m.Light
	case "tv":
		if m.TV != "" {
			return m.TV
		}
	}
	return m.Dark
}

func (tf *tokensFile) modeHex(m modeVal, mode string) (string, error) {
	v := m.pick(mode)
	if v == "" {
		return "", fmt.Errorf("no %s value", mode)
	}
	return tf.hexOf(v)
}

func (tf *tokensFile) roleHex(role, mode string) (string, error) {
	m, ok := tf.Color.get(role)
	if !ok {
		return "", fmt.Errorf("no colour role %q", role)
	}
	h, err := tf.modeHex(m, mode)
	if err != nil {
		return "", fmt.Errorf("color.%s: %w", role, err)
	}
	return h, nil
}

// allModes is every mode a surface declares.
func (tf *tokensFile) allModes() []string {
	var out []string
	for _, l := range [][]string{tf.Modes.Web, tf.Modes.Site, tf.Modes.TV} {
		for _, m := range l {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out
}

func (tf *tokensFile) targetOn(name string) bool { return slices.Contains(tf.Targets, name) }

// stateEntries lists every styled state: the seven, neutral, attention.pair.
func (tf *tokensFile) stateEntries() []struct {
	name string
	st   stateTok
} {
	type e = struct {
		name string
		st   stateTok
	}
	var out []e
	for _, k := range tf.State.Keys {
		out = append(out, e{k, tf.State.Vals[k]})
	}
	out = append(out, e{"neutral", tf.Neutral})
	for _, k := range tf.Attention.Keys {
		out = append(out, e{"attention." + k, tf.Attention.Vals[k]})
	}
	return out
}

// operand is one side of a contrast pair, already resolved for a mode.
type operand struct {
	name, hex string
}

// expand resolves a contrast operand. A "state.*.<part>" operand yields one
// entry per styled state, keyed by state so the two sides can be zipped.
func (tf *tokensFile) expand(name, mode string) (list []operand, perState map[string]operand, err error) {
	switch {
	case strings.HasPrefix(name, "state.*."):
		part := strings.TrimPrefix(name, "state.*.")
		perState = map[string]operand{}
		for _, e := range tf.stateEntries() {
			var m modeVal
			switch part {
			case "color":
				m = e.st.Color
			case "ink":
				m = e.st.Ink
			case "soft":
				m = e.st.Soft
			default:
				return nil, nil, fmt.Errorf("unknown state part %q", part)
			}
			h, err := tf.modeHex(m, mode)
			if err != nil {
				return nil, nil, fmt.Errorf("%s.%s: %w", e.name, part, err)
			}
			perState[e.name] = operand{e.name + "." + part, h}
		}
		return nil, perState, nil
	case name == "handshake.on" || name == "handshake.ground":
		l := tf.Handshake.On
		if name == "handshake.ground" {
			l = tf.Handshake.Ground
		}
		for _, v := range l {
			h, err := tf.hexOf(v)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", name, err)
			}
			list = append(list, operand{name + "." + v, h})
		}
		return list, nil, nil
	}
	if _, ok := tf.Color.get(name); ok {
		h, err := tf.roleHex(name, mode)
		return []operand{{name, h}}, nil, err
	}
	h, err := tf.hexOf(name)
	return []operand{{name, h}}, nil, err
}

// checkContrast measures every rule in every mode. Alpha on the background
// is composited over the mode's canvas, alpha on the text over the background.
func checkContrast(tf *tokensFile) []error {
	var errs []error
	for _, mode := range tf.allModes() {
		canvasHex, err := tf.roleHex("canvas", mode)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		canvas, _ := parseHex(canvasHex)
		canvas.a = 1
		for i, r := range tf.Contrast {
			var pairs [][2]operand
			for _, fgName := range r.FG {
				fgList, fgPer, err := tf.expand(fgName, mode)
				if err != nil {
					errs = append(errs, fmt.Errorf("contrast[%d] %s: %w", i, mode, err))
					continue
				}
				for _, bgName := range r.BG {
					bgList, bgPer, err := tf.expand(bgName, mode)
					if err != nil {
						errs = append(errs, fmt.Errorf("contrast[%d] %s: %w", i, mode, err))
						continue
					}
					switch {
					case fgPer != nil && bgPer != nil:
						for _, e := range tf.stateEntries() {
							pairs = append(pairs, [2]operand{fgPer[e.name], bgPer[e.name]})
						}
					case fgPer != nil:
						for _, e := range tf.stateEntries() {
							for _, b := range bgList {
								pairs = append(pairs, [2]operand{fgPer[e.name], b})
							}
						}
					case bgPer != nil:
						for _, e := range tf.stateEntries() {
							for _, f := range fgList {
								pairs = append(pairs, [2]operand{f, bgPer[e.name]})
							}
						}
					default:
						for _, f := range fgList {
							for _, b := range bgList {
								pairs = append(pairs, [2]operand{f, b})
							}
						}
					}
				}
			}
			for _, p := range pairs {
				fg, _ := parseHex(p[0].hex)
				bg, _ := parseHex(p[1].hex)
				bg = bg.over(canvas)
				fg = fg.over(bg)
				if got := contrastRatio(fg, bg); got < r.Min {
					errs = append(errs, fmt.Errorf("contrast %s: %s (%s) on %s (%s) is %.2f:1, needs %.1f:1 (%s)",
						mode, p[0].name, p[0].hex, p[1].name, p[1].hex, got, r.Min, r.Why))
				}
			}
		}
	}
	return errs
}

func TestTokensContrast(t *testing.T) {
	tf, _ := loadTokens(t)
	report(t, checkContrast(tf))
}
