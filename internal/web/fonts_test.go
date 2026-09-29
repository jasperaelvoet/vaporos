package web

// The control center's fonts are cut by tools/fonts/fonts.py from
// design/fonts/fonts.json and committed under static/fonts/. These tests check
// what ships against that manifest without Python (ARCH §4.4): the files, their
// hashes and budgets, the licences, the stylesheet's @font-face rules, the
// preloads and the generated fallback faces.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Font budgets. The phone is the main client, often on Wi-Fi. Raising one is
// a design decision: change it here and in fonts.json's "budget" together.
const (
	budgetFontsRaw   = 120_000 // every WOFF2 under static/fonts/
	budgetPreloads   = 2       // fonts one page may preload
	budgetPreloadRaw = 60_000  // each preloaded font
)

// fontManifest is design/fonts/fonts.json, as far as the web UI reads it.
type fontManifest struct {
	Families map[string]struct {
		Family        string
		LicenseFile   string
		LicenseSha256 string
	}
	Budget struct{ WebTotal, Preloads, PreloadEach int }
	Faces  []fontFace
}

type fontFace struct {
	Role, Face, Family string
	Wdth, Wght         float64
	Web                string
	Preload            bool
	Result             struct {
		Web *struct {
			Bytes  int
			Sha256 string
		}
		Fallback struct {
			Family                                                       string
			Local                                                        []string
			SizeAdjust, AscentOverride, DescentOverride, LineGapOverride string
		}
	}
}

// asset is the face's path under static/, as the asset store keys it.
func (f fontFace) asset() string { return strings.TrimPrefix(f.Web, "internal/web/static/") }

func (f fontFace) key() string { return f.Role + "/" + f.Face }

func readFontManifest(t *testing.T) fontManifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "design", "fonts", "fonts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m fontManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("design/fonts/fonts.json: %v", err)
	}
	return m
}

// webFaces are the faces with a WOFF2 for the control center.
func (m fontManifest) webFaces() []fontFace {
	var out []fontFace
	for _, f := range m.Faces {
		if f.Web != "" {
			out = append(out, f)
		}
	}
	return out
}

// cssFontFace is one @font-face rule: its declarations, keyed by property.
type cssFontFace map[string]string

var (
	// cssFontStack matches a font-family or --font-* declaration that lists
	// more than one family.
	cssFontStack = regexp.MustCompile(`((?:--[\w-]*font-family|--font-[\w-]+|font-family))\s*:\s*([^;{}]*,[^;{}]*)`)
	cssURL       = regexp.MustCompile(`url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]*))\s*\)`)
	cssLocal     = regexp.MustCompile(`local\(\s*"([^"]*)"\s*\)`)
)

// fontFaceRules parses every @font-face rule in css. Comments and strings are
// blanked first (cssBlank keeps offsets), so a ';' or '}' inside a quoted
// family name or URL cannot split a declaration.
func fontFaceRules(css string) []cssFontFace {
	blank := cssBlank(css)
	var out []cssFontFace
	for i := 0; ; {
		k := strings.Index(blank[i:], "@font-face")
		if k < 0 {
			return out
		}
		open := strings.IndexByte(blank[i+k:], '{')
		if open < 0 {
			return out
		}
		from := i + k + open + 1
		end := strings.IndexByte(blank[from:], '}')
		if end < 0 {
			end = len(blank) - from
		}
		rule := cssFontFace{}
		start := from
		for j := from; j <= from+end; j++ {
			if j < from+end && blank[j] != ';' {
				continue
			}
			if prop, val, ok := strings.Cut(css[start:j], ":"); ok {
				rule[strings.ToLower(strings.TrimSpace(prop))] = strings.TrimSpace(val)
			}
			start = j + 1
		}
		out = append(out, rule)
		i = from + end
	}
}

func unquote(s string) string { return strings.Trim(s, `"'`) }

// family is the rule's font-family without quotes.
func (r cssFontFace) family() string { return unquote(r["font-family"]) }

