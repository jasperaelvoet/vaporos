#!/usr/bin/env bash
#
# Make a Ventoy USB stick image that holds a VaporOS ISO as a plain file, the
# way a user who keeps a Ventoy stick boots it. tests/qemu-smoke.sh boots it
# with STICK=. Ventoy is a pinned release, checked against its sha256 before
# anything of it runs; it runs as root (sudo) because Ventoy2Disk.sh needs a
# block device, here a loop device on the image file.
#
#   tests/ventoy-stick.sh ISO IMAGE
#
# Linux only. Needs sudo, losetup, parted (or fdisk), curl and an exFAT
# capable kernel.
set -euo pipefail

VENTOY_VERSION=1.1.17
VENTOY_SHA256=7fb4ed08cef6a6b4d39dd19260d8c80291a78dfdf9af7d461571e23cbbc43805

ISO=${1:?usage: $0 ISO IMAGE}
IMG=${2:?usage: $0 ISO IMAGE}
[[ -f $ISO ]] || { echo "no ISO at $ISO" >&2; exit 1; }

tmp=$(mktemp -d)
loop=""
cleanup() {
    if [[ -n $loop ]]; then
        sudo umount "$tmp/mnt" 2>/dev/null || true
        sudo losetup -d "$loop" 2>/dev/null || true
    fi
    sudo rm -rf "$tmp"
}
trap cleanup EXIT

tgz=$tmp/ventoy.tar.gz
curl -fsSL --retry 3 -o "$tgz" \
    "https://github.com/ventoy/Ventoy/releases/download/v$VENTOY_VERSION/ventoy-$VENTOY_VERSION-linux.tar.gz"
echo "$VENTOY_SHA256  $tgz" | sha256sum -c --quiet - || { echo "Ventoy $VENTOY_VERSION: sha256 mismatch" >&2; exit 1; }
tar -xzf "$tgz" -C "$tmp"

rm -f "$IMG"
truncate -s 4G "$IMG"
loop=$(sudo losetup -fP --show "$IMG")
# Defaults, as a user gets them: MBR, Secure Boot support, exFAT labelled
# Ventoy. It asks twice before it erases the disk.
(cd "$tmp/ventoy-$VENTOY_VERSION" && printf 'y\ny\n' | sudo sh ./Ventoy2Disk.sh -I "$loop") >"$tmp/ventoy.log" 2>&1 ||
    { cat "$tmp/ventoy.log" >&2; exit 1; }
sudo partprobe "$loop" 2>/dev/null || true
sudo udevadm settle 2>/dev/null || true
[[ -b ${loop}p1 ]] || { cat "$tmp/ventoy.log" >&2; echo "Ventoy made no partition 1 on $loop" >&2; exit 1; }

mkdir -p "$tmp/mnt"
sudo mount -t exfat "${loop}p1" "$tmp/mnt"
# In a folder, as people keep them; the default image boots without a key
# press, and with no second menu (normal mode, grub2 mode, ...).
iso_name=$(basename "$ISO")
sudo mkdir -p "$tmp/mnt/isos" "$tmp/mnt/ventoy"
sudo cp "$ISO" "$tmp/mnt/isos/$iso_name"
printf '{"control": [{"VTOY_DEFAULT_IMAGE": "/isos/%s"}, {"VTOY_MENU_TIMEOUT": "1"}, {"VTOY_SECONDARY_BOOT_MENU": "0"}]}\n' "$iso_name" |
    sudo tee "$tmp/mnt/ventoy/ventoy.json" >/dev/null
sudo umount "$tmp/mnt"
sudo losetup -d "$loop"
loop=""
sudo chown "$(id -u):$(id -g)" "$IMG"
echo "Ventoy $VENTOY_VERSION stick with isos/$iso_name: $IMG"
