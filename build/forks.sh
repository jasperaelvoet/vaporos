#!/usr/bin/env bash
#
# Builds VaporOS's forked packages (forks/pkg/*: PyroWave and Sunshine with the
# PyroWave codec) and serves them as the local pacman repo [vaporos] that
# build/pacman.conf lists before every other repo. Runs inside the vos-builder
# container, as a stage of build/build.sh or on its own.
#
# Cached in /work/forks by a key over forks/: a build compiles them only when
# a patch, a PKGBUILD or a pinned commit changes. Prints the key on stdout.
#
# Environment: SRC (default /src), WORK (default /work), VOS_MEM_KIB (optional).
set -euo pipefail

SRC=${SRC:-/src}
WORK=${WORK:-/work}
FORKS=$WORK/forks
REPO=$FORKS/repo
# Built in this order: each package's own makedepends may include the ones before it.
PKGS=(pyrowave sunshine)

key=$(cd "$SRC" && { find forks -type f ! -name '*.md' -print0; printf 'build/forks.sh\0'; } |
      LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -c1-16)
if [[ -f $REPO/.key && $(<"$REPO/.key") == "$key" && -f $REPO/vaporos.db ]]; then
    echo "$key"
    exit 0
fi

log() { printf '    %s\n' "$*" >&2; }

# C++ compile jobs: the builder has more cores than memory for Sunshine's (about 1.5 GiB each).
# A container's /proc/meminfo is the host kernel's, so also take this cgroup's limit and
# VOS_MEM_KIB, the memory run.sh found outside the container (an LXC's own limit).
mem_kib=$(awk '/^MemAvailable:/ { print $2 }' /proc/meminfo)
if [[ -r /sys/fs/cgroup/memory.max && $(</sys/fs/cgroup/memory.max) =~ ^[0-9]+$ ]]; then
    cg_kib=$(( $(</sys/fs/cgroup/memory.max) / 1024 ))
    (( cg_kib >= mem_kib )) || mem_kib=$cg_kib
fi
if [[ ${VOS_MEM_KIB:-} =~ ^[0-9]+$ ]] && (( VOS_MEM_KIB < mem_kib )); then
    mem_kib=$VOS_MEM_KIB
fi
jobs=$(( mem_kib / 1572864 ))
(( jobs >= 1 )) || jobs=1
(( jobs <= $(nproc) )) || jobs=$(nproc)

log "building ${PKGS[*]} (forks $key, $jobs jobs)"
rm -rf "$FORKS"
mkdir -p "$REPO" "$FORKS/build"

# makepkg refuses to run as root; the build user owns only the build tree.
id builder >/dev/null 2>&1 || useradd -m builder
# The image's sync databases are as old as the image; refresh them (and so upgrade) first.
pacman -Syu --noconfirm --needed base-devel git >/dev/null

for pkg in "${PKGS[@]}"; do
    dir=$FORKS/build/$pkg
    cp -r "$SRC/forks/pkg/$pkg" "$dir"
    if [[ -d $SRC/forks/$pkg ]]; then
        mkdir -p "$dir/patches"
        cp "$SRC/forks/$pkg"/*.patch "$dir/patches/" 2>/dev/null || true
    fi
    chown -R builder: "$dir"

    # makedepends and depends as root, so makepkg needs no sudo. The repo's
    # own packages come from the files built just before.
    mapfile -t deps < <(cd "$dir" && bash -c 'source PKGBUILD; printf "%s\n" "${depends[@]}" "${makedepends[@]}"' | grep -v -x -F -f <(printf '%s\n' "${PKGS[@]}") || true)
    ((${#deps[@]} == 0)) || pacman -S --noconfirm --needed "${deps[@]}" >/dev/null
    for own in "${PKGS[@]}"; do
        [[ $own == "$pkg" ]] && break
        pacman -U --noconfirm --needed "$REPO"/"$own"-[0-9]*.pkg.tar.zst >/dev/null
    done

    log "makepkg $pkg"
    if ! (cd "$dir" && su builder -c "MAKEFLAGS=-j$jobs CMAKE_BUILD_PARALLEL_LEVEL=$jobs PKGDEST=$dir/out makepkg --noconfirm --nodeps --cleanbuild" >"$dir/makepkg.log" 2>&1); then
        tail -n 60 "$dir/makepkg.log" >&2
        echo "error: makepkg $pkg failed (full log: $dir/makepkg.log)" >&2
        exit 1
    fi
    cp "$dir"/out/*.pkg.tar.zst "$REPO/"
done

repo-add -q "$REPO/vaporos.db.tar.gz" "$REPO"/*.pkg.tar.zst
echo "$key" >"$REPO/.key"
echo "$key"
