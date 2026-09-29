# Font sources

Generated from `fonts.json` by `tools/fonts/fonts.py`; edit the manifest, not this file.

Every face VaporOS ships is cut from a variable original in the Google Fonts
repository, pinned to one commit by URL and sha256. The originals are not
committed: `fonts.py` downloads them into `tools/fonts/.cache` and refuses any
whose hash differs. All three families are under the SIL Open Font License 1.1;
the script refuses any other licence and any licence text that is not the
unmodified OFL 1.1.

## Rebuild

```sh
python3 tools/fonts/fonts.py           # build, record sizes and hashes in fonts.json
python3 tools/fonts/fonts.py --check   # rebuild in memory and compare; writes nothing
```

The first run makes `tools/fonts/.venv` with the hash-pinned `requirements.txt`
(fontTools 4.66.0, brotli 1.2.0). Nothing here runs in the OS build.

## Originals

| Family | File | sha256 | Licence | Reserved Font Name | Name inside the outputs | Source |
|---|---|---|---|---|---|---|
| Anybody | [Anybody[wdth,wght].ttf](https://raw.githubusercontent.com/google/fonts/23e54b51ddffbc7713c583748e3bd86f62b1fa4a/ofl/anybody/Anybody%5Bwdth,wght%5D.ttf) | `b184bd7e6ca8348bbaecec98951565729d7e89b7872d4898a1f9981342b5b64c` | [OFL-Anybody.txt](OFL-Anybody.txt) `7f0313b042b4…` | none | Anybody | [upstream](https://github.com/Etcetera-Type-Co/Anybody) |
| Martian Mono | [MartianMono[wdth,wght].ttf](https://raw.githubusercontent.com/google/fonts/23e54b51ddffbc7713c583748e3bd86f62b1fa4a/ofl/martianmono/MartianMono%5Bwdth,wght%5D.ttf) | `c3467843ec1c2574b05fbcfd7147c7bfbcf63ddca8fc2bcb9d117f1bfb1b22e7` | [OFL-MartianMono.txt](OFL-MartianMono.txt) `ddafd2c3f37e…` | none | Martian Mono | [upstream](https://github.com/evilmartians/mono) |
| Mona Sans | [MonaSans[wdth,wght].ttf](https://raw.githubusercontent.com/google/fonts/23e54b51ddffbc7713c583748e3bd86f62b1fa4a/ofl/monasans/MonaSans%5Bwdth,wght%5D.ttf) | `fd6e79634b5ae804a45aac7e2e3c2a325b41291fba59034f4732b0135b8475b3` | [OFL-MonaSans.txt](OFL-MonaSans.txt) `9261dcb61fb5…` | "Mona" | VOS UI | [upstream](https://github.com/github/mona-sans) |

A family with a Reserved Font Name is renamed inside every output: a subset is a
Modified Version under the OFL, which may not use the reserved name. The
copyright line (name ID 0) and the licence fields (13, 14) are always kept.

## Faces

Each face is a static instance (`fontTools.varLib.instancer`), subset with
`fontTools.subset` (no hinting, desubroutinized, `.notdef` kept, name IDs
0, 1, 2, 3, 4, 5, 6, 13, 14, timestamps untouched so the output is byte-identical):

- **web**: WOFF2, `U+0020-007E,U+00A0-00FF,U+0131,U+0152-0153,U+02C6,U+02DA,U+02DC,U+2013-2014,U+2018-201A,U+201C-201E,U+2022,U+2026,U+2039-203A,U+20AC,U+2190-2193,U+2212,U+2713,U+2715`, features ccmp, locl, kern, liga, calt, tnum, case.
  Served from `internal/web/static/fonts/`; the licence texts are not served (U-9).
- **tv**: TrueType, `U+0020-007E,U+00A0-017F,U+2013-2014,U+2018-2019,U+201C-201D,U+2022,U+2026`, features kern
  (GPOS pair kerning only: `x/image/font/sfnt` reads no GSUB, no hints and no
  variations). Embedded from `internal/display/welcome/fonts/` beside the OFL texts.

| Role | Face | Family | wdth | wght | Web | TV | Preload |
|---|---|---|---|---|---|---|---|
| display | cold | Anybody | 72 | 560 | `anybody-cold.woff2` 10,064 B | `anybody-cold.ttf` 24,772 B |  |
| display | warm | Anybody | 100 | 700 | `anybody-warm.woff2` 10,308 B | `anybody-warm.ttf` 25,268 B | yes |
| display | hot | Anybody | 124 | 800 | `anybody-hot.woff2` 10,380 B | `anybody-hot.ttf` 25,668 B |  |
| ui | regular | Mona Sans | 100 | 400 | `monasans-regular.woff2` 19,120 B | `monasans-regular.ttf` 45,388 B | yes |
| ui | semibold | Mona Sans | 100 | 600 | `monasans-semibold.woff2` 19,420 B | — |  |
| mono | medium | Martian Mono | 100 | 500 | `martianmono-medium.woff2` 8,556 B | `martianmono-medium.ttf` 23,508 B |  |
| mono | semibold | Martian Mono | 100 | 600 | `martianmono-semibold.woff2` 8,568 B | `martianmono-semibold.ttf` 23,520 B |  |

Web total 86,416 B (budget 120,000); TV total 168,124 B.

## Website

The website travels between the cuts, so it gets a variable subset (same
Latin set) that its prebuild copies next to the static files above:

| File | Family | Axes | Size |
|---|---|---|---|
| `design/fonts/site/anybody-variable.woff2` | Anybody | wdth 56–136, wght 300–860 | 48,900 B |

## Fallbacks

`internal/web/styles/fonts-fallback.css` holds one `local()` face per web face,
in the family `"<family> fallback"`, with `size-adjust` matching the average
width of English text and the ascent, descent and line-gap overrides matching
the web face. The local fonts' widths are recorded in `fonts.json`
(`fallbackMetrics`), measured once with `--measure-fallbacks` on macOS, so the
build needs no system fonts.
