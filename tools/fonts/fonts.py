#!/usr/bin/env python3
"""Build VaporOS's fonts from design/fonts/fonts.json.

    python3 tools/fonts/fonts.py                      build every output and record the results
    python3 tools/fonts/fonts.py --check              rebuild in memory, compare, write nothing
    python3 tools/fonts/fonts.py --measure-fallbacks  re-measure the local fallback fonts (macOS)

The first run makes tools/fonts/.venv and installs requirements.txt there with
--require-hashes. Originals are downloaded by the URL pinned in fonts.json into
tools/fonts/.cache and used only when their sha256 matches. Nothing here runs
in the OS build: the outputs are committed.

What it does, per face in fonts.json:
  1. refuses any licence but OFL-1.1, checks the licence text is the unmodified
     OFL 1.1, and checks the Reserved Font Names the licence declares against
     the manifest (a family with one is renamed inside every output);
  2. instances the variable original at the face's wdth and wght;
  3. subsets it to Latin and writes a WOFF2 for the control center
     (internal/web/static/fonts/) and, for faces the TV draws, a static TTF with
     no hinting and no GSUB (internal/display/welcome/fonts/, beside the OFL);
  4. measures it and writes a metric-matched local() fallback face per face
     into internal/web/styles/fonts-fallback.css (the fontaine method).
It also writes the website's variable cuts (design/fonts/site/), copies the
licences into design/fonts/, writes design/fonts/SOURCES.md, and records every
output's size and sha256 in fonts.json, which the Go tests check.
"""

import argparse
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path

from licence import check_licence, check_rfn_free
from manifest import FALLBACK_CSS, MANIFEST, NAME_IDS, OUT_DIRS, PINS, ROOT, SOURCES_MD, Fail, dump, load, parse_unicodes, sha256
from text import fallback_css, sources_md

HERE = ROOT / "tools" / "fonts"
VENV = HERE / ".venv"
CACHE = HERE / ".cache"

# English letter frequencies (percent of letters) with spaces as 18% of all
# characters: the sample the fallback width is matched on.
LETTER_FREQ = {
    "a": 8.2, "b": 1.5, "c": 2.8, "d": 4.3, "e": 12.7, "f": 2.2, "g": 2.0, "h": 6.1, "i": 7.0,
    "j": 0.15, "k": 0.77, "l": 4.0, "m": 2.4, "n": 6.7, "o": 7.5, "p": 1.9, "q": 0.095, "r": 6.0,
    "s": 6.3, "t": 9.1, "u": 2.8, "v": 0.98, "w": 2.4, "x": 0.15, "y": 2.0, "z": 0.074,
}
SPACE_SHARE = 0.18

# System fonts the fallback metrics were measured from (macOS paths).
SYSTEM_FONTS = {
    "Arial": ("/System/Library/Fonts/Supplemental/Arial.ttf", 0),
    "Arial Bold": ("/System/Library/Fonts/Supplemental/Arial Bold.ttf", 0),
    "Menlo Regular": ("/System/Library/Fonts/Menlo.ttc", 0),
    "Menlo Bold": ("/System/Library/Fonts/Menlo.ttc", 1),
}

# ---- environment -----------------------------------------------------------


def bootstrap():
    """Import fontTools and brotli at the pinned versions, making the venv first if needed."""
    try:
        import brotli  # noqa: F401
        import fontTools

        if fontTools.version != PINS["fontTools"] or brotli.version != PINS["brotli"]:
            raise ImportError(f"fontTools {fontTools.version}, brotli {brotli.version}")
        return
    except ImportError as e:
        if Path(sys.prefix).resolve() == VENV.resolve():
            sys.exit(f"fonts.py: the venv has the wrong packages ({e}); delete tools/fonts/.venv and rerun")
    py = VENV / "bin" / "python"
    if not py.exists():
        print("fonts.py: making tools/fonts/.venv", file=sys.stderr)
        subprocess.run([sys.executable, "-m", "venv", str(VENV)], check=True)
        subprocess.run([str(py), "-m", "pip", "install", "--quiet", "--require-hashes", "--only-binary=:all:",
                        "--no-deps", "-r", str(HERE / "requirements.txt")], check=True)
    os.execv(str(py), [str(py), str(Path(__file__).resolve()), *sys.argv[1:]])


