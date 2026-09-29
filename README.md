# VaporOS

An immutable Linux distribution built on CachyOS (and so on Arch). Right now it is deliberately bare:
kernel, systemd, a shell, and the machinery needed to install and update
itself. Nothing else.

## How it works

The whole OS is one read-only erofs image, built from prebuilt CachyOS and Arch
packages: the CachyOS kernel, CachyOS's x86-64-v3 builds of everything they
rebuild, and `cachyos-settings` (zram swap, sysctl and I/O-scheduler tuning,
ananicy-cpp, systemd-oomd). The repos are set up exactly as CachyOS's own
`cachyos-repo.sh` does it (`build/pacman.conf`).
There is no package manager on the installed machine.

```
packages.txt + rootfs/  ──build──▶  root.erofs + vmlinuz + initramfs  ──┬──▶  ISO ──▶ vos install
                                         (+ manifest.env)               └──▶  vos update (A/B)
```

* The disk has two OS slots (`vos_a`, `vos_b`), an ESP and a data partition.
* `/` is the erofs image, read-only.
* `/etc` is an overlay: the image's `/etc` plus your local changes.
* `/var` (and `/home`, `/root`, …) lives on the data partition.
* `vos update` writes the new image to the slot that is not running and boots
  it with systemd-boot boot counting. If it never boots cleanly, the previous
  slot comes back on its own. `vos rollback` switches back by hand.

## Develop

You need ssh key access to a Proxmox host
(`ssh-copy-id root@192.168.1.2`). Then:

```sh
make
```

That is the whole loop. It builds whatever changed, then makes the dev VM run
that build. If the VM does not exist yet, it creates it and installs to its disk
automatically. If it already exists, it runs `vos update` and reboots into the
new slot. When nothing changed, it finishes in seconds.

| Command | |
| --- | --- |
| `make` | build if needed, then install or update the dev VM |
| `make shell` | serial shell in the VM (user `vapor`, password `vapor`; Ctrl-O leaves) |
| `make console` | the VM's display in a native Screen Sharing window |
| `make log` | follow the VM's serial console |
| `make reset` | wipe the dev VM and install it from scratch |
| `make test` | reinstall the dev VM from scratch and check it end to end |
| `make status` / `make destroy` | |
| `make refresh` | rebuild the package base with the newest CachyOS/Arch packages |
| `make release` | smaller image, slower compression |
| `make clean` | remove `out/` and the build volume |

Builds run in a cached Arch container (`build/`) inside a Docker LXC on the
Proxmox host (`vaporos-builder`), which `make` creates the first time. They
cannot run on the Mac: x86-64-v3 packages need AVX2, which Rosetta does not
offer. Nothing is compiled. The result is copied into `out/` and, on the
Proxmox host, served to the VM over HTTP
for `vos update`.

The defaults (Proxmox host `192.168.1.2`, dev VM `9000`, builder LXC `9010`,
`local-lvm`, `vmbr0`, …) are at the top of `scripts/dev.sh`. To override them,
put `KEY=value` lines in a gitignored `.dev.env`. The tooling refuses to touch
a VMID whose name is not its own. `CONSOLE=0 make` never opens a window.

## Installing on real hardware

Boot the ISO **in UEFI mode** (Secure Boot off: the kernel is unsigned) on an x86-64-v3 CPU (Intel Haswell / AMD Zen or newer),
then run `vos install`.

## Layout

| Path | What it is |
| --- | --- |
| `packages.txt` | Every package in the image. |
| `rootfs/` | Files overlaid onto the image, including `vos` and the initramfs hook. |
| `build/` | The build container and the script that runs in it. |
| `scripts/` | `dev.sh` (the dev loop), `build.sh`, and `serial.py` (drives the VM's serial console). |
