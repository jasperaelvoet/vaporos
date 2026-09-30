<p align="center">
  <img src="internal/web/static/icon.svg" width="96" height="96" alt="VaporOS logo">
</p>

<h1 align="center">VaporOS</h1>

<p align="center">
  <strong>Your gaming PC as a headless Steam streaming box.</strong><br>
  An immutable, CachyOS-based appliance for PCs with an AMD GPU. Play with Moonlight on your TV, phone or laptop.
</p>

<p align="center">
  <a href="https://github.com/jasperaelvoet/vaporos/actions/workflows/build.yml"><img src="https://img.shields.io/github/actions/workflow/status/jasperaelvoet/vaporos/build.yml?branch=main&label=build" alt="Build status"></a>
  <a href="https://github.com/jasperaelvoet/vaporos/releases/latest"><img src="https://img.shields.io/github/v/release/jasperaelvoet/vaporos?label=release" alt="Latest release"></a>
</p>

<p align="center">
  <a href="https://jasperaelvoet.github.io/vaporos/"><strong>Website</strong></a> ·
  <a href="https://jasperaelvoet.github.io/vaporos/download/">Download</a> ·
  <a href="https://jasperaelvoet.github.io/vaporos/install/">Install guide</a> ·
  <a href="https://jasperaelvoet.github.io/vaporos/faq/">FAQ</a>
</p>

## What it is

