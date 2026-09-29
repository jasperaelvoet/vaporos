#!/usr/bin/env bash
# Build VaporOS locally. Everything happens in a cached Arch container;
# all binaries come prebuilt from the Arch repos, so nothing is compiled.
#
#   ./scripts/build.sh                 incremental build -> out/
#   REFRESH=1 ./scripts/build.sh       re-pacstrap with the newest packages
#   COMPRESS=lzma,level=6 ./scripts/build.sh   smaller image for releases
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p out

docker build -q --platform linux/amd64 -t vos-builder build/ >/dev/null

# Package cache and build tree live in Docker volumes, not the macOS checkout:
# they need real Linux ownership, xattrs and overlayfs, and it keeps rebuilds
# from re-downloading anything.
docker run --rm --privileged --platform linux/amd64 \
    -v "$PWD:/src:ro" \
    -v "$PWD/out:/out" \
    -v vos-pkgcache:/var/cache/pacman/pkg \
    -v vos-work:/work \
    -e VERSION -e COMPRESS -e REFRESH \
    vos-builder /src/build/build.sh