// cssKeywords maps the weight and width keywords a minifier may write to numbers.
var cssKeywords = map[string]string{
	"normal": "", "bold": "700", "ultra-condensed": "50%", "extra-condensed": "62.5%", "condensed": "75%",
	"semi-condensed": "87.5%", "semi-expanded": "112.5%", "expanded": "125%", "extra-expanded": "150%", "ultra-expanded": "200%",
}

// matches reports whether the rule declares f's weight and width.
func (r cssFontFace) matches(f fontFace) bool {
	norm := func(v, normal string) string {
		if k, ok := cssKeywords[v]; ok {
			if k == "" {
				return normal
			}
			return k
		}
		return v
	}
	return norm(r["font-weight"], "400") == fmt.Sprint(f.Wght) && norm(r["font-stretch"], "100%") == fmt.Sprintf("%v%%", f.Wdth)
}

func TestFonts(t *testing.T) {
	m := readFontManifest(t)
	faces := m.webFaces()
	if len(faces) == 0 {
		t.Fatal("design/fonts/fonts.json lists no web faces")
	}
	byAsset := map[string]fontFace{}
	for _, f := range faces {
		if !strings.HasPrefix(f.Web, "internal/web/static/fonts/") || path.Ext(f.Web) != ".woff2" {
			t.Errorf("%s: web %q must be a .woff2 under internal/web/static/fonts/", f.key(), f.Web)
		}
		if _, dup := byAsset[f.asset()]; dup {
			t.Errorf("%s: %s is listed twice", f.key(), f.Web)
		}
		byAsset[f.asset()] = f
	}

	t.Run("files", func(t *testing.T) {
		for _, set := range uiSets {
			for _, f := range faces {
				if !set.servesStatic(f.asset()) {
					t.Errorf("UI set %q does not serve %s", set.Name, f.asset())
				}
			}
		}
		u := testUI(t, activeSet)
		for _, f := range faces {
			a := u.assets.files[f.asset()]
			switch {
			case a == nil:
				t.Errorf("%s: %s is not embedded; run python3 tools/fonts/fonts.py", f.key(), f.Web)
				continue
			case a.ctype != "font/woff2" || a.gz != nil:
				t.Errorf("%s: served as %q (gzip variant %v), want font/woff2 without gzip", f.key(), a.ctype, a.gz != nil)
			case !bytes.HasPrefix(a.body, []byte("wOF2")):
				t.Errorf("%s: %s is not WOFF2", f.key(), f.Web)
			}
			sum := sha256.Sum256(a.body)
			if r := f.Result.Web; r == nil || r.Bytes != len(a.body) || r.Sha256 != hex.EncodeToString(sum[:]) {
				t.Errorf("%s: %s (%d bytes) differs from the result fonts.json records: run python3 tools/fonts/fonts.py", f.key(), f.Web, len(a.body))
			}
		}
		// Only the manifest's outputs ship: no leftover cut, and no licence
		// text (U-9: the OFL stays in design/fonts and in the TV's embed).
		walkStatic(t, func(name string, b []byte) {
			if strings.HasPrefix(name, "fonts/") {
				if _, ok := byAsset[name]; !ok {
					t.Errorf("static/%s ships, but design/fonts/fonts.json does not produce it", name)
				}
			}
		})
	})

	t.Run("budget", func(t *testing.T) {
		if m.Budget.WebTotal != budgetFontsRaw || m.Budget.Preloads != budgetPreloads || m.Budget.PreloadEach != budgetPreloadRaw {
			t.Errorf("fonts.json budget %+v differs from this test's (%d, %d, %d); change both together", m.Budget, budgetFontsRaw, budgetPreloads, budgetPreloadRaw)
		}
		total, preloads := 0, 0
		for _, f := range faces {
			if f.Result.Web == nil {
				continue
			}
			total += f.Result.Web.Bytes
			if f.Preload {
				preloads++
				if f.Result.Web.Bytes > budgetPreloadRaw {
					t.Errorf("%s is preloaded at %d bytes, budget is %d", f.Web, f.Result.Web.Bytes, budgetPreloadRaw)
				}
			}
		}
		t.Logf("web fonts: %d bytes in %d files, %d preloaded", total, len(faces), preloads)
		if total > budgetFontsRaw {
			t.Errorf("the web fonts total %d bytes, budget is %d", total, budgetFontsRaw)
		}
		if preloads > budgetPreloads {
			t.Errorf("%d faces are preloaded, budget is %d", preloads, budgetPreloads)
		}
	})

	t.Run("licences", func(t *testing.T) {
		for id, fam := range m.Families {
			b, err := os.ReadFile(filepath.Join(repoRoot, "design", "fonts", fam.LicenseFile))
			sum := sha256.Sum256(b)
			if err != nil || hex.EncodeToString(sum[:]) != fam.LicenseSha256 || !bytes.Contains(b, []byte("SIL OPEN FONT LICENSE Version 1.1")) {
				t.Errorf("%s: design/fonts/%s is missing or not the pinned OFL text", id, fam.LicenseFile)
			}
		}
	})

	t.Run("tokens", func(t *testing.T) { testFontsMatchTokens(t, m) })

	t.Run("font-face", func(t *testing.T) { testFontFaceDecls(t, m, faces, byAsset) })

	t.Run("preloads", func(t *testing.T) {
		forEachSet(t, func(t *testing.T, set uiSet) { testFontPreloads(t, set, faces) })
	})

	t.Run("fallback", func(t *testing.T) { testFallbackFaces(t, faces) })
}

