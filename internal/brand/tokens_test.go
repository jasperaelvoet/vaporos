package brand

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// checkSchema covers everything but contrast and fonts: decoding already
// refused unknown keys, this checks values and cross references.
func checkSchema(tf *tokensFile, raw []byte) []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if tf.Schema != 1 {
		bad("schema is %d, want 1", tf.Schema)
	}
	if !slices.Contains(directions, tf.Direction) {
		bad("direction %q is not one of %v", tf.Direction, directions)
	}
	if tf.Direction != "draft" && bytes.Contains(raw, []byte(`"SLOT"`)) {
		bad(`direction %q still has "SLOT" values`, tf.Direction)
	}
	for i, t := range tf.Targets {
		if !slices.Contains(knownTarget, t) || slices.Contains(tf.Targets[:i], t) {
			bad("targets: %q is unknown or repeated", t)
		}
	}

	// Modes and theme.
	for _, m := range [][]string{tf.Modes.Web, tf.Modes.Site} {
		if len(m) == 0 {
			bad("modes: web and site each need at least one mode")
		}
		for _, v := range m {
			if v != "dark" && v != "light" {
				bad("modes: %q is not dark or light", v)
			}
		}
	}
	if !slices.Equal(tf.Modes.TV, []string{"tv"}) {
		bad(`modes.tv must be ["tv"]`)
	}
	if !slices.Contains(tf.Modes.Web, tf.Theme.Default) {
		bad("theme.default %q is not a web mode", tf.Theme.Default)
	}
	if tf.Theme.Switch && len(tf.Modes.Web) < 2 {
		bad("theme.switch needs both a dark and a light web mode")
	}
	needLight := slices.Contains(tf.Modes.Web, "light") || slices.Contains(tf.Modes.Site, "light")
	if tf.App.Name == "" || tf.App.ShortName == "" || tf.App.Description == "" {
		bad("app: name, shortName and description are required")
	}

	// Palette, ramps and roles: names must not collide unless they agree.
	names := map[string]string{}
	for _, k := range tf.Palette.Keys {
		p := tf.Palette.Vals[k]
		if !nameRe.MatchString(k) {
			bad("palette: %q is not a kebab-case name", k)
		}
		if !hexRe.MatchString(p.Hex) || len(p.Hex) != 7 {
			bad("palette.%s: %q is not lowercase #rrggbb", k, p.Hex)
		}
		names[k] = "palette." + k
	}
	for _, k := range tf.Ramp.Keys {
		r := tf.Ramp.Vals[k]
		if !nameRe.MatchString(r.Prefix) || len(r.Stops) < 2 || len(r.Stops) > 16 {
			bad("ramp.%s: needs a lowercase prefix and 2 to 16 stops", k)
		}
		for i, h := range r.Stops {
			if !hexRe.MatchString(h) || len(h) != 7 {
				bad("ramp.%s[%d]: %q is not lowercase #rrggbb", k, i, h)
			}
			n := r.Prefix + strconv.Itoa(i)
			if prev, dup := names[n]; dup {
				bad("ramp.%s: stop name %q collides with %s", k, n, prev)
			}
			names[n] = "ramp." + k
		}
	}
	for _, role := range colorRoles {
		if _, ok := tf.Color.get(role); !ok {
			bad("color: role %q is missing", role)
		}
	}
	for _, role := range tf.Color.Keys {
		m := tf.Color.Vals[role]
		if !nameRe.MatchString(role) {
			bad("color: %q is not a kebab-case name", role)
		}
		modes := []string{"dark", "tv"}
		if needLight {
			modes = append(modes, "light")
		} else if m.Light != "" {
			bad("color.%s: has a light value but no surface lists the light mode", role)
		}
		for _, mode := range modes {
			if _, err := tf.roleHex(role, mode); err != nil {
				bad("%v", err)
			}
		}
		if _, clash := names[role]; clash {
			// A role may share a palette name only when it is that colour.
			for _, mode := range modes {
				if m.pick(mode) != role {
					bad("color.%s: shares its name with %s but is a different colour in %s", role, names[role], mode)
				}
			}
		}
	}

	// States.
	if !slices.Equal(tf.State.Keys, stateOrder) {
		bad("state: want exactly %v in that order, got %v", stateOrder, tf.State.Keys)
	}
	if !slices.Equal(tf.Attention.Keys, []string{"pair"}) {
		bad(`attention: want exactly ["pair"], got %v`, tf.Attention.Keys)
	}
	for _, e := range tf.stateEntries() {
		st, n := e.st, e.name
		switch {
		case n == "neutral" && st.Label != "":
			bad("neutral: label must be empty")
		case n != "neutral" && !sentenceCase(st.Label):
			bad("%s: label %q must be sentence case and at most 16 characters", n, st.Label)
		}
		for part, m := range map[string]modeVal{"color": st.Color, "ink": st.Ink, "soft": st.Soft} {
			modes := []string{"dark", "tv"}
			if needLight {
				modes = append(modes, "light")
			}
			for _, mode := range modes {
				if _, err := tf.modeHex(m, mode); err != nil {
					bad("%s.%s: %v", n, part, err)
				}
			}
		}
		if _, ok := tf.Motion.Pattern.get(st.Motion); !ok {
			bad("%s: motion %q is not a pattern", n, st.Motion)
		}
		if st.Reduced != "steady" && st.Reduced != "none" {
			bad(`%s: reduced must be "steady" or "none", got %q`, n, st.Reduced)
		} else if _, ok := tf.Motion.Pattern.get(st.Reduced); !ok && st.Reduced != "none" {
			bad("%s: reduced %q is not a pattern", n, st.Reduced)
		}
		if utf8.RuneCountInString(st.Signage) > 20 {
			bad("%s: signage %q is longer than 20 characters", n, st.Signage)
		}
		l := st.Look
		display, _ := tf.Font.get("display")
		if _, ok := display.Faces.get(l.Cut); !ok {
			bad("%s: look.cut %q is not a display face", n, l.Cut)
		}
		if _, ok := tf.Ramp.get(l.Map); !ok {
			bad("%s: look.map %q is not a ramp", n, l.Map)
		}
		if l.Heat < 0 || l.Heat > 1 || l.Peak < 0 || l.Peak > 1 {
			bad("%s: look.heat and look.peak must be within 0..1", n)
		}
		if l.From < 0.3 || l.From > 2 {
			bad("%s: look.from %v is outside 0.3..2", n, l.From)
		}
		if l.Reach < 20 || l.Reach > 800 {
			bad("%s: look.reach %v is outside 20..800", n, l.Reach)
		}
	}

	// Type, radius, spacing, breakpoints.
	for _, k := range tf.Text.Keys {
		tx := tf.Text.Vals[k]
		if !nameRe.MatchString(k) && !scaleRe.MatchString(k) {
			bad("text: %q is not a valid name", k)
		}
		if !sizeRe.MatchString(tx.Size) || !leadingRe.MatchString(tx.Leading) ||
			(tx.Tracking != "" && !trackingRe.MatchString(tx.Tracking)) {
			bad("text.%s: size must be rem or clamp(), leading rem or unitless, tracking em", k)
		}
	}
	for _, k := range tf.Radius.Keys {
		if !lengthRe.MatchString(tf.Radius.Vals[k]) {
			bad("radius.%s: %q is not a px or rem length", k, tf.Radius.Vals[k])
		}
	}
	if !lengthRe.MatchString(tf.Spacing.Base) {
		bad("spacing.base: %q is not a length", tf.Spacing.Base)
	}
	for _, k := range tf.Spacing.Named.Keys {
		if !nameRe.MatchString(k) || !lengthRe.MatchString(tf.Spacing.Named.Vals[k]) {
			bad("spacing.named.%s: needs a kebab-case name and a length", k)
		}
	}
	for _, k := range tf.Breakpoint.Keys {
		if !nameRe.MatchString(k) || !strings.HasSuffix(tf.Breakpoint.Vals[k], "rem") {
			bad("breakpoint.%s: needs a kebab-case name and a rem length", k)
		}
	}

	errs = append(errs, checkMotion(tf)...)

	// Filter tables.
	for _, id := range tf.Filter.Maps.Keys {
		fm := tf.Filter.Maps.Vals[id]
		if !nameRe.MatchString(id) {
			bad("filter.maps: %q is not a valid element id", id)
		}
		if _, ok := tf.Ramp.get(fm.Ramp); !ok {
			bad("filter.maps.%s: ramp %q does not exist", id, fm.Ramp)
		}
		if fm.Bands < 2 || fm.Bands > 64 || fm.Sub < 1 || fm.Sub > 32 || fm.Line <= 0 || fm.Line > 1 {
			bad("filter.maps.%s: bands 2..64, sub 1..32, line within (0, 1]", id)
		}
	}
	tu := tf.Filter.Turbulence
	if tu.BaseFrequency == "" || tu.Octaves < 1 || tu.Octaves > 4 || tu.Scale <= 0 || tu.Blur < 0 {
		bad("filter.turbulence: baseFrequency, 1..4 octaves, a positive scale and blur ≥ 0")
	}

	// Handshake.
	if len(tf.Handshake.On) == 0 || len(tf.Handshake.Ground) == 0 || len(tf.Handshake.On) > 16 || len(tf.Handshake.Ground) > 16 {
		bad("handshake: on and ground each need 1 to 16 colours")
	}

	errs = append(errs, checkTV(tf)...)
	return errs
}

