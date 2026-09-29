#!/usr/bin/env bash
#
# Makes the VaporOS live ISO (UEFI, USB/CD hybrid) from the OS files. It needs
# no privileges, mounts or network, only jq, mkfs.fat (dosfstools), mtools and
# xorriso, so it runs in the build container (build/build.sh) and on a plain CI
# runner alike. CI signs manifest.json in a job that runs no package code and
# makes the ISO there, after signing, so the medium carries the signature.
#
#   build/iso.sh DIR ISO
#
# DIR holds root.erofs, vmlinuz, initramfs.img, manifest.json,
# manifest.json.sig (optional) and systemd-bootx64.efi. The version and the
# kernel command line come from manifest.json. The ISO's /vos/ holds all of
# them but systemd-boot: that is what the initramfs hook and the installer
# read (docs/CONTRACTS.md, /run/vos/medium/vos/). Its ESP holds systemd-boot,
# one live entry and the kernel pair. No menu, no editor: the kernel command
# line cannot be changed at boot.
set -euo pipefail

die() { printf 'iso.sh: %s\n' "$*" >&2; exit 1; }
(( $# == 2 )) || { echo "usage: $0 DIR ISO" >&2; exit 2; }
dir=$1 iso=$2

for f in root.erofs vmlinuz initramfs.img manifest.json systemd-bootx64.efi; do
    [[ -s $dir/$f ]] || die "$dir/$f is missing"
done
version=$(jq -er '.version | strings' "$dir/manifest.json") || die "manifest.json has no version"
cmdline=$(jq -er '.cmdline | strings' "$dir/manifest.json") || die "manifest.json has no cmdline"
[[ -n $cmdline && $cmdline != *$'\n'* ]] || die "the manifest's cmdline must be one line"
[[ " $cmdline " != *" console=tty0 "* ]] || die "the manifest's cmdline puts a console on tty0"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# mtools refuses some images mkfs.fat makes (sectors not a multiple of the
# track size); a plain FAT image has no geometry to get wrong.
export MTOOLS_SKIP_CHECK=1

esp=$tmp/efiboot.img
esp_kib=$(( ( $(stat -c %s "$dir/vmlinuz") + $(stat -c %s "$dir/initramfs.img") ) / 1024 + 8192 ))
mkfs.fat -C -n VOS_EFI "$esp" "$esp_kib" >/dev/null
mmd -i "$esp" ::/EFI ::/EFI/BOOT ::/loader ::/loader/entries ::/vos
mcopy -i "$esp" "$dir/systemd-bootx64.efi" ::/EFI/BOOT/BOOTX64.EFI
mcopy -i "$esp" "$dir/vmlinuz" "$dir/initramfs.img" ::/vos/
printf '%s\n' 'timeout 0' 'editor no' 'auto-entries no' 'auto-firmware no' 'console-mode keep' >"$tmp/loader.conf"
mcopy -i "$esp" "$tmp/loader.conf" ::/loader/loader.conf
cat >"$tmp/live.conf" <<ENTRY
title   VaporOS $version (installer)
linux   /vos/vmlinuz
initrd  /vos/initramfs.img
options vos.mode=live vos.label=VOS_LIVE $cmdline
ENTRY
mcopy -i "$esp" "$tmp/live.conf" ::/loader/entries/live.conf

grep -qx 'editor no' <<<"$(mtype -i "$esp" ::/loader/loader.conf)" ||
    die "the ISO's loader.conf does not say 'editor no'"
[[ $(mtype -i "$esp" ::/loader/entries/live.conf) != *console=tty0* ]] ||
    die "the ISO's live entry puts a console on tty0"

# Only these files go on the ISO, under /vos (graft points: nothing is copied).
files=(root.erofs vmlinuz initramfs.img manifest.json)
[[ ! -f $dir/manifest.json.sig ]] || files+=(manifest.json.sig)
graft=()
for f in "${files[@]}"; do graft+=("/vos/$f=$dir/$f"); done

rm -f "$iso"
if ! xorriso -as mkisofs \
        -iso-level 3 -full-iso9660-filenames -joliet -joliet-long -rational-rock \
        -volid VOS_LIVE -appid "VaporOS $version" -publisher VaporOS \
        -partition_offset 16 \
        -append_partition 2 C12A7328-F81F-11D2-BA4B-00A0C93EC93B "$esp" \
        -appended_part_as_gpt \
        -e --interval:appended_partition_2:all:: -no-emul-boot \
        -graft-points -output "$iso" "${graft[@]}" >"$tmp/xorriso.log" 2>&1; then
    tail -n 40 "$tmp/xorriso.log" >&2
    die "xorriso could not write $iso"
fi
[[ -s $iso ]] || die "xorriso did not write $iso"
