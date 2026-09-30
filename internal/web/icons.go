package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// legacyIcons are the 37 icons the eight-page UI had when it was set aside
// (3208d29). Its pages inline exactly these, so the frozen UI's HTML does
// not grow with every icon the new one appends (the drawings moved to
// design/icons unchanged).
var legacyIcons = []string{"home", "phone", "gamepad", "monitor", "drive", "update", "power", "sliders", "logout", "menu",
	"close", "check", "alert", "info", "external", "download", "key", "terminal", "restart", "coffee", "moon", "plus",
	"trash", "globe", "zap", "cpu", "wifi", "shield", "hdr", "usb", "library", "clock", "sparkle", "eye", "file",
	"chevron", "back"}

// spriteHTML returns the named icons, or every icon when none is named, as
// <symbol id="i-name"> in one hidden SVG.
func spriteHTML(only ...string) template.HTML {
	names := only
	if len(names) == 0 {
		for n := range iconPaths {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" class="sprite" aria-hidden="true" focusable="false">`)
	for _, n := range names {
		fmt.Fprintf(&b, `<symbol id="i-%s" viewBox="0 0 24 24">%s</symbol>`, n, iconPaths[n])
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// iconHTML is the template function {{icon "name"}}. An unknown name fails
// the render, so a typo shows up in the page tests rather than as a blank.
func iconHTML(name string) (template.HTML, error) {
	if _, ok := iconPaths[name]; !ok {
		return "", fmt.Errorf("unknown icon %q", name)
	}
	return template.HTML(`<svg class="icon" aria-hidden="true" focusable="false" width="20" height="20"><use href="#i-` + name + `"></use></svg>`), nil
}

var (
	templateIcon = regexp.MustCompile(`\{\{-?\s*icon\s+"([^"]+)"`)
	scriptIcon   = regexp.MustCompile(`\bicon\(\s*'([^']+)'`)
)

// iconsUsed lists the icons a set names: in its templates ({{icon "x"}}),
// its registry (the tab icons) and its scripts (icon('x')), so its pages
// carry only those. Scripts name icons with literals; TestScriptsMatchMarkup
// checks each one.
func iconsUsed(fsys fs.FS, set uiSet, assets *assetStore) ([]string, error) {
	seen := map[string]bool{}
	for _, p := range append([]page{set.Installer}, set.Pages...) {
		if p.Icon != "" {
			seen[p.Icon] = true
		}
	}
	files := []string{path.Join(set.Templates, "layout.html")}
	for _, glob := range []string{path.Join(set.Templates, "pages", "*.html"), set.Partials} {
		m, err := fs.Glob(fsys, glob)
		if err != nil {
			return nil, err
		}
		files = append(files, m...)
	}
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		for _, m := range templateIcon.FindAllSubmatch(b, -1) {
			seen[string(m[1])] = true
		}
	}
	for name, a := range assets.files {
		if path.Ext(name) == ".js" && set.ownsStatic(name) {
			for _, m := range scriptIcon.FindAllSubmatch(a.body, -1) {
				seen[string(m[1])] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		if _, ok := iconPaths[n]; !ok {
			return nil, fmt.Errorf("unknown icon %q", n)
		}
		out = append(out, n)
	}
	return out, nil
}
