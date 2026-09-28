#!/bin/bash
# Reshape the Arch filesystem layout into the one ostree/bootc require:
# everything mutable lives in /var, and the top-level paths are symlinks.
set -euxo pipefail

# /var must be empty in the image; it is populated per-machine at first boot
# by systemd-tmpfiles from /usr/lib/tmpfiles.d.
mv /home/* /var/ 2>/dev/null || true
rm -rf /home /srv /opt /root /usr/local

ln -s var/home  /home
ln -s var/srv   /srv
ln -s var/opt   /opt
ln -s var/roothome /root
ln -s ../var/usrlocal /usr/local

# /boot is a mountpoint on the installed system.
mkdir -p /boot /sysroot

# Anything pacman put in /var is machine state, not image content.
rm -rf /var/*
mkdir -p /var

# bootc reads the machine-id state to decide "first boot"; an empty file means
# "provision me", a missing file breaks systemd.
: > /etc/machine-id
