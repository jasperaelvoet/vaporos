package welcome

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden layout pins the geometry of six fixtures on four screens, so a
// layout change shows up as a reviewable JSON diff. It holds no pixels: PNG
// goldens of procedural art would be large, and float rounding differs
// between arm64 and amd64.
var (
	goldenFixtures = []string{"installer-ready", "ready", "updating", "pairing", "no-gpu", "long-unicode"}
	goldenSizes    = []image.Point{{1920, 1080}, {1280, 720}, {1080, 1920}, {3440, 1440}}
	goldenPath     = filepath.Join("testdata", "layout.golden.json")
)

type goldenText struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Px   int    `json:"px"`
	Rect [4]int `json:"rect"`
}

type goldenCase struct {
	Fixture string            `json:"fixture"`
	Size    string            `json:"size"`
	Module  int               `json:"module"`
	Rects   map[string][4]int `json:"rects"`
	Texts   []goldenText      `json:"texts"`
}

func rect4(r image.Rectangle) [4]int { return [4]int{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y} }

func goldenOf(name string, sz image.Point, l *layout) goldenCase {
	c := goldenCase{Fixture: name, Size: sizeName(sz), Module: l.Module, Rects: map[string][4]int{}}
	for k, r := range map[string]image.Rectangle{
		"mark": l.Mark, "signature": l.Signature, "progress": l.Progress, "callout": l.Callout,
		"panel": l.Panel, "qrCard": l.QRCard, "qrFrame": l.QRFrame,
	} {
		if !r.Empty() {
			c.Rects[k] = rect4(r)
		}
	}
	for _, it := range l.Texts {
		c.Texts = append(c.Texts, goldenText{Role: it.Role.String(), Text: it.Text, Px: it.Px, Rect: rect4(it.Rect)})
	}
	return c
}

// marshalGolden writes one case per block and one text per line.
func marshalGolden(cases []goldenCase) []byte {
	var b bytes.Buffer
	b.WriteString("[\n")
	for i, c := range cases {
		rects, _ := json.Marshal(c.Rects)
		fmt.Fprintf(&b, " {\"fixture\": %q, \"size\": %q, \"module\": %d,\n  \"rects\": %s,\n  \"texts\": [\n", c.Fixture, c.Size, c.Module, rects)
		for j, t := range c.Texts {
			line, _ := json.Marshal(t)
			b.WriteString("   ")
			b.Write(line)
			if j < len(c.Texts)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString("  ]}")
		if i < len(cases)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]\n")
	return b.Bytes()
}

// TestLayoutGolden compares the layout with testdata/layout.golden.json:
// roles, strings and pixel sizes exactly, rectangles within 1 px.
// VOS_GEN_WELCOME=1 rewrites the file.
func TestLayoutGolden(t *testing.T) {
	fx := loadFixtures(t)
	var got []goldenCase
	for _, name := range goldenFixtures {
		for _, sz := range goldenSizes {
			got = append(got, goldenOf(name, sz, computeLayout(fx[name], sz.X, sz.Y)))
		}
	}
	if os.Getenv("VOS_GEN_WELCOME") != "" {
		if err := os.WriteFile(goldenPath, marshalGolden(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}
	const regen = "run VOS_GEN_WELCOME=1 go test ./internal/display/welcome -run TestLayoutGolden and review the diff"
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%v; %s", err, regen)
	}
	var want []goldenCase
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatalf("%s: %v; %s", goldenPath, err, regen)
	}
	if len(want) != len(got) {
		t.Fatalf("%s has %d cases, want %d; %s", goldenPath, len(want), len(got), regen)
	}
	for i, g := range got {
		if msg := goldenDiff(want[i], g); msg != "" {
			t.Errorf("stale: %s: %s@%s: %s; %s", goldenPath, g.Fixture, g.Size, msg, regen)
		}
	}
}

// goldenDiff describes the first difference between want and got.
func goldenDiff(want, got goldenCase) string {
	near := func(a, b [4]int) bool {
		for i := range a {
			if abs(a[i]-b[i]) > 1 {
				return false
			}
		}
		return true
	}
	switch {
	case want.Fixture != got.Fixture || want.Size != got.Size:
		return fmt.Sprintf("case is %s@%s in the file", want.Fixture, want.Size)
	case want.Module != got.Module:
		return fmt.Sprintf("module %d, golden %d", got.Module, want.Module)
	case len(want.Texts) != len(got.Texts):
		return fmt.Sprintf("%d texts, golden %d", len(got.Texts), len(want.Texts))
	}
	var keys []string
	for k := range want.Rects {
		keys = append(keys, k)
	}
	for k := range got.Rects {
		if _, ok := want.Rects[k]; !ok {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		w, okw := want.Rects[k]
		g, okg := got.Rects[k]
		if okw != okg || !near(w, g) {
			return fmt.Sprintf("%s %v, golden %v", k, g, w)
		}
	}
	for i, w := range want.Texts {
		g := got.Texts[i]
		if w.Role != g.Role || w.Text != g.Text || w.Px != g.Px || !near(w.Rect, g.Rect) {
			return fmt.Sprintf("text %d is %s %q %dpx %v, golden %s %q %dpx %v", i,
				g.Role, g.Text, g.Px, g.Rect, w.Role, w.Text, w.Px, w.Rect)
		}
	}
	return ""
}

// The golden check fails on a changed string and on a rectangle 2 px off,
// and forgives 1 px.
func TestLayoutGoldenDiff(t *testing.T) {
	base := goldenCase{Fixture: "ready", Size: "1920x1080", Module: 16,
		Rects: map[string][4]int{"qrCard": {1248, 276, 1776, 804}},
		Texts: []goldenText{{Role: "status", Text: "Ready to stream", Px: 60, Rect: [4]int{128, 325, 560, 384}}}}
	clone := func(edit func(*goldenCase)) goldenCase {
		b, _ := json.Marshal(base)
		var c goldenCase
		json.Unmarshal(b, &c)
		edit(&c)
		return c
	}
	if msg := goldenDiff(base, clone(func(c *goldenCase) { c.Rects["qrCard"] = [4]int{1249, 275, 1777, 805} })); msg != "" {
		t.Errorf("1 px off: %s", msg)
	}
	for what, c := range map[string]goldenCase{
		"string":   clone(func(c *goldenCase) { c.Texts[0].Text = "Ready to Stream" }),
		"px":       clone(func(c *goldenCase) { c.Texts[0].Px = 59 }),
		"rect":     clone(func(c *goldenCase) { c.Texts[0].Rect[2] += 2 }),
		"new rect": clone(func(c *goldenCase) { c.Rects["callout"] = [4]int{1, 2, 3, 4} }),
	} {
		if msg := goldenDiff(base, c); msg == "" || !strings.Contains(msg, "golden") {
			t.Errorf("%s change not caught: %q", what, msg)
		}
	}
}
