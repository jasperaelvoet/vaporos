package brand

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// cssOpts says what one CSS target gets.
type cssOpts struct {
	header    string
	modes     []string // the first is the default
	fontFaces bool     // @font-face rules (the site loads fonts with next/font instead)
}

// genericFamilies are CSS keywords, written without quotes.
var genericFamilies = []string{"serif", "sans-serif", "monospace", "cursive", "fantasy", "system-ui",
	"ui-serif", "ui-sans-serif", "ui-monospace", "ui-rounded", "math", "emoji", "fangsong", "-apple-system", "BlinkMacSystemFont"}

func fontStack(f fontTok) string {
	return stackOf(f, false)
}

// stackOf is f's font stack. With local, a family whose faces ship as web
// fonts gets its metric-matched "<family> fallback" (styles/fonts-fallback.css,
// from tools/fonts) right after it, so the swap to the web font moves nothing.
func stackOf(f fontTok, local bool) string {
	parts := []string{strconv.Quote(f.Family)}
	if local && f.hasWeb() {
		parts = append(parts, strconv.Quote(f.Family+" fallback"))
	}
	for _, fb := range f.Fallback {
		if slices.Contains(genericFamilies, fb) {
			parts = append(parts, fb)
		} else {
			parts = append(parts, strconv.Quote(fb))
		}
	}
	return strings.Join(parts, ", ")
}

// hasWeb reports whether any of f's faces ships as a web font.
func (f fontTok) hasWeb() bool {
	for _, k := range f.Faces.Keys {
		if f.Faces.Vals[k].Web != "" {
			return true
		}
	}
	return false
}

// modeDefaults orders a surface's modes so its default comes first.
func modeDefaults(modes []string, def string) []string {
	if !slices.Contains(modes, def) {
		return modes
	}
	out := []string{def}
	for _, m := range modes {
		if m != def {
			out = append(out, m)
		}
	}
	return out
}

// modeVars are the colour variables that change with the mode: the roles
// and every state's three colours.
func modeVars(tf *tokensFile, mode string) ([][2]string, error) {
	var out [][2]string
	for _, role := range tf.Color.Keys {
		h, err := tf.roleHex(role, mode)
		if err != nil {
			return nil, err
		}
		out = append(out, [2]string{"--color-" + role, h})
	}
	for _, e := range tf.stateEntries() {
		name := "state-" + e.name
		if strings.HasPrefix(e.name, "attention.") {
			name = "attention-" + strings.TrimPrefix(e.name, "attention.")
		}
		for _, p := range []struct {
			suffix string
			v      modeVal
		}{{"", e.st.Color}, {"-ink", e.st.Ink}, {"-soft", e.st.Soft}} {
			h, err := tf.modeHex(p.v, mode)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", e.name, err)
			}
			out = append(out, [2]string{"--color-" + name + p.suffix, h})
		}
	}
	return out, nil
}