// testFontFaceDecls checks each set's app.css: every @font-face for a web
// font points to a file fonts.json produces, relative to the stylesheet, with
// that face's family, weight and width and a font-display; a set that uses the
// fonts also carries their fallback faces and names them in a font stack; and
// once any stylesheet declares the fonts, every shipped WOFF2 is declared.
func testFontFaceDecls(t *testing.T, m fontManifest, faces []fontFace, byAsset map[string]fontFace) {
	declared := map[string]bool{}
	for _, set := range uiSets {
		b, err := content.ReadFile(path.Join("static", set.Static, "app.css"))
		if err != nil {
			continue
		}
		css := string(b)
		families := map[string]bool{}
		for _, r := range fontFaceRules(css) {
			if strings.HasSuffix(r.family(), " fallback") {
				continue
			}
			if !strings.Contains(r["src"], "url(") {
				continue
			}
			if r["font-display"] == "" {
				t.Errorf("%s: @font-face for %q has no font-display", set.Name, r.family())
			}
			for _, u := range cssURL.FindAllStringSubmatch(r["src"], -1) {
				ref := u[1] + u[2] + u[3]
				if strings.HasPrefix(ref, "/") || strings.Contains(ref, ":") {
					t.Errorf("%s: @font-face url(%s) must be relative to app.css", set.Name, ref)
					continue
				}
				name := path.Join(set.Static, ref)
				f, ok := byAsset[name]
				switch {
				case !ok:
					t.Errorf("%s: @font-face url(%s) is not a font fonts.json produces", set.Name, ref)
				case r.family() != m.Families[f.Family].Family || !r.matches(f):
					t.Errorf("%s: @font-face for %s declares %q %s %s, want %q %v %v%%", set.Name, ref,
						r.family(), r["font-weight"], r["font-stretch"], m.Families[f.Family].Family, f.Wght, f.Wdth)
				default:
					declared[name] = true
					families[r.family()] = true
				}
			}
		}
		// A set that uses the web fonts carries their fallback faces, and
		// every font stack that names a family names its fallback right
		// after it, so the swap moves nothing.
		for fam := range families {
			rules := 0
			for _, r := range fontFaceRules(css) {
				if r.family() == fam+" fallback" {
					rules++
				}
			}
			if rules == 0 {
				t.Errorf("%s: app.css declares %q without its fallback faces; import styles/fonts-fallback.css", set.Name, fam)
			}
		}
		for _, d := range cssFontStack.FindAllStringSubmatch(css, -1) {
			stack := strings.Split(d[2], ",")
			for i, name := range stack {
				if fam := unquote(strings.TrimSpace(name)); families[fam] && (i+1 == len(stack) || unquote(strings.TrimSpace(stack[i+1])) != fam+" fallback") {
					t.Errorf("%s: %s lists %q without %q right after it; list it in tokens.json", set.Name, d[1], fam, fam+" fallback")
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Logf("no stylesheet declares the web fonts yet (tokens.json lists no web files)")
		return
	}
	for _, f := range faces {
		if !declared[f.asset()] {
			t.Errorf("%s ships, but no stylesheet declares it: drop it from fonts.json or use it", f.Web)
		}
	}
}

var (
	htmlLink = regexp.MustCompile(`<link\b[^>]*>`)
	htmlAttr = regexp.MustCompile(`([a-zA-Z-]+)(?:\s*=\s*"([^"]*)")?`)
)

// testFontPreloads checks every page's <link rel="preload" as="font">: only
// the faces fonts.json marks, typed and CORS-mode (or the browser fetches the
// font twice), at most two, and all of them once the set's stylesheet uses them.
func testFontPreloads(t *testing.T, set uiSet, faces []fontFace) {
	u := testUI(t, set)
	want := map[string]bool{}
	for _, f := range faces {
		if f.Preload {
			want[f.asset()] = true
		}
	}
	css, _ := content.ReadFile(path.Join("static", set.Static, "app.css"))
	usesFonts := false
	for _, f := range faces {
		usesFonts = usesFonts || bytes.Contains(css, []byte(path.Base(f.Web)))
	}
	seen := map[string]bool{}
	for script, body := range renderAll(t, set) {
		n := 0
		for _, tag := range htmlLink.FindAllString(body, -1) {
			attrs := map[string]string{}
			for _, a := range htmlAttr.FindAllStringSubmatch(strings.TrimSuffix(strings.TrimPrefix(tag, "<link"), ">"), -1) {
				attrs[strings.ToLower(a[1])] = a[2]
			}
			if attrs["rel"] != "preload" || attrs["as"] != "font" {
				continue
			}
			n++
			name := strings.TrimPrefix(attrs["href"], u.assets.prefix()+"/")
			if !want[name] {
				t.Errorf("%s: preloads %s, which fonts.json does not mark preload", script, attrs["href"])
			}
			if _, ok := attrs["crossorigin"]; !ok || attrs["type"] != "font/woff2" {
				t.Errorf("%s: the preload of %s needs type=\"font/woff2\" and crossorigin", script, attrs["href"])
			}
			seen[name] = true
		}
		if n > budgetPreloads {
			t.Errorf("%s: %d font preloads, budget is %d", script, n, budgetPreloads)
		}
	}
	if !usesFonts {
		if len(seen) > 0 {
			t.Errorf("the set's stylesheet declares no web font, but its pages preload %v", keysOf(seen))
		}
		return
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("no page preloads %s, which fonts.json marks preload", name)
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// testFallbackFaces checks the generated styles/fonts-fallback.css against the
// results fonts.json records: exactly one local() face per web face, in the
// family "<family> fallback", with that face's weight, width and metrics.
func testFallbackFaces(t *testing.T, faces []fontFace) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "internal", "web", "styles", "fonts-fallback.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	if !strings.HasPrefix(css, "/* Code generated") || !strings.Contains(strings.SplitN(css, "\n", 2)[0], "DO NOT EDIT") {
		t.Errorf("styles/fonts-fallback.css lacks its generated-file header")
	}
	rules := fontFaceRules(css)
	used := make([]bool, len(rules))
	for _, f := range faces {
		fb := f.Result.Fallback
		n := 0
		for i, r := range rules {
			if r.family() != fb.Family || !r.matches(f) {
				continue
			}
			n++
			used[i] = true
			var local []string
			for _, l := range cssLocal.FindAllStringSubmatch(r["src"], -1) {
				local = append(local, l[1])
			}
			got := []string{strings.Join(local, ","), r["size-adjust"], r["ascent-override"], r["descent-override"], r["line-gap-override"]}
			want := []string{strings.Join(fb.Local, ","), fb.SizeAdjust, fb.AscentOverride, fb.DescentOverride, fb.LineGapOverride}
			if strings.Contains(r["src"], "url(") || strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("%s: fallback face %q is stale (%v, want %v): run python3 tools/fonts/fonts.py", f.key(), fb.Family, got, want)
			}
		}
		if n != 1 || !strings.HasSuffix(fb.Family, " fallback") {
			t.Errorf("%s: %d fallback faces named %q at weight %v and width %v%%, want exactly one", f.key(), n, fb.Family, f.Wght, f.Wdth)
		}
	}
	for i, r := range rules {
		if !used[i] {
			t.Errorf("styles/fonts-fallback.css: face %q %s %s belongs to no web face", r.family(), r["font-weight"], r["font-stretch"])
		}
	}
}

// testFontsMatchTokens checks that tokens.json and fonts.json describe the same
// faces. tokens.json names each face's web file and preload once the fonts are
// wired in; until then it may name none, but never only some.
func testFontsMatchTokens(t *testing.T, m fontManifest) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "design", "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		Font map[string]struct {
			Family string
			Faces  map[string]struct {
				Wdth, Wght float64
				Web        string
				Preload    bool
			}
		}
	}
	if err := json.Unmarshal(b, &tok); err != nil {
		t.Fatalf("design/tokens.json: %v", err)
	}
	listed := 0
	for _, role := range tok.Font {
		for _, fc := range role.Faces {
			if fc.Web != "" {
				listed++
			}
		}
	}
	webFaces := map[string]fontFace{}
	for _, f := range m.Faces {
		role, ok := tok.Font[f.Role]
		fc, okFace := role.Faces[f.Face]
		switch {
		case !ok || !okFace:
			t.Errorf("%s: fonts.json cuts a face tokens.json does not define", f.key())
			continue
		case role.Family != m.Families[f.Family].Family:
			t.Errorf("%s: tokens.json family %q, fonts.json %q", f.key(), role.Family, m.Families[f.Family].Family)
		case fc.Wdth != f.Wdth || fc.Wght != f.Wght:
			t.Errorf("%s: tokens.json wdth %v wght %v, fonts.json wdth %v wght %v", f.key(), fc.Wdth, fc.Wght, f.Wdth, f.Wght)
		}
		if f.Web != "" {
			webFaces[f.key()] = f
		}
		if listed > 0 && (fc.Web != f.Web || fc.Preload != f.Preload) {
			t.Errorf("%s: tokens.json web %q preload %v, fonts.json web %q preload %v", f.key(), fc.Web, fc.Preload, f.Web, f.Preload)
		}
	}
	for r, role := range tok.Font {
		for k, fc := range role.Faces {
			if _, ok := webFaces[r+"/"+k]; fc.Web != "" && !ok {
				t.Errorf("%s/%s: tokens.json names %s, which fonts.json does not produce", r, k, fc.Web)
			}
		}
	}
	if listed == 0 {
		t.Logf("tokens.json lists no web font files yet")
	}
}

// The rule parser reads both the generator's tokens.css and Tailwind's
// minified output, and a ';' or '}' inside a string splits nothing.
func TestFontFaceRules(t *testing.T) {
	css := `/* @font-face { x } */
@font-face {
  font-family: "Anybody";
  src: url("fonts/anybody-cold.woff2") format("woff2");
  font-weight: 560;
  font-stretch: 72%;
  font-display: swap;
}
@font-face{font-family:"Odd;}Name";src:url(fonts/anybody-warm.woff2)format("woff2");font-weight:bold;font-stretch:normal}`
	got := fontFaceRules(css)
	if len(got) != 2 {
		t.Fatalf("parsed %d rules, want 2: %v", len(got), got)
	}
	cold := fontFace{Wdth: 72, Wght: 560}
	warm := fontFace{Wdth: 100, Wght: 700}
	if got[0].family() != "Anybody" || got[0]["font-display"] != "swap" || !got[0].matches(cold) || got[0].matches(warm) {
		t.Errorf("first rule: %v", got[0])
	}
	if got[1].family() != "Odd;}Name" || !got[1].matches(warm) {
		t.Errorf("second rule: %v", got[1])
	}
	if u := cssURL.FindStringSubmatch(got[1]["src"]); u == nil || u[1]+u[2]+u[3] != "fonts/anybody-warm.woff2" {
		t.Errorf("second rule's url: %v", u)
	}
}
