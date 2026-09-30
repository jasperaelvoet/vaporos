package brand

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// design/logo.svg is read with encoding/xml into this shape. The subset is
// described in the file's own header comment; everything outside it fails
// with the reason, so a design-tool export that needs flattening says what.

const logoPath = "design/logo.svg"

// logoDrawings are the drawings design/logo.svg must hold, in output order.
var logoDrawings = []struct{ id, goName, tsName, doc string }{
	{"mark", "Mark", "mark", "the compact mark, the white-hot core in one amber band: 64 px and below, and all chrome"},
	{"mark-light", "MarkLight", "markLight", "the compact mark on a light ground: an ash V in an orange band"},
	{"mono", "MarkMono", "mono", "the one-colour mark: the core V in currentColor"},
	{"wordmark", "Wordmark", "wordmark", "Vapor in the hot cut (currentColor), OS in the cold cut in smoke"},
	{"icon", "AppIcon", "icon", "the 512 app icon: the tile, the far-field isotherms and the full four-band mark"},
	{"icon-small", "AppIconSmall", "iconSmall", "the app icon for 64 px and below: the tile, one ring and the compact mark"},
}

// logoSym is one drawing of logo.svg: a nested <svg id viewBox>.
type logoSym struct {
	ID      string
	ViewBox [4]float32
	Paths   []logoPart
}

// logoPart keeps a path's source attributes (the SVG outputs reuse them
// verbatim) beside the normalized form the rasterizer draws.
type logoPart struct {
	Group, Class, D, Fill, Stroke, Width string
	Path                                 Path
}

func (s *logoSym) drawing() *Drawing {
	d := &Drawing{ViewBox: s.ViewBox}
	for _, p := range s.Paths {
		d.Paths = append(d.Paths, p.Path)
	}
	return d
}

var (
	kebabRe = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	colorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)
)

// logoAttrHint explains why an attribute is refused.
var logoAttrHint = map[string]string{
	"transform":        "transforms are not in the subset: bake them into the path data",
	"fill-rule":        "the subset always fills nonzero: make holes with opposite winding",
	"clip-rule":        "clips are not in the subset: clip the paths in the design tool",
	"clip-path":        "clips are not in the subset: clip the paths in the design tool",
	"mask":             "masks are not in the subset",
	"filter":           "filters are not in the subset",
	"opacity":          "opacity is not in the subset: use a solid colour",
	"fill-opacity":     "opacity is not in the subset: use a solid colour",
	"stroke-opacity":   "opacity is not in the subset: use a solid colour",
	"style":            "style attributes are not in the subset: use fill and stroke",
	"id":               "ids belong only on the drawings (the nested <svg>s)",
	"stroke-dasharray": "dashes are not in the subset",
}

func readLogo(t testing.TB) []logoSym {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, logoPath))
	if err != nil {
		t.Fatal(err)
	}
	syms, err := parseLogo(b)
	if err != nil {
		t.Fatalf("%s: %v", logoPath, err)
	}
	return syms
}

