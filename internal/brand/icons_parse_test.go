package brand

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func TestLogoParse(t *testing.T) {
	syms := readLogo(t)
	var ids []string
	for _, s := range syms {
		ids = append(ids, s.ID)
	}
	for _, want := range logoDrawings {
		if !slices.Contains(ids, want.id) {
			t.Errorf("%s has no drawing %q", logoPath, want.id)
		}
	}

	ok := func(body string) string {
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><svg id="m" viewBox="0 0 64 64">` + body + `</svg></svg>`
	}
	good := `<path d="M0 0h10v10z" fill="#000004"/>`
	if _, err := parseLogo([]byte(ok(good))); err != nil {
		t.Fatalf("a minimal drawing was refused: %v", err)
	}
	for _, c := range []struct{ doc, want string }{
		{ok(`<path d="M0 0L10 10" fill="none" stroke="#ffffff" stroke-width="2" stroke-linecap="butt" stroke-linejoin="round"/>`), "round"},
		{ok(`<path d="M0 0L10 10" fill="none" stroke="#ffffff" stroke-linecap="round" stroke-linejoin="round"/>`), "stroke-width"},
		{ok(`<path d="M0 0A5 5 0 0 1 10 10" fill="#ffffff"/>`), "arcs"},
		{ok(`<path d="M0 0h10v10z" fill="#ffffff" transform="scale(2)"/>`), "transforms"},
		{ok(`<path d="M0 0h10v10z" fill="#ffffff" fill-rule="evenodd"/>`), "nonzero"},
		{ok(`<path d="M0 0h10v10z" fill="#ffffff" clip-path="url(#c)"/>`), "clips"},
		{ok(`<path d="M0 0h10v10z" fill="#ffffff" mask="url(#m)"/>`), "masks"},
		{ok(`<path d="M0 0h10v10z" fill="#ffffff" filter="url(#f)"/>`), "filters"},
		{ok(`<path d="M0 0h10v10z" fill="#ffffff" opacity=".5"/>`), "opacity"},
		{ok(`<path d="M0 0h10v10z" fill="url(#g)"/>`), "#rrggbb"},
		{ok(`<path d="M0 0h10v10z" fill="#FFFFFF"/>`), "lowercase"},
		{ok(`<path d="M0 0h10v10z"/>`), "needs fill"},
		{ok(`<path d="M0 0h10v10z" fill="none"/>`), "paints nothing"},
		{ok(`<path d="L0 0h10" fill="#ffffff"/>`), "start with M"},
		{ok(`<path d="M0 0 C1 2" fill="#ffffff"/>`), "missing numbers"},
		{ok(`<path d="M0 0h10v10z" fill="#ffffff" id="x"/>`), "ids belong"},
		{ok(`<use href="#m"/>`), "<use>"},
		{ok(`<text>VaporOS</text>`), "<text>"},
		{ok(`<rect width="10" height="10"/>`), "<rect>"},
		{ok(`<defs/>`), "<defs>"},
		{ok(`<clipPath/>`), "<clipPath>"},
		{ok(`<g>` + good + `</g>`), "class="},
		{ok(`<g class="glyph"><g class="glyph">` + good + `</g></g>`), "<g>"},
		{`<svg xmlns="http://www.w3.org/2000/svg"><svg viewBox="0 0 1 1">` + good + `</svg></svg>`, "kebab-case id"},
		{`<svg xmlns="http://www.w3.org/2000/svg"><svg id="m">` + good + `</svg></svg>`, "viewBox"},
		{`<svg xmlns="http://www.w3.org/2000/svg"><svg id="m" viewBox="0 0 1 1">` + good + `</svg><svg id="m" viewBox="0 0 1 1">` + good + `</svg></svg>`, "twice"},
		{`<svg xmlns="http://www.w3.org/2000/svg">` + good + `</svg>`, "<path> in the root <svg>"},
		{`<svg xmlns="http://www.w3.org/2000/svg" transform="scale(2)"><svg id="m" viewBox="0 0 1 1">` + good + `</svg></svg>`, "transforms"},
		{`<svg><svg id="m" viewBox="0 0 1 1">` + good + `</svg></svg>`, "SVG <svg>"},
	} {
		_, err := parseLogo([]byte(c.doc))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s\n  got error %v, want one mentioning %q", c.doc, err, c.want)
		}
	}
}

func TestFlattenPath(t *testing.T) {
	for _, c := range []struct {
		d, ops string
		pts    []float32
	}{
		{"M0 0l10 0c0 5 5 10 10 10s5 5 10 10", "MLCC", []float32{0, 0, 10, 0, 10, 5, 15, 10, 20, 10, 25, 10, 25, 15, 30, 20}},
		{"M0 0H10V10h-5v-5Z", "MLLLLZ", []float32{0, 0, 10, 0, 10, 10, 5, 10, 5, 5}},
		{"M0 0Q5 5 10 0T20 0", "MQQ", []float32{0, 0, 5, 5, 10, 0, 15, -5, 20, 0}},
		{"m1 1 2 0 0 2z m5 5h1", "MLLZML", []float32{1, 1, 3, 1, 3, 3, 6, 6, 7, 6}},
		{"M0 0S5 5 10 0", "MC", []float32{0, 0, 0, 0, 5, 5, 10, 0}},
		{"M.5-.5l1e1.5", "ML", []float32{0.5, -0.5, 10.5, 0}},
	} {
		ops, pts, err := parsePathData(c.d)
		if err != nil {
			t.Errorf("%q: %v", c.d, err)
			continue
		}
		if ops != c.ops || !slices.Equal(pts, c.pts) {
			t.Errorf("%q = %s %v, want %s %v", c.d, ops, pts, c.ops, c.pts)
		}
	}
	// A curve flattens within tolerance and ends where the data ends.
	p := Path{Ops: "MCQ", Pts: []float32{0, 0, 0, 20, 30, 20, 30, 0, 45, -20, 60, 0}}
	pl := p.polylines(xform{s: 1})
	if len(pl) != 1 {
		t.Fatalf("got %d polylines", len(pl))
	}
	end := pl[0][len(pl[0])-1]
	if math.Abs(end.x-60) > 1e-9 || math.Abs(end.y) > 1e-9 {
		t.Errorf("flattened path ends at %v, want (60, 0)", end)
	}
	if len(pl[0]) < 10 {
		t.Errorf("a 30-unit curve flattened into only %d points", len(pl[0]))
	}
	for _, bad := range []string{"M0 0 A1 1 0 0 1 2 2", "", "0 0", "M0 0 L", "M0 0 X1 1"} {
		if _, _, err := parsePathData(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
