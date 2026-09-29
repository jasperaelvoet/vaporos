package welcome

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// linear maps an sRGB channel to linear light.
var linear = func() (t [256]float64) {
	for v := range t {
		s := float64(v) / 255
		if s <= 0.04045 {
			t[v] = s / 12.92
		} else {
			t[v] = math.Pow((s+0.055)/1.055, 2.4)
		}
	}
	return t
}()

// luminance is WCAG 2's relative luminance of an sRGB colour.
func luminance(c color.RGBA) float64 {
	return 0.2126*linear[c.R] + 0.7152*linear[c.G] + 0.0722*linear[c.B]
}

func contrast(a, b color.RGBA) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// Every line reads against the worst pixel under it, with the text left
// out: 4.5:1, and 7:1 for the status, the address and the setup code. The
// fixtures run in their own tone on every pixel size (three of them at 4K),
// and the states that decorate the most run in every tone on 1280×720.
func TestTextContrast(t *testing.T) {
	fx := loadFixtures(t)
	check := func(at string, st State, sz image.Point) {
		l := computeLayout(st, sz.X, sz.Y)
		bg := paint(l, false)
		for _, it := range l.Texts {
			want := 4.5
			if it.Role == roleStatus || it.Role == roleURL || it.Role == roleCode {
				want = 7
			}
			worst := math.Inf(1)
			for y := it.Rect.Min.Y; y < it.Rect.Max.Y; y += 4 {
				for x := it.Rect.Min.X; x < it.Rect.Max.X; x += 4 {
					worst = min(worst, contrast(it.Color, bg.RGBAAt(x, y)))
				}
			}
			if worst < want {
				t.Errorf("%s: %s %q is %.2f:1 at worst, want %.1f:1", at, it.Role, it.Text, worst, want)
			}
		}
	}
	for _, name := range fixtureNames {
		for _, sz := range pixelSizes {
			// 4K costs seconds under -race; three fixtures cover its plates.
			if sz.X*sz.Y > 1920*1080 && name != "installer-ready" && name != "pairing" && name != "updating" {
				continue
			}
			check(name+"@"+sizeName(sz), fx[name], sz)
		}
	}
	for _, name := range []string{"installer-running", "pairing", "streaming", "no-gpu"} {
		for _, tone := range tones {
			check(name+"/"+string(tone), withTone(fx[name], tone), image.Pt(1280, 720))
		}
	}
}

// A status-only change (the next percent of an install) copies the cached
// background instead of painting it again, and the copy is what a fresh
// paint gives.
func TestBackgroundCached(t *testing.T) {
	st := loadFixtures(t)["installer-running"]
	Render(st, 1280, 720)
	n := bgPainted.Load()
	next := st
	next.Status, next.Detail, next.Progress = "Installing VaporOS… 38%", "Copying the boot loader", 38
	Render(next, 1280, 720)
	if got := bgPainted.Load(); got != n {
		t.Errorf("a status change painted the background %d more times", got-n)
	}
	Render(st, 1024, 768)
	if got := bgPainted.Load(); got != n+1 {
		t.Errorf("a new size painted the background %d times, want once", got-n)
	}

	l := computeLayout(st, 1280, 720)
	fresh := image.NewRGBA(image.Rect(0, 0, l.W, l.H))
	theLook.background(fresh, theLook.backdrop(l))
	if !bytes.Equal(fresh.Pix, background(theLook.backdrop(l)).Pix) {
		t.Error("the cached background differs from a fresh one")
	}

	// It stays small: at most bgCacheSize entries and bgCacheBytes.
	for _, sz := range []image.Point{{640, 480}, {800, 480}, {1280, 800}, {1366, 768}, {3840, 2160}, {4096, 2160}, {1920, 1080}} {
		Render(st, sz.X, sz.Y)
	}
	bgMu.Lock()
	size := 0
	for _, e := range bgEntries {
		size += len(e.img.Pix)
	}
	if len(bgEntries) > bgCacheSize || size > bgCacheBytes {
		t.Errorf("cache holds %d backgrounds, %d bytes", len(bgEntries), size)
	}
	bgMu.Unlock()
}

// BenchmarkRender draws the ready screen: warm is a redraw with the
// background cached (a status change), cold paints it too (a new size or
// tone).
func BenchmarkRender(b *testing.B) {
	st := loadFixtures(b)["ready"]
	for _, sz := range []image.Point{{1920, 1080}, {3840, 2160}} {
		b.Run(sizeName(sz)+"/warm", func(b *testing.B) {
			Render(st, sz.X, sz.Y)
			for b.Loop() {
				Render(st, sz.X, sz.Y)
			}
		})
		b.Run(sizeName(sz)+"/cold", func(b *testing.B) {
			for b.Loop() {
				bgMu.Lock()
				bgEntries = nil
				bgMu.Unlock()
				Render(st, sz.X, sz.Y)
			}
		})
	}
}

// The tone of a state reaches the layout; a word outside the vocabulary is
// neutral, and progress is clamped.
func TestLayoutTone(t *testing.T) {
	for _, c := range []struct {
		st      State
		tone    brand.State
		percent int
	}{
		{State{Tone: brand.Updating, Progress: 42}, brand.Updating, 42},
		{State{Tone: "bogus", Progress: 140}, brand.Neutral, 100},
		{State{Progress: -3}, brand.Neutral, 0},
	} {
		l := computeLayout(c.st, 1280, 720)
		if l.Tone != c.tone || l.Percent != c.percent {
			t.Errorf("%+v: tone %q percent %d, want %q %d", c.st, l.Tone, l.Percent, c.tone, c.percent)
		}
	}
}
