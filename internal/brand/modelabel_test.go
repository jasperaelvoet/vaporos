package brand

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func readVectors(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "design", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("design/%s: %v", name, err)
	}
}

func TestScreenShapeVectors(t *testing.T) {
	var vec struct {
		Aspect struct{ Min, Max, Unknown, Precision float64 }
		Cases  []struct {
			Mode   string
			HDR    bool
			OK     bool
			W, H   int
			Hz     float64
			Label  string
			Aspect float64
		}
	}
	readVectors(t, "screen-shape-vectors.json", &vec)
	if vec.Aspect.Min != ShapeAspectMin || vec.Aspect.Max != ShapeAspectMax {
		t.Errorf("aspect limits: vectors %v..%v, brand %v..%v", vec.Aspect.Min, vec.Aspect.Max, ShapeAspectMin, ShapeAspectMax)
	}
	if got := ShapeAspect(0, 0); math.Abs(got-vec.Aspect.Unknown) > vec.Aspect.Precision {
		t.Errorf("ShapeAspect(0, 0) = %v, want %v", got, vec.Aspect.Unknown)
	}
	if len(vec.Cases) < 10 {
		t.Fatalf("only %d cases", len(vec.Cases))
	}
	for _, c := range vec.Cases {
		m, ok := ParseMode(c.Mode)
		if ok != c.OK {
			t.Errorf("ParseMode(%q) ok = %v, want %v", c.Mode, ok, c.OK)
			continue
		}
		if got := ModeLabel(c.Mode, c.HDR); got != c.Label {
			t.Errorf("ModeLabel(%q, %v) = %q, want %q", c.Mode, c.HDR, got, c.Label)
		}
		if !ok {
			continue
		}
		if m.W != c.W || m.H != c.H || m.Hz != c.Hz {
			t.Errorf("ParseMode(%q) = %+v, want %dx%d@%v", c.Mode, m, c.W, c.H, c.Hz)
		}
		if got := ShapeAspect(m.W, m.H); math.Abs(got-c.Aspect) > vec.Aspect.Precision {
			t.Errorf("ShapeAspect(%d, %d) = %.4f, want %v", m.W, m.H, got, c.Aspect)
		}
	}
}