func sentenceCase(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 16 {
		return false
	}
	for i, r := range s {
		if i == 0 && !unicode.IsUpper(r) || i > 0 && unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

// checkAll is every validation, in the order a designer would fix them.
func checkAll(tf *tokensFile, raw []byte) []error {
	errs := checkSchema(tf, raw)
	errs = append(errs, checkFonts(tf)...)
	return append(errs, checkContrast(tf)...)
}

func TestTokensSchema(t *testing.T) {
	tf, raw := loadTokens(t)
	report(t, checkSchema(tf, raw))
}

func TestTokensFonts(t *testing.T) {
	tf, _ := loadTokens(t)
	report(t, checkFonts(tf))
}

func TestStateVocabulary(t *testing.T) {
	tf, _ := loadTokens(t)
	var got []string
	for _, s := range States {
		got = append(got, string(s))
	}
	if !slices.Equal(got, stateOrder) || !slices.Equal(tf.State.Keys, stateOrder) {
		t.Fatalf("vocabulary drifted: brand.States %v, tokens.json %v, canonical %v", got, tf.State.Keys, stateOrder)
	}
	for _, s := range append(States, Neutral) {
		if p, ok := ParseState(string(s)); !ok || p != s {
			t.Errorf("ParseState(%q) = %q, %v", s, p, ok)
		}
		if StyleOf(s).Motion == "" || StyleOf(s).Reduced == "" {
			t.Errorf("StyleOf(%q) has no motion", s)
		}
	}
	if p, ok := ParseState("bogus"); ok || p != Neutral || StyleOf("bogus") != StyleOf(Neutral) {
		t.Error("an unknown state must parse to Neutral, false and style as neutral")
	}
}

// TestSchemaRefusesMistakes shows the checks bite: each edit of the real
// file must produce an error that names it.
func TestSchemaRefusesMistakes(t *testing.T) {
	_, raw := loadTokens(t)
	for _, c := range []struct{ old, new, want string }{
		{`"direction": "redline"`, `"direction": "neon"`, "direction"},
		{`"schema": 1,`, `"schema": 1, "colour": {},`, "unknown field"},
		{`"label": "Ready",`, `"label": "READY",`, "sentence case"},
		{`"ash":       { "hex": "#0b0a0c"`, `"ash":       { "hex": "#0B0A0C"`, "lowercase"},
		{`"ink-3":       { "dark": "dim" }`, `"ink-3":       { "dark": "line" }`, "contrast"},
		{`"pip": { "periodMs": 2400 }`, `"pip": { "periodMs": 200 }`, "3 Hz"},
		{`"restart-needed": {`, `"restart_needed": {`, "state"},
		{`"motion": "steps"`, `"motion": "wobble"`, "not a pattern"},
		{`"dark": "ash", "light": "h9"`, `"dark": "smoke", "light": "h9"`, "tv.qr"},
		{`"status":         { "font": "display", "face": "state", "px": 128`, `"status":         { "font": "display", "face": "state", "px": 240`, "16..200"},
		{`"stretch": 900`, `"stretch": 2900`, "0..2000"},
	} {
		edited := bytes.Replace(raw, []byte(c.old), []byte(c.new), 1)
		if bytes.Equal(edited, raw) {
			t.Errorf("case %q: the edit did not apply; update the test", c.want)
			continue
		}
		tf, err := decodeTokens(edited)
		var errs []error
		if err != nil {
			errs = []error{err}
		} else {
			errs = checkAll(tf, edited)
		}
		if !strings.Contains(fmt.Sprint(errs), c.want) {
			t.Errorf("editing %q → %q: want an error mentioning %q, got %v", c.old, c.new, c.want, errs)
		}
	}
}

// TestBrandImportsNothingFromRepo keeps brand a leaf: any package, the TV
// renderer and the web server included, may import it without a cycle.
func TestBrandImportsNothingFromRepo(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, f := range files {
		ast, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range ast.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); strings.HasPrefix(p, "github.com/jasperaelvoet/vaporos") {
				t.Errorf("%s imports %s; internal/brand must import nothing from the repo", f, p)
			}
		}
	}
}
