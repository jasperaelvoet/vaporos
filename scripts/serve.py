#!/usr/bin/env python3
"""Serve a directory over HTTP, with byte ranges, for `vos update --from`.

    serve.py DIR ADDRESS PORT

`python3 -m http.server` answers a Range request with the whole file, so an
update from it falls back to downloading the whole image; this one answers
with 206, as ghcr.io does (docs/CONTRACTS.md "Block index").
"""
import functools
import http.server
import os
import re
import sys


class Handler(http.server.SimpleHTTPRequestHandler):
    def send_head(self):
        self.left = None
        m = re.fullmatch(r"bytes=(\d+)-(\d*)", self.headers.get("Range", ""))
        path = self.translate_path(self.path)
        if not m or not os.path.isfile(path):
            return super().send_head()
        size = os.path.getsize(path)
        start = int(m[1])
        end = min(int(m[2]) if m[2] else size - 1, size - 1)
        if start > end:
            self.send_response(416)
            self.send_header("Content-Range", f"bytes */{size}")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return None
        f = open(path, "rb")
        f.seek(start)
        self.left = end - start + 1
        self.send_response(206)
        self.send_header("Content-Type", self.guess_type(path))
        self.send_header("Content-Range", f"bytes {start}-{end}/{size}")
        self.send_header("Content-Length", str(self.left))
        self.end_headers()
        return f

    def copyfile(self, source, outputfile):
        if self.left is None:
            return super().copyfile(source, outputfile)
        while self.left > 0:
            b = source.read(min(self.left, 1 << 20))
            if not b:
                break
            outputfile.write(b)
            self.left -= len(b)


def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__.strip().splitlines()[2].strip())
    directory, address, port = sys.argv[1:]
    handler = functools.partial(Handler, directory=directory)
    http.server.ThreadingHTTPServer((address, int(port)), handler).serve_forever()


if __name__ == "__main__":
    main()
