# syntax=docker/dockerfile:1
#
# WaterVaporOS -- a bare, immutable, Arch-based bootable container image.
#
# The bootc/bootupd packages come prebuilt from Containerfile.packages; see
# PACKAGES_IMAGE below. Everything else is assembled here.

ARG PACKAGES_IMAGE=ghcr.io/jasperaelvoet/watervaporos/aur-packages:latest
FROM ${PACKAGES_IMAGE} AS aurpkgs

FROM docker.io/library/archlinux:base AS os

ARG OS_NAME="WaterVaporOS"
ARG OS_ID="watervapor"
ARG OS_VERSION="0.1.0"

# pacman 7's seccomp sandbox cannot initialise under qemu-user emulation
# ("error restricting syscalls via seccomp: 22"), which is how an x86_64 Arch
# image builds on an arm64 host. The build is already isolated by the container.
RUN echo 'DisableSandbox' >> /etc/pacman.conf

COPY --from=aurpkgs /pkgs /tmp/pkgs

# Base system. dracut (not mkinitcpio) because ostree ships a dracut module
# that knows how to boot a composefs/ostree root.
RUN pacman -Syu --noconfirm --needed \
        linux linux-firmware \
        dracut \
        ostree composefs skopeo \
        systemd systemd-sysvcompat \
        e2fsprogs dosfstools xfsprogs btrfs-progs \
        efibootmgr \
        iproute2 iputils openssh sudo \
        nano vim less which man-db \
    && pacman -U --noconfirm /tmp/pkgs/*.pkg.tar.zst \
    && rm -rf /tmp/pkgs

COPY image/ /

# ostree/bootc expect the mutable state to live under /var. Arch's
# `filesystem` package ships these as real directories, so convert them.
RUN /usr/lib/watervapor/ostree-prep.sh

# Build the initramfs into the location bootc looks for it.
RUN KVER="$(basename "$(ls -d /usr/lib/modules/*/)")" && \
    dracut --force --no-hostonly --reproducible --zstd \
        --add "ostree" \
        --kver "$KVER" \
        "/usr/lib/modules/$KVER/initramfs.img" && \
    rm -f /usr/lib/modules/"$KVER"/vmlinuz-fallback

RUN sed -e "s/@OS_NAME@/${OS_NAME}/g" \
        -e "s/@OS_ID@/${OS_ID}/g" \
        -e "s/@OS_VERSION@/${OS_VERSION}/g" \
        -i /usr/lib/os-release && \
    ln -sf ../usr/lib/os-release /etc/os-release

# Final tidy + the ostree commit metadata that makes this a bootable container.
RUN pacman -Scc --noconfirm && \
    rm -rf /var/cache/pacman/pkg/* /var/lib/pacman/sync/* && \
    bootc container lint

LABEL containers.bootc="1"
LABEL org.opencontainers.image.title="${OS_NAME}"
LABEL org.opencontainers.image.version="${OS_VERSION}"
