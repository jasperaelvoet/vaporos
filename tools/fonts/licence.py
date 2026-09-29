"""Licence checks: only the unmodified SIL Open Font License 1.1 is accepted,
and a Reserved Font Name never survives into a Modified Version's names.

A subset is a Modified Version under the OFL (it deletes components), so a
family whose licence reserves a name is renamed inside every output. The
copyright line (name ID 0) and the licence fields (13, 14) always stay.
"""

import re

from manifest import Fail, sha256

# sha256 of the OFL 1.1 text from PREAMBLE to the end, whitespace collapsed to
# single spaces. Every accepted licence file must match it exactly.
OFL_11_BODY_SHA256 = "65a67a379d9157a5d3e26b95313206afe983db9743688fc98d9545c9f24bbe7d"
OFL_URLS = ("https://scripts.sil.org/OFL", "http://scripts.sil.org/OFL", "https://openfontlicense.org", "http://openfontlicense.org")


def reserved_names(text):
    """The Reserved Font Names a copyright line or licence header declares."""
    out = []
    for m in re.finditer(r"Reserved\s+Font\s+Names?\s*:?\s*((?:[\"“][^\"”]+[\"”][\s,]*(?:and\s+)?)+)", text):
        out += re.findall(r"[\"“]([^\"”]+)[\"”]", m.group(1))
    if not out and re.search(r"Reserved\s+Font\s+Name", text):
        raise Fail(f"cannot read the Reserved Font Name in {text.strip()[:120]!r}; quote it")
    return sorted(set(out))


def check_licence(fid, fam, licence_bytes, font):
    """Refuse anything but an unmodified OFL 1.1, and check the Reserved Font Names."""
    if fam.get("license") != "OFL-1.1":
        raise Fail(f"{fid}: licence {fam.get('license')!r} refused; only OFL-1.1 fonts may ship")
    text = licence_bytes.decode("utf-8")
    if "SIL OPEN FONT LICENSE Version 1.1" not in text or "PREAMBLE" not in text:
        raise Fail(f"{fid}: {fam['licenseUrl']} is not the SIL Open Font License 1.1")
    header, body = text.split("PREAMBLE", 1)
    if sha256(" ".join(("PREAMBLE" + body).split()).encode()) != OFL_11_BODY_SHA256:
        raise Fail(f"{fid}: the licence text differs from the OFL 1.1")
    name = font["name"]
    desc, url = name.getDebugName(13) or "", name.getDebugName(14) or ""
    if "SIL Open Font License, Version 1.1" not in desc or url.rstrip("/") not in OFL_URLS:
        raise Fail(f"{fid}: the font's own licence fields (name IDs 13, 14) do not name the OFL 1.1: {desc[:80]!r}, {url!r}")
    rfn = sorted(set(reserved_names(header)) | set(reserved_names(name.getDebugName(0) or "")))
    if rfn != sorted(fam.get("rfn", [])):
        raise Fail(f"{fid}: the licence reserves {rfn}, fonts.json lists {fam.get('rfn', [])}")
    for r in rfn:
        if r.lower() in fam["name"].lower():
            raise Fail(f"{fid}: name {fam['name']!r} uses the Reserved Font Name {r!r}; subsets are Modified Versions and must be renamed")


def check_rfn_free(fid, fam, font):
    """No name record but the copyright and licence may carry a Reserved Font Name."""
    for rec in font["name"].names:
        if rec.nameID in (0, 13, 14):
            continue
        s = rec.toUnicode().lower()
        for r in fam.get("rfn", []):
            if r.lower() in s:
                raise Fail(f"{fid}: name ID {rec.nameID} {rec.toUnicode()!r} still uses the Reserved Font Name {r!r}")
