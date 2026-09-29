# The VaporOS build environment. Nothing here is compiled: it is a stock
# Arch container plus the handful of repo packages needed to turn a pacstrapped
# tree into an erofs image and a bootable ISO. Cached, and rebuilt weekly.
FROM docker.io/library/archlinux:base

# pacman 7 sandboxes its downloader with seccomp, which fails when x86_64 runs
# translated on an arm64 host ("error restricting syscalls via seccomp").
# The container is already the sandbox.
RUN sed -i '/^\[options\]/a DisableSandbox' /etc/pacman.conf

# The ISO week of the build (build/run.sh and CI pass it): a new week rebuilds
# everything below, so the pacman keyrings and mirrorlists pacstrap relies on
# are never more than a week old, even when archlinux:base has not changed.
ARG VOS_WEEK=

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

# jq writes image.json and manifest.json; libcap (setcap/getcap) gives Sunshine
# its capabilities and checks them in the finished image; nftables checks the
# firewall ruleset; openssl verifies the manifest signature independently of
# the Go code that made it. (edid-decode is not in the Arch repos; the EDID is
# checked by the Go tests instead.)
RUN pacman -Syu --noconfirm --needed \
        arch-install-scripts \
        erofs-utils \
        dosfstools mtools \
        e2fsprogs \
        libisoburn \
        rsync \
        jq \
        libcap \
        nftables \
        openssl \
    && pacman -Scc --noconfirm