def fetch(url, want):
    """Return the bytes at url, from the cache when its hash matches."""
    CACHE.mkdir(parents=True, exist_ok=True)
    path = CACHE / want
    if path.exists():
        b = path.read_bytes()
        if sha256(b) == want:
            return b
    print(f"fonts.py: downloading {url}", file=sys.stderr)
    with urllib.request.urlopen(url, timeout=120) as r:
        b = r.read()
    got = sha256(b)
    if got != want:
        raise Fail(f"{url}: sha256 is {got}, fonts.json pins {want}")
    path.write_bytes(b)
    return b


# ---- building --------------------------------------------------------------


def load_source(src_bytes):
    from fontTools.ttLib import TTFont

    return TTFont(io.BytesIO(src_bytes), recalcTimestamp=False, recalcBBoxes=True)


def check_axes(fid, font, want):
    have = {a.axisTag: (a.minValue, a.maxValue) for a in font["fvar"].axes}
    for tag, v in want.items():
        lo, hi = (v, v) if isinstance(v, (int, float)) else v
        if tag not in have or lo < have[tag][0] or hi > have[tag][1]:
            raise Fail(f"{fid}: axis {tag} {v} is outside the original's {have.get(tag)}")


def instance(src_bytes, coords):
    from fontTools.varLib import instancer

    font = load_source(src_bytes)
    limits = {k: (tuple(v) if isinstance(v, list) else v) for k, v in coords.items()}
    return instancer.instantiateVariableFont(font, limits, inplace=True, updateFontNames=False)


def set_names(font, family, style):
    """Name the output after what it is: family and cut, e.g. "Anybody Cold"."""
    name = font["name"]
    version = name.getDebugName(5) or "Version 1.000"
    m = re.search(r"(\d+\.\d+)", version)
    full = family if style == "Regular" else f"{family} {style}"
    ps = re.sub(r"[^A-Za-z0-9-]", "", family.replace(" ", "") + "-" + style.replace(" ", ""))
    for nid in (1, 2, 3, 4, 6, 16, 17, 21, 22, 25):
        name.removeNames(nameID=nid)
    for nid, s in ((1, full), (2, "Regular"), (3, f"{m.group(1) if m else '1.000'};VOS;{ps}"), (4, full), (6, ps)):
        name.setName(s, nid, 3, 1, 0x409)


def subset(font, unicodes, features, flavor, drop=()):
    from fontTools import subset as fsub

    o = fsub.Options()
    o.layout_features = list(features)
    o.name_IDs = NAME_IDS
    o.name_legacy = False
    o.name_languages = [0x409]
    o.hinting = False
    o.desubroutinize = True
    o.notdef_outline = True
    o.notdef_glyph = True
    o.glyph_names = False
    o.recalc_timestamp = False
    o.drop_tables = list(o.drop_tables) + ["STAT", "DSIG", "meta", *drop]
    s = fsub.Subsetter(o)
    s.populate(unicodes=unicodes)
    s.subset(font)
    font.flavor = flavor
    buf = io.BytesIO()
    font.save(buf)
    return buf.getvalue()


def avg_width(font):
    """Frequency-weighted advance of lowercase English and the space, in font units."""
    cmap, hmtx = font.getBestCmap(), font["hmtx"]
    total = sum(LETTER_FREQ.values())
    w = 0.0
    for ch, f in LETTER_FREQ.items():
        w += hmtx[cmap[ord(ch)]][0] * f / total * (1 - SPACE_SHARE)
    return w + hmtx[cmap[0x20]][0] * SPACE_SHARE


def pct(x):
    return f"{round(x * 100, 2):g}%"


