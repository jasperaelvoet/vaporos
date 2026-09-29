package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/jasperaelvoet/vaporos/internal/brand"
)

// The app icons, the manifest and both logo partials are generated from
// design/logo.svg by internal/brand (VOS_GEN_DESIGN=1 go test ./internal/brand
// -run TestGenerateDesign), which also checks they are fresh. These tests
// check what the pages and browsers rely on.

func readPNG(t *testing.T, name string) image.Image {
	t.Helper()
	b, err := content.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return img
}

func alphaAt(img image.Image, x, y int) uint32 {
	_, _, _, a := img.At(x, y).RGBA()
	return a >> 8
}

// TestIconPNGs checks the sizes, and where each icon must be opaque: iOS
// turns transparency black, so the touch icon and the maskable icon are
// full-bleed; the manifest's "any" icons keep the tile's rounded corners.
func TestIconPNGs(t *testing.T) {
	icons := []struct {
		name      string
		size      int
		fullBleed bool
		optional  bool // shipped only within the icon budget (U-11)
	}{
		{"icon-180.png", 180, true, false},
		{"icon-192.png", 192, false, false},
		{"icon-512.png", 512, false, true},
		{"icon-maskable-512.png", 512, true, true},
	}
	for _, ic := range icons {
		t.Run(ic.name, func(t *testing.T) {
			if _, err := content.ReadFile("static/" + ic.name); err != nil && ic.optional {
				t.Skipf("%s is not shipped (over the %d-byte icon budget)", ic.name, budgetIconRaw)
			}
			img := readPNG(t, ic.name)
			if b := img.Bounds(); b.Dx() != ic.size || b.Dy() != ic.size {
				t.Fatalf("%s is %dx%d, want %dx%d", ic.name, b.Dx(), b.Dy(), ic.size, ic.size)
			}
			if a := alphaAt(img, ic.size/2, ic.size/2); a != 255 {
				t.Errorf("the centre has alpha %d, want 255", a)
			}
			if ic.fullBleed {
				for y := 0; y < ic.size; y++ {
					for x := 0; x < ic.size; x++ {
						if a := alphaAt(img, x, y); a != 255 {
							t.Fatalf("pixel (%d,%d) has alpha %d; a full-bleed icon is opaque", x, y, a)
						}
					}
				}
			} else if a := alphaAt(img, 0, 0); a != 0 {
				t.Errorf("the corner has alpha %d; the rounded tile leaves it transparent", a)
			}
		})
	}
}

// TestManifest checks the web app manifest the pages link.
func TestManifest(t *testing.T) {
	b, err := content.ReadFile("static/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		StartURL        string `json:"start_url"`
		Scope           string `json:"scope"`
		Display         string `json:"display"`
		BackgroundColor string `json:"background_color"`
		ThemeColor      string `json:"theme_color"`
		Icons           []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest.webmanifest: %v", err)
	}
	if m.ID != "/" || m.StartURL != "/" || m.Scope != "/" {
		t.Errorf("id, start_url and scope = %q, %q, %q; want all \"/\"", m.ID, m.StartURL, m.Scope)
	}
	if m.Name != brand.AppName || m.ShortName != brand.AppShortName || m.Display != "standalone" {
		t.Errorf("name %q, short_name %q, display %q", m.Name, m.ShortName, m.Display)
	}
	if m.BackgroundColor != brand.ThemeColorDark || m.ThemeColor != brand.ThemeColorDark {
		t.Errorf("background_color %q, theme_color %q; want the dark canvas %q", m.BackgroundColor, m.ThemeColor, brand.ThemeColorDark)
	}
	u := testUI(t, activeSet)
	_, h := newHandler(t, activeSet, false, "")
	purposes := map[string]bool{}
	for _, ic := range m.Icons {
		purposes[ic.Purpose] = true
		if ic.Purpose != "any" && ic.Purpose != "maskable" {
			t.Errorf("%s: purpose %q, want any or maskable", ic.Src, ic.Purpose)
		}
		if strings.Contains(ic.Src, "/") {
			t.Errorf("%s: icon paths stay relative to the manifest, so they carry its version", ic.Src)
		}
		if rec := get(h, u.assets.prefix()+"/"+ic.Src); rec.Code != 200 || rec.Header().Get("Content-Type") != ic.Type {
			t.Errorf("%s: served %d as %q, manifest says %q", ic.Src, rec.Code, rec.Header().Get("Content-Type"), ic.Type)
		}
		if ic.Type == "image/png" {
			img := readPNG(t, ic.Src)
			if got := fmt.Sprintf("%dx%d", img.Bounds().Dx(), img.Bounds().Dy()); got != ic.Sizes {
				t.Errorf("%s is %s, manifest says %s", ic.Src, got, ic.Sizes)
			}
		}
	}
	if !purposes["any"] {
		t.Error("the manifest lists no icon for any purpose")
	}
}

var logoDefine = regexp.MustCompile(`\{\{-?\s*define "logo"\s*-?\}\}`)

// templateFiles lists the set's template files: its layout, pages and partials.
func templateFiles(t *testing.T, set uiSet) []string {
	t.Helper()
	var names []string
	for _, glob := range []string{path.Join(set.Templates, "layout.html"), path.Join(set.Templates, "pages", "*.html"), set.Partials} {
		m, err := fs.Glob(content, glob)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, m...)
	}
	return names
}

// TestLogoDefinedOnce: each set defines "logo" exactly once, in its generated
// partial, so no hand-drawn copy can drift from design/logo.svg.
func TestLogoDefinedOnce(t *testing.T) {
	for _, set := range uiSets {
		t.Run(set.Name, func(t *testing.T) {
			var where []string
			for _, name := range templateFiles(t, set) {
				b, _ := content.ReadFile(name)
				for range logoDefine.FindAll(b, -1) {
					where = append(where, name)
				}
			}
			if len(where) != 1 || path.Dir(where[0]) != path.Dir(set.Partials) {
				t.Errorf(`"logo" is defined in %v; want once, in %s/logo.html`, where, path.Dir(set.Partials))
			}
		})
	}
}

// TestLogoPartialHasNoIDs: the logo carries no ids and no gradients, so a page
// may show it any number of times, and every page of a set shows it.
func TestLogoPartialHasNoIDs(t *testing.T) {
	body := regexp.MustCompile(`(?s)\{\{define "logo"\}\}(.*?)\{\{end\}\}`)
	for _, set := range uiSets {
		t.Run(set.Name, func(t *testing.T) {
			b, err := content.ReadFile(path.Join(path.Dir(set.Partials), "logo.html"))
			if err != nil {
				t.Fatal(err)
			}
			if regexp.MustCompile(`\sid=|<(linear|radial)Gradient|url\(#|<style|\sstyle=`).Match(b) {
				t.Errorf("%s has an id, a gradient or a style", set.Partials)
			}
			m := body.FindSubmatch(b)
			if m == nil {
				t.Fatalf(`%s does not define "logo"`, set.Partials)
			}
			if len(set.Pages) == 0 {
				return
			}
			for page, html := range renderAll(t, set) {
				if !strings.Contains(html, string(m[1])) {
					t.Errorf("%s does not render the logo", page)
				}
			}
		})
	}
}
