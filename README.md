# VaporOS

An immutable, Arch-based Linux distribution. Right now it is deliberately bare:
kernel, systemd, a shell, and the machinery needed to install and update
itself. Nothing else.

## How it works

The whole OS is one read-only erofs image, built from prebuilt Arch packages.
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

You need OrbStack (or any Docker) and ssh key access to a Proxmox host
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
| `make test` | end-to-end install test on a separate throwaway VM |
| `make status` / `make destroy` | |
| `make refresh` | rebuild the package base with the newest Arch packages |
| `make release` | smaller image, slower compression |
| `make clean` | remove `out/` and the build volume |

Builds run in a cached Arch container (`build/`). Nothing is compiled, and a
rebuild that only touches `rootfs/` takes about 15 seconds. Proxmox only runs
the VM: each build is rsynced to it (as a delta) and served to the VM over HTTP
for `vos update`.

The defaults (Proxmox host `192.168.1.2`, dev VM `9000`, test VM `9001`,
`local-lvm`, `vmbr0`, …) are at the top of `scripts/dev.sh`. To override them,
put `KEY=value` lines in a gitignored `.dev.env`. The tooling refuses to touch
a VMID whose name is not its own. `CONSOLE=0 make` never opens a window.

## Installing on real hardware

Boot the ISO **in UEFI mode** (Secure Boot off: the Arch kernel is unsigned),
then run `vos install`.

## Layout

| Path | What it is |
| --- | --- |
| `packages.txt` | Every package in the image. |
| `rootfs/` | Files overlaid onto the image, including `vos` and the initramfs hook. |
| `build/` | The build container and the script that runs in it. |
| `scripts/` | `dev.sh` (the dev loop), `build.sh`, and `serial.py` (drives the VM's serial console). |
