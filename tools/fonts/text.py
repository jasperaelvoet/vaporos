"""The generated text files: the fallback faces and SOURCES.md."""

from pathlib import Path

from manifest import NAME_IDS, PINS


def fallback_css(m):
    lines = [
        "/* Code generated from design/fonts/fonts.json by tools/fonts/fonts.py. DO NOT EDIT. */",
        "/* One metric-matched local fallback per web font face (the fontaine method):",
        '   list "<family> fallback" right after "<family>" in a font stack, and until',
        "   the web font arrives the text takes the same line box and about the same",
        "   width, so the swap moves nothing. size-adjust matches the average width of",
        "   English text; the overrides then match the face's ascent, descent and gap. */",
    ]
    for face in m["faces"]:
        if not face.get("web"):
            continue
        fb = face["result"]["fallback"]
        src = ", ".join(f'local("{n}")' for n in fb["local"])
        lines += [
            "",
            "@font-face {",
            f'  font-family: "{fb["family"]}";',
            f"  src: {src};",
            f"  font-weight: {face['wght']};",
            f"  font-stretch: {face['wdth']}%;",
            "  font-style: normal;",
            f"  size-adjust: {fb['sizeAdjust']};",
            f"  ascent-override: {fb['ascentOverride']};",
            f"  descent-override: {fb['descentOverride']};",
            f"  line-gap-override: {fb['lineGapOverride']};",
            "}",
        ]
    return "\n".join(lines) + "\n"


def sources_md(m):
    fams = m["families"]
    rows = []
    for fid, f in fams.items():
        rfn = ", ".join(f'"{r}"' for r in f.get("rfn", [])) or "none"
        rows.append(f"| {f['family']} | [{Path(f['url'].replace('%5B', '[').replace('%5D', ']')).name}]({f['url']}) | `{f['sha256']}` | "
                    f"[{f['licenseFile']}]({f['licenseFile']}) `{f['licenseSha256'][:12]}…` | {rfn} | {f['name']} | [upstream]({f['upstream']}) |")
    faces = []
    for face in m["faces"]:
        r = face["result"]
        web = f"`{Path(face['web']).name}` {r['web']['bytes']:,} B" if face.get("web") else "—"
        tv = f"`{Path(face['tv']).name}` {r['tv']['bytes']:,} B" if face.get("tv") else "—"
        faces.append(f"| {face['role']} | {face['face']} | {fams[face['family']]['family']} | {face['wdth']} | {face['wght']} | {web} | {tv} | "
                     f"{'yes' if face.get('preload') else ''} |")
    site = []
    for s in m.get("site", []):
        axes = ", ".join(f"{k} {v[0]}–{v[1]}" for k, v in s["axes"].items())
        site.append(f"| `{s['out']}` | {fams[s['family']]['family']} | {axes} | {s['result']['bytes']:,} B |")
    web_total = sum(f["result"]["web"]["bytes"] for f in m["faces"] if f.get("web"))
    tv_total = sum(f["result"]["tv"]["bytes"] for f in m["faces"] if f.get("tv"))
    return "\n".join([
        "# Font sources",
        "",
        "Generated from `fonts.json` by `tools/fonts/fonts.py`; edit the manifest, not this file.",
        "",
        "Every face VaporOS ships is cut from a variable original in the Google Fonts",
        "repository, pinned to one commit by URL and sha256. The originals are not",
        "committed: `fonts.py` downloads them into `tools/fonts/.cache` and refuses any",
        "whose hash differs. All three families are under the SIL Open Font License 1.1;",
        "the script refuses any other licence and any licence text that is not the",
        "unmodified OFL 1.1.",
        "",
        "## Rebuild",
        "",
        "```sh",
        "python3 tools/fonts/fonts.py           # build, record sizes and hashes in fonts.json",
        "python3 tools/fonts/fonts.py --check   # rebuild in memory and compare; writes nothing",
        "```",
        "",
        "The first run makes `tools/fonts/.venv` with the hash-pinned `requirements.txt`",
        f"(fontTools {PINS['fontTools']}, brotli {PINS['brotli']}). Nothing here runs in the OS build.",
        "",
        "## Originals",
        "",
        "| Family | File | sha256 | Licence | Reserved Font Name | Name inside the outputs | Source |",
        "|---|---|---|---|---|---|---|",
        *rows,
        "",
        "A family with a Reserved Font Name is renamed inside every output: a subset is a",
        "Modified Version under the OFL, which may not use the reserved name. The",
        "copyright line (name ID 0) and the licence fields (13, 14) are always kept.",
        "",
        "## Faces",
        "",
        "Each face is a static instance (`fontTools.varLib.instancer`), subset with",
        "`fontTools.subset` (no hinting, desubroutinized, `.notdef` kept, name IDs",
        f"{', '.join(map(str, NAME_IDS))}, timestamps untouched so the output is byte-identical):",
        "",
        f"- **web**: WOFF2, `{m['unicodes']['web']}`, features {', '.join(m['features']['web'])}.",
        "  Served from `internal/web/static/fonts/`; the licence texts are not served (U-9).",
        f"- **tv**: TrueType, `{m['unicodes']['tv']}`, features {', '.join(m['features']['tv'])}",
        "  (GPOS pair kerning only: `x/image/font/sfnt` reads no GSUB, no hints and no",
        "  variations). Embedded from `internal/display/welcome/fonts/` beside the OFL texts.",
        "",
        "| Role | Face | Family | wdth | wght | Web | TV | Preload |",
        "|---|---|---|---|---|---|---|---|",
        *faces,
        "",
        f"Web total {web_total:,} B (budget {m['budget']['webTotal']:,}); TV total {tv_total:,} B.",
        "",
        "## Website",
        "",
        "The website travels between the cuts, so it gets a variable subset (same",
        "Latin set) that its prebuild copies next to the static files above:",
        "",
        "| File | Family | Axes | Size |",
        "|---|---|---|---|",
        *site,
        "",
        "## Fallbacks",
        "",
        "`internal/web/styles/fonts-fallback.css` holds one `local()` face per web face,",
        'in the family `"<family> fallback"`, with `size-adjust` matching the average',
        "width of English text and the ascent, descent and line-gap overrides matching",
        "the web face. The local fonts' widths are recorded in `fonts.json`",
        "(`fallbackMetrics`), measured once with `--measure-fallbacks` on macOS, so the",
        "build needs no system fonts.",
        "",
    ])
