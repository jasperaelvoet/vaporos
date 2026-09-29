package welcome

import (
	"bytes"
	"testing"

	"golang.org/x/image/font/opentype"
)

// A device name with runes no face can draw (emoji, CJK in the Go fonts)
// loses them instead of showing .notdef boxes, and still fits its column.
func TestUnrenderableRunesDropped(t *testing.T) {
	loadFonts()
	st := loadFixtures(t)["long-unicode"]
	clean := st
	clean.Status = "Łukasz’s Steam Deck wants to pair"
	for _, sz := range []struct{ w, h int }{{1920, 1080}, {1280, 720}, {1080, 1920}} {
		l := computeLayout(st, sz.w, sz.h)
		for _, it := range l.Texts {
			if it.Role == roleStatus && it.Text != clean.Status {
				t.Errorf("%dx%d: status %q, want %q", sz.w, sz.h, it.Text, clean.Status)
			}
			if !it.Rect.In(l.Safe) || it.Rect.Overlaps(l.QRCard) {
				t.Errorf("%dx%d: %s %q at %v does not fit", sz.w, sz.h, it.Role, it.Text, it.Rect)
			}
		}
		if !bytes.Equal(Render(st, sz.w, sz.h).Pix, Render(clean, sz.w, sz.h).Pix) {
			t.Errorf("%dx%d: the dropped runes still left marks", sz.w, sz.h)
		}
	}
	for in, want := range map[string]string{
		"📺 テレビ":               "",
		"  Living\troom  TV ": "Living room TV",
		"Zoë 🎮 Deck":          "Zoë Deck",
		"3840 × 2160 · HDR":   "3840 × 2160 · HDR",
	} {
		if got := drawable(fontRegular, in); got != want {
			t.Errorf("drawable(%q) = %q, want %q", in, got, want)
		}
	}
}

// Moonlight names in Latin Extended, Greek and Cyrillic keep every letter.
// The draft's Go faces cover those scripts themselves, so each name is one
// run in its own face; a direction face that lacks them draws the missing
// runes in Go Regular, the fallback, instead.
func TestFallbackRuns(t *testing.T) {
	loadFonts()
	for _, name := range []string{"Łukasz’s Deck", "Дмитрий’s Steam Deck", "Σοφία’s iPad", "Zoë’s Legion Go", "Ørjan’s ROG Ally"} {
		for face, f := range map[string]*opentype.Font{"regular": fontRegular, "bold": fontBold, "mono": fontMono} {
			if got := drawable(f, name); got != name {
				t.Errorf("%s: %q became %q", face, name, got)
			}
		}
	}
}
