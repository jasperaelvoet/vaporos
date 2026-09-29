package welcome

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// statusText joins the status lines of l into the sentence they set.
func statusText(l *layout) string {
	var parts []string
	for _, it := range l.Texts {
		if it.Role == roleStatus {
			parts = append(parts, it.Text)
		}
	}
	return strings.Join(parts, " ")
}

// A device name with runes no face can draw (emoji, CJK) loses them instead
// of showing .notdef boxes, and still fits its column.
func TestUnrenderableRunesDropped(t *testing.T) {
	loadFonts()
	st := loadFixtures(t)["long-unicode"]
	clean := st
	clean.Status = "Łukasz’s Steam Deck wants to pair"
	for _, sz := range []struct{ w, h int }{{1920, 1080}, {1280, 720}, {1080, 1920}} {
		l := computeLayout(st, sz.w, sz.h)
		if got := statusText(l); got != clean.Status {
			t.Errorf("%dx%d: status %q, want %q", sz.w, sz.h, got, clean.Status)
		}
		for _, it := range l.Texts {
			if !it.Rect.In(l.Safe) || it.Rect.Overlaps(l.QRCard) {
				t.Errorf("%dx%d: %s %q at %v does not fit", sz.w, sz.h, it.Role, it.Text, it.Rect)
			}
		}
		if !bytes.Equal(Render(st, sz.w, sz.h).Pix, Render(clean, sz.w, sz.h).Pix) {
			t.Errorf("%dx%d: the dropped runes still left marks", sz.w, sz.h)
		}
	}
	ui := tvFont(brand.TVType.Detail, "")
	for in, want := range map[string]string{
		"📺 テレビ":               "",
		"  Living\troom  TV ": "Living room TV",
		"Zoë 🎮 Deck":          "Zoë Deck",
		"3840 × 2160 · HDR":   "3840 × 2160 · HDR",
	} {
		if got := drawable(ui, in); got != want {
			t.Errorf("drawable(%q) = %q, want %q", in, got, want)
		}
	}
}

// Moonlight names in Latin Extended, Greek and Cyrillic keep every letter:
// each TV face draws what it has, and the letters it lacks go to Go
// Regular, the fallback, in runs of their own.
func TestFallbackRuns(t *testing.T) {
	loadFonts()
	for key, f := range tvFonts {
		for _, name := range []string{"Łukasz’s Deck", "Дмитрий’s Steam Deck", "Σοφία’s iPad", "Zoë’s Legion Go", "Ørjan’s ROG Ally"} {
			if got := drawable(f, name); got != name {
				t.Errorf("%s: %q became %q", key, name, got)
			}
			var joined strings.Builder
			for _, r := range runs(f, name) {
				joined.WriteString(r.s)
				if r.f != f && r.f != fontFallback {
					t.Errorf("%s: %q drawn in a third face", key, r.s)
				}
			}
			if joined.String() != name {
				t.Errorf("%s: runs of %q join to %q", key, name, joined.String())
			}
		}
		// The brand cuts carry Latin only: Cyrillic and Greek letters come
		// from the fallback.
		rs := runs(f, "Дмитрий’s Deck")
		if len(rs) < 2 || rs[0].f != fontFallback || !strings.HasPrefix(rs[0].s, "Дмитрий") {
			t.Errorf("%s: %q runs %v, want the name in the fallback", key, "Дмитрий’s Deck", runFaces(rs))
		}
	}
	// A line mixing faces measures and draws as the sum of its runs.
	d := tvFont(brand.TVType.Status, "warm")
	if a, b := advance26(d, 64, "Σοφία’s iPad"), advance26(d, 64, "Σοφία")+advance26(d, 64, "’s iPad"); a != b {
		t.Errorf("advance of a mixed line %v, runs %v", a, b)
	}
}

func runFaces(rs []run) []string {
	var out []string
	for _, r := range rs {
		name := "brand"
		if r.f == fontFallback {
			name = "fallback"
		}
		out = append(out, name+":"+r.s)
	}
	return out
}

// Every "<font>/<face>" a TV type role draws with has a cut in fonts/, and
// the renderer's table names the same files as design/fonts/fonts.json.
func TestTVFontTable(t *testing.T) {
	_, tv := readTVFonts(t)
	byKey := map[string]string{}
	for _, f := range tv {
		byKey[f.key()] = f.file()
	}
	var keys []string
	for k := range tvFontFiles {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if byKey[k] != fontFile(k) {
			t.Errorf("tvFontFiles[%q] = %q, fonts.json cuts %q", k, fontFile(k), byKey[k])
		}
	}
	for k := range drawnFaces() {
		if _, ok := tvFontFiles[k]; !ok {
			t.Errorf("brand.TVType draws with %s, which the renderer does not load", k)
		}
	}
	loadFonts()
	for k, f := range tvFonts {
		if f == nil || f == fontFallback {
			t.Errorf("%s did not load", k)
		}
	}
}
