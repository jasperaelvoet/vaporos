package welcome

// The TV's faces are static TrueType cuts that tools/fonts/fonts.py writes into
// fonts/ from design/fonts/fonts.json (TOK §2.7). These tests check them
// without Python: every file is one the manifest produces and a TV type role
// draws, each is a static font the sfnt package reads, and each covers the
// TV's corpus.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

const tvFontDir = "fonts"

// tvFontManifest is design/fonts/fonts.json, as far as the TV reads it.
type tvFontManifest struct {
	Families map[string]struct{ Family, LicenseFile, LicenseSha256 string }
	Faces    []tvFontFace
}

type tvFontFace struct {
	Role, Face, Family, TV string
	Result                 struct {
		TV *struct {
			Bytes  int
			Sha256 string
		}
	}
}

func (f tvFontFace) key() string  { return f.Role + "/" + f.Face }
func (f tvFontFace) file() string { return filepath.Base(f.TV) }

// readTVFonts returns the manifest and its faces with a TV cut.
func readTVFonts(t *testing.T) (tvFontManifest, []tvFontFace) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "design", "fonts", "fonts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m tvFontManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("design/fonts/fonts.json: %v", err)
	}
	var tv []tvFontFace
	for _, f := range m.Faces {
		if f.TV == "" {
			continue
		}
		if filepath.ToSlash(filepath.Dir(f.TV)) != "internal/display/welcome/fonts" || filepath.Ext(f.TV) != ".ttf" {
			t.Errorf("%s: tv %q must be a .ttf in internal/display/welcome/fonts", f.key(), f.TV)
		}
		tv = append(tv, f)
	}
	if len(tv) == 0 {
		t.Fatal("design/fonts/fonts.json lists no TV faces")
	}
	return m, tv
}

// drawnFaces lists every "<font>/<face>" a brand.TVType role draws with; a
// role set in the state's cut draws with every cut a state or the pairing
// modifier can take.
func drawnFaces() map[string]bool {
	cuts := map[string]bool{brand.StyleOf(brand.Neutral).Cut: true, brand.AttentionPair.Cut: true}
	for _, s := range brand.States {
		cuts[brand.StyleOf(s).Cut] = true
	}
	out := map[string]bool{}
	v := reflect.ValueOf(brand.TVType)
	for i := 0; i < v.NumField(); i++ {
		tt, ok := v.Field(i).Interface().(brand.TVText)
		switch {
		case !ok:
		case tt.Face == "state":
			for c := range cuts {
				out[tt.Font+"/"+c] = true
			}
		default:
			out[tt.Font+"/"+tt.Face] = true
		}
	}
	return out
}