- **A console you stream from.** VaporOS runs Steam (in its gamepad UI) and
  [Sunshine](https://github.com/LizardByte/Sunshine), and you play with
  [Moonlight](https://moonlight-stream.org) on any device on your network.
- **No monitor needed once it's installed.** Games render on a virtual display that takes on each
  Moonlight device's resolution, frame rate and HDR. If a monitor is plugged
  in, it only shows a welcome screen with the web address and a QR code.
- **Managed from your phone.** Setup, pairing, updates, drives and power all
  happen in a web page. There is no terminal to use and no package manager.

## Features

- **Streams at your device's exact resolution, frame rate and HDR.** VaporOS learns
  each device's mode and adds it to the virtual display.
- **Phone-first web UI** at `http://vapor.local`, in four tabs: **Home** (what
  the PC is doing now), **Devices** (pairing and paired devices), **Screen**
  (the virtual display and stream settings: encoder, bitrate, controller type)
  and **System** (updates, power, storage, name and password, SSH, logs).
- **A/B updates with automatic rollback.** A new version is written to the
  other system slot and starts on the next restart. If it fails its health
  check, the PC goes back to the previous version by itself and won't install
  the failed one again. You can also roll back by hand.
- **Signed updates.** Every update carries an ed25519 signature that the
  installed system checks before writing anything, and every file's size and
  SHA-256 are verified before the new version can start.
- **Read-only system.** The OS is one read-only image. Your settings,
  Steam and games live on a separate data partition.
- **CachyOS underneath.** The CachyOS kernel, x86-64-v3 packages and
  `cachyos-settings` tuning.
- **Keeps the games you have.** The installer finds Steam libraries on other
  drives and mounts them for Steam without changing anything on them. You can
  add more drives later.
- **Powers off when nobody plays**, but not while you stream, download or
  update, after a delay you choose. Moonlight wakes it again over
  Wake-on-LAN (see [Wake it](#wake-it)).

## Requirements

| | |
| --- | --- |
| CPU | x86-64-v3: Intel Haswell / AMD Zen or newer |
| GPU | AMD Radeon. NVIDIA and Intel GPUs are not supported for streaming. |
| Firmware | UEFI, with **Secure Boot off** (the kernel is unsigned) |
| Drive | At least 24.5 GiB (about 26.3 GB); 32 GB or larger is recommended. **VaporOS takes the whole drive.** |
| Network | A wired (Ethernet) connection to your home network |
| Monitor | A monitor or TV during the install: it shows the setup code. Not needed afterwards. |
| Also | A USB stick for the installer, and a phone or computer with a browser |

## Install

1. **Download** the ISO (`vaporos-<version>.iso`) from the
   [website](https://jasperaelvoet.github.io/vaporos/download/) or the
   [latest release](https://github.com/jasperaelvoet/vaporos/releases/latest).
2. **Write it to a USB stick** with balenaEtcher, Rufus (DD mode) or `dd`.
3. **Boot the PC from the stick in UEFI mode.** A monitor shows an address
   (`http://vaporos-setup.local`), a QR code and a setup code.
4. **Scan the QR code** or open the address on your phone. The installer asks
   for the setup code, then walks you through the drive, the PC's name and
   admin password, the time zone and existing Steam libraries. Remove the
   stick when it's done and restart.

After the restart, manage VaporOS at **`http://vapor.local`** (or
`http://<name>.local` if you picked another name).

Forgot the admin password? Boot the USB stick again and choose **Repair**. Repair
reinstalls VaporOS, keeps your games, settings and paired devices, and lets you
set a new password.

## Pair Moonlight

1. Install Moonlight: App Store (iPhone, iPad, Apple TV), Google Play
   (Android, Google TV), or [moonlight-stream.org](https://moonlight-stream.org)
   (Windows, Mac, Linux).
2. Open Moonlight on the same network. It usually finds VaporOS by itself.
   If it doesn't, add the PC by the address shown on the **Devices** tab.
3. Moonlight shows a 4-digit PIN. Enter it on the **Devices** tab of the web UI
   (every page asks when a device is waiting).

## Wake it

When VaporOS has powered itself off, Moonlight wakes it: open Moonlight and
pick the PC. A browser can't send the Wake-on-LAN packet, so the web UI can't
wake it. A page that is open when the PC powers off says so, shows how to wake
it, and reconnects by itself once it's back. For another Wake-on-LAN app or a
router that can wake devices, the **Wake it** card under **System › Power**
lists the wired adapter's MAC address and the network's broadcast address,
with Copy buttons. Note them while VaporOS is on.

## Updates and rollback

- An install follows the channel of the ISO it came from: `main` for the latest release, the branch name for prereleases. By default VaporOS downloads new versions in
  the background and starts them on the next restart. You can turn that off
  under **System › Updates** and install by hand.
- **Go back** under **System › Updates** starts the previous version on the
  next restart.
- If a new version doesn't start properly, VaporOS returns to the previous
  one by itself and lists it under "Versions that didn't start".
- Other channels exist, but they carry test builds. They are published as prereleases.

## Verify a download

Each release has `vaporos-<version>.iso`, `manifest.json`,
`manifest.json.sig` and `SHA256SUMS`.

```bash
# The download is intact, not damaged (works on Linux and current macOS)
sha256sum -c --ignore-missing SHA256SUMS

# manifest.json was signed by the release key (needs OpenSSL 3; on macOS
# install it with Homebrew, the built-in LibreSSL cannot check ed25519)
echo 'QxVIrsSODt/z6zcaOySN7oZd16dragAFbCyZhMcFT4E=' > release.pub
printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00' > release.der
base64 -d < release.pub >> release.der
base64 -d < manifest.json.sig > manifest.sig
openssl pkeyutl -verify -pubin -keyform DER -inkey release.der -rawin \
  -in manifest.json -sigfile manifest.sig
```

The checksum catches a damaged download. The signature proves `manifest.json`
came from the release key; it covers the system image files, not the ISO
itself, and `SHA256SUMS` is not signed. The installer only installs a system
image whose manifest carries a valid signature.

The public key is [`keys/release.pub`](keys/release.pub). See
[keys/README.md](keys/README.md) for the key and signature formats.

## FAQ

Common questions (monitors, dual boot, other GPUs, Secure Boot) are answered
on the [website FAQ](https://jasperaelvoet.github.io/vaporos/faq/).

## Development

The internal contracts (disk layout, update format, HTTP API, session
protocol, units) are in [docs/CONTRACTS.md](docs/CONTRACTS.md). Coding agents
start at [AGENTS.md](AGENTS.md).

The OS is built from prebuilt CachyOS and Arch packages (`packages.txt`, with
the repos set up as in `build/pacman.conf`) plus the `rootfs/` overlay and one
static Go binary, `vos`. That binary is the whole control plane: web UI and API,
installer, updater and display manager.

```
packages.txt + rootfs/ + vos  ──build──▶  root.erofs + vmlinuz + initramfs.img  ──┬──▶  ISO ──▶ web installer
                                              (+ manifest.json, signed)           └──▶  vos update (A/B)
```

### The dev loop

You need SSH key access to a Proxmox host (`ssh-copy-id root@192.168.1.2`).
Then run:

```sh
make
```

This builds whatever changed, then brings the dev VM to that build. If the VM doesn't exist yet,
it creates it and installs through the web installer's API. Otherwise it runs
`vos update` and reboots into the new slot.

| Command | |
| --- | --- |
| `make` | build if needed, then install or update the dev VM |
| `make shell` | serial shell in the VM (Ctrl-O leaves) |
| `make console` | the VM's display in a native Screen Sharing window |
| `make log` | follow the VM's serial console |
| `make reset` | wipe the dev VM and install it from scratch |
| `make test` | reinstall from scratch and check it end to end (API, hardening, update, rollback, health fallback) |
| `make go-test` | `go vet` and `go test` locally, with no VM and no builder |
| `make status` / `make destroy` | |
| `make refresh` | rebuild the package base with the newest CachyOS/Arch packages |
| `make release` | smaller image, slower compression |
| `make clean` | remove `out/` and the build volume |

Images are assembled in a cached Arch container (`build/`). The container runs in a Docker LXC on the
Proxmox host (`vaporos-builder`, created on first use). Only the Go binary is
compiled. x86-64-v3 packages need AVX2, which Rosetta doesn't offer, so the
image can't be built on a Mac.

The defaults (Proxmox host `192.168.1.2`, dev VM `9000`, builder LXC `9010`,
`local-lvm`, `vmbr0`, …) are at the top of `scripts/dev.sh` and
`scripts/build.sh`. To override them, put `KEY=value` lines in a gitignored `.dev.env`. The
tooling refuses to touch a VMID whose name isn't its own. `CONSOLE=0 make`
never opens a window.

CI (`.github/workflows/build.yml`) builds and tests every push in QEMU. When the
signing key is configured, it publishes each push: a GitHub release (`main` is
latest, other branches are prereleases) and the OS as an OCI artifact on
`ghcr.io/jasperaelvoet/vaporos`, tagged with the version and the branch.
The website (`website/`) deploys to GitHub Pages through `pages.yml`.

### Layout

| Path | What it is |
| --- | --- |
| `cmd/vos/` | The multi-call `vos` binary. |
| `internal/` | Its packages: daemon, API, web UI, installer, updater, display, Sunshine, storage, power. |
| `packages.txt` | Every package in the image. |
| `rootfs/` | Files overlaid onto the image: units, initramfs hook, firewall, os-release. |
| `build/` | The build container and the scripts that run in it. |
| `scripts/` | `dev.sh` (the dev loop), `build.sh`, `serial.py` (drives the VM's serial console). |
| `tests/` | The QEMU smoke test CI runs, and the in-VM checks. |
| `keys/` | The public release key. |
| `design/` | The one source for the look: tokens, logo, icons and fonts, for the web UI, the website and the welcome screen. |
| `tools/` | Development tooling for the web UI (Node) and the fonts (Python). None of it is in the OS image. |
| `website/` | The project website (Next.js). |
| `docs/CONTRACTS.md` | Interfaces between the pieces. |

## License

No license yet. VaporOS is not affiliated with Valve.
