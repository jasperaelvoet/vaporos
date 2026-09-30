# design/

The one source for how VaporOS looks, on three surfaces: the control center (Go
templates and Tailwind), the website (Next.js and Tailwind) and the TV welcome screen
(drawn in Go). The direction is **REDLINE**: the product seen through a thermal camera.
The box's state is its temperature on one heat scale (Matplotlib's Inferno), width is
temperature, and faults are the only cold colour.

| File | What | Owner |
|---|---|---|
| `tokens.json` | Every token: palette, ramps, colour roles, the state vocabulary, type, radii, motion, the heat fields' noise and isotherm maps, the handshake palette, the TV's type and look | redesign F1 |
| `voice.md` | What the copy may say in the REDLINE voice, and what always stays plain | redesign F1 |
| `screen-shape-vectors.json` | The screen shape's readout (`2560 × 1440 · 120 Hz`) and aspect, shared by Go and `fmt.js` | redesign F1 |
| `handshake-vectors.json` | The TV-to-phone handshake mark for a set of hostnames | redesign F1 |
| `fonts/` | Font manifest, licences and sources | redesign F3 |
| `logo.svg`, `icons/` | The mark and the UI icons | redesign F4 |

## Regenerate

Every output is written by one Go test, so `go build` never runs a generator and Go stays
the only thing compiled in the OS build:

```sh
VOS_GEN_DESIGN=1 go test ./internal/brand -run TestGenerateDesign
```

It validates `tokens.json` first and writes nothing if a check fails. Then it writes the
targets listed in `targets`:

| Target | Output | Read by |
|---|---|---|
| `tv` | `internal/brand/tokens_gen.go` | the TV renderer (`brand.StyleOf`, `brand.TV*`, `brand.HeatRamp`), `internal/web` (theme colours) |
| `web` | `internal/web/styles/tokens.css`, `design/heat-vectors.json` | the control center's Tailwind entry, `styles/app.css`; the heat fields' tables, which `internal/web/jstest/heatmap.test.mjs` holds the painter to |
| `site` | `website/src/styles/tokens.gen.css`, `website/src/lib/tokens.gen.ts` | the website's `globals.css`; canvas, WebGL, GSAP and the TV mock |
| `web-icons` | (F4) the app icons, manifest and logo partials | |

`go test ./internal/brand` (and so `go test -race ./...` in CI) runs
`TestDesignOutputsFresh`, which regenerates in memory and fails on any committed output
that differs, printing the command above. Never edit a generated file by hand.

## Using the tokens

**Tailwind utilities** (both CSS targets): `bg-canvas`, `bg-surface`, `text-ink-2`,
`border-line`, `bg-accent text-accent-ink`, the raw palette (`bg-soot`, `text-bone`,
`text-ink-ready`), the ramps (`bg-h0` … `bg-h9`, `bg-c0` … `bg-c9`), `font-display`,
`font-ui`, `font-mono`, `text-2xs` … `text-hero`, `rounded-xs` … `rounded-pill`,
`p-gutter`, `min-h-target` (44 px), `rail:` (64rem, the side rail), `ease-standard`,
`ease-sheet`, `ease-tach` and `ease-width` (the springs as `linear()`). Tailwind's stock
colours and fonts are reset, so only these exist.

**State.** Put `data-state` on an element (`ready`, `streaming`, `updating`,
`restart-needed`, `asleep`, `fault`, `installing`, or `""` for neutral) and read:

