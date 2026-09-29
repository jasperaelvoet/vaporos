#!/usr/bin/env bash
#
# Runs inside the vos-builder container. Produces, in /out:
#   root.erofs              the OS: one read-only image with kernel modules + userspace
#   vmlinuz                 the kernel that matches it
#   initramfs.img           the initramfs with the vos hook
#   manifest.json           version, kernel cmdline, checksums: what `vos update` reads
#   manifest.json.sig       its ed25519 signature (when a signing key is available)
#   vaporos-<version>.iso   live system + web installer, UEFI, USB/CD hybrid
#
# Environment:
#   VERSION          YYYYMMDD.HHMMSS, UTC (default: now). rollback_index is the
#                    same instant in unix seconds, so it only ever grows.
#   CHANNEL          the update channel the image follows by default (default: dev)
#   GIT              the commit the image is built from
#   VOS_DEBUG        1: dev image -- root autologin on the serial console, trusts
#                    and is signed with the builder's dev key, fast compression.
#                    0 (default): release image.
#   COMPRESS         mkfs.erofs compressor (default: lz4hc,9 for debug builds,
#                    zstd,level=9 for release builds)
#   EROFS_PCLUSTER   physical cluster size for zstd/lzma (default: 262144)
#   REFRESH=1        re-pacstrap even though packages.txt and pacman.conf are unchanged
#   VOS_SIGNING_KEY  base64 ed25519 private key; if set, signs the manifest
#                    (release builds; debug builds use /keys/dev.key). Only
#                    `vos sign` gets to see it.
#   VOS_ALLOW_STUB=1 tolerate `vos edid generate` and `vos sign` reporting
#                    "not implemented" (while those Go subcommands are stubs)
#
# Mounts: /src (the repo, read-only; /src/.build/vos is the linux/amd64 vos
# binary), /out, /work and /var/cache/pacman/pkg (caches), and optionally /keys
# (dev.key + dev.pub, the builder's dev signing key).
#
# Stages, and what makes them fast:
#   base      pacstrap of packages.txt. Cached in /work/base and reused until
#             packages.txt or pacman.conf changes (or REFRESH=1).
#   rootfs    an overlayfs on top of base with our files and configuration.
#             Thrown away every build, so it costs seconds.
#   images    initramfs (cached on kernel, package set and hook), erofs, ISO.
#   checks    the finished erofs is mounted read-only and inspected; a bad
#             image never reaches /out.
set -euo pipefail

# The release signing key goes to `vos sign` and nothing else: out of the
# environment with it before anything runs, or every package scriptlet
# pacstrap runs (and mkinitcpio, locale-gen, ...) could read it.
SIGNING_KEY=${VOS_SIGNING_KEY:-}
unset VOS_SIGNING_KEY

SRC=/src
# shellcheck source=build/lib.sh
. "$SRC/build/lib.sh"
WORK=/work
OUT=/out
KEYS=/keys
VOS=$SRC/.build/vos
ROOT=$WORK/rootfs
CHECK=$WORK/check
STAGE=$WORK/stage

VOS_DEBUG=${VOS_DEBUG:-0}
VOS_ALLOW_STUB=${VOS_ALLOW_STUB:-0}
CHANNEL=${CHANNEL:-dev}
GIT=${GIT:-unknown}
REFRESH=${REFRESH:-0}
# GitHub refuses release assets of 2 GiB and more; keep a margin.
ISO_LIMIT=$(( 1950 * 1024 * 1024 * 1024 / 1000 ))