// parseLogo reads logo.svg's subset.
func parseLogo(b []byte) ([]logoSym, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var (
		syms  []logoSym
		stack []string
		group string
	)
	fail := func(format string, a ...any) error {
		line, _ := dec.InputPos()
		return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, a...))
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			name, parent := tok.Name.Local, strings.Join(stack, ">")
			attrs := map[string]string{}
			for _, a := range tok.Attr {
				k := a.Name.Local
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && k == "xmlns") {
					continue
				}
				if a.Name.Space != "" {
					return nil, fail("<%s %s:%s>: namespaced attributes are not in the subset", name, a.Name.Space, k)
				}
				attrs[k] = a.Value
			}
			allow := func(keys ...string) error {
				for k := range attrs {
					if !slices.Contains(keys, k) {
						if h, ok := logoAttrHint[k]; ok {
							return fail("<%s %s>: %s", name, k, h)
						}
						return fail("<%s %s>: the attribute is not in the logo subset", name, k)
					}
				}
				return nil
			}
			switch {
			case parent == "" && name == "svg":
				if tok.Name.Space != "http://www.w3.org/2000/svg" {
					return nil, fail("the root must be an SVG <svg> element")
				}
				if err := allow("viewBox", "width", "height", "style"); err != nil {
					return nil, err
				}
			case parent == "svg" && (name == "title" || name == "desc"):
			case parent == "svg" && name == "svg":
				if err := allow("id", "x", "y", "width", "height", "viewBox"); err != nil {
					return nil, err
				}
				id := attrs["id"]
				if !kebabRe.MatchString(id) {
					return nil, fail("a drawing needs a kebab-case id, got %q", id)
				}
				for _, s := range syms {
					if s.ID == id {
						return nil, fail("drawing %q is defined twice", id)
					}
				}
				vb, err := numbers(attrs["viewBox"])
				if err != nil || len(vb) != 4 || vb[2] <= 0 || vb[3] <= 0 {
					return nil, fail("drawing %q needs a viewBox of four numbers with a positive size", id)
				}
				for _, k := range []string{"x", "y", "width", "height"} {
					if v, ok := attrs[k]; ok {
						if _, err := strconv.ParseFloat(v, 64); err != nil {
							return nil, fail("drawing %q: %s=%q is not a number", id, k, v)
						}
					}
				}
				syms = append(syms, logoSym{ID: id, ViewBox: [4]float32{vb[0], vb[1], vb[2], vb[3]}})
			case parent == "svg>svg" && name == "g":
				if err := allow("class"); err != nil {
					return nil, err
				}
				group = attrs["class"]
				if !slices.Contains([]string{"tile", "field", "glyph"}, group) {
					return nil, fail(`<g> needs class="tile", "field" or "glyph", got %q`, group)
				}
			case (parent == "svg>svg" || parent == "svg>svg>g") && name == "path":
				if err := allow("d", "fill", "stroke", "stroke-width", "stroke-linecap", "stroke-linejoin", "class"); err != nil {
					return nil, err
				}
				p, err := logoPathOf(attrs, group)
				if err != nil {
					return nil, fail("%v", err)
				}
				s := &syms[len(syms)-1]
				s.Paths = append(s.Paths, p)
			default:
				where := "<" + parent + ">"
				switch parent {
				case "":
					where = "the document"
				case "svg":
					where = "the root <svg>"
				}
				return nil, fail("<%s> in %s is not in the logo subset (drawings of paths only; see the header of %s)", name, where, logoPath)
			}
			stack = append(stack, name)
		case xml.EndElement:
			if tok.Name.Local == "g" {
				group = ""
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 && (stack[len(stack)-1] == "title" || stack[len(stack)-1] == "desc") {
				continue
			}
			if len(bytes.TrimSpace(tok)) > 0 {
				return nil, fail("text outside <title> is not in the subset")
			}
		case xml.ProcInst, xml.Directive:
			return nil, fail("processing instructions and directives are not in the subset")
		}
	}
	if len(syms) == 0 {
		return nil, fmt.Errorf("no drawings: the root <svg> needs nested <svg id viewBox> drawings")
	}
	for _, s := range syms {
		if len(s.Paths) == 0 {
			return nil, fmt.Errorf("drawing %q has no paths", s.ID)
		}
	}
	return syms, nil
}

func logoPathOf(a map[string]string, group string) (logoPart, error) {
	lp := logoPart{Group: group, Class: a["class"], D: a["d"], Fill: a["fill"], Stroke: a["stroke"], Width: a["stroke-width"]}
	p := Path{Group: group, Class: lp.Class}
	if lp.Class != "" && !kebabRe.MatchString(lp.Class) {
		return lp, fmt.Errorf("class %q is not one kebab-case name", lp.Class)
	}
	paint := func(what, v string) (Paint, error) {
		switch {
		case v == "none":
			return Paint{}, nil
		case v == "currentColor":
			return Paint{Current: true}, nil
		case colorRe.MatchString(v):
			c, _ := parseHex(v)
			return Paint{Color: color.NRGBA{uint8(math.Round(c.r * 255)), uint8(math.Round(c.g * 255)), uint8(math.Round(c.b * 255)), 255}}, nil
		}
		return Paint{}, fmt.Errorf("%s %q: use lowercase #rrggbb, none or currentColor", what, v)
	}
	var err error
	if _, ok := a["fill"]; !ok {
		return lp, fmt.Errorf("a path needs fill: #rrggbb, none or currentColor (SVG's default black is never meant)")
	}
	if p.Fill, err = paint("fill", lp.Fill); err != nil {
		return lp, err
	}
	if s, ok := a["stroke"]; ok && s != "none" {
		if p.Stroke, err = paint("stroke", s); err != nil {
			return lp, err
		}
		w, err := strconv.ParseFloat(lp.Width, 32)
		if err != nil || w <= 0 {
			return lp, fmt.Errorf("a stroked path needs a positive stroke-width, got %q", lp.Width)
		}
		p.Width = float32(w)
		if a["stroke-linecap"] != "round" || a["stroke-linejoin"] != "round" {
			return lp, fmt.Errorf(`strokes need stroke-linecap="round" and stroke-linejoin="round" (the renderer draws only round strokes)`)
		}
	} else {
		for _, k := range []string{"stroke-width", "stroke-linecap", "stroke-linejoin"} {
			if _, ok := a[k]; ok {
				return lp, fmt.Errorf("%s without a stroke", k)
			}
		}
	}
	if !p.Fill.visible() && !p.Stroke.visible() {
		return lp, fmt.Errorf("the path paints nothing")
	}
	if p.Ops, p.Pts, err = parsePathData(lp.D); err != nil {
		return lp, err
	}
	lp.Path = p
	return lp, nil
}

