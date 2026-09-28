# WaterVaporOS

An immutable, Arch-based Linux distribution built as a **bootable container**
(bootc). Right now it is deliberately bare: kernel, systemd, a shell, and the
machinery needed to install and update itself. Nothing else.

## How it works

The operating system *is* an OCI image. There is no package manager on the
installed machine.

```
Containerfile  ──build──▶  watervaporos:latest  ──┬──▶  ISO ──▶ bootc install to-disk
   (the OS)                  (the OS, as an        │
                              OCI image)           └──▶  bootc upgrade  (in-place update)
```

* **Kernel, firmware, and all of userspace live in the image.** You upgrade the
  kernel by rebuilding the image, not by running pacman on the machine.
* `/usr` is read-only and content-addressed via ostree/composefs.
* `/etc` is three-way merged on each deploy, so your local edits survive.
* `/var` is machine state and is never part of the image.
* The previous deployment stays on disk, so a bad update is one reboot away
  from being undone.

## Building

**Use CI.** Arch is x86_64-only, so on Apple silicon everything runs under qemu
emulation -- the `bootc` compile alone took over 15 minutes locally. The
GitHub Actions workflow builds natively in a fraction of that and publishes the
image to `ghcr.io`, which is where `bootc upgrade` pulls from anyway.

Locally, the OS build is quick *provided* it can pull the prebuilt AUR packages
that CI publishes:

```sh
./scripts/build-image.sh   # the OS itself -> out/image.tar
./scripts/build-iso.sh     # a live installer ISO carrying it -> out/*.iso
```

To build those packages yourself instead (slow under emulation):

```sh
make packages
IMAGE_ARGS='--build-arg PACKAGES_IMAGE=localhost/watervaporos-aur-packages:latest' \
  ./scripts/build-image.sh
```

## Installing

Boot the ISO **in UEFI mode**, then:

```sh
watervapor-install
```

It asks for a target disk, then hands the image to `bootc install to-disk`.

## Updating an installed system

```sh
sudo bootc upgrade && systemctl reboot
sudo bootc rollback              # if the new one misbehaves
```

## Layout

| Path | What it is |
| --- | --- |
| `Containerfile` | The OS definition. Stage 1 builds `bootc`/`bootupd` from the AUR; stage 2 is the OS. |
| `image/` | Files overlaid into the OS image (os-release, tmpfiles, the ostree layout prep). |
| `iso/` | archiso profile overlay: package list, installer script, live-session banner. |
| `scripts/` | Build entry points. |

## Known rough edges

* Arch has no SELinux, so `libsepol`/`libselinux` are built from the AUR purely
  to satisfy bootc's link requirements. They are not enforcing anything.
* The ISO embeds the full OS image, so it is large.
* Not yet verified end to end on real hardware.