// Every file in fonts/ is embedded, so a leftover cut would only bloat the
// binary: each .ttf must be a manifest output that a TV type role draws, and
// each .txt the licence of a family the TV ships.
func TestNoUnusedFontFiles(t *testing.T) {
	m, tv := readTVFonts(t)
	byFile := map[string]tvFontFace{}
	licences := map[string]string{} // file → sha256
	for _, f := range tv {
		byFile[f.file()] = f
		fam := m.Families[f.Family]
		licences[fam.LicenseFile] = fam.LicenseSha256
	}
	entries, err := os.ReadDir(tvFontDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		b, err := os.ReadFile(filepath.Join(tvFontDir, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		got := hex.EncodeToString(sum[:])
		if f, ok := byFile[name]; ok {
			if r := f.Result.TV; r == nil || r.Bytes != len(b) || r.Sha256 != got {
				t.Errorf("fonts/%s differs from the result fonts.json records: run python3 tools/fonts/fonts.py", name)
			}
			continue
		}
		if want, ok := licences[name]; ok {
			if got != want {
				t.Errorf("fonts/%s is not the pinned licence text", name)
			}
			continue
		}
		t.Errorf("fonts/%s is embedded, but design/fonts/fonts.json does not produce it", name)
	}
	for file := range byFile {
		if _, err := os.Stat(filepath.Join(tvFontDir, file)); err != nil {
			t.Errorf("fonts/%s is missing: run python3 tools/fonts/fonts.py", file)
		}
	}
	for file := range licences {
		if _, err := os.Stat(filepath.Join(tvFontDir, file)); err != nil {
			t.Errorf("fonts/%s is missing: the OFL travels with every TV font", file)
		}
	}

	drawn := drawnFaces()
	cut := map[string]string{}
	for _, f := range tv {
		cut[f.key()] = f.file()
		if !drawn[f.key()] {
			t.Errorf("fonts/%s (%s) is drawn by no brand.TVType role: drop its tv cut from fonts.json", f.file(), f.key())
		}
	}
	for _, k := range sortedFontKeys(drawn) {
		if cut[k] == "" {
			t.Errorf("brand.TVType draws with %s, which has no TV cut in fonts.json", k)
		}
	}
	if len(brand.TVFontFiles) == 0 {
		t.Logf("tokens.json names no TV font files yet; checked against brand.TVType instead")
		return
	}
	for k, file := range brand.TVFontFiles {
		if cut[k] != filepath.Base(file) {
			t.Errorf("brand.TVFontFiles[%q] = %q, fonts.json cuts %q", k, file, cut[k])
		}
	}
	for k := range cut {
		if _, ok := brand.TVFontFiles[k]; !ok {
			t.Errorf("fonts.json cuts %s for the TV, but brand.TVFontFiles does not name it", k)
		}
	}
}

func sortedFontKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tvPunct is the punctuation the TV's copy uses (TOK §2.7).
const tvPunct = "…’‘“”–—·×•°"

// tvGaps are the corpus letters a family never drew; the renderer's goregular
// fallback draws them. Nothing in ASCII or tvPunct may be listed.
var tvGaps = map[string]string{
	"Anybody":   "ŉſ",
	"Mona Sans": "µĸŉŎŏſ",
}

// tvCorpus is what a TV face must draw. Every face draws ASCII, the
// punctuation and the renderer's own labels. The display and ui faces also
// draw device names and passed-through messages, so they cover Latin-1 and
// Latin Extended-A; the mono face draws addresses, codes and versions.
func tvCorpus(role string) []rune {
	var out []rune
	for r := rune(0x20); r <= 0x7e; r++ {
		out = append(out, r)
	}
	out = append(out, []rune(tvPunct)...)
	l := brand.TVLabels
	for _, s := range []string{l.CodeLabel, l.QRCaption, l.VersionPrefix, l.InstallerPrefix, l.Starting, l.ScaleCold, l.ScaleHot} {
		out = append(out, []rune(s)...)
	}
	if role == "mono" {
		return out
	}
	for r := rune(0xa0); r <= 0x17f; r++ {
		if r != 0xad { // the soft hyphen is never drawn
			out = append(out, r)
		}
	}
	return out
}

// Every TV face is a static TrueType font the sfnt package reads (no fvar:
// sfnt has no variations), covers its corpus, and keeps its kerning (GPOS pair
// kerning is all sfnt applies).
func TestFontsCoverCorpus(t *testing.T) {
	m, tv := readTVFonts(t)
	for _, f := range tv {
		t.Run(f.key(), func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(tvFontDir, f.file()))
			if err != nil {
				t.Fatal(err)
			}
			if tables := sfntTables(b); tables["fvar"] || tables["GSUB"] || !tables["glyf"] {
				t.Errorf("fonts/%s: tables %v; the TV needs static TrueType outlines and no GSUB", f.file(), sortedFontKeys(tables))
			}
			fnt, err := sfnt.Parse(b)
			if err != nil {
				t.Fatalf("fonts/%s: %v", f.file(), err)
			}
			family := m.Families[f.Family].Family
			gaps := tvGaps[family]
			var buf sfnt.Buffer
			var missing, unexpected []string
			for _, r := range tvCorpus(f.Role) {
				if gi, err := fnt.GlyphIndex(&buf, r); err == nil && gi != 0 {
					continue
				}
				if strings.ContainsRune(gaps, r) && r > 0x7e && !strings.ContainsRune(tvPunct, r) {
					missing = append(missing, string(r))
					continue
				}
				unexpected = append(unexpected, fmt.Sprintf("%q U+%04X", r, r))
			}
			if len(unexpected) > 0 {
				t.Errorf("fonts/%s (%s) cannot draw %s", f.file(), family, strings.Join(unexpected, ", "))
			}
			if len(missing) > 0 {
				t.Logf("fonts/%s leaves %s to the fallback face", f.file(), strings.Join(missing, " "))
			}
			if f.Role != "mono" && !fontKerns(fnt, &buf) {
				t.Errorf("fonts/%s: no kerning between AV, To, Te, Ty, VA or Yo; GPOS pair kerning was lost", f.file())
			}
		})
	}
}

// fontKerns reports whether any common Latin pair kerns tighter at 100 ppem.
func fontKerns(f *sfnt.Font, buf *sfnt.Buffer) bool {
	for _, p := range []string{"AV", "To", "Te", "Ty", "VA", "Yo"} {
		a, _ := f.GlyphIndex(buf, rune(p[0]))
		b, _ := f.GlyphIndex(buf, rune(p[1]))
		if k, err := f.Kern(buf, a, b, fixed.I(100), font.HintingNone); err == nil && k < 0 {
			return true
		}
	}
	return false
}

// sfntTables reads the table directory of a TrueType or OpenType file.
func sfntTables(b []byte) map[string]bool {
	out := map[string]bool{}
	if len(b) < 12 {
		return out
	}
	n := int(b[4])<<8 | int(b[5])
	for i := 0; i < n && 12+16*i+4 <= len(b); i++ {
		out[string(b[12+16*i:12+16*i+4])] = true
	}
	return out
}
