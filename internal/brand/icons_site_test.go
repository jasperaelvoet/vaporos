package brand

import (
	"fmt"
	"strings"
)

// The site-icons target: the website's favicon, touch and manifest icons (the
// site is not held to the device's icon budget) and the mark as data.

func init() { iconTargets = append(iconTargets, iconTarget{"site-icons", siteIcons}) }

func siteIcons(_ *tokensFile, syms map[string]*logoSym) ([]output, []string, error) {
	icon := syms["icon"].drawing()
	const pub = "website/public/"
	return []output{
		{"site-icons", pub + "favicon.svg", []byte(svgFile(syms["icon-small"]))},
		{"site-icons", pub + "apple-touch-icon.png", encodePNG(RenderIcon(icon, 180, true))},
		{"site-icons", pub + "icon-192.png", encodePNG(RenderIcon(icon, 192, false))},
		{"site-icons", pub + "icon-512.png", encodePNG(RenderIcon(icon, 512, false))},
		{"site-icons", pub + "icon-maskable-512.png", encodePNG(RenderIcon(icon, 512, true))},
		{"site-icons", "website/src/lib/logo.gen.ts", []byte(logoTS(syms))},
	}, nil, nil
}

// ---- website/src/lib/logo.gen.ts

func logoTS(syms map[string]*logoSym) string {
	var b strings.Builder
	b.WriteString(`// Code generated from design/logo.svg by go test ./internal/brand (VOS_GEN_DESIGN=1). DO NOT EDIT.
// The VaporOS mark as data, for <LogoMark> and friends: every drawing's paths with what they
// paint. Strokes are round-capped and round-joined; fills are nonzero. "currentColor" takes
// the element's colour. In the app icons, group names the tile, the field and the glyph.

export type LogoPath = {
  readonly d: string;
  readonly fill?: string;
  readonly stroke?: string;
  readonly strokeWidth?: number;
  readonly className?: string;
  readonly group?: "tile" | "field" | "glyph";
};

export type LogoDrawing = { readonly viewBox: string; readonly paths: readonly LogoPath[] };

export const logo = {
`)
	for _, ld := range logoDrawings {
		s := syms[ld.id]
		fmt.Fprintf(&b, "  // %s\n  %s: {\n    viewBox: %s,\n    paths: [\n", ld.doc, ld.tsName, tsString(vbAttr(s)))
		for _, p := range s.Paths {
			f := []string{"d: " + tsString(p.D)}
			if p.Fill != "none" {
				f = append(f, "fill: "+tsString(p.Fill))
			}
			if p.Stroke != "" && p.Stroke != "none" {
				f = append(f, "stroke: "+tsString(p.Stroke), "strokeWidth: "+p.Width)
			}
			if p.Class != "" {
				f = append(f, "className: "+tsString(p.Class))
			}
			if p.Group != "" {
				f = append(f, "group: "+tsString(p.Group))
			}
			fmt.Fprintf(&b, "      { %s },\n", strings.Join(f, ", "))
		}
		b.WriteString("    ],\n  },\n")
	}
	b.WriteString("} as const satisfies Record<string, LogoDrawing>;\n\nexport type LogoName = keyof typeof logo;\n")
	return b.String()
}