def fallback_face(font, fam, fb, fbm):
    """The fontaine method: match the average width, then the line box."""
    upm = font["head"].unitsPerEm
    hhea = font["hhea"]
    mine = avg_width(font) / upm
    theirs = fbm["avgWidth"] / fbm["unitsPerEm"]
    size = mine / theirs
    em = upm * size
    return {
        "family": f"{fam['family']} fallback",
        "local": fb["local"],
        "sizeAdjust": pct(size),
        "ascentOverride": pct(hhea.ascent / em),
        "descentOverride": pct(abs(hhea.descent) / em),
        "lineGapOverride": pct(hhea.lineGap / em),
    }


def build(m):
    """Return ({repo path: bytes}, manifest with results) for manifest m."""
    out, results, site_results = {}, [], []
    fams = m["families"]
    sources, licences = {}, {}
    for fid, fam in fams.items():
        src = fetch(fam["url"], fam["sha256"])
        lic = fetch(fam["licenseUrl"], fam["licenseSha256"])
        font = load_source(src)
        check_licence(fid, fam, lic, font)
        check_axes(fid, font, fam["axes"])
        sources[fid], licences[fid] = src, lic
        out[f"design/fonts/{fam['licenseFile']}"] = lic

    web_u = parse_unicodes(m["unicodes"]["web"])
    tv_u = parse_unicodes(m["unicodes"]["tv"])
    seen = set()
    for face in m["faces"]:
        fid = face["family"]
        fam = fams[fid]
        key = f"{face['role']}/{face['face']}"
        if key in seen:
            raise Fail(f"face {key} is listed twice")
        seen.add(key)
        coords = {"wdth": face["wdth"], "wght": face["wght"]}
        check_axes(key, load_source(sources[fid]), coords)
        res = {}
        for kind in ("web", "tv"):
            path = face.get(kind)
            if not path:
                continue
            font = instance(sources[fid], coords)
            set_names(font, fam["name"], face["style"])
            if kind == "web":
                if not path.startswith("internal/web/static/fonts/") or not path.endswith(".woff2"):
                    raise Fail(f"{key}: web must be a .woff2 in internal/web/static/fonts/")
                b = subset(font, web_u, m["features"]["web"], "woff2")
            else:
                if not path.startswith("internal/display/welcome/fonts/") or not path.endswith(".ttf"):
                    raise Fail(f"{key}: tv must be a .ttf in internal/display/welcome/fonts/")
                b = subset(font, tv_u, m["features"]["tv"], None, drop=("GSUB",))
                out[f"internal/display/welcome/fonts/{fam['licenseFile']}"] = licences[fid]
            check_rfn_free(key, fam, font)
            out[path] = b
            res[kind] = {"bytes": len(b), "sha256": sha256(b)}
        metrics_font = instance(sources[fid], coords)
        fb = m["fallbacks"][face["fallback"]]
        res["fallback"] = fallback_face(metrics_font, fam, fb, m["fallbackMetrics"][fb["metrics"]])
        results.append(res)

    for s in m.get("site", []):
        fam = fams[s["family"]]
        if not s["out"].startswith("design/fonts/site/") or not s["out"].endswith(".woff2"):
            raise Fail(f"site {s['out']}: must be a .woff2 in design/fonts/site/")
        check_axes(s["out"], load_source(sources[s["family"]]), s["axes"])
        font = instance(sources[s["family"]], s["axes"])
        set_names(font, fam["name"], s["style"])
        b = subset(font, web_u, m["features"]["web"], "woff2")
        check_rfn_free(s["out"], fam, font)
        out[s["out"]] = b
        site_results.append({"bytes": len(b), "sha256": sha256(b)})

    check_budgets(m, results)
    new = json.loads(json.dumps(m))
    for face, res in zip(new["faces"], results):
        face["result"] = res
    for s, res in zip(new.get("site", []), site_results):
        s["result"] = res
    out[FALLBACK_CSS] = fallback_css(new).encode()
    out[SOURCES_MD] = sources_md(new).encode()
    out["design/fonts/fonts.json"] = dump(new).encode()
    return out, new


