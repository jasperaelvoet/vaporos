package brand

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// siteOutputs writes the website's tokens: the Tailwind CSS (no @font-face;
// the site loads fonts with next/font/local) and the same values as data.
func siteOutputs(tf *tokensFile, sha string) ([]output, error) {
	modes := modeDefaults(tf.Modes.Site, tf.Theme.Default)
	css, err := cssTokens(tf, sha, cssOpts{
		header: "/* Code generated from design/tokens.json by go test ./internal/brand (VOS_GEN_DESIGN=1). DO NOT EDIT.\n" +
			"   The website's Tailwind v4 tokens: src/app/globals.css imports it. Direction: " + tf.Direction + ". */",
		modes: modes,
	})
	if err != nil {
		return nil, err
	}
	ts, err := tsTokens(tf, sha, modes)
	if err != nil {
		return nil, err
	}
	return []output{
		{"site", "website/src/styles/tokens.gen.css", []byte(css)},
		{"site", "website/src/lib/tokens.gen.ts", []byte(ts)},
	}, nil
}

// obj is a TypeScript object literal that keeps its key order.
type obj []kv

type kv struct {
	k string
	v any
}

func stateObj(tf *tokensFile, st stateTok, mode string) obj {
	l := st.Look
	return obj{
		{"label", st.Label}, {"color", tf.mustHex(st.Color.pick(mode))}, {"ink", tf.mustHex(st.Ink.pick(mode))},
		{"soft", tf.mustHex(st.Soft.pick(mode))}, {"motion", st.Motion}, {"reduced", st.Reduced}, {"signage", st.Signage},
		{"heat", l.Heat}, {"cut", l.Cut}, {"from", l.From}, {"map", l.Map}, {"reach", l.Reach}, {"peak", l.Peak},
	}
}

// tsTokens renders website/src/lib/tokens.gen.ts: the tokens as `as const`
// data for canvas and WebGL painters, GSAP, state labels and the TV mock.
// State colours are the site's default mode; tv has its own role colours.
func tsTokens(tf *tokensFile, sha string, modes []string) (string, error) {
	palette := obj{}
	for _, k := range tf.Palette.Keys {
		palette = append(palette, kv{k, tf.Palette.Vals[k].Hex})
	}
	ramps := obj{}
	for _, k := range tf.Ramp.Keys {
		ramps = append(ramps, kv{k, tf.Ramp.Vals[k].Stops})
	}
	colors := obj{}
	for _, mode := range append(slices.Clone(modes), "tv") {
		m := obj{}
		for _, role := range tf.Color.Keys {
			h, err := tf.roleHex(role, mode)
			if err != nil {
				return "", err
			}
			m = append(m, kv{role, h})
		}
		colors = append(colors, kv{mode, m})
	}
	states := obj{}
	for _, k := range tf.State.Keys {
		states = append(states, kv{k, stateObj(tf, tf.State.Vals[k], modes[0])})
	}
	attention := obj{}
	for _, k := range tf.Attention.Keys {
		attention = append(attention, kv{k, stateObj(tf, tf.Attention.Vals[k], modes[0])})
	}
	fonts := obj{}
	for _, r := range tf.Font.Keys {
		f := tf.Font.Vals[r]
		faces := obj{}
		for _, k := range f.Faces.Keys {
			faces = append(faces, kv{k, obj{{"wdth", f.Faces.Vals[k].Wdth}, {"wght", f.Faces.Vals[k].Wght}}})
		}
		fonts = append(fonts, kv{r, obj{{"family", f.Family}, {"stack", fontStack(f)}, {"features", f.Features}, {"faces", faces}}})
	}
	text := obj{}
	for _, k := range tf.Text.Keys {
		t := tf.Text.Vals[k]
		text = append(text, kv{k, obj{{"size", t.Size}, {"leading", t.Leading}, {"tracking", t.Tracking}}})
	}
	radius := obj{}
	for _, k := range tf.Radius.Keys {
		radius = append(radius, kv{k, tf.Radius.Vals[k]})
	}
	mo := tf.Motion
	durations, eases, patterns := obj{}, obj{}, obj{}
	for _, k := range mo.Duration.Keys {
		durations = append(durations, kv{k, mo.Duration.Vals[k]})
	}
	for _, k := range mo.Ease.Keys {
		eases = append(eases, kv{k, mo.Ease.Vals[k]})
	}
	for _, k := range mo.Pattern.Keys {
		p := mo.Pattern.Vals[k]
		po := obj{}
		if p.PeriodMs > 0 {
			po = append(po, kv{"periodMs", p.PeriodMs})
		}
		if p.StepMs > 0 {
			po = append(po, kv{"stepMs", p.StepMs})
		}
		if p.Hz > 0 {
			po = append(po, kv{"hz", p.Hz})
		}
		patterns = append(patterns, kv{k, po})
	}
	var on, ground []string
	for _, v := range tf.Handshake.On {
		on = append(on, tf.mustHex(v))
	}
	for _, v := range tf.Handshake.Ground {
		ground = append(ground, tf.mustHex(v))
	}
	tv := tf.TV
	tvType := obj{}
	for _, r := range tv.Type.roles() {
		v := r.v
		o := obj{{"font", v.Font}, {"face", v.Face}, {"px", v.Px}}
		if v.PortraitPx != 0 {
			o = append(o, kv{"portraitPx", v.PortraitPx})
		}
		if v.Tracking != 0 {
			o = append(o, kv{"tracking", v.Tracking})
		}
		if v.Upper {
			o = append(o, kv{"upper", true})
		}
		tvType = append(tvType, kv{r.key, o})
	}
	params := obj{}
	for _, k := range tv.Look.Params.Keys {
		params = append(params, kv{k, tv.Look.Params.Vals[k]})
	}
	lb := tf.Labels.TV
	root := obj{
		{"sha", sha},
		{"direction", tf.Direction},
		{"app", obj{{"name", tf.App.Name}, {"shortName", tf.App.ShortName}, {"description", tf.App.Description}}},
		{"palette", palette},
		{"ramp", ramps},
		{"color", colors},
		{"states", states},
		{"neutral", stateObj(tf, tf.Neutral, modes[0])},
		{"attention", attention},
		{"font", fonts},
		{"text", text},
		{"radius", radius},
		{"motion", obj{
			{"duration", durations}, {"ease", eases}, {"pattern", patterns},
			{"hold", obj{{"ms", mo.Hold.Ms}, {"releaseMs", mo.Hold.ReleaseMs}, {"fireMs", mo.Hold.FireMs}, {"nudgeMs", mo.Hold.NudgeMs}}},
			{"scene", obj{{"coolMs", mo.Scene.CoolMs}, {"heatMs", mo.Scene.HeatMs}}},
		}},
		{"handshake", obj{{"on", on}, {"ground", ground}}},
		{"tv", obj{
			{"reference", obj{{"landscape", tv.Reference.Landscape[:]}, {"portrait", tv.Reference.Portrait[:]}}},
			{"safeInset", tv.SafeInset},
			{"type", tvType},
			{"qr", obj{
				{"card", obj{{"landscape", tv.QR.Card.Landscape}, {"portrait", tv.QR.Card.Portrait}}},
				{"quietModules", tv.QR.QuietModules}, {"dark", tf.mustHex(tv.QR.Dark)}, {"light", tf.mustHex(tv.QR.Light)},
			}},
			{"look", obj{{"background", tv.Look.Background}, {"signature", tv.Look.Signature}, {"qrFrame", tv.Look.QRFrame}, {"params", params}}},
			{"labels", obj{
				{"codeLabel", lb.CodeLabel}, {"qrCaption", lb.QRCaption}, {"versionPrefix", lb.VersionPrefix},
				{"installerPrefix", lb.InstallerPrefix}, {"starting", lb.Starting}, {"scaleCold", lb.ScaleCold}, {"scaleHot", lb.ScaleHot},
			}},
		}},
	}
	var b strings.Builder
	b.WriteString("// Code generated from design/tokens.json by go test ./internal/brand (VOS_GEN_DESIGN=1). DO NOT EDIT.\n")
	b.WriteString("// The design tokens as data, for canvas and WebGL painters, GSAP and the TV mock.\n")
	b.WriteString("// Style with the utilities from src/styles/tokens.gen.css; read values here.\n\n")
	b.WriteString("export const tokens = ")
	if err := writeTS(&b, root, ""); err != nil {
		return "", err
	}
	b.WriteString(" as const;\n\nexport type Tokens = typeof tokens;\nexport type StateName = keyof typeof tokens.states;\n")
	return b.String(), nil
}

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func tsKey(k string) string {
	if identRe.MatchString(k) {
		return k
	}
	return tsString(k)
}

