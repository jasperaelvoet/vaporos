package welcome

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// layoutSizes are the screens the geometry tests run over. Laying out is
// cheap, so the list covers every shape we know of: small panels, 4:3, 5:4,
// 16:10, 16:9 up to 4K, DCI 4K, ultrawide 21:9 and 32:9, and portrait.
var layoutSizes = []image.Point{
	{640, 480}, {800, 480}, {480, 800}, {1024, 768}, {1280, 720}, {1280, 800},
	{1280, 1024}, {1366, 768}, {1920, 1080}, {1920, 1200}, {2560, 1080}, {2560, 1440},
	{3440, 1440}, {3840, 2160}, {4096, 2160}, {5120, 1440}, {1080, 1920},
}

// pixelSizes are the screens the pixel tests render. Rendering is costly
// under -race, so the list is short: bochs in the VM (1024×768 and
// 1280×800), 720p, 1080p, portrait and 4K.
var pixelSizes = []image.Point{{1024, 768}, {1280, 800}, {1280, 720}, {1920, 1080}, {1080, 1920}, {3840, 2160}}

// tones is every tone a state file can carry: the vocabulary, neutral, and
// a word outside it.
var tones = append(append([]brand.State{}, brand.States...), brand.Neutral, "bogus")

// fixtureNames are the hand-written states in testdata/, copied from what
// internal/display/status.go builds; the previews, the golden and the pixel
// tests all use them.
var fixtureNames = []string{
	"first-run", "going-to-sleep", "installer-done", "installer-failed", "installer-ready", "installer-running",
	"long-unicode", "no-gpu", "offline", "pairing", "ready", "ready-staged", "restart-needed",
	"starting", "streaming", "updating",
}

func sizeName(p image.Point) string { return fmt.Sprintf("%dx%d", p.X, p.Y) }

// loadFixtures reads every testdata/<name>.json, refusing unknown fields.
func loadFixtures(t testing.TB) map[string]State {
	t.Helper()
	out := map[string]State{}
	for _, name := range fixtureNames {
		b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var st State
		if err := dec.Decode(&st); err != nil {
			t.Fatalf("testdata/%s.json: %v", name, err)
		}
		out[name] = st
	}
	return out
}

// The fixtures are exactly the listed files, each a state vosd could write:
// a known tone (or none), attention only "pair", progress within 0..100.
func TestFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.json"))
	var names []string
	for _, f := range files {
		if n := strings.TrimSuffix(filepath.Base(f), ".json"); !strings.HasSuffix(n, ".golden") {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, fixtureNames) {
		t.Errorf("testdata has %v, want %v", names, fixtureNames)
	}
	for name, st := range loadFixtures(t) {
		if _, ok := brand.ParseState(string(st.Tone)); !ok {
			t.Errorf("%s: tone %q is not in the vocabulary", name, st.Tone)
		}
		if st.Attention != "" && st.Attention != AttentionPair {
			t.Errorf("%s: attention %q", name, st.Attention)
		}
		if st.Progress < 0 || st.Progress > 100 {
			t.Errorf("%s: progress %d", name, st.Progress)
		}
		if st.Status == "" || st.Title == "" {
			t.Errorf("%s: no status or title", name)
		}
	}
	if loadFixtures(t)["starting"] != Placeholder {
		t.Error("testdata/starting.json is not the placeholder")
	}
}