step() { printf '\n\e[1;34m==>\e[0m \e[1m%s\e[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '\e[1;33mwarning:\e[0m %s\n' "$*" >&2; }
die()  { printf '\e[1;31merror:\e[0m %s\n' "$*" >&2; exit 1; }
t0=$SECONDS
elapsed() { printf '    (%ss)\n' $((SECONDS - t0)); t0=$SECONDS; }
mib() { echo $(( ($1 + 524288) / 1048576 )); }

# Unmount whatever a failed build left mounted, so the next one starts clean.
cleanup() {
    if mountpoint -q "$CHECK" 2>/dev/null; then umount "$CHECK" || true; fi
    umount -R "$ROOT" 2>/dev/null || true
}
trap cleanup EXIT

# --------------------------------------------------------------- inputs ----

[[ $VOS_DEBUG == 0 || $VOS_DEBUG == 1 ]] || die "VOS_DEBUG must be 0 or 1, not '$VOS_DEBUG'"
[[ $CHANNEL =~ ^[A-Za-z0-9._-]+$ ]] || die "CHANNEL '$CHANNEL' is not a valid channel (tag) name"

if [[ -z ${VERSION:-} ]]; then
    VERSION=$(date -u +%Y%m%d.%H%M%S)
fi
[[ $VERSION =~ ^([0-9]{4})([0-9]{2})([0-9]{2})\.([0-9]{2})([0-9]{2})([0-9]{2})$ ]] ||
    die "VERSION must be YYYYMMDD.HHMMSS (UTC), not '$VERSION'"
v=("${BASH_REMATCH[@]}")
ROLLBACK_INDEX=$(date -u -d "${v[1]}-${v[2]}-${v[3]} ${v[4]}:${v[5]}:${v[6]}" +%s 2>/dev/null) &&
    [[ $(date -u -d "@$ROLLBACK_INDEX" +%Y%m%d.%H%M%S) == "$VERSION" ]] ||
    die "VERSION '$VERSION' is not a valid UTC time"
CREATED=$(date -u -d "@$ROLLBACK_INDEX" +%Y-%m-%dT%H:%M:%SZ)
unset v

if [[ -z ${COMPRESS:-} ]]; then
    if [[ $VOS_DEBUG == 1 ]]; then COMPRESS=lz4hc,9; else COMPRESS=zstd,level=9; fi
fi

# The kernel command line every VaporOS boot entry carries; the image ships
# it in /usr/lib/vos/cmdline and the manifest repeats it for updates.
IMAGE_CMDLINE=$(<"$SRC/rootfs/usr/lib/vos/cmdline")
[[ $IMAGE_CMDLINE != *$'\n'* && -n $IMAGE_CMDLINE ]] || die "rootfs/usr/lib/vos/cmdline must be one line"
[[ " $IMAGE_CMDLINE " != *" console=tty0 "* ]] || die "the image cmdline must not put a console on tty0"

# The image is made of x86-64-v3 packages, and pacstrap runs their install
# scriptlets, so the build has to happen on a CPU that can run them.
/usr/lib/ld-linux-x86-64.so.2 --help | grep -q 'x86-64-v3 (supported' || {
    die "this CPU cannot run x86-64-v3 code; build on the Proxmox builder (the default)"
}

[[ -f $VOS && -x $VOS ]] || die "$VOS is missing: build it first (scripts/build.sh does: GOOS=linux GOARCH=amd64 go build -o .build/vos ./cmd/vos)"
vos_version=$("$VOS" version 2>&1) || die "$VOS does not run here: $vos_version"
[[ $vos_version == "$VERSION" ]] ||
    warn "the vos binary reports version '$vos_version', the image is $VERSION (build both with the same VERSION)"

mode=release; [[ $VOS_DEBUG == 1 ]] && mode=debug
step "VaporOS $VERSION ($mode, channel $CHANNEL, git ${GIT:0:12}, $COMPRESS)"

# A Go subcommand that is still a stub fails with "not implemented". With
# VOS_ALLOW_STUB=1 that one failure is a warning; anything else is an error.
# Usage: stub_ok WHAT OUTPUT
stub_ok() {
    if [[ $VOS_ALLOW_STUB == 1 && $2 == *"not implemented"* ]]; then
        warn "$1 is not implemented yet (VOS_ALLOW_STUB=1): $2"
        return 0
    fi
    return 1
}

# ------------------------------------------------------------------ base ----

mapfile -t PACKAGES < <(sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e '/^[[:space:]]*$/d' "$SRC/packages.txt")
base_key=$(cat "$SRC/packages.txt" "$SRC/build/pacman.conf" | sha256sum | cut -c1-16)

if [[ $REFRESH == 1 || ! -f $WORK/base/.vos-key || $(<"$WORK/base/.vos-key") != "$base_key" ]]; then
    step "Installing ${#PACKAGES[@]} packages (base cache miss)"
    rm -rf "$WORK/base"
    mkdir -p "$WORK/base"
    # -c: use the (volume-backed) host package cache; -G: keep the host keyring
    # out of the image; the image never runs pacman anyway.
    pacstrap -c -G -C "$SRC/build/pacman.conf" "$WORK/base" "${PACKAGES[@]}" >"$WORK/pacstrap.log" 2>&1 ||
        { tail -n 40 "$WORK/pacstrap.log" >&2; die "pacstrap failed (full log: $WORK/pacstrap.log)"; }
    # pacstrap exits 0 even when scriptlets fail; see check_pacstrap_log.
    check_pacstrap_log "$WORK/pacstrap.log" >&2 ||
        die "a package scriptlet or hook failed (full log: $WORK/pacstrap.log)"
    echo "$base_key" >"$WORK/base/.vos-key"
    elapsed
else
    step "Reusing cached base ($base_key)"
fi

# pacman on the base's own database (the sync databases pacstrap fetched are
# there too, for -Si).
basepac() {
    pacman --config "$SRC/build/pacman.conf" --root "$WORK/base" \
        --dbpath "$WORK/base/var/lib/pacman" "$@"
}

# A "repo/package" line in packages.txt pins the repo, e.g. extra/gamescope
# over the older one in cachyos-v3. Check that the pin held: the installed
# version must be the pinned repo's.
for want in "${PACKAGES[@]}"; do
    [[ $want == */* ]] || continue
    pkg=${want#*/}
    have=$(basepac -Q "$pkg" 2>/dev/null | cut -d' ' -f2) || die "$pkg is not installed"
    pinned=$(basepac -Si "$want" 2>/dev/null | awk '$1 == "Version" { print $3 }')
    [[ $have == "$pinned" ]] || die "$pkg is $have, but packages.txt pins $want ($pinned)"
    info "$pkg $have from ${want%%/*}"
done

step "Largest packages"
basepac -Qi | awk '
    $1 == "Name" { name = $3 }
    $1 == "Installed" && $2 == "Size" {
        n = $4; u = $5
        mib = (u == "GiB") ? n * 1024 : (u == "MiB") ? n : (u == "KiB") ? n / 1024 : n / 1048576
        printf "%9.1f MiB  %s\n", mib, name
    }' | sort -rn >"$WORK/sizes.txt"
head -n 25 "$WORK/sizes.txt"
info "$(wc -l <"$WORK/sizes.txt") packages, $(awk '{ s += $1 } END { printf "%.0f", s }' "$WORK/sizes.txt") MiB installed"

# ---------------------------------------------------------------- rootfs ----
step "Assembling rootfs"
cleanup
rm -rf "$WORK/upper" "$WORK/ovl" "$ROOT"
mkdir -p "$WORK/upper" "$WORK/ovl" "$ROOT"
mount -t overlay overlay -o "lowerdir=$WORK/base,upperdir=$WORK/upper,workdir=$WORK/ovl" "$ROOT"
rm -f "$ROOT/.vos-key"

mapfile -t kernels < <(find "$ROOT/usr/lib/modules" -mindepth 2 -maxdepth 2 -name vmlinuz -printf '%h\n')
(( ${#kernels[@]} == 1 )) || die "expected exactly one kernel in the image, found ${#kernels[@]}"
KVER=${kernels[0]##*/}
info "kernel $KVER"

# Our files. Ownership (and any extended attributes) come from the macOS
# checkout, so drop them: everything is root's.
cp -a --no-preserve=ownership,xattr "$SRC/rootfs/." "$ROOT/"
chmod 0755 "$ROOT/usr/lib/vos/vos-firewall" "$ROOT/usr/lib/vos/fail-reboot"
sed -i "s/@VERSION@/$VERSION/g" "$ROOT/usr/lib/os-release"
ln -sf ../usr/lib/os-release "$ROOT/etc/os-release"

# ---- vos: the control plane, and the systemd generator it doubles as
install -Dm0755 "$VOS" "$ROOT/usr/bin/vos"
mkdir -p "$ROOT/usr/lib/systemd/system-generators"
ln -sfn /usr/bin/vos "$ROOT/usr/lib/systemd/system-generators/vos-generator"

# ---- the virtual display's EDID, generated by the same Go code vosd uses
# to add learned modes later
edid=$ROOT/usr/lib/firmware/edid/vaporos.bin
mkdir -p "${edid%/*}"
if ! msg=$("$VOS" edid generate --out "$edid" 2>&1); then
    stub_ok "vos edid generate" "$msg" || die "vos edid generate failed: $msg"
    rm -f "$edid"
fi

# ---- what this image is
jq -n --arg version "$VERSION" --arg channel "$CHANNEL" --arg git "$GIT" \
      --arg kernel "$KVER" --argjson rollback_index "$ROLLBACK_INDEX" \
      --argjson debug "$( ((VOS_DEBUG)) && echo true || echo false)" \
      '{version: $version, channel: $channel, git: $git, kernel: $kernel,
        rollback_index: $rollback_index, debug: $debug}' >"$ROOT/usr/lib/vos/image.json"

# ---- trusted update-signing keys: base64 of a raw 32-byte ed25519 public key
install_pubkey() {
    is_ed25519_pubkey "$1" || die "$1 is not an ed25519 public key (one line of base64, 32 bytes)"
    install -Dm0644 "$1" "$ROOT/usr/lib/vos/keys/$2"
    info "trusts $2"
}
mkdir -p "$ROOT/usr/lib/vos/keys"
[[ -f $SRC/keys/release.pub ]] && install_pubkey "$SRC/keys/release.pub" release.pub
if [[ $VOS_DEBUG == 1 ]]; then
    if [[ -f $KEYS/dev.pub ]]; then
        install_pubkey "$KEYS/dev.pub" dev.pub
    else
        warn "no $KEYS/dev.pub: this debug image trusts no dev key"
    fi
fi
[[ -n $(ls -A "$ROOT/usr/lib/vos/keys") ]] ||
    warn "the image trusts no update-signing key; it will refuse every update"

# ---- Sunshine: KMS capture needs cap_sys_admin (and cap_sys_nice for its
# encoder threads). The package's scriptlet sets them, but it fails in the
# chroot (see check_pacstrap_log), so set them here and check the result.
sunshine_bin=$(chroot "$ROOT" readlink -f /usr/bin/sunshine)
setcap cap_sys_admin,cap_sys_nice+p "$ROOT$sunshine_bin"
[[ $(getcap "$ROOT$sunshine_bin") == *"cap_sys_admin,cap_sys_nice=p"* ]] ||
    die "could not set Sunshine's capabilities on $sunshine_bin"

# ---- users: vapor comes from sysusers.d/vos.conf. Create it in the image's
# /etc/passwd now, rather than on first boot, so that no copy of passwd ever
# lands in the /etc overlay on the data partition and hides later images' users.
systemd-sysusers --root="$ROOT" >/dev/null
grep -q '^vapor:x:1000:1000:' "$ROOT/etc/passwd" || die "sysusers did not create vapor as uid 1000"
for group in input render video seat audio; do
    id_groups=$(awk -F: -v g="$group" '$1 == g { print $4 }' "$ROOT/etc/group")
    [[ ,$id_groups, == *,vapor,* ]] || die "vapor is not in group $group"
done

# ---- name resolution: *.local through avahi (nss-mdns), resolved for the rest
sed -i -E 's/^hosts:.*/hosts: mymachines mdns_minimal [NOTFOUND=return] resolve [!UNAVAIL=return] files myhostname dns/' \
    "$ROOT/etc/nsswitch.conf"
grep -q '^hosts: .*mdns_minimal' "$ROOT/etc/nsswitch.conf" || die "could not set up nss-mdns in nsswitch.conf"
ln -sf ../run/systemd/resolve/stub-resolv.conf "$ROOT/etc/resolv.conf"

# ---- locale: en_US.UTF-8, which Steam expects (C.UTF-8 comes with glibc)
sed -i -E 's/^#(en_US\.UTF-8 UTF-8)/\1/' "$ROOT/etc/locale.gen"
arch-chroot "$ROOT" locale-gen >/dev/null
chroot "$ROOT" localedef --list-archive | grep -qix 'en_US.utf8' || die "locale-gen did not build en_US.UTF-8"

# ---- WoL link: must carry the same naming policy as 99-default.link (it
# replaces it for wired interfaces)
link_policy() { grep -E '^(NamePolicy|AlternativeNamesPolicy|MACAddressPolicy)=' "$1" | sort; }
[[ $(link_policy "$ROOT/usr/lib/systemd/network/99-default.link") == \
   "$(link_policy "$ROOT/usr/lib/systemd/network/50-vos-wol.link")" ]] ||
    die "50-vos-wol.link's naming policy differs from systemd's 99-default.link; copy it over"

# ---- units
systemctl --quiet --root="$ROOT" enable \
    vosd.service vos-firewall.service vos-health.service \
    seatd.service avahi-daemon.service power-profiles-daemon.service \
    systemd-networkd.service systemd-resolved.service \
    systemd-timesyncd.service iwd.service fstrim.timer
# PipeWire for every user (only vapor exists): sockets activate it on demand.
systemctl --quiet --root="$ROOT" --global enable \
    pipewire.socket pipewire-pulse.socket wireplumber.service
# The CachyOS services (ananicy-cpp, systemd-oomd, the wireless regdomain
# setter) are enabled by cachyos-settings' own install scriptlet, as on CachyOS.
# sshd is enabled per machine by vos-generator (config.json ssh.enabled);
# vos-welcome is started by vosd.

# ---- never a terminal: no gettys anywhere, no first-boot questions, no
# debug shell. (logind.conf.d/vos.conf stops autovt from spawning any.)
# And no reboot from a keyboard: every keyboard feeds the kernel's console
# keyboard, Sunshine's virtual one included, so Ctrl+Alt+Del from a Moonlight
# client would otherwise reboot the box mid-stream. (system.conf.d/vos.conf
# turns off the 7-presses burst reboot; sysctl.d/99-vos.conf turns off SysRq.)
sysdir=$ROOT/etc/systemd/system
rm -rf "$sysdir/getty.target.wants"
masked=(getty@.service autovt@.service console-getty.service
        systemd-firstboot.service debug-shell.service ctrl-alt-del.target)
if [[ $VOS_DEBUG == 1 ]]; then
    # Debug images: a root shell on the serial console, for the dev loop and
    # for debugging. tty1 stays untouched.
    mkdir -p "$sysdir/getty.target.wants" "$ROOT/usr/lib/systemd/system/serial-getty@ttyS0.service.d"
    ln -sfn /usr/lib/systemd/system/serial-getty@.service "$sysdir/getty.target.wants/serial-getty@ttyS0.service"
    cat >"$ROOT/usr/lib/systemd/system/serial-getty@ttyS0.service.d/vos-autologin.conf" <<'UNIT'
# Debug images only: root, logged in, on the serial console.
[Service]
ExecStart=
ExecStart=-/usr/bin/agetty --autologin root --noclear --keep-baud %I 115200,38400,9600 $TERM
UNIT
else
    masked+=(serial-getty@.service)
fi
for unit in "${masked[@]}"; do
    ln -sfn /dev/null "$sysdir/$unit"
done

# An empty machine-id marks first boot; systemd fills it in and commits it to
# the writable /etc overlay.
: >"$ROOT/etc/machine-id"
# /etc is up to date with /usr by construction (the build ran ldconfig,
# sysusers, hwdb). Without this stamp, the first boot would run those again
# (ConditionNeedsUpdate=/etc) and leave their output -- ld.so.cache, ... -- in
# the /etc overlay, where it would shadow every later image's copy.
touch "$ROOT/etc/.updated"

# ---- the immutable layout
# Everything mutable lives in /var (the data partition), so the top-level
# directories that users write to become symlinks into it.
rm -rf "$ROOT/home" "$ROOT/srv" "$ROOT/opt" "$ROOT/root" "$ROOT/mnt" "$ROOT/usr/local"
ln -s var/home     "$ROOT/home"
ln -s var/srv      "$ROOT/srv"
ln -s var/opt      "$ROOT/opt"
ln -s var/mnt      "$ROOT/mnt"
ln -s var/roothome "$ROOT/root"
ln -s ../var/usrlocal "$ROOT/usr/local"
mkdir -p "$ROOT/state" "$ROOT/efi"

# Keep the package database (so `pacman -Q` still answers "what's in this
# image?") but move it out of /var, which ships empty. The sync databases
# are only a snapshot of the repos at build time; leave them out.
mv "$ROOT/var/lib/pacman" "$ROOT/usr/lib/vos/pacman-db"
rm -rf "$ROOT/usr/lib/vos/pacman-db/sync"
sed -i 's|^#\?DBPath.*|DBPath = /usr/lib/vos/pacman-db/|' "$ROOT/etc/pacman.conf"
find "$ROOT/var" -mindepth 1 -delete
rm -rf "$ROOT/boot"/*
elapsed

# --------------------------------------------------------------- initramfs --
# It depends only on the kernel, the installed packages and our hook/config,
# so it is cached on exactly those; most rebuilds skip mkinitcpio. The package
# list is part of the key so REFRESH=1 (same packages.txt, newer packages)
# never reuses an initramfs built for older ones.
initrd_key=$( { echo "$KVER"; basepac -Q
                cat "$SRC/rootfs/usr/lib/vos/mkinitcpio.conf" "$SRC"/rootfs/usr/lib/initcpio/{hooks,install}/vos
              } | sha256sum | cut -c1-16)
INITRD_CACHE=$WORK/cache/initramfs-$initrd_key.img
if [[ -s $INITRD_CACHE ]]; then
    step "Reusing cached initramfs ($initrd_key)"
else
    step "Building initramfs for $KVER"
    arch-chroot "$ROOT" mkinitcpio -c /usr/lib/vos/mkinitcpio.conf -k "$KVER" \
        -g /vos-initramfs.img 2>&1 | grep -E '^==> ERROR' || true
    [[ -s $ROOT/vos-initramfs.img ]] || die "mkinitcpio failed"
    rm -rf "$WORK/cache"; mkdir -p "$WORK/cache"
    mv "$ROOT/vos-initramfs.img" "$INITRD_CACHE"
    elapsed
fi

rm -rf "$STAGE"; mkdir -p "$STAGE/vos"
cp "$INITRD_CACHE" "$STAGE/vos/initramfs.img"
cp "$ROOT/usr/lib/modules/$KVER/vmlinuz" "$STAGE/vos/vmlinuz"
cp "$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi" "$WORK/systemd-bootx64.efi"

# ------------------------------------------------------------------ erofs ---
# zstd and lzma need large physical clusters to compress well (the default
# 4 KiB cluster wastes most of their advantage); tail packing then saves the
# partly filled last block of every file. Measured on this image (2.8 GiB,
# 8 cores): lz4hc,9 1645 MiB in 24 s; zstd level 9 with 256 KiB clusters
# 1310 MiB in 2 min; level 15 only 1% smaller in 8 min, because a third of the
# image (firmware) is compressed already. -Eall-fragments and -Ededupe were
# left out on purpose: they make mkfs.erofs single-threaded, over an hour for
# about 1%.
erofs_extra=()
case $COMPRESS in
    lz4*) ;;
    *)
        erofs_extra+=(-C"${EROFS_PCLUSTER:-262144}")
        probe=$WORK/erofs-probe
        rm -rf "$probe"; mkdir -p "$probe/src"; echo probe >"$probe/src/f"
        if mkfs.erofs -z"$COMPRESS" -Eztailpacking --quiet "$probe/img" "$probe/src" >/dev/null 2>&1; then
            erofs_extra+=(-Eztailpacking)
        fi
        rm -rf "$probe"
        ;;
esac
step "Creating root.erofs (-z$COMPRESS${erofs_extra[*]:+ ${erofs_extra[*]}})"
mkfs.erofs -z"$COMPRESS" "${erofs_extra[@]}" -T0 --workers="$(nproc)" --quiet \
    "$STAGE/vos/root.erofs" "$ROOT"
umount -R "$ROOT"
info "root.erofs: $(mib "$(stat -c %s "$STAGE/vos/root.erofs")") MiB"
elapsed

# ----------------------------------------------------------------- checks ---
# Check what ships, not the build tree: mount the erofs read-only and look.

# The builder runs in an LXC whose /dev has no loop devices, though the kernel
# has plenty. Make the nodes; mount's own loop setup then asks the kernel for
# a free one (and releases it on umount).
loop_nodes() {
    [[ -e /dev/loop-control ]] || mknod -m 0660 /dev/loop-control c 10 237
    local i
    for i in $(seq 0 63); do
        [[ -e /dev/loop$i ]] || mknod -m 0660 "/dev/loop$i" b 7 "$i"
    done
}

check_image() {
    local img=$1 m=$CHECK caps edid_head unit bin sysrq key
    local -a problems=() gettys=()
    loop_nodes
    mkdir -p "$m"
    mount -t erofs -o ro,loop "$img" "$m" || die "cannot mount $img to check it"
    problem() { problems+=("$*"); }

    caps=$(getcap "$m$sunshine_bin")
    [[ $caps == *"cap_sys_admin,cap_sys_nice=p"* ]] ||
        problem "sunshine lacks cap_sys_admin,cap_sys_nice=p in the image (getcap: '${caps:-none}')"

    [[ -f $m/usr/bin/vos && -x $m/usr/bin/vos ]] || problem "/usr/bin/vos is missing or not executable"
    [[ $(readlink "$m/usr/lib/systemd/system-generators/vos-generator") == /usr/bin/vos ]] ||
        problem "the vos-generator symlink is missing"
    [[ $(stat -c %a "$m/usr/lib/vos/vos-firewall") == 755 ]] || problem "vos-firewall is not executable"
    for bin in jq nft gamescope steam sunshine seatd modetest; do
        [[ -x $m/usr/bin/$bin ]] || problem "/usr/bin/$bin is missing"
    done

    jq -e --arg v "$VERSION" --argjson d "$( ((VOS_DEBUG)) && echo true || echo false)" \
        '.version == $v and (.channel | type) == "string" and (.git | type) == "string"
         and (.kernel | type) == "string" and (.rollback_index | type) == "number"
         and .debug == $d' "$m/usr/lib/vos/image.json" >/dev/null ||
        problem "/usr/lib/vos/image.json is missing, invalid or wrong"
    [[ $(<"$m/usr/lib/vos/cmdline") == "$IMAGE_CMDLINE" ]] || problem "/usr/lib/vos/cmdline is wrong"

    if [[ -s $m/usr/lib/firmware/edid/vaporos.bin ]]; then
        edid_head=$(od -An -tx1 -N8 "$m/usr/lib/firmware/edid/vaporos.bin" | tr -d ' \n')
        [[ $edid_head == 00ffffffffffff00 && $(( $(stat -c %s "$m/usr/lib/firmware/edid/vaporos.bin") % 128 )) == 0 ]] ||
            problem "/usr/lib/firmware/edid/vaporos.bin is not an EDID"
    elif [[ $VOS_ALLOW_STUB != 1 ]]; then
        problem "/usr/lib/firmware/edid/vaporos.bin is missing"
    fi

    grep -q '^vapor:x:1000:1000:' "$m/etc/passwd" || problem "user vapor (1000) is missing"
    [[ $(readlink "$m/mnt") == var/mnt ]] || problem "/mnt is not a symlink to var/mnt"
    [[ -f $m/etc/.updated ]] || problem "/etc/.updated is missing"

    # Gettys: none in release images; only the serial one in debug images.
    for unit in getty@.service autovt@.service console-getty.service systemd-firstboot.service \
                debug-shell.service ctrl-alt-del.target; do
        [[ $(readlink "$m/etc/systemd/system/$unit") == /dev/null ]] || problem "$unit is not masked"
    done
    # No keypress, local or from a Moonlight client, reboots the box.
    grep -qx 'CtrlAltDelBurstAction=none' "$m/usr/lib/systemd/system.conf.d/vos.conf" 2>/dev/null ||
        problem "system.conf.d/vos.conf does not set CtrlAltDelBurstAction=none"
    sysrq=$(effective_sysctl "$m" kernel.sysrq)
    [[ $sysrq == 0 ]] || problem "kernel.sysrq is '${sysrq:-unset}' after all sysctl.d files, expected 0"
    for key in HandleRebootKey HandleSuspendKey HandleHibernateKey; do
        grep -qx "$key=ignore" "$m/usr/lib/systemd/logind.conf.d/vos.conf" 2>/dev/null ||
            problem "logind.conf.d/vos.conf does not set $key=ignore"
    done
    mapfile -t gettys < <(find "$m/etc/systemd/system" "$m/usr/lib/systemd/system" \
        -path '*/getty.target.wants/*' -printf '%f\n' | sort)
    if [[ $VOS_DEBUG == 1 ]]; then
        [[ ${gettys[*]} == serial-getty@ttyS0.service ]] ||
            problem "debug image should start exactly serial-getty@ttyS0, has: ${gettys[*]:-none}"
        [[ -e $m/usr/lib/vos/keys/dev.pub || ! -f $KEYS/dev.pub ]] || problem "the debug image does not trust dev.pub"
    else
        (( ${#gettys[@]} == 0 )) || problem "release image starts gettys: ${gettys[*]}"
        [[ $(readlink "$m/etc/systemd/system/serial-getty@.service") == /dev/null ]] ||
            problem "serial-getty@.service is not masked"
        [[ ! -e $m/usr/lib/vos/keys/dev.pub ]] || problem "a release image must not trust dev.pub"
    fi

    for unit in vosd.service vos-firewall.service vos-health.service; do
        [[ -L $m/etc/systemd/system/multi-user.target.wants/$unit ]] || problem "$unit is not enabled"
    done
    [[ -L $m/etc/systemd/system/boot-complete.target.requires/vos-health.service ]] ||
        problem "vos-health.service is not required by boot-complete.target"
    [[ ! -e $m/etc/systemd/system/multi-user.target.wants/sshd.service ]] || problem "sshd is enabled"

    umount "$m"
    if (( ${#problems[@]} )); then
        printf '    - %s\n' "${problems[@]}" >&2
        die "the image failed its checks"
    fi
}

step "Checking root.erofs"
check_image "$STAGE/vos/root.erofs"

check_firewall "$SRC/rootfs/usr/lib/vos/vos-firewall" "$SRC/rootfs/usr/lib/vos/nftables.nft" ||
    die "the firewall does not load"
info "all checks passed"
elapsed

# --------------------------------------------------------------- manifest ---
sha() { sha256sum "$1" | cut -d' ' -f1; }
artifact() { # artifact NAME -> {"name","size","sha256"}
    jq -n --arg name "$1" --argjson size "$(stat -c %s "$STAGE/vos/$1")" --arg sha "$(sha "$STAGE/vos/$1")" \
        '{name: $name, size: $size, sha256: $sha}'
}
jq -n --arg version "$VERSION" --argjson rollback_index "$ROLLBACK_INDEX" \
      --arg channel "$CHANNEL" --arg git "$GIT" --arg created "$CREATED" \
      --arg kernel "$KVER" --arg cmdline "$IMAGE_CMDLINE" \
      --argjson root "$(artifact root.erofs)" \
      --argjson kernel_file "$(artifact vmlinuz)" \
      --argjson initrd "$(artifact initramfs.img)" \
      '{schema: 1, product: "vaporos", version: $version, rollback_index: $rollback_index,
        channel: $channel, git: $git, created: $created, kernel: $kernel, cmdline: $cmdline,
        min_updater: 1,
        artifacts: {root: $root, kernel: $kernel_file, initrd: $initrd}}' >"$STAGE/vos/manifest.json"

# `vos sign --key KEYSPEC MANIFEST` writes MANIFEST.sig. The signature is then
# checked independently (verify_ed25519, with openssl) against the public key
# devices will use. The release key is in vos's environment alone.
sign_manifest() { # sign_manifest KEYSPEC PUBKEY
    local msg
    rm -f "$STAGE/vos/manifest.json.sig"
    if ! msg=$(VOS_SIGNING_KEY=$SIGNING_KEY "$VOS" sign --key "$1" "$STAGE/vos/manifest.json" 2>&1); then
        stub_ok "vos sign" "$msg" && return 0
        die "vos sign failed: $msg"
    fi
    [[ -s $STAGE/vos/manifest.json.sig ]] || die "vos sign did not write manifest.json.sig"
    if [[ -f $2 ]]; then
        verify_ed25519 "$2" "$STAGE/vos/manifest.json" "$STAGE/vos/manifest.json.sig" ||
            die "manifest.json.sig does not verify against ${2##*/}"
        info "signed and verified against ${2##*/}"
    else
        warn "signed, but ${2##*/} is not available to verify the signature"
    fi
}

step "Writing manifest.json"
if [[ -n $SIGNING_KEY ]]; then
    sign_manifest env:VOS_SIGNING_KEY "$SRC/keys/release.pub"
elif [[ $VOS_DEBUG == 1 && -s $KEYS/dev.key ]]; then
    sign_manifest "$KEYS/dev.key" "$KEYS/dev.pub"
else
    warn "no signing key: manifest.json is unsigned"
fi

# -------------------------------------------------------------------- ISO ---
step "Creating ISO"
# The El Torito / USB ESP: systemd-boot, one live entry, and the kernel pair.
# No menu, no editor: the kernel command line cannot be changed at boot.
ESPIMG=$WORK/efiboot.img
esp_kib=$(( ( $(stat -c %s "$STAGE/vos/vmlinuz") + $(stat -c %s "$STAGE/vos/initramfs.img") ) / 1024 + 8192 ))
rm -f "$ESPIMG"
mkfs.fat -C -n VOS_EFI "$ESPIMG" "$esp_kib" >/dev/null
mmd -i "$ESPIMG" ::/EFI ::/EFI/BOOT ::/loader ::/loader/entries ::/vos
mcopy -i "$ESPIMG" "$WORK/systemd-bootx64.efi" ::/EFI/BOOT/BOOTX64.EFI
mcopy -i "$ESPIMG" "$STAGE/vos/vmlinuz" "$STAGE/vos/initramfs.img" ::/vos/
printf '%s\n' 'timeout 0' 'editor no' 'auto-entries no' 'auto-firmware no' 'console-mode keep' |
    mcopy -i "$ESPIMG" - ::/loader/loader.conf
cat <<ENTRY | mcopy -i "$ESPIMG" - ::/loader/entries/live.conf
title   VaporOS $VERSION (installer)
linux   /vos/vmlinuz
initrd  /vos/initramfs.img
options vos.mode=live vos.label=VOS_LIVE $IMAGE_CMDLINE
ENTRY

loader_conf=$(mtype -i "$ESPIMG" ::/loader/loader.conf)
grep -qx 'editor no' <<<"$loader_conf" || die "the ISO's loader.conf does not say 'editor no'"
live_entry=$(mtype -i "$ESPIMG" ::/loader/entries/live.conf)
[[ $live_entry != *console=tty0* ]] || die "the ISO's live entry puts a console on tty0"

ISO="vaporos-$VERSION.iso"
xorriso -as mkisofs \
    -iso-level 3 -full-iso9660-filenames -joliet -joliet-long -rational-rock \
    -volid VOS_LIVE -appid "VaporOS $VERSION" -publisher VaporOS \
    -partition_offset 16 \
    -append_partition 2 C12A7328-F81F-11D2-BA4B-00A0C93EC93B "$ESPIMG" \
    -appended_part_as_gpt \
    -e --interval:appended_partition_2:all:: -no-emul-boot \
    -output "$WORK/$ISO" "$STAGE" 2>&1 | grep -iE 'error|fail' || true
[[ -s $WORK/$ISO ]] || die "xorriso did not write the ISO"
elapsed

iso_bytes=$(stat -c %s "$WORK/$ISO")
if (( iso_bytes >= ISO_LIMIT )); then
    msg="the ISO is $(mib "$iso_bytes") MiB, over the $(mib "$ISO_LIMIT") MiB release limit (GitHub caps assets at 2 GiB)"
    if [[ $VOS_DEBUG == 1 ]]; then warn "$msg (a debug build; release builds fail here)"; else die "$msg"; fi
fi

# ------------------------------------------------------------------ out -----
step "Publishing to out/"
rm -f "$OUT"/*.iso "$OUT/manifest.env" "$OUT/manifest.json.sig"
for f in root.erofs vmlinuz initramfs.img manifest.json manifest.json.sig; do
    if [[ -f $STAGE/vos/$f ]]; then cp "$STAGE/vos/$f" "$OUT/"; fi
done
mv "$WORK/$ISO" "$OUT/"
info "root.erofs  $(mib "$(stat -c %s "$OUT/root.erofs")") MiB ($COMPRESS)"
info "$ISO  $(mib "$iso_bytes") MiB (limit $(mib "$ISO_LIMIT") MiB)"
ls -l "$OUT"
