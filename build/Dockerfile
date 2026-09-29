# The VaporOS build environment. Nothing here is compiled: it is a stock
# Arch container plus the handful of repo packages needed to turn a pacstrapped
# tree into an erofs image and a bootable ISO. Built once, then cached.
FROM docker.io/library/archlinux:base

# pacman 7 sandboxes its downloader with seccomp, which fails when x86_64 runs
# translated on an arm64 host ("error restricting syscalls via seccomp").
# The container is already the sandbox.
RUN sed -i '/^\[options\]/a DisableSandbox' /etc/pacman.conf

RUN pacman -Syu --noconfirm --needed \
        arch-install-scripts \
        erofs-utils \
        dosfstools mtools \
        libisoburn \
        rsync \
    && pacman -Scc --noconfirm