func numbers(s string) ([]float32, error) {
	var out []float32
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' }) {
		v, err := strconv.ParseFloat(f, 32)
		if err != nil {
			return nil, err
		}
		out = append(out, float32(v))
	}
	return out, nil
}

var pathTok = regexp.MustCompile(`[A-Za-z]|[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?`)

// parsePathData normalizes SVG path data to absolute M, L, Q, C and Z:
// relative forms become absolute, H and V become L, S and T become C and Q
// with the reflected control point. Arcs are refused.
func parsePathData(d string) (string, []float32, error) {
	toks := pathTok.FindAllString(d, -1)
	if rest := strings.Trim(pathTok.ReplaceAllString(d, ""), " ,\t\r\n"); strings.Trim(rest, " ,\t\r\n") != "" {
		return "", nil, fmt.Errorf("path data %q has characters that are not commands or numbers", clip(d))
	}
	var (
		ops           []byte
		pts           []float64
		cx, cy        float64 // current point
		sx, sy        float64 // subpath start
		rx, ry        float64 // last control point, for S and T
		lastOp        byte
		cmd           byte
		started, have bool
	)
	num := func(i int) (float64, error) {
		if i >= len(toks) || !strings.ContainsAny(toks[i][:1], "+-.0123456789") {
			return 0, fmt.Errorf("path data %q: command %c is missing numbers", clip(d), cmd)
		}
		return strconv.ParseFloat(toks[i], 64)
	}
	for i := 0; i < len(toks); {
		if c := toks[i][0]; (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			cmd = c
			i++
			have = false
			if cmd == 'Z' || cmd == 'z' {
				if !started {
					return "", nil, fmt.Errorf("path data %q must start with M", clip(d))
				}
				ops = append(ops, 'Z')
				cx, cy, lastOp = sx, sy, 'Z'
				continue
			}
		} else if cmd == 0 || cmd == 'Z' || cmd == 'z' {
			return "", nil, fmt.Errorf("path data %q: a number without a command", clip(d))
		}
		upper := cmd &^ 0x20
		rel := cmd != upper
		if upper == 'A' {
			return "", nil, fmt.Errorf("path data %q: arcs (A) are not in the subset: convert them to curves", clip(d))
		}
		n := map[byte]int{'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'T': 2}[upper]
		if n == 0 {
			return "", nil, fmt.Errorf("path data %q: unknown command %c", clip(d), cmd)
		}
		if !started && upper != 'M' {
			return "", nil, fmt.Errorf("path data %q must start with M", clip(d))
		}
		v := make([]float64, n)
		for k := range v {
			f, err := num(i + k)
			if err != nil {
				return "", nil, err
			}
			v[k] = f
		}
		i += n
		abs := func(k int) (float64, float64) {
			if rel {
				return cx + v[k], cy + v[k+1]
			}
			return v[k], v[k+1]
		}
		switch upper {
		case 'M':
			if have { // extra pairs after M are lines
				x, y := abs(0)
				ops, pts = append(ops, 'L'), append(pts, x, y)
				cx, cy, lastOp = x, y, 'L'
				break
			}
			x, y := abs(0)
			ops, pts = append(ops, 'M'), append(pts, x, y)
			cx, cy, sx, sy, lastOp, started = x, y, x, y, 'M', true
		case 'L', 'H', 'V':
			x, y := cx, cy
			switch upper {
			case 'L':
				x, y = abs(0)
			case 'H':
				x = v[0]
				if rel {
					x += cx
				}
			case 'V':
				y = v[0]
				if rel {
					y += cy
				}
			}
			ops, pts = append(ops, 'L'), append(pts, x, y)
			cx, cy, lastOp = x, y, 'L'
		case 'C', 'S':
			var x1, y1 float64
			k := 0
			if upper == 'C' {
				x1, y1 = abs(0)
				k = 2
			} else if lastOp == 'C' {
				x1, y1 = 2*cx-rx, 2*cy-ry
			} else {
				x1, y1 = cx, cy
			}
			x2, y2 := abs(k)
			x, y := abs(k + 2)
			ops, pts = append(ops, 'C'), append(pts, x1, y1, x2, y2, x, y)
			rx, ry, cx, cy, lastOp = x2, y2, x, y, 'C'
		case 'Q', 'T':
			var x1, y1 float64
			k := 0
			if upper == 'Q' {
				x1, y1 = abs(0)
				k = 2
			} else if lastOp == 'Q' {
				x1, y1 = 2*cx-rx, 2*cy-ry
			} else {
				x1, y1 = cx, cy
			}
			x, y := abs(k)
			ops, pts = append(ops, 'Q'), append(pts, x1, y1, x, y)
			rx, ry, cx, cy, lastOp = x1, y1, x, y, 'Q'
		}
		have = true
	}
	if !started {
		return "", nil, fmt.Errorf("path data %q is empty", clip(d))
	}
	out := make([]float32, len(pts))
	for i, v := range pts {
		out[i] = float32(v)
	}
	return string(ops), out, nil
}