// cssTokens is the Tailwind v4 input shared by the control center and the
// site: a plain @theme (so utilities read variables and modes can override
// them), the plain :root values scripts read, and the [data-state] helpers.
func cssTokens(tf *tokensFile, sha string, o cssOpts) (string, error) {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	base := o.modes[0]
	w("%s\n\n@theme {\n  --color-*: initial;\n\n  /* Palette */\n", o.header)
	for _, k := range tf.Palette.Keys {
		if _, isRole := tf.Color.get(k); isRole {
			continue // the role of the same name carries it (validated equal)
		}
		p := tf.Palette.Vals[k]
		w("  --color-%s: %s; /* %s */\n", k, p.Hex, p.Use)
	}
	for _, k := range tf.Ramp.Keys {
		r := tf.Ramp.Vals[k]
		w("\n  /* %s0 to %s%d: %s */\n", r.Prefix, r.Prefix, len(r.Stops)-1, r.Use)
		for i, h := range r.Stops {
			w("  --color-%s%d: %s;\n", r.Prefix, i, h)
		}
	}
	vars, err := modeVars(tf, base)
	if err != nil {
		return "", err
	}
	w("\n  /* Roles, then each state's signal, the text on it and its tint (%s) */\n", base)
	for _, v := range vars {
		w("  %s: %s;\n", v[0], v[1])
	}

	w("\n  --font-*: initial;\n")
	for _, r := range tf.Font.Keys {
		w("  --font-%s: %s;\n", r, stackOf(tf.Font.Vals[r], o.fontFaces))
		if f := tf.Font.Vals[r].Features; f != "" {
			w("  --font-%s--font-feature-settings: %s;\n", r, f)
		}
	}
	if ui, ok := tf.Font.get("ui"); ok {
		w("  --default-font-family: %s;\n", stackOf(ui, o.fontFaces))
	}
	if mono, ok := tf.Font.get("mono"); ok {
		w("  --default-mono-font-family: %s;\n", stackOf(mono, o.fontFaces))
		if mono.Features != "" {
			w("  --default-mono-font-feature-settings: %s;\n", mono.Features)
		}
	}

	w("\n")
	for _, k := range tf.Text.Keys {
		tx := tf.Text.Vals[k]
		w("  --text-%s: %s;\n  --text-%s--line-height: %s;\n", k, tx.Size, k, tx.Leading)
		if tx.Tracking != "" {
			w("  --text-%s--letter-spacing: %s;\n", k, tx.Tracking)
		}
	}
	w("\n")
	for _, k := range tf.Radius.Keys {
		w("  --radius-%s: %s;\n", k, tf.Radius.Vals[k])
	}
	w("\n  --spacing: %s;\n", tf.Spacing.Base)
	for _, k := range tf.Spacing.Named.Keys {
		w("  --spacing-%s: %s;\n", k, tf.Spacing.Named.Vals[k])
	}
	for _, k := range tf.Breakpoint.Keys {
		w("  --breakpoint-%s: %s;\n", k, tf.Breakpoint.Vals[k])
	}
	w("\n")
	for _, k := range tf.Motion.Ease.Keys {
		w("  --ease-%s: %s;\n", k, tf.Motion.Ease.Vals[k])
	}
	w("}\n\n")

	// Values scripts read with getComputedStyle live outside @theme, which
	// Tailwind would prune when no utility uses them.
	w(":root {\n  color-scheme: %s;\n  --vos-tokens: %q;\n", schemeOf(base), sha)
	for _, k := range tf.Motion.Duration.Keys {
		w("  --vos-dur-%s: %dms;\n", k, tf.Motion.Duration.Vals[k])
	}
	h := tf.Motion.Hold
	w("  --hold-ms: %dms;\n  --hold-release-ms: %dms;\n  --hold-fire-ms: %dms;\n  --hold-nudge-ms: %dms;\n", h.Ms, h.ReleaseMs, h.FireMs, h.NudgeMs)
	for _, k := range tf.Motion.Pattern.Keys {
		p := tf.Motion.Pattern.Vals[k]
		if p.PeriodMs > 0 {
			w("  --vos-pattern-%s-ms: %dms;\n", k, p.PeriodMs)
		}
		if p.StepMs > 0 {
			w("  --vos-pattern-%s-ms: %dms;\n", k, p.StepMs)
		}
	}
	w("  --vos-scene-cool-ms: %dms;\n  --vos-scene-heat-ms: %dms;\n", tf.Motion.Scene.CoolMs, tf.Motion.Scene.HeatMs)
	display, _ := tf.Font.get("display")
	for _, k := range display.Faces.Keys {
		fc := display.Faces.Vals[k]
		w("  --cut-%s-w: %d%%;\n  --cut-%s-g: %d;\n", k, fc.Wdth, k, fc.Wght)
	}
	w("}\n")

	for _, mode := range o.modes[1:] {
		vars, err := modeVars(tf, mode)
		if err != nil {
			return "", err
		}
		var body strings.Builder
		for _, v := range vars {
			fmt.Fprintf(&body, "    %s: %s;\n", v[0], v[1])
		}
		w("\n@media (prefers-color-scheme: %s) {\n  :root:not([data-theme=%q]) {\n    color-scheme: %s;\n%s  }\n}\n",
			mode, base, schemeOf(mode), body.String())
		w(":root[data-theme=%q] {\n  color-scheme: %s;\n%s}\n", mode, schemeOf(mode), strings.ReplaceAll(body.String(), "    ", "  "))
	}

	// The state helpers: components use bg-(--state), text-(--state-ink), and
	// the direction's CSS reads the heat position and the cut.
	// Neutral first (it is also the :root default), then the seven, then
	// the attention modifiers, which win when both attributes are set.
	entries := tf.stateEntries()
	neutralAt := len(tf.State.Keys)
	order := append([]int{neutralAt}, seq(0, neutralAt)...)
	order = append(order, seq(neutralAt+1, len(entries))...)
	for _, i := range order {
		e := entries[i]
		var sel, name string
		switch {
		case e.name == "neutral":
			sel, name = ":root,\n[data-state=\"\"]", "state-neutral"
		case strings.HasPrefix(e.name, "attention."):
			k := strings.TrimPrefix(e.name, "attention.")
			sel, name = fmt.Sprintf("[data-attention=%q]", k), "attention-"+k
		default:
			sel, name = fmt.Sprintf("[data-state=%q]", e.name), "state-"+e.name
		}
		fc := display.Faces.Vals[e.st.Look.Cut]
		w("\n%s {\n  --state: var(--color-%s);\n  --state-ink: var(--color-%s-ink);\n  --state-soft: var(--color-%s-soft);\n", sel, name, name, name)
		w("  --state-heat: %s;\n  --state-cut-w: %d%%;\n  --state-cut-g: %d;\n  --state-from: %s;\n}\n",
			fmtNum(e.st.Look.Heat), fc.Wdth, fc.Wght, fmtNum(e.st.Look.From))
	}

	if o.fontFaces {
		for _, r := range tf.Font.Keys {
			f := tf.Font.Vals[r]
			for _, k := range f.Faces.Keys {
				fc := f.Faces.Vals[k]
				if fc.Web == "" {
					continue
				}
				rel := strings.TrimPrefix(fc.Web, "internal/web/static/") // relative to the compiled static/app.css
				w("\n@font-face {\n  font-family: %q;\n  src: url(%q) format(\"woff2\");\n  font-weight: %d;\n  font-stretch: %d%%;\n  font-style: normal;\n  font-display: swap;\n}\n",
					f.Family, rel, fc.Wght, fc.Wdth)
			}
		}
	}
	return b.String(), nil
}

func schemeOf(mode string) string {
	if mode == "light" {
		return "light"
	}
	return "dark"
}

func seq(from, to int) []int {
	var out []int
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}