def check_budgets(m, results):
    b = m["budget"]
    web = [(f, r["web"]["bytes"]) for f, r in zip(m["faces"], results) if "web" in r]
    total = sum(n for _, n in web)
    if total > b["webTotal"]:
        raise Fail(f"the web fonts total {total} bytes, budget is {b['webTotal']}")
    pre = [(f, n) for f, n in web if f.get("preload")]
    if len(pre) > b["preloads"]:
        raise Fail(f"{len(pre)} fonts are preloaded, budget is {b['preloads']}")
    for f, n in pre:
        if n > b["preloadEach"]:
            raise Fail(f"{f['web']} is preloaded at {n} bytes, budget is {b['preloadEach']}")
    tv = sum(r["tv"]["bytes"] for r in results if "tv" in r)
    if tv > b["tvTotal"]:
        raise Fail(f"the TV fonts total {tv} bytes, budget is {b['tvTotal']}")

# ---- commands --------------------------------------------------------------


def stale(out):
    extra = []
    for d, exts in OUT_DIRS.items():
        p = ROOT / d
        if not p.is_dir():
            continue
        for f in sorted(p.iterdir()):
            rel = f"{d}/{f.name}"
            if f.is_file() and f.suffix in exts and rel not in out:
                extra.append(rel)
    return extra


def measure_fallbacks(m):
    from fontTools.ttLib import TTFont

    for name, (path, num) in SYSTEM_FONTS.items():
        font = TTFont(path, fontNumber=num)
        got = font["name"].getDebugName(4)
        if got != name:
            raise Fail(f"{path}#{num} is {got!r}, not {name!r}")
        m["fallbackMetrics"][name] = {
            "unitsPerEm": font["head"].unitsPerEm,
            "avgWidth": round(avg_width(font), 2),
            "measured": f"{Path(path).name}, {font['name'].getDebugName(5)}",
        }
    MANIFEST.write_text(dump(m))
    print("fonts.py: fallbackMetrics updated; now rebuild")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--check", action="store_true", help="rebuild in memory and compare with the committed files")
    ap.add_argument("--measure-fallbacks", action="store_true", help="measure the local fallback fonts (macOS)")
    args = ap.parse_args()
    bootstrap()
    m = load()
    if args.measure_fallbacks:
        return measure_fallbacks(m)
    out, new = build(m)
    if args.check:
        bad = []
        for rel, b in sorted(out.items()):
            p = ROOT / rel
            if not p.exists():
                bad.append(f"missing: {rel}")
            elif p.read_bytes() != b:
                bad.append(f"differs: {rel}")
        bad += [f"stale: {rel}" for rel in stale(out)]
        if bad:
            print("\n".join(bad), file=sys.stderr)
            sys.exit("fonts.py --check: outputs are out of date; run python3 tools/fonts/fonts.py")
        print(f"fonts.py --check: {len(out)} outputs match")
        return
    for rel in stale(out):
        (ROOT / rel).unlink()
        print(f"removed stale {rel}")
    for rel, b in sorted(out.items()):
        p = ROOT / rel
        if p.exists() and p.read_bytes() == b:
            continue
        p.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.NamedTemporaryFile(dir=p.parent, delete=False) as t:
            t.write(b)
        os.replace(t.name, p)
        os.chmod(p, 0o644)
        print(f"wrote {rel} ({len(b):,} B)")
    web = sum(f["result"]["web"]["bytes"] for f in new["faces"] if f.get("web"))
    tv = sum(f["result"]["tv"]["bytes"] for f in new["faces"] if f.get("tv"))
    print(f"fonts.py: web {web:,} B, tv {tv:,} B")


if __name__ == "__main__":
    try:
        main()
    except Fail as e:
        sys.exit(f"fonts.py: {e}")
