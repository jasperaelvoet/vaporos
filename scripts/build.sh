#!/usr/bin/env bash
# Build VaporOS. The one thing compiled is the Go control plane, `vos`
# (static, linux/amd64, here on the Mac, in seconds); everything else comes
# prebuilt from the CachyOS and Arch repos and is assembled in a cached Arch
# container (build/run.sh, build/build.sh).
#
# The image is built from x86-64-v3 packages, which only run on a CPU that has
# AVX2 & co. (Rosetta on a Mac does not), so by default the container runs in
# a Docker LXC on the Proxmox host ("the builder"), created on first use. The
# result lands on the Proxmox host, next to the VM that uses it, and is pulled
# back into out/.
#
#   ./scripts/build.sh                  incremental dev build -> out/
#   REFRESH=1 ./scripts/build.sh        re-pacstrap with the newest packages
#   VOS_DEBUG=0 ./scripts/build.sh      release image: no serial console, zstd,
#                                       unsigned, fails over the ISO size limit
#   COMPRESS=zstd,level=15 ./scripts/build.sh   any mkfs.erofs -z value
#   VOS_ALLOW_STUB=1 ./scripts/build.sh tolerate vos subcommands that are
#                                       still stubs ("not implemented")
#   BUILDER=local ./scripts/build.sh    use this machine's Docker (needs x86-64-v3)
#   ./scripts/build.sh clean            empty the builder's caches
#
# Debug builds (the default here) are signed with a dev key that lives on the
# builder only (BUILD_DIR/keys), made by `vos keygen` on first use; the image
# trusts it, so the dev VM accepts `vos update` from these builds.
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

say()  { printf '\e[1;34m==>\e[0m \e[1m%s\e[0m\n' "$*"; }
die()  { printf '\e[1;31merror:\e[0m %s\n' "$*" >&2; exit 1; }
pve()  { ssh -o LogLevel=ERROR "$PVE_USER@$PVE_HOST" "$@"; }

# `build.sh clean`: drop the builder's package cache, build tree and output.
# (The dev signing key in BUILD_DIR/keys stays.)
if [[ ${1:-} == clean ]]; then
    if [[ $BUILDER == local ]]; then
        docker volume rm -f vos-work vos-pkgcache >/dev/null
    else
        pve "pct status $BUILDER_ID >/dev/null 2>&1 && pct exec $BUILDER_ID -- docker volume rm -f vos-work vos-pkgcache >/dev/null; rm -rf $BUILD_DIR/out"
    fi
    exit
fi

# ------------------------------------------------------------ versions ----

# One instant names the build: the image version, the vos binary's version
# and (as unix seconds) the manifest's rollback_index.
export VERSION=${VERSION:-$(date -u +%Y%m%d.%H%M%S)}
export VOS_DEBUG=${VOS_DEBUG:-1}
export CHANNEL=${CHANNEL:-dev}
export COMPRESS=${COMPRESS:-} REFRESH=${REFRESH:-} VOS_ALLOW_STUB=${VOS_ALLOW_STUB:-0}
commit=$(git rev-parse HEAD 2>/dev/null || echo unknown)
short=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
if [[ -n $(git status --porcelain 2>/dev/null) ]]; then
    commit+=-dirty short+=-dirty
fi
export GIT=$commit

# ------------------------------------------------------------ vos (Go) ----

command -v go >/dev/null || die "Go is not installed (brew install go)"
say "Building vos $VERSION ($short)"
mkdir -p .build out
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v3 go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION -X main.commit=$short" \
    -o .build/vos ./cmd/vos

if [[ $BUILDER == local ]]; then
    rm -f out/.built-on
    # Package cache, build tree and dev key live in Docker volumes, not the
    # checkout: they need real Linux ownership, xattrs and overlayfs, and it
    # keeps rebuilds from re-downloading anything.
    export DOCKER_PLATFORM=linux/amd64
    exec build/run.sh "$PWD" "$PWD/out" vos-keys
fi

# ------------------------------------------------------------- builder ----

# A privileged Debian LXC with Docker. Privileged, with AppArmor off inside,
# because the build container itself runs --privileged: pacstrap and the
# rootfs overlay need real mounts, and the image checks loop-mount the result.
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

# (Fetching the result reads the extension images' names from its manifest.)
command -v jq >/dev/null || die "jq is not installed (brew install jq)"
ensure_builder
rm -f out/.built-on

# keys/ holds the release public key; .build/ the vos binary.
rsync -rlpt --delete --exclude .DS_Store packages.txt build rootfs extensions keys .build \
    "$PVE_USER@$PVE_HOST:$BUILD_DIR/src/"

env_args=
for var in VERSION COMPRESS REFRESH VOS_DEBUG CHANNEL GIT VOS_ALLOW_STUB; do
    env_args+=" $var=$(printf %q "${!var}")"
done
pve "pct exec $BUILDER_ID -- env$env_args bash /vos/src/build/run.sh /vos/src /vos/out /vos/keys"

# Pull the result back. The builder's manifest.json names this build's
# extension images: it comes first, and nothing in out/ changes until it is
# read. Consecutive builds share most of their bytes, so rsync against the
# previous build: rename the old ISO to the new name first, since the name
# carries the version.
say "Fetching the build into out/"
rsync -t "$PVE_USER@$PVE_HOST:$BUILD_DIR/out/manifest.json" out/.manifest.json.part ||
    die "cannot fetch the builder's manifest.json"
ext=$(jq -r '.extensions // {} | .[].name' out/.manifest.json.part) ||
    die "the builder's manifest.json cannot be read"
for f in $ext; do
    [[ $f =~ ^ext-[a-z][a-z0-9-]{0,31}\.raw$ ]] || die "the builder's manifest.json names an extension image '$f'"
done
sig=$(pve "if test -f $BUILD_DIR/out/manifest.json.sig; then echo signed; fi") ||
    die "cannot reach $PVE_HOST to fetch the build"
iso=vaporos-$VERSION.iso
old=$(ls out/vaporos-*.iso 2>/dev/null | head -1) || true
[[ -z $old || $old == "out/$iso" ]] || mv "$old" "out/$iso"
rm -f out/manifest.env out/manifest.json out/manifest.json.sig
# The extension images: their names carry no version, so rsync deltas
# against the last build's as is; only those of extensions this build no
# longer has go.
for f in out/ext-*.raw; do
    [[ -e $f ]] || continue
    case $'\n'$ext$'\n' in
        *$'\n'"${f#out/}"$'\n'*) ;;
        *) rm -f "$f" ;;
    esac
done
for f in root.erofs root.erofs.idx vmlinuz initramfs.img $ext "$iso"; do
    rsync -t --inplace --partial "$PVE_USER@$PVE_HOST:$BUILD_DIR/out/$f" "out/$f"
done
# The manifest last, so out/ never pairs a new one with older files.
mv out/.manifest.json.part out/manifest.json
if [[ $sig == signed ]]; then
    rsync -t "$PVE_USER@$PVE_HOST:$BUILD_DIR/out/manifest.json.sig" out/manifest.json.sig
fi
find out -name 'vaporos-*.iso' ! -name "$iso" -delete
# dev.sh copies from here on the Proxmox host instead of uploading out/.
echo "$BUILD_DIR/out" >out/.built-on
