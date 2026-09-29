#!/usr/bin/env bash
#
# Runs one VaporOS build in the vos-builder container on this Docker host:
# the builder LXC (scripts/build.sh runs it there over SSH) or, with
# BUILDER=local, the Mac itself.
#
#   build/run.sh SRC OUT KEYS
#
#   SRC   the build inputs (packages.txt build rootfs keys .build/vos),
#         mounted read-only
#   OUT   where the artifacts land
#   KEYS  a directory or Docker volume holding the dev signing key (dev.key,
#         dev.pub); debug builds create it on first use. It never leaves the
#         builder and is never in git.
#
# Environment passed on to build/build.sh: VERSION COMPRESS REFRESH VOS_DEBUG
# CHANNEL GIT VOS_ALLOW_STUB. DOCKER_PLATFORM=linux/amd64 is for Macs.
#
# The caches (/work, the pacman package cache) are the Docker volumes vos-work
# and vos-pkgcache. Two builds at once would trample them, so a build waits
# for the one before it to finish.
set -euo pipefail

(( $# == 3 )) || { echo "usage: $0 SRC OUT KEYS" >&2; exit 2; }
src=$1 out=$2 keys=$3
export VERSION COMPRESS REFRESH CHANNEL GIT
export VOS_DEBUG=${VOS_DEBUG:-1} VOS_ALLOW_STUB=${VOS_ALLOW_STUB:-0}

platform=()
[[ -z ${DOCKER_PLATFORM:-} ]] || platform=(--platform "$DOCKER_PLATFORM")

mkdir -p "$out"
if command -v flock >/dev/null; then
    exec 9>"$out/.build.lock"
    if ! flock -n 9; then
        echo "==> Waiting for the build that is already running on this builder"
        flock 9
    fi
fi

# The builder image. Docker's layer cache rebuilds it when build/Dockerfile
# changes. Once a week it is rebuilt from a freshly pulled archlinux:base as
# well (VOS_WEEK invalidates the keyring layer), as CI's is: pacstrap checks
# every package against this image's pacman keyring and fetches from its
# mirrorlists, and both go stale. Offline, the image that is there is used.
week=$(date -u +%G-W%V)
built=$(docker image inspect -f '{{ index .Config.Labels "vos.week" }}' vos-builder 2>/dev/null) || built=""
build_image() {
    docker build -q "${platform[@]}" "$@" --build-arg "VOS_WEEK=$week" --label "vos.week=$week" \
        -t vos-builder "$src/build" >/dev/null
}
if [[ $built != "$week" ]]; then
    echo "==> Refreshing the builder image ($week)"
    if ! build_image --pull; then
        docker image inspect vos-builder >/dev/null 2>&1 || { echo "error: could not build the builder image" >&2; exit 1; }
        echo "warning: could not refresh the builder image; using the one there is" >&2
    fi
else
    build_image
fi

# A host path for KEYS is created private; a volume name is Docker's to create.
if [[ $keys == /* ]]; then
    mkdir -p "$keys"
    chmod 0700 "$keys"
fi

# The dev signing key, made once per builder by the vos binary being built.
if [[ $VOS_DEBUG == 1 ]] &&
   ! docker run --rm "${platform[@]}" -v "$keys:/keys" vos-builder test -s /keys/dev.key; then
    echo "==> Creating the builder's dev signing key"
    if ! msg=$(docker run --rm "${platform[@]}" -v "$src:/src:ro" -v "$keys:/keys" \
                   vos-builder /src/.build/vos keygen --out /keys/dev 2>&1); then
        if [[ $VOS_ALLOW_STUB == 1 && $msg == *"not implemented"* ]]; then
            echo "warning: vos keygen is not implemented yet (VOS_ALLOW_STUB=1); the build is unsigned" >&2
        else
            echo "error: vos keygen failed: $msg" >&2
            exit 1
        fi
    fi
fi

exec docker run --rm --privileged "${platform[@]}" \
    -v "$src:/src:ro" \
    -v "$out:/out" \
    -v "$keys:/keys:ro" \
    -v vos-pkgcache:/var/cache/pacman/pkg \
    -v vos-work:/work \
    -e VERSION -e COMPRESS -e REFRESH -e VOS_DEBUG -e CHANNEL -e GIT -e VOS_ALLOW_STUB \
    vos-builder /src/build/build.sh
