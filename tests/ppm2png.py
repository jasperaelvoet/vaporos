#!/usr/bin/env python3
"""Convert a binary PPM (P6, what QEMU's screendump writes) to PNG.

  ppm2png.py IN.ppm OUT.png

The VM tests keep screendumps as a record that the display only ever shows
the logo, black or the welcome screen. PNG is what every viewer opens, and
this needs nothing but the standard library, so it runs as is on the Proxmox
host and on a CI runner.
"""
import struct, sys, zlib


def read_ppm(data):
    """Return (width, height, rgb bytes) of a P6 image with maxval <= 255."""
    fields, pos = [], 0
    # Header: magic, width, height and maxval, separated by whitespace, with
    # '#' comments running to the end of the line.
    while len(fields) < 4:
        while pos < len(data) and data[pos:pos + 1].isspace():
            pos += 1
        if data[pos:pos + 1] == b"#":
            nl = data.find(b"\n", pos)
            if nl < 0:
                raise ValueError("truncated PPM header")
            pos = nl + 1
            continue
        start = pos
        while pos < len(data) and not data[pos:pos + 1].isspace():
            pos += 1
        if start == pos:
            raise ValueError("truncated PPM header")
        fields.append(data[start:pos])
    pos += 1  # exactly one whitespace byte separates the header from the raster

    if fields[0] != b"P6":
        raise ValueError(f"not a binary PPM (magic {fields[0]!r})")
    try:
        width, height, maxval = (int(f) for f in fields[1:])
    except ValueError:
        raise ValueError("bad PPM header") from None
    if width <= 0 or height <= 0 or not 0 < maxval < 256:
        raise ValueError(f"unsupported PPM: {width}x{height}, maxval {maxval}")

    size = width * height * 3
    rgb = data[pos:pos + size]
    if len(rgb) != size:
        raise ValueError(f"PPM raster is {len(rgb)} bytes, expected {size}")
    if maxval != 255:
        rgb = rgb.translate(bytes(min(255, v * 255 // maxval) for v in range(256)))
    return width, height, rgb


def chunk(tag, body):
    return (struct.pack(">I", len(body)) + tag + body
            + struct.pack(">I", zlib.crc32(tag + body) & 0xFFFFFFFF))


def to_png(width, height, rgb):
    stride = width * 3
    # Filter type 0 (none) on every scanline; zlib does the real work.
    raw = b"".join(b"\x00" + rgb[y * stride:(y + 1) * stride] for y in range(height))
    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw, 6))
            + chunk(b"IEND", b""))


def main(argv):
    if len(argv) != 2:
        sys.exit(__doc__)
    with open(argv[0], "rb") as f:
        png = to_png(*read_ppm(f.read()))
    with open(argv[1], "wb") as f:
        f.write(png)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except (OSError, ValueError) as e:
        sys.exit(f"ppm2png: {e}")