| Variable | Meaning |
|---|---|
| `--state`, `--state-ink`, `--state-soft` | the signal colour, text on it, a tint for large areas (`bg-(--state) text-(--state-ink)`) |
| `--state-heat` | position on the heat scale, 0 to 1 (the needle, the field's source opacity) |
| `--state-cut-w`, `--state-cut-g` | the state word's cut: `font-stretch` and `font-weight` |
| `--state-from` | the `scaleX` the state word stretches from |

`data-attention="pair"` overrides them while a device waits for its PIN. The heat fields
are canvases that `internal/web/static/js/ui/thermal.js` paints (in a Worker, with
`heatmap.js`) from the ramps, `--vos-heat-air` and `--vos-heat-bands`; the fault's cold
map is the `c` ramp.

**Plain `:root` values** that scripts read: `--vos-dur-*` (every duration), `--hold-ms`
(1200 ms) with `--hold-release-ms`, `--hold-fire-ms` and `--hold-nudge-ms`,
`--vos-pattern-pip-ms`, `--vos-scene-cool-ms` and `--vos-scene-heat-ms`, the heat
fields' `--vos-heat-air` (`fx fy octaves seed scale`) and `--vos-heat-bands` (`bands sub
line`), the cuts
`--cut-{cold,warm,hot}-{w,g}`, and `--vos-tokens` (the tokens hash).

**Go.** `brand.StyleOf(state)` gives the label, colours, motion pattern, heat, cut and the
TV field's reach and peak; `brand.ParseState` maps unknown strings to neutral.
`brand.ModeLabel(mode, hdr)` formats a mode, `brand.ShapeAspect(w, h)` clamps the screen
shape, and `brand.HandshakeFor(hostname)` gives the handshake mark and its `SVG()`.
`internal/brand` imports nothing from this repository.

## Schema (`schema: 1`)

Keys are exact: an unknown or misspelled key fails. Colours are lowercase `#rrggbb` (or
`#rrggbbaa`), or the name of a palette colour or ramp stop.

- `direction`: `redline`. `targets`: the outputs to write and check. `modes`: `web` and
  `site` list `dark` and/or `light`, `tv` is `["tv"]`. REDLINE is dark only, so there is no
  theme switch (`theme.switch: false`) and both theme colours are the dark canvas.
- `palette`: named colours with their use. `ramp`: `heat` (`h0`–`h9`) and `cold`
  (`c0`–`c9`), named by `prefix` + index.
- `color`: the roles every surface uses (`canvas`, `canvas-2`, `surface`, `surface-2`,
  `line`, `line-strong`, `ink`, `ink-2`, `ink-3`, `accent`, `accent-ink`, `focus`,
  `danger`, `danger-ink`), per mode; `tv` falls back to `dark`.
- `state`: exactly the seven states in canonical order, then `neutral` and
  `attention.pair`. Each has a sentence-case `label` (≤ 16 characters), `color`, `ink`,
  `soft`, a `motion` pattern and its `reduced` form (`steady` or `none`), a lower-case
  `signage` word, and a `look`: `heat` (0..1), `cut` (a display face), `from`, `map`
  (`heat` or `cold`), and the TV field's `reach` and `peak`. A staged update is `ready`.
- `contrast`: rules measured with WCAG 2.x luminance in every mode. `state.*.<part>`
  expands to every state (and neutral and pairing), paired state by state when both
  sides use it. Alpha is composited over the canvas. The rules include focus on
  every ground at 3:1 and ink on every state tint at 4.5:1.
- `font`: `display` (Anybody: cold, warm and hot cuts), `ui` (Mona Sans), `mono`
  (Martian Mono), each with fallbacks and static faces. F3 adds each face's `web`
  (WOFF2) and `tv` (TTF) file; the generator then writes `@font-face` rules and
  `brand.TVFontFiles`.
- `text`, `radius`, `spacing`, `breakpoint`: Tailwind scales.
- `motion`: `duration` (0–2000 ms), `ease` (`cubic-bezier()` or `linear()`), `spring`
  (checked against its `linear()` ease), `pattern` (flash rate ≤ 3 Hz), `hold`, `scene`.
  Heat changes are asymmetric: 600 ms to heat, 1400 ms to cool.
- `filter`: the heat fields' noise (the SVG spec's `feTurbulence`, which the painter ports;
  `blur` is unused since the fields became canvases) and the discrete isotherm tables for
  the heat and cold maps. Every map shares one `bands`, `sub` and `line`.
- `handshake`: the `on` and `ground` colours of the handshake mark (every pair ≥ 3:1).
- `tv`: reference canvases, title-safe inset, the type scale in reference pixels, the
  QR card (luma and ≥ 12:1 checked), the look and its parameters. `labels.tv`: the
  renderer's own copy.
