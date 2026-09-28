#!/usr/bin/env bash
# Build the WaterVaporOS OCI image -- this is the operating system itself.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=${IMAGE:-localhost/watervaporos:latest}
OUT=${OUT:-out}

mkdir -p "$OUT"

# Arch is x86_64-only, so on an arm64 host this runs under emulation. The slow
# part (compiling bootc) lives in Containerfile.packages and is pulled prebuilt;
# set IMAGE_ARGS to point PACKAGES_IMAGE somewhere else.
docker build \
    --platform linux/amd64 \
    ${IMAGE_ARGS:-} \
    --tag "$IMAGE" \
    --file Containerfile \
    .

echo "==> exporting $IMAGE to $OUT/image.tar"
docker save "$IMAGE" -o "$OUT/image.tar"
ls -lh "$OUT/image.tar"
