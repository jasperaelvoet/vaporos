#!/usr/bin/env bash
# Build the installable ISO. The ISO is a live Arch environment that carries
# the OS image from build-image.sh and installs it with `bootc install`.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT=${OUT:-out}
[[ -f $OUT/image.tar ]] || { echo "run scripts/build-image.sh first" >&2; exit 1; }

# mkarchiso needs root, loop devices and an x86_64 userspace, so it runs in a
# privileged Arch container rather than on the macOS host.
docker run --rm --privileged --platform linux/amd64 \
    -v "$PWD:/work" -w /work \
    docker.io/library/archlinux:base-devel \
    bash -euxo pipefail -c '
        pacman -Syu --noconfirm --needed archiso erofs-utils

        rm -rf /tmp/profile
        cp -r /usr/share/archiso/configs/releng /tmp/profile
        cp -r iso/. /tmp/profile/

        # Ship the OS image inside the live root, where the installer
        # expects to find it. archiso only copies airootfs onto the medium.
        install -Dm644 '"$OUT"'/image.tar \
            /tmp/profile/airootfs/usr/share/watervapor/image.tar

        rm -rf /tmp/work
        mkarchiso -v -w /tmp/work -o '"$OUT"' /tmp/profile
    '
ls -lh "$OUT"/*.iso
