#!/usr/bin/env bash
#
# Runs inside the vos-builder container. Produces, in /out:
#   root.erofs      the OS: one read-only image with kernel modules + userspace
#   vmlinuz         the kernel that matches it
#   initramfs.img   the initramfs with the vos hook
#   manifest.env    version + checksums, what `vos update` reads
#   vaporos-<version>.iso   live + installer, UEFI, USB/CD hybrid
#
# Stages, and what makes them fast:
#   base      pacstrap of packages.txt. Cached in /work/base and reused until
#             packages.txt or pacman.conf changes (or REFRESH=1).
#   rootfs    an overlayfs on top of base with our files and configuration.
#             Thrown away every build, so it costs seconds.
#   images    erofs + initramfs + ISO.
set -euo pipefail

SRC=/src
WORK=/work
OUT=/out
VERSION=${VERSION:-$(date -u +%Y%m%d.%H%M%S)}
COMPRESS=${COMPRESS:-lz4hc,9}
REFRESH=${REFRESH:-0}

step() { printf '\n\e[1;34m==>\e[0m \e[1m%s\e[0m\n' "$*"; }
t0=$SECONDS
elapsed() { printf '    (%ss)\n' $((SECONDS - t0)); t0=$SECONDS; }

# The image is made of x86-64-v3 packages, and pacstrap runs their install
# scriptlets, so the build has to happen on a CPU that can run them.
/usr/lib/ld-linux-x86-64.so.2 --help | grep -q 'x86-64-v3 (supported' || {
    echo "error: this CPU cannot run x86-64-v3 code; build on the Proxmox builder (the default)" >&2
    exit 1
}

mapfile -t PACKAGES < <(sed -e 's/#.*//' -e '/^\s*$/d' "$SRC/packages.txt")
base_key=$(cat "$SRC/packages.txt" "$SRC/build/pacman.conf" | sha256sum | cut -c1-16)

# ------------------------------------------------------------------ base ----
if [[ $REFRESH == 1 || ! -f $WORK/base/.vos-key || $(<"$WORK/base/.vos-key") != "$base_key" ]]; then
    step "Installing ${#PACKAGES[@]} packages (base cache miss)"
    rm -rf "$WORK/base"
    mkdir -p "$WORK/base"
    # -c: use the (volume-backed) host package cache; -G: keep the host keyring
    # out of the image; the image never runs pacman anyway.
    # pacstrap exits 0 even when scriptlets or hooks fail; catch that here.
    pacstrap -c -G -C "$SRC/build/pacman.conf" "$WORK/base" "${PACKAGES[@]}" >"$WORK/pacstrap.log" 2>&1 ||
        { tail -n 30 "$WORK/pacstrap.log" >&2; exit 1; }
    if grep -qE '^error: command failed|^Fatal glibc' "$WORK/pacstrap.log"; then
        grep -B2 -E '^error: command failed|^Fatal glibc' "$WORK/pacstrap.log" >&2
        echo "pacstrap: a package scriptlet or hook failed (full log: $WORK/pacstrap.log)" >&2
        exit 1
    fi
    echo "$base_key" >"$WORK/base/.vos-key"
    elapsed
else
    step "Reusing cached base ($base_key)"
fi

# ---------------------------------------------------------------- rootfs ----
step "Assembling rootfs"
ROOT=$WORK/rootfs
umount -R "$ROOT" 2>/dev/null || true
rm -rf "$WORK/upper" "$WORK/ovl" "$ROOT"
mkdir -p "$WORK/upper" "$WORK/ovl" "$ROOT"
mount -t overlay overlay -o "lowerdir=$WORK/base,upperdir=$WORK/upper,workdir=$WORK/ovl" "$ROOT"
trap 'umount -R "$ROOT" 2>/dev/null || true' EXIT

# Our files. Ownership comes from the macOS checkout, so force root.
cp -a --no-preserve=ownership "$SRC/rootfs/." "$ROOT/"
chmod 0440 "$ROOT/etc/sudoers.d/10-wheel"
chmod 0755 "$ROOT/usr/bin/vos"
sed -i "s/@VERSION@/$VERSION/g" "$ROOT/usr/lib/os-release"
ln -sf ../usr/lib/os-release "$ROOT/etc/os-release"
rm -f "$ROOT/.vos-key"

systemctl --root="$ROOT" enable \
    systemd-networkd.service systemd-resolved.service \
    systemd-timesyncd.service iwd.service \
    fstrim.timer >/dev/null 2>&1
# The CachyOS services (ananicy-cpp, systemd-oomd, the wireless regdomain
# setter) are enabled by cachyos-settings' own install scriptlet, as on CachyOS.
# Asks for locale/timezone/root password on first boot; the installer does that.
systemctl --root="$ROOT" mask systemd-firstboot.service >/dev/null 2>&1
ln -sf ../run/systemd/resolve/stub-resolv.conf "$ROOT/etc/resolv.conf"

# An empty machine-id marks first boot; systemd fills it in and commits it to
# the writable /etc overlay.
: >"$ROOT/etc/machine-id"

# ---- the immutable layout
# Everything mutable lives in /var (the data partition), so the top-level
# directories that users write to become symlinks into it.
rm -rf "$ROOT/home" "$ROOT/srv" "$ROOT/opt" "$ROOT/root" "$ROOT/usr/local"
ln -s var/home     "$ROOT/home"
ln -s var/srv      "$ROOT/srv"
ln -s var/opt      "$ROOT/opt"
ln -s var/roothome "$ROOT/root"
ln -s ../var/usrlocal "$ROOT/usr/local"
mkdir -p "$ROOT/state" "$ROOT/efi"

