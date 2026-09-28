#!/bin/bash
# Build the named AUR packages in dependency order, installing each one before
# moving to the next so that later packages can link against earlier ones.
# Every resulting package ends up in /pkgs for the OS stage to consume.
set -euo pipefail

# Upstream release-signing keys for the source tarballs these PKGBUILDs fetch.
# makepkg verifies sha256sums too, but the signatures are the stronger check,
# so import the keys rather than passing --skippgpcheck.
KEYS=(
    CDCAE8C927C6BE31   # SELinux userspace releases (libsepol, libselinux)
)
for key in "${KEYS[@]}"; do
    gpg --batch --keyserver keyserver.ubuntu.com --recv-keys "$key"
done

sudo install -d -o build -g build /pkgs
cd /home/build

for pkg in "$@"; do
    echo "==> building $pkg"
    git clone --depth 1 "https://aur.archlinux.org/${pkg}.git"
    # --nocheck: bootc's test suite expects a real root filesystem and fails
    # inside an unprivileged build container.
    ( cd "$pkg" && makepkg -si --noconfirm --noprogressbar --nocheck )
    cp "$pkg"/*.pkg.tar.zst /pkgs/
done
