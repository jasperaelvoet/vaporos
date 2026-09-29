#!/usr/bin/env bash
# Build VaporOS. Everything happens in a cached Arch container; all binaries
# come prebuilt from the CachyOS and Arch repos, so nothing is compiled.
#
# The image is built from x86-64-v3 packages, which only run on a CPU that has
# AVX2 & co. (Rosetta on a Mac does not), so by default the container runs in
# a Docker LXC on the Proxmox host ("the builder"), created on first use. The
# result lands on the Proxmox host, next to the VM that uses it, and is pulled
# back into out/.
#
#   ./scripts/build.sh                 incremental build -> out/
#   REFRESH=1 ./scripts/build.sh       re-pacstrap with the newest packages
#   COMPRESS=lzma,level=6 ./scripts/build.sh   smaller image for releases
#   BUILDER=local ./scripts/build.sh   use this machine's Docker (needs x86-64-v3)
#   ./scripts/build.sh clean           empty the builder's caches
set -euo pipefail
cd "$(dirname "$0")/.."
[[ -f .dev.env ]] && . ./.dev.env

BUILDER=${BUILDER:-pve}
PVE_HOST=${PVE_HOST:-192.168.1.2}
PVE_USER=${PVE_USER:-root}
BUILDER_ID=${BUILDER_ID:-9010}
BUILDER_NAME=vaporos-builder
DISK_STORAGE=${DISK_STORAGE:-local-lvm}
BRIDGE=${BRIDGE:-vmbr0}
# On the Proxmox host; bind-mounted into the builder at /vos.
BUILD_DIR=${BUILD_DIR:-/var/lib/vos-build}

mkdir -p out

say()  { printf '\e[1;34m==>\e[0m \e[1m%s\e[0m\n' "$*"; }
die()  { printf '\e[1;31merror:\e[0m %s\n' "$*" >&2; exit 1; }
pve()  { ssh -o LogLevel=ERROR "$PVE_USER@$PVE_HOST" "$@"; }

if [[ $BUILDER == local ]]; then
    rm -f out/.built-on
    docker build -q --platform linux/amd64 -t vos-builder build/ >/dev/null
    # Package cache and build tree live in Docker volumes, not the checkout:
    # they need real Linux ownership, xattrs and overlayfs, and it keeps
    # rebuilds from re-downloading anything.
    exec docker run --rm --privileged --platform linux/amd64 \
        -v "$PWD:/src:ro" \
        -v "$PWD/out:/out" \
        -v vos-pkgcache:/var/cache/pacman/pkg \
        -v vos-work:/work \
        -e VERSION -e COMPRESS -e REFRESH \
        vos-builder /src/build/build.sh
fi

# ------------------------------------------------------------- builder ----

# A privileged Debian LXC with Docker. Privileged, with AppArmor off inside,
# because the build container itself runs --privileged: pacstrap and the
# rootfs overlay need real mounts.
create_builder() {
    local tmpl
    say "Creating the builder LXC $BUILDER_ID ($BUILDER_NAME) on $PVE_HOST"
    tmpl=$(pve "pveam list local | awk '/debian-13-standard_.*_amd64/ {print \$1}' | sort -V | tail -1")
    if [[ -z $tmpl ]]; then
        tmpl=$(pve "pveam update >/dev/null; pveam available --section system |
                    awk '/debian-13-standard_.*_amd64/ {print \$2}' | sort -V | tail -1")
        [[ -n $tmpl ]] || die "no Debian 13 LXC template available on $PVE_HOST"
        pve "pveam download local $tmpl >/dev/null"
        tmpl=local:vztmpl/$tmpl
    fi
    pve "mkdir -p $BUILD_DIR &&
         pct create $BUILDER_ID $tmpl \
            --hostname $BUILDER_NAME \
            --unprivileged 0 --features nesting=1 \
            --cores \$(nproc) --memory 4096 --swap 1024 \
            --rootfs $DISK_STORAGE:40 \
            --net0 name=eth0,bridge=$BRIDGE,ip=dhcp \
            --mp0 $BUILD_DIR,mp=/vos \
            --onboot 0 >/dev/null &&
         printf '%s\n' 'lxc.apparmor.profile: unconfined' 'lxc.cgroup2.devices.allow: a' 'lxc.cap.drop:' \
            >>/etc/pve/lxc/$BUILDER_ID.conf &&
         pct start $BUILDER_ID"
    say "Installing Docker in the builder"
    pve "pct exec $BUILDER_ID -- bash -c '
            for i in \$(seq 60); do getent hosts deb.debian.org >/dev/null && break; sleep 1; done
            export DEBIAN_FRONTEND=noninteractive LC_ALL=C
            apt-get -qq update && apt-get -qq install -y docker.io rsync >/dev/null'" ||
        die "could not install Docker in the builder (pct enter $BUILDER_ID on $PVE_HOST to look)"
}

ensure_builder() {
    local name
    name=$(pve "pct config $BUILDER_ID 2>/dev/null | sed -n 's/^hostname: //p'") || true
    [[ -z $name || $name == "$BUILDER_NAME" ]] ||
        die "CT $BUILDER_ID is '$name', not '$BUILDER_NAME' -- refusing to touch it (set BUILDER_ID)"
    if [[ -z $name ]]; then
        create_builder
    elif ! pve "pct status $BUILDER_ID | grep -q running"; then
        pve "pct start $BUILDER_ID"
    fi
}

# ---------------------------------------------------------------- build ----

# `build.sh clean`: drop the builder's package cache, build tree and output.
if [[ ${1:-} == clean ]]; then
    pve "pct status $BUILDER_ID >/dev/null 2>&1 && pct exec $BUILDER_ID -- docker volume rm -f vos-work vos-pkgcache >/dev/null; rm -rf $BUILD_DIR/out"
    exit
fi

ensure_builder
rm -f out/.built-on

rsync -rlpt --delete --exclude .DS_Store packages.txt build rootfs \
    "$PVE_USER@$PVE_HOST:$BUILD_DIR/src/"

pve "pct exec $BUILDER_ID -- env VERSION='${VERSION:-}' COMPRESS='${COMPRESS:-}' REFRESH='${REFRESH:-}' bash -c '
        set -e
        mkdir -p /vos/out
        docker build -q -t vos-builder /vos/src/build >/dev/null
        docker run --rm --privileged \
            -v /vos/src:/src:ro \
            -v /vos/out:/out \
            -v vos-pkgcache:/var/cache/pacman/pkg \
            -v vos-work:/work \
            -e VERSION -e COMPRESS -e REFRESH \
            vos-builder /src/build/build.sh'"

# Pull the result back. Consecutive builds share most of their bytes, so
# rsync against the previous build: rename the old ISO to the new name first,
# since the name carries the version.
say "Fetching the build into out/"
iso=$(pve "cd $BUILD_DIR/out && ls vaporos-*.iso")
old=$(ls out/vaporos-*.iso 2>/dev/null | head -1) || true
[[ -z $old || $old == "out/$iso" ]] || mv "$old" "out/$iso"
for f in root.erofs vmlinuz initramfs.img manifest.env "$iso"; do
    rsync -t --inplace --partial "$PVE_USER@$PVE_HOST:$BUILD_DIR/out/$f" "out/$f"
done
find out -name 'vaporos-*.iso' ! -name "$iso" -delete
# dev.sh copies from here on the Proxmox host instead of uploading out/.
echo "$BUILD_DIR/out" >out/.built-on