# Keep the package database (so `pacman -Q` still answers "what's in this
# image?") but move it out of /var, which ships empty.
mkdir -p "$ROOT/usr/lib/vos"
mv "$ROOT/var/lib/pacman" "$ROOT/usr/lib/vos/pacman-db"
sed -i 's|^#\?DBPath.*|DBPath = /usr/lib/vos/pacman-db/|' "$ROOT/etc/pacman.conf"
find "$ROOT/var" -mindepth 1 -delete
rm -rf "$ROOT/boot"/*

# ---- initramfs
# It depends only on the package base and our hook/config, so cache it on
# exactly those; most rebuilds (changing vos, configs, ...) skip mkinitcpio.
KVER=$(basename "$(ls -d "$ROOT"/usr/lib/modules/*/)")
initrd_key=$(cat "$WORK/base/.vos-key" "$SRC/rootfs/usr/lib/vos/mkinitcpio.conf" \
    "$SRC"/rootfs/usr/lib/initcpio/{hooks,install}/vos | sha256sum | cut -c1-16)
INITRD_CACHE=$WORK/cache/initramfs-$initrd_key.img
if [[ -s $INITRD_CACHE ]]; then
    step "Reusing cached initramfs ($initrd_key)"
else
    step "Building initramfs for $KVER"
    arch-chroot "$ROOT" mkinitcpio -c /usr/lib/vos/mkinitcpio.conf -k "$KVER" \
        -g /vos-initramfs.img 2>&1 | grep -E '^==> ERROR' || true
    [[ -s $ROOT/vos-initramfs.img ]] || { echo "mkinitcpio failed" >&2; exit 1; }
    rm -rf "$WORK/cache"; mkdir -p "$WORK/cache"
    mv "$ROOT/vos-initramfs.img" "$INITRD_CACHE"
    elapsed
fi

STAGE=$WORK/stage
rm -rf "$STAGE"; mkdir -p "$STAGE/vos"
cp "$INITRD_CACHE" "$STAGE/vos/initramfs.img"
cp "$ROOT/usr/lib/modules/$KVER/vmlinuz" "$STAGE/vos/vmlinuz"
cp "$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi" "$WORK/systemd-bootx64.efi"

# ---------------------------------------------------------------- images ----
step "Creating root.erofs ($COMPRESS)"
mkfs.erofs -z"$COMPRESS" -T0 --workers="$(nproc)" \
    --quiet "$STAGE/vos/root.erofs" "$ROOT"
elapsed
umount -R "$ROOT"; trap - EXIT

sha() { sha256sum "$1" | cut -d' ' -f1; }
cat >"$STAGE/vos/manifest.env" <<MANIFEST
VOS_VERSION=$VERSION
VOS_KERNEL=$KVER
VOS_ROOT_SHA256=$(sha "$STAGE/vos/root.erofs")
VOS_ROOT_SIZE=$(stat -c %s "$STAGE/vos/root.erofs")
VOS_VMLINUZ_SHA256=$(sha "$STAGE/vos/vmlinuz")
VOS_INITRAMFS_SHA256=$(sha "$STAGE/vos/initramfs.img")
MANIFEST

step "Creating ISO"
# The El Torito / USB ESP: systemd-boot, one live entry, and the kernel pair.
ESPIMG=$WORK/efiboot.img
esp_kib=$(( ( $(stat -c %s "$STAGE/vos/vmlinuz") + $(stat -c %s "$STAGE/vos/initramfs.img") ) / 1024 + 8192 ))
rm -f "$ESPIMG"
mkfs.fat -C -n VOS_EFI "$ESPIMG" "$esp_kib" >/dev/null
mmd -i "$ESPIMG" ::/EFI ::/EFI/BOOT ::/loader ::/loader/entries ::/vos
mcopy -i "$ESPIMG" "$WORK/systemd-bootx64.efi" ::/EFI/BOOT/BOOTX64.EFI
mcopy -i "$ESPIMG" "$STAGE/vos/vmlinuz" "$STAGE/vos/initramfs.img" ::/vos/
printf 'timeout 3\nconsole-mode keep\n' | mcopy -i "$ESPIMG" - ::/loader/loader.conf
cat <<ENTRY | mcopy -i "$ESPIMG" - ::/loader/entries/live.conf
title   VaporOS $VERSION (live / install)
linux   /vos/vmlinuz
initrd  /vos/initramfs.img
options vos.mode=live vos.label=VOS_LIVE console=ttyS0,115200 console=tty0
ENTRY

ISO="vaporos-$VERSION.iso"
xorriso -as mkisofs \
    -iso-level 3 -full-iso9660-filenames -joliet -joliet-long -rational-rock \
    -volid VOS_LIVE -appid "VaporOS $VERSION" -publisher VaporOS \
    -partition_offset 16 \
    -append_partition 2 C12A7328-F81F-11D2-BA4B-00A0C93EC93B "$ESPIMG" \
    -appended_part_as_gpt \
    -e --interval:appended_partition_2:all:: -no-emul-boot \
    -output "$WORK/$ISO" "$STAGE" 2>&1 | grep -iE 'error|fail' || true
elapsed

# ------------------------------------------------------------------ out -----
step "Publishing to out/"
rm -f "$OUT"/*.iso
cp "$STAGE/vos/root.erofs" "$STAGE/vos/vmlinuz" "$STAGE/vos/initramfs.img" \
   "$STAGE/vos/manifest.env" "$OUT/"
mv "$WORK/$ISO" "$OUT/"
ls -lh "$OUT"
