package web

import (
	"fmt"
	"html/template"
	"sort"
	"strings"
)

// spriteHTML returns every icon as <symbol id="i-name"> in one hidden SVG.
func spriteHTML() template.HTML {
	names := make([]string, 0, len(iconPaths))
	for n := range iconPaths {
		names = append(names, n)
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
