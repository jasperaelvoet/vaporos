# The VaporOS build environment. Nothing here is compiled: it is a stock
# Arch container plus the handful of repo packages needed to turn a pacstrapped
# tree into an erofs image and a bootable ISO. Built once, then cached.
FROM docker.io/library/archlinux:base

# pacman 7 sandboxes its downloader with seccomp, which fails when x86_64 runs
# translated on an arm64 host ("error restricting syscalls via seccomp").
# The container is already the sandbox.
RUN sed -i '/^\[options\]/a DisableSandbox' /etc/pacman.conf

# The image is built from CachyOS packages (see build/pacman.conf). pacstrap
# verifies them against this container's keyring, resolves their mirrors from
# this container's /etc/pacman.d and installs them with this container's
# pacman, so set it up the way CachyOS's own cachyos-repo.sh does: trust the
# signing key, then install the keyring, the mirrorlists and CachyOS's pacman.
# The tools below stay stock Arch.
RUN pacman-key --init \
    && pacman-key --recv-keys F3B607488DB35A47 --keyserver hkps://keyserver.ubuntu.com \
    && pacman-key --lsign-key F3B607488DB35A47 \
    && printf '[cachyos]\nServer = https://mirror.cachyos.org/repo/$arch/$repo\n' >/tmp/cachyos.conf \
    && cat /etc/pacman.conf /tmp/cachyos.conf >/tmp/pacman.conf \
    && pacman -Sy --noconfirm --config /tmp/pacman.conf \
        cachyos-keyring cachyos-mirrorlist cachyos-v3-mirrorlist \
        cachyos-v4-mirrorlist cachyos/pacman \
    && rm /tmp/cachyos.conf /tmp/pacman.conf

RUN pacman -Syu --noconfirm --needed \
        arch-install-scripts \
        erofs-utils \
        dosfstools mtools \
        libisoburn \
        rsync \
    && pacman -Scc --noconfirm
