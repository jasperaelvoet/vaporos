"""Shared paths, the manifest's JSON form and small helpers for fonts.py.

Standard library only: fonts.py imports this before its venv is ready.
"""

import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = ROOT / "design" / "fonts" / "fonts.json"
FALLBACK_CSS = "internal/web/styles/fonts-fallback.css"
SOURCES_MD = "design/fonts/SOURCES.md"
PINS = {"fontTools": "4.66.0", "brotli": "1.2.0"}

# The generated output directories. A file there that fonts.json does not
# produce is stale: the build removes it and --check reports it.
OUT_DIRS = {
    "internal/web/static/fonts": (".woff2",),
    "internal/display/welcome/fonts": (".ttf", ".txt"),
    "design/fonts/site": (".woff2",),
}

# Name records kept in every output: copyright, the names, version, and the
# licence description and URL (OFL condition 2: the notice travels with the font).
NAME_IDS = [0, 1, 2, 3, 4, 5, 6, 13, 14]


class Fail(Exception):
    """A check refused the manifest, a download or an output."""


def sha256(b):
    return hashlib.sha256(b).hexdigest()


def load():
    m = json.loads(MANIFEST.read_text())
    if m.get("schema") != 1:
        raise Fail("fonts.json: schema must be 1")
    return m


def parse_unicodes(spec):
    out = set()
    for part in spec.split(","):
        part = part.strip().upper().removeprefix("U+")
        a, _, b = part.partition("-")
        out.update(range(int(a, 16), int(b or a, 16) + 1))
    return sorted(out)


def dump(v, ind=0):
    """JSON with short scalar arrays and small scalar objects kept on one line."""
    pad, inner = "  " * ind, "  " * (ind + 1)
    scalar = lambda x: not isinstance(x, (dict, list))  # noqa: E731
    flat_ok = lambda x: scalar(x) or (isinstance(x, list) and all(map(scalar, x)))  # noqa: E731
    if isinstance(v, dict):
        if not v:
            return "{}"
        flat = "{ " + ", ".join(f"{json.dumps(k)}: {dump(x, ind + 1)}" for k, x in v.items()) + " }"
        if all(flat_ok(x) for x in v.values()) and len(flat) + len(pad) <= 100:
            return flat + ("\n" if ind == 0 else "")
        body = ",\n".join(f"{inner}{json.dumps(k)}: {dump(x, ind + 1)}" for k, x in v.items())
        return "{\n" + body + "\n" + pad + "}" + ("\n" if ind == 0 else "")
    if isinstance(v, list):
        if all(scalar(x) for x in v):
            return "[" + ", ".join(json.dumps(x, ensure_ascii=False) for x in v) + "]"
        return "[\n" + ",\n".join(inner + dump(x, ind + 1) for x in v) + "\n" + pad + "]"
    return json.dumps(v, ensure_ascii=False)