func tsString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// tsScalar renders a leaf value, or false when v is not one.
func tsScalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return tsString(x), true
	case int:
		return strconv.Itoa(x), true
	case float64:
		return fmtNum(x), true
	case bool:
		return strconv.FormatBool(x), true
	case []string:
		q := make([]string, len(x))
		for i, s := range x {
			q[i] = tsString(s)
		}
		return "[" + strings.Join(q, ", ") + "]", true
	case []int:
		q := make([]string, len(x))
		for i, n := range x {
			q[i] = strconv.Itoa(n)
		}
		return "[" + strings.Join(q, ", ") + "]", true
	}
	return "", false
}

// writeTS prints an object: one line when every member is a scalar and it
// fits in 110 columns, otherwise one member per line.
func writeTS(b *strings.Builder, o obj, indent string) error {
	if len(o) == 0 {
		b.WriteString("{}")
		return nil
	}
	flat, ok := make([]string, 0, len(o)), true
	for _, p := range o {
		s, isScalar := tsScalar(p.v)
		if !isScalar {
			ok = false
			break
		}
		flat = append(flat, tsKey(p.k)+": "+s)
	}
	if line := "{ " + strings.Join(flat, ", ") + " }"; ok && len(indent)+len(line) <= 110 {
		b.WriteString(line)
		return nil
	}
	b.WriteString("{\n")
	for _, p := range o {
		b.WriteString(indent + "  " + tsKey(p.k) + ": ")
		if s, isScalar := tsScalar(p.v); isScalar {
			b.WriteString(s)
		} else if child, isObj := p.v.(obj); isObj {
			if err := writeTS(b, child, indent+"  "); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("tokens.gen.ts: %s has an unsupported value %T", p.k, p.v)
		}
		b.WriteString(",\n")
	}
	b.WriteString(indent + "}")
	return nil
}
