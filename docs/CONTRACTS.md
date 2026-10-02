# VaporOS internal contracts

This file is the single source of truth for every interface between the
pieces of VaporOS: files on disk, the HTTP API, the session protocol,
the update format, the kernel command line, unit names and the serial
lines the test harness reads. Change a contract here first, then in code.

## Product in one paragraph

VaporOS is an immutable, CachyOS-based appliance that turns a PC with an AMD
GPU into a headless Steam streaming box. The root filesystem is a read-only
erofs image in one of two A/B slot partitions. `/etc` is an overlay and
`/var` lives on the data partition. A Go daemon, `vosd` (the `vos` binary,
subcommand `daemon`), serves a web UI on port 80. That UI manages Sunshine,
pairing, updates, storage and power, and it runs the installer when booted
from the ISO. gamescope renders Steam on a forced *virtual* connector, and
Sunshine captures it with KMS. The virtual display follows each Moonlight
client's resolution, fps and HDR. A physical monitor only ever shows a
welcome screen (URL + QR), drawn by `vos welcome`, and never a terminal.

## Binary

There is one static Go binary, `/usr/bin/vos` (module
`github.com/jasperaelvoet/vaporos`, `CGO_ENABLED=0 GOAMD64=v3`). It is
multi-call:

| argv | What runs |
| --- | --- |
| `vos daemon` | `vosd`: web UI/API, policy, updates, display manager |
| `vos welcome` | welcome screen renderer (DRM master, dumb buffers) |
| `vos session begin` / `vos session end` | Sunshine prep-cmd hooks (run as `vapor`) |
| `vos session launch <steam-url>` | a game's Sunshine `detached` command (as `vapor`, never root): waits up to 90 s for gamescope's Wayland socket and a Steam holding `~/.steam/steam.pipe` (else, at the deadline, a `steam` process inside gamescope), then runs `steam <url>` with that Steam's `DISPLAY`, `WAYLAND_DISPLAY`, `GAMESCOPE_WAYLAND_DISPLAY`, `XDG_RUNTIME_DIR`, `XAUTHORITY`, `DBUS_SESSION_BUS_ADDRESS`. `steam://` URLs only; never starts a Steam of its own; always exits 0 |
| `vos install [flags]` | CLI install (dev and tests); the web installer uses the same code |
| `vos update [--from SRC] [--force] [--stage-only]` | fetch, verify and write the idle slot |
| `vos rollback` | next boot uses the other slot (rolling back to an older one holds the version left, see "Update state") |
| `vos status [--json]` | version, booted slot, both slots, staged/failed/held |
| `vos health` | boot health check (vos-health.service, see "Health") |
| `vos edid generate --out FILE [--modes-from FILE]` / `vos edid decode FILE` | EDID generator |
| `vos sign --key FILE\|env:VAR MANIFEST` / `vos keygen --out PREFIX` | ed25519 manifest signing |
| `vos ext validate DESCRIPTOR...` | build: load and check each source descriptor (`extension.json`: schema, every field, no `build` section); one problem per line on stderr as `<file>: <problem>`, exit 1 on any |
| `vos ext check-tree --id ID --descriptor FILE --tree DIR --base DIR [--other DIR]... [--json OUT]` | build: check an extension's image tree against its descriptor, the base and the extensions built before it; one problem per line on stderr (`<id>: <problem>`, warnings as `<id>: warning: <text>`), exit 1 on any. `--json` (on success) writes `{"permissions","runs_as_root","warnings"}` |
| `vos ext catalog --stage DIR --out DIR` | build: from `ext-<id>.raw`, `<id>.json`, `<id>.build.json` (check-tree's `--json`) and the optional `<id>.key` and `<id>.packages.txt` in DIR, write `extensions.list`, `extensions.json` (the manifest's `extensions` object) and `descriptors/<id>.json` (with the `build` section) |
| `vos ext digest FILE...` | prints `<fs-verity digest>  <file>` per file |
| `vos ext fetch [--from SRC] [--version V] [--state-dir DIR] [--seed [--repair]] [ids...]` | fetch and seal into the store the extension images of version V (default: the booted image's) from SRC (default: config.json's `update.source`; a registry at the tag V, replacing any tag in SRC). It reads and verifies V's signed manifest as `vos update` does and refuses a source that serves another version, then fetches the images the store lacks of `ids` (default: `wanted` ∪ the manifest's core; with `--seed` core is always added), with their requirements, as vosd does (see Extensions). `--state-dir` uses DIR as `/var/lib/vos` (the installer's target). `--seed` (needs `--from`) then writes `slots/a.json` from the manifest (under the update lock) and, under the store lock, `wanted` (the ids given less core; without ids an existing `wanted` stays, else it is written empty) and a new `enabled` set, without a trial, of the target ids whose image sealed and whose requirements did; `--repair` also removes `slots/b.json`, `pending` and `failed`. Prints `{"bytes":N,"total":N}` lines on stdout (the bytes of the missing images, never going down); exit 0 when every image is sealed, 1 when one is not or anything else fails (reasons on stderr; `--seed` still seeds what sealed), 2 on bad arguments |
| `vos ext launch [--app N\|--shortcut ID/KEY] [--] CMD [ARGS...]` | Steam launch dispatcher (see Extensions). Its options end at `--` or at the first other word, CMD, after which nothing is read: N is a Steam app id (decimal, 1–4294967295), ID/KEY an extension id and one of its shortcut keys, and at most one of them is given. Until the dispatcher hooks anything it execs CMD (looked up in `PATH` unless absolute; `argv[0]` as given) with ARGS and the environment unchanged; `--shortcut` refuses instead (exit 1, reason on stderr) unless `/run/vos/extensions.json`, which it reads as `vapor`, lists ID as mounted; `--app` always runs CMD. Exit 2 on bad arguments, 1 when it refuses or CMD cannot be run |
| `vos-generator` (argv[0], systemd generator symlink) | mount units and SSH from config.json; wants for the services of mounted extensions and the trial drop-in (see Units) |
| `vos version` | prints the version |

The build embeds the version with `-ldflags "-X main.version=… -X main.commit=…"`.

## Paths

| Path | Owner | What |
| --- | --- | --- |
| `/usr/lib/vos/image.json` | build | `{"version","channel","git","kernel","rollback_index","debug":bool}` for the running image |
| `/usr/lib/vos/cmdline` | build | kernel args every VaporOS entry carries (one line) |
| `/usr/lib/vos/keys/*.pub` | build | trusted update-signing keys (base64 raw 32-byte ed25519 public key, one line) |
| `/usr/lib/firmware/edid/vaporos.bin` | build (`vos edid generate`) | the default virtual-display EDID |
| `/usr/share/vos/` | build | templates (sunshine.conf defaults, apps.json), static assets |
| `/var/lib/vos/config.json` | vosd | machine configuration (schema below) |
| `/var/lib/vos/auth.json` | vosd, installer | `{"user":"admin","hash":"$argon2id$…"}`, mode 0600 |
| `/var/lib/vos/sessions.json` | vosd | web sessions `{sha256(token): {csrf, expires}}`, mode 0600 |
| `/var/lib/vos/update-state.json` | vosd, `vos update` | see "Update state" |
| `/var/lib/vos/clients.json` | vosd | learned Moonlight client modes, one entry per client and mode: `{"<name> WxH@R": {name,w,h,fps,hdr,last_seen}}` (a file keyed by name alone, without `name`, is read as that client's one mode), at most 64, the least recently seen dropped first; every entry's mode stays in the learned EDID (newest first, up to the 30 extra modes) until DELETE `/display/modes/{mode}` removes it |
| `/var/lib/vos/sunshine-api.json` | vosd | `{"user","password"}` for Sunshine's local API, mode 0600 |
| `/var/lib/vos/cmdline` | installer, vosd | machine-specific kernel args (boot disk, virtual connector + EDID) |
| `/var/lib/vos/steam-libraries.json` | vosd | `{"pending":["/var/mnt/<label>[/SteamLibrary]"]}`: adopted libraries still to be added to Steam's library list, which vosd changes only while Steam is not running |
| `/var/lib/vos/firmware/edid/vaporos.bin` | vosd | EDID with learned modes; overrides the image one via `firmware_class.path=/var/lib/vos/firmware`. vosd also hands each new version to the running kernel, best effort: it writes `/sys/kernel/debug/dri/<minor or PCI address>/<C>/edid_override`, re-probes the connector by switching its sysfs `status` to `on-digital` and back to `on`, and sends a `change` uevent with `HOTPLUG=1`. It counts only when the connector's sysfs `edid` then matches. vosd skips this during a stream (it applies at `session.end`) and when the new EDID drops the mode on screen. If the kernel refuses, or gamescope does not reach a mode added this way within 15 s, vosd stops trying until the next boot and the mode applies after a reboot |
| `/var/lib/vos/health-ok` | `vos health` | JSON `{"gpu":bool,"stream":bool,"lan":bool,"lan_mac":"<address>"}` from the last good boot; `lan_mac` is the `address` of the network device that had the LAN (omitted when unknown) |
| `/usr/lib/vos/extensions.list` | build | the image's extension catalog (see Extensions) |
| `/usr/share/vos/extensions/<id>.json` | build | each extension's descriptor, with what the build verified |
| `/var/lib/vos/ext/` | vosd, initramfs, `vos health`, `vos ext fetch` | the extension store, sets and trial state (see Extensions) |
| `/run/vos/extensions.json` | initramfs | which extensions this boot mounted, and why others were skipped |
| `/run/vos/ext-trial-ok` | `vos health` | the number of the set whose extension trial passed health this boot (one line, written atomically), so vosd can promote it when health could not (see Health) |
| `/run/modprobe.d/vos-ext.conf` | initramfs | kernel module options of the mounted extensions |
| `/run/systemd/system.conf.d/50-vos-trial.conf` | initramfs | `[Manager]` `RuntimeWatchdogSec=60s`, on trial boots only (see Extensions, Trial and promotion) |
| `/run/vos/session.sock` | vosd | session protocol, mode 0660 root:vapor |
| `/run/vos/welcome.json` | vosd | what the welcome screen shows (below), mode 0600 (it holds the setup code) |
| `/run/vos/medium/vos/` | initramfs (live) | ISO contents: root.erofs, vmlinuz, initramfs.img, manifest.json(.sig) |
| `/run/vos/host` | initramfs (live) | read-only mount of the exFAT, FAT or NTFS partition the ISO is an `.iso` file on (Ventoy); absent when the ISO is a device |
| `/efi` | fstab automount of `PARTLABEL=vos_esp` | ESP (systemd-boot, `/vos/<ver>/{vmlinuz,initramfs.img}`, `loader/entries/vos-<ver>[+N[-M]].conf`). The initramfs gives the boot disk's partitions udev `link_priority=100` (`/run/udev/rules.d/61-vos-boot-disk.rules`); vos refuses an ESP that is not on the disk `/` is on |
| `/var/home/vapor` | tmpfiles | gaming user home (Steam, Sunshine config/state) |
| `/var/mnt/<label>` | generator | adopted game library disks (`/mnt` → `var/mnt`), mounted `nofail,noatime,nosuid,nodev,x-systemd.device-timeout=10s` (+`uid=1000,gid=1000` for ntfs3) |

Tests override these through the package variables in `internal/config`.

## Users

- `vapor` (uid/gid 1000) is created by sysusers. It is in groups `input render video seat audio`, has no password, lingers, and its home is `/var/home/vapor`. It runs gamescope, Steam, Sunshine and PipeWire as user units.
- `vosd` (root) reads and writes under `/var/home/vapor` and `/run/user/1000` only through `internal/gamerfs`: no symlink is followed in any component, every step is relative to a directory descriptor, and only regular files are read (bounded, non-blocking).
- `root` is locked.
- The web admin is `admin`, with the password in `auth.json` only (not a Unix account). Passwords are 8–1024 characters (`auth.ValidatePassword`), everywhere a password is set.
- Debug images (`image.json.debug=true`) autologin root on the ttyS0 serial console. Release images have no gettys at all.

## config.json (schema 1)

```json
{
  "schema": 1,
  "update":  {"source": "oci://ghcr.io/jasperaelvoet/vaporos", "channel": "main", "auto": "stage"},
  "power":   {"idle_shutdown": false, "idle_minutes": 15},
  "display": {"virtual_connector": "DP-1", "hdr": true, "extra_modes": ["2796x1290@120"]},
  "storage": {"libraries": [{"uuid": "…", "label": "SATA1TB", "mountpoint": "/var/mnt/SATA1TB", "fstype": "ext4"}]},
  "ssh":     {"enabled": false, "keys": []},
  "web":     {"https": false, "allow_public": false}
}
```
A missing file or field means the default. `power.idle_shutdown` defaults to false; a new install sets it to true when a wired NIC supports Wake-on-LAN (magic packet, `power.WakeOnLANCapable`), so a PC is never switched off with no way to wake it remotely; a repair keeps the existing value. Services change the shared config only through `config.Mutate` and read it through `Snapshot`/`View`. `update.channel` defaults to
`image.json.channel`. `auto` is `"stage"` (download and stage; applies on
next boot) or `"off"`.

## Kernel command line

A boot entry's `options` are built as: `vos.slot=<a|b>` + image cmdline + machine cmdline.
- **Image cmdline** (`/usr/lib/vos/cmdline`, and `manifest.cmdline` for a new image):
  `quiet loglevel=3 rd.udev.log_level=3 systemd.show_status=false rd.systemd.show_status=false vt.global_cursor_default=0 systemd.getty_auto=0 panic=10 console=ttyS0,115200`
- **Machine cmdline** (`/var/lib/vos/cmdline`): `vos.disk=<GPT disk GUID of the install disk, lowercase>`
  (written by the installer; the initramfs and `vos update` look for `vos_*` partitions only on that disk;
  without it the initramfs goes by `/dev/disk/by-partlabel` and `vos update` by the disk `/` is on, then the label),
  plus, on a machine with a supported GPU,
  `video=<C>:e drm.edid_firmware=<C>:edid/vaporos.bin firmware_class.path=/var/lib/vos/firmware`
- **Live ISO entry:** `vos.mode=live vos.label=VOS_LIVE vos.version=<version>` + the image cmdline.
  The initramfs mounts the device labelled `vos.label` (a written stick or a CD; the first 3 s are its alone),
  or else the first `*.iso` file, on any exFAT, FAT or NTFS partition, whose ISO 9660 label is `vos.label`
  and whose `/vos/manifest.json` has `version` = `vos.version` (Ventoy in normal mode, which cannot hook into
  systemd-boot). It waits 30 s for either, then reboots.
- **Test knobs:** `vos.health.fail=1` makes `vos health` fail on a counted boot or an extension trial (ignored otherwise). `vos.ext=0` mounts no extension. `vos.debug=1` is reserved.

`vos update` writes the new entry with the *new* manifest's cmdline. `vosd`
rewrites both slots' entries when the machine cmdline changes
(`update.ApplyMachineCmdline`: under the update lock, entries first and the
file last, keeping `vos.disk` unless the new cmdline names one).

## Disk layout (unchanged from bash `vos`, with slot sizing updated)

GPT:
1. `vos_esp` (vfat, 512 MiB, label `VOS_ESP`)
2. `vos_a`
3. `vos_b`
4. `vos_data` (ext4, label `vos_data`, rest of the disk)

Slot size: `clamp(3 × image size, 8 GiB, 16 GiB)`. The minimum disk is
`512 MiB + 2×slot + 8 GiB`. On `vos_data`: `var/`, `etc/{upper,work}`.
Mounting is done by the initramfs hook (erofs slot ro at `/`, data at `/state`,
`/var` bind, `/etc` overlay `index=off`, then the mounted extensions as a
read-only overlay on `/usr`, see Extensions). vos_data is ext4 with the
`verity` feature (`mkfs.ext4 -O verity -b 4096`; older installs get it at boot, or from a repair install's
`tune2fs -O verity` after `e2fsck`). It finds the partitions by GPT name
on the `vos.disk` disk (and reboots if that disk does not appear). Before any
reboot it takes, it renames an uncounted entry of the failing slot to
`+0-1` when the other slot has a bootable entry.

`loader.conf`:
```
timeout 0
editor no
auto-entries no
auto-firmware no
console-mode keep
```

Entry `vos-<ver>[+3].conf`:
```
title VaporOS
version <ver>
sort-key vapor
linux /vos/<ver>/vmlinuz
initrd /vos/<ver>/initramfs.img
options vos.slot=<s> <cmdline>
```

## Update format

The build writes `manifest.json` next to `root.erofs`, `vmlinuz` and `initramfs.img`:
```json
{"schema":1,"product":"vaporos","version":"20260929.123456","rollback_index":1790690000,
 "channel":"main","git":"<sha>","created":"2026-09-29T12:34:56Z","kernel":"7.2.8-1-cachyos",
 "cmdline":"quiet …","min_updater":1,
 "artifacts":{"root":{"name":"root.erofs","size":123,"sha256":"…"},
              "kernel":{"name":"vmlinuz","size":123,"sha256":"…"},
              "initrd":{"name":"initramfs.img","size":123,"sha256":"…"}}}
```
- `version` is the UTC build or commit time, `YYYYMMDD.HHMMSS`.
- `rollback_index` is the same instant as unix seconds (monotonic across dev and CI).
- The signature is `manifest.json.sig`: base64 of the ed25519 signature over the exact bytes of `manifest.json`.
- Private keys are base64 of the 64-byte ed25519 private key.
- The release public key is `keys/release.pub` in the repo. Debug builds also trust `dev.pub`; the dev key lives on the builder LXC and is never in git.
- The release key signs only in CI's `sign` job, which runs no package code; `build/build.sh` refuses to run with a release key in reach. Publishing waits for the VM test.
- A channel tag only ever moves to a manifest with a higher `rollback_index`. Cleanup never deletes a version a channel tag points to and keeps the newest 10 version-tagged versions per channel.

**Sources** (`vos update --from`, or `config.update.source` + `channel`):
- `oci://ghcr.io/jasperaelvoet/vaporos` with a tag, which is the channel (branch name, `main` by default). Pulled anonymously:
  1. `GET https://ghcr.io/token?scope=repository:jasperaelvoet/vaporos:pull&service=ghcr.io`
  2. manifest with `Accept: application/vnd.oci.image.manifest.v1+json`
  3. blobs by `org.opencontainers.image.title` annotation: `manifest.json`, `manifest.json.sig`, `root.erofs`, `vmlinuz`, `initramfs.img`, and each extension image `ext-<id>.raw`. Follow the 307 redirect; resume with `Range`.
  4. an extension image also with no tag at all, as the blob `/v2/<repo>/blobs/sha256:<sha256>`, which works while any tag still holds it (token, redirect and resume as above).
- `http(s)://host/dir/` or a local dir: the same files by name.

**Acceptance:**
- The signature verifies against any key in `/usr/lib/vos/keys`.
- `schema` = 1 and `min_updater` ≤ 1.
- `version` ≠ booted version, unless `--force`.
- `rollback_index` > booted image's, unless `--force` or `--allow-downgrade`.
- Without a picked version or `--force`: `rollback_index` > `held.rollback_index`.
- The version is not in `failed`, unless `--force`.
- Every artifact's size and sha256 match, and every extension image's size, sha256 and fs-verity digest.
- Staging refuses (unless `--force`) while the running entry is on trial, or while it is marked bad and the idle slot's entry is bootable (a rollback waiting for a restart). Neither is recorded as `last_error`.

**Write order:**
0. Fetch kernel and initrd, then fetch and seal (`store.Put`) the extension images the new image needs (`wanted` ∪ its core, with requirements, as its manifest lists them) that the store lacks, before anything is unhooked. Before fetching, the data partition must have those images' bytes plus 2 GiB free, else the stage fails. On a boot whose report has reason `no-verity` no image is fetched, counted or given space (they could not be sealed), and a seal refused as unsupported stops the fetching: either way the new image then starts without them (logged). Any other failure fails the stage.
1. Under ext.lock: remove the idle slot's entries, then write `ext/slots/<idle>.json` from the new manifest (an empty `extensions` object when it has none). An image of step 0 that GC removed in between (no slot file named it yet) is fetched again.
2. Stream root into the idle slot partition while hashing.
3. Re-read and verify.
4. Kernel and initrd to `/efi/vos/<ver>/` (tmp + rename).
5. Write entry `vos-<ver>+3.conf` last. The running entry stays a fallback: `+0-1` for a downgrade or the same version, re-blessed if it was at `+0` and the new version is newer. systemd-boot's NVRAM overrides (`LoaderConfigTimeout`, `LoaderEntryDefault`, `LoaderEntryPreferred`) are cleared; the installer clears them after `bootctl install` too.
6. Record `staged` in update-state (and `held` for a downgrade).

**Update state** (`/var/lib/vos/update-state.json`):
```json
{"booted":"<ver>","staged":{"version":"<ver>","slot":"b","at":"RFC3339"},"failed":["<ver>"],
 "available":{"version":"<ver>","size":123,"checked":"RFC3339"},"checked":"RFC3339","last_error":"",
 "held":{"version":"<ver>","rollback_index":N}}
```
`available.size` is the root plus the extension images this machine would
still have to fetch for it (Write order step 0; none on a boot with reason
`no-verity`).
On daemon start: if `staged.version` ≠ booted, and the staged entry has no
tries left (or is gone), append it to `failed` and clear `staged`. If it
equals booted, clear `staged` once the boot is no longer on trial.
`held` (omitted when unset) is the newest version the user went back from: set by
rolling back to an older slot or staging an older version; cleared by rolling
forward to it (or past it), by staging it or a newer one, or once a version at
or above it passes health.

## Health

`vos-health.service` is `RequiredBy=boot-complete.target`, `Before=boot-complete.target`, `Type=oneshot`, and runs on every installed boot. It runs `vos health`, whose checks are:
- `/state` is mounted rw and `/etc` is an overlay;
- `vosd` answers `GET http://127.0.0.1/api/v1/ping` within 60 s;
- `user@1000.service` is active;
- if `health-ok.gpu`, a DRM card with an amdgpu (or other supported) driver exists;
- if `health-ok.stream`, `vos-sunshine.service` is active;
- if `health-ok.lan`, the LAN is up within 60 s of the start (the check runs alongside the others): some network device (an interface with `/sys/class/net/<if>/device`; not `lo`, bridges or tunnels) that is up (`IFF_UP` in its `flags`; without `flags`, a readable `carrier`) with `carrier` 1 has an IPv4 address that is neither loopback nor link-local, or a global or unique local IPv6 one. If none has by the deadline, it fails when the device whose `address` is `health-ok.lan_mac` is gone, or when a network device is down or has a link without such an address (NetworkManager brings up every device, also without a cable); when every network device is up with `carrier` 0, or there is none (nothing is plugged in), it passes and keeps `health-ok.lan` and `lan_mac`;
- `vos.health.fail=1` forces a failure.

A check that `health-ok` does not require is still looked at once (the LAN
check: what it found by the time the others are done), and the result goes
into the new `health-ok`.

It exits non-zero only on a counted boot when another entry would boot once
this one runs out of tries, and on an extension trial (`/run/vos/extensions.json`
mode `pending`; see Extensions); otherwise a failing check is logged as degraded
and it exits 0. It writes `health-ok` unless it fails, and prints the
`VOS-HEALTH` serial line on every outcome. `FailureAction=reboot`, and
systemd-boot counting (or the trial's tries) does the rest. After an
extension trial that passes, it first writes the booted set's number to
`/run/vos/ext-trial-ok` (temp + rename), so vosd can still promote the set
should the next step not get the lock. After a good boot whose report
mounted anything on purpose (mode other than `off`), it takes ext.lock
(waiting up to 30 s) and records the boot (proven images, promotion; see
"Trial and promotion"); a problem there is logged and never fails the boot.

## Extensions

Extensions add software that is not part of the VaporOS image (Proton,
CoolerControl, TruckersMP, ...). They are curated: each is a directory in
`extensions/<id>/` of this repository, built by the same build as the image it
belongs to, against that image's exact packages, and listed in its signed
manifest. Each extension is itself immutable: one sealed, read-only image.

**Source** (`extensions/<id>/`): `extension.json`, the descriptor (schema 1,
unknown fields rejected; `internal/extensions/descriptor`), and `files/usr/...`,
copied into the image. Integration logic is Go in `vos`
(`internal/extensions/<id>`). Non-goals: no `/opt` or `/usr/local` payloads, no
AUR or DKMS, no `.ko`, no `sysusers.d`, no confext, no plugin stores that run
code as root.

**Image** (`ext-<id>.raw`): an erofs whose only top-level directory is `usr/`,
made with `mkfs.erofs -T0 --all-root -U <uuid>`, compressed as the root is
(zstd in release builds); `<uuid>` is the name-based SHA-1 UUID (version 5) of
`vaporos-ext-<id>` in the URL namespace. It holds the extension's packages
(resolved from the base's own sync snapshot), less what their scriptlets and
hooks leave behind (anything outside `usr/`, and files of the base or a
requirement they redo, such as caches; the build warns when the extension
ships inputs for such a cache: icons, mime packages, schemas, GIO or pixbuf
modules), its `fetch[]` files (in `usr/lib/vos/ext/<id>/`, each noted with
its licence, URL and sha256 in `usr/share/licenses/<id>/fetched.txt`; a
`license_file`, the archive member holding the licence text, goes beside it
as `usr/share/licenses/<id>/<its base name>`), its `files/`, and
`usr/lib/vos/ext/<id>/{extension.json,packages.txt,module-options}`.
Installing the packages may only add to the base and the requirements: the
build fails if it upgrades, downgrades, reinstalls, replaces or removes a
package they have, or removes a path under `usr/` (a whiteout or opaque
directory in the overlay it installs into).
`module-options` lists the `module param` pairs the extension may set (one per
line). Its identity is its fs-verity digest: `fsverity digest --hash-alg=sha256
--block-size=4096`, no salt, as 64 hex digits. The build fails an image that:
ships anything outside `usr/`, or whose packages own a file there; ships a
path the base ships, or one another extension ships unless both are the same
file (bytes and mode, or symlink target); has a directory (even an empty one,
wherever it is) where either has a file or symlink, or one that merges with a
directory of theirs but has another mode or owner (the merged directory takes
the image's); writes under
`usr/lib/systemd`, `usr/lib/udev`, `usr/share/dbus-1`, `usr/share/polkit-1`,
`usr/lib/security`, `usr/share/vulkan`, any other `*.d/` hook directory (one
not reviewed as harmless) or `usr/lib/vos/**` except
`usr/lib/vos/ext/<id>/**`, or anything under `usr/share/vos`, beyond the categories its
descriptor declares in `permissions` (`service`, `user-service`, `udev`,
`sysctl`, `modules`, `tmpfiles`, `polkit`, `dbus`, `compat-tool`), with any
difference between declared and found failing too; ships `sysusers.d`, `*.ko`,
`hwdb.d`, `ld.so.conf.d`, credentials (`usr/lib/credstore`,
`usr/lib/credstore.encrypted`: systemd imports sysctl, tmpfiles, sysusers and
SSH key settings from them), firmware (`usr/lib/firmware`, `updates/`
included), trust anchors and PKCS#11 modules (`usr/share/p11-kit`,
`usr/lib/pkcs11`, `usr/share/ca-certificates`), GIO modules, pixbuf loaders or
GSettings schemas (`usr/lib/gio/modules`, `usr/lib/gdk-pixbuf-2.0`,
`usr/share/glib-2.0/schemas`, whose caches are the base's, so they would
silently not load), network configuration (NetworkManager, networkd,
nftables, `net.*` sysctls), setuid/setgid files or file capabilities,
whiteouts or `trusted.overlay.*` xattrs; sets a sysctl key the base or another
extension sets; has a `tmpfiles.d` line (read as systemd-tmpfiles reads it,
with only the `%S %C %L %t %T %V %%` specifiers) whose path is outside the
extension's own areas (`/var/lib/vos/ext/data/<id>`,
`/var/home/vapor/.local/share/vaporos/ext/<id>`, `/run/<id>`,
`/var/cache/<id>`, `/var/log/<id>`), whose `L` target or `C` source is in
neither those nor `/usr`, whose mode sets setuid or setgid, or whose type is
`c` or `b` (device nodes) or `t` or `T` (extended attributes); has a unit,
alias or drop-in directory whose name ends in `-` before `@` or its type (a
systemd prefix drop-in applies to every unit with that prefix), a unit or alias
the base has (in `usr/lib/systemd/<scope>` or `etc/systemd/<scope>`) by name or
template, a template the base has an instance of, a drop-in that is a symlink,
or two drop-ins of the same name for one unit in directories whose order
systemd leaves open (an instance's hides its template's); ships a system unit
that runs commands (a service, a socket with `Exec*=` commands, a mount or a
swap) whose last `TimeoutStartSec=` or `TimeoutSec=` (only `TimeoutSec=` in
`[Socket]`, `[Mount]` and `[Swap]`) is not a finite one set by a drop-in of
its own, any unit or drop-in with `Before=` on a unit of the base, or drop-ins
and `.wants`/`.requires` for a unit it does not ship (a unit's drop-ins are
those of `<unit>.d/*.conf`, its template's and its aliases', merged by file
name before these checks); or has an ELF (outside
`elf_exempt`) with a `DT_NEEDED` that resolves nowhere: not through its
`DT_RUNPATH` (else `DT_RPATH`, with `$ORIGIN`) in the image or the base, not
in the loader's default directories (`usr/lib` and `usr/lib/x86_64-linux-gnu`,
or `usr/lib32` for 32-bit) of the image or the base, and not in the base's
`ld.so.conf` directories (in the
base's `ld.so.cache`, which never lists the image's libraries). `vos ext
check-tree` also checks that `strip` paths are gone, that the `services` units
exist, and that `usr/lib/vos/ext/<id>/` holds the source descriptor, the
descriptor's exact `module_options` pairs and every `fetch` file. Its
`runs_as_root` is true when a system unit runs a command as root: any mount or
swap; a service, or a socket with commands, without a `User=` other than root
and without `DynamicUser=yes`; a command prefixed `+`, `!` or `!!`; or
`PermissionsStartOnly=yes` with commands besides `ExecStart=`. It warns,
without failing, on `TAG+="uaccess"` (VaporOS has no seat), Python `.pth`
files under `usr/lib/python*` (they run code in every Python program), files
in `usr/share/mime/packages` or `usr/share/icons` (the base's caches do not
list them) and, on Linux, when it lacks `CAP_SYS_ADMIN` in the initial user
namespace, which hides `trusted.*` xattrs from it.

**Catalog** (`/usr/lib/vos/extensions.list`, in the image, so the read-only
slot is the trust anchor; `internal/extensions/catalog`):
```
# comment
dispatcher 1
ext <id> <sha256> <size> <fsverity> <core|-> [requires...]
```
`ext` lines come in dependency order; unknown line kinds are ignored.
`dispatcher` is the `vos ext launch` level the image's vos understands. The
build also ships every descriptor, with a `build` section it fills in
(`size`, `packages`, verified `permissions`, `runs_as_root`), as
`/usr/share/vos/extensions/<id>.json`. The build fails a catalog where two
extensions set Steam's default compatibility tool or force one on the same
app, or whose images could not all mount at once (a `lowerdir` over 3800
bytes).

**Manifest:** `manifest.json` gains `"extensions": {"<id>": {"name":
"ext-<id>.raw", "size", "sha256", "fsverity", "core"?, "requires"?, "key"?}}`
(optional; `schema` and `min_updater` stay 1, older updaters ignore it). `key`
is the build's input key: an image is rebuilt only when it changes. It is the
sha256 of the descriptor, `files/`, the resolved package files, the `fetch[]`
sha256s, the build scripts, the mkfs flags and the requirements' keys. Ids match
`^[a-z][a-z0-9-]{0,31}$`; `requires` must name extensions of the same manifest,
without cycles. The OCI artifact carries each image as a layer titled
`ext-<id>.raw` (media type `application/vnd.vaporos.extension.v1.erofs`); http
and directory sources serve it by that name next to `manifest.json`.

**Store** (`/var/lib/vos/ext/`, root's):

| Path | What |
| --- | --- |
| `images/<sha256>.raw` | A sealed image: written to a temp file, fsynced, sha256-checked, closed, reopened read-only, `FS_IOC_ENABLE_VERITY` (sha256, 4096, no salt), `FS_IOC_MEASURE_VERITY` compared with the catalog, then renamed into place and the directory fsynced. A file under its final name is always sealed; one without fs-verity, or with another digest, is deleted and fetched again |
| `wanted` | the ids the user added, one per line (core ids are always wanted) |
| `sets/<n>/ids`, `sets/<n>/modprobe.conf`, `sets/<n>/tries` | one attempt at a set of extensions: ids (one per line, catalog order), the module options it sets (`options <module> <param>=<value>` lines, module and param `[A-Za-z0-9_-]+`, value `[0-9A-Za-z_x.-]+`; other lines are dropped), boots left to try it (one digit; anything else reads as 0). `<n>` is a decimal number that is never used twice: the larger of `sets/.next` and one more than any set in `sets/` or named by a link or the boot report. Written into `sets/.tmp-<n>`, fsynced, renamed |
| `sets/.next` | the high-water mark: the next set number (one decimal line), written atomically after each new set is renamed into place and never lowered; anything else reads as 0 |
| `enabled`, `pending` | symlinks `sets/<n>` (relative): the last good set, and the set on trial. Renaming `pending` into place is the commit of a change |
| `proven` | `<id> <fsverity>` lines: images that passed a boot, pruned by GC |
| `failed` | `<fingerprint>` lines: sets whose trial failed. The fingerprint is the hex sha256 of the set's sorted, unique `<id> <fsverity>` lines (digests from the booted catalog) followed by its sorted, unique option lines, each ending in `\n` |
| `skip-once` | present: the next boot mounts no extension, then the initramfs removes it |
| `slots/<a\|b>.json` | `{"version","extensions":{...manifest extensions...}}`: the catalog of each slot's image, from its signed manifest (`vos update` writes the idle slot's at Write order 1, the installer `a.json` through `vos ext fetch --seed`), or, for the booted slot, from the booted catalog (vosd) |
| `settings/<id>.json` | the extension's settings (never in config.json) |
| `data/<id>/` | its `system` data area (`home` ones are in `/var/home/vapor/.local/share/vaporos/ext/<id>/`, `library` ones in `<library>/VaporOS/<id>`) |

Images are sealed without the lock (a final name is always sealed), and may
be fetched before the `wanted`, set or slot file that keeps them, so GC
leaves an image, sealed or temp (`images/.tmp-*`), alone for an hour after
its last write (mtime). `/run/vos/ext.lock` (flock) serialises every write to
`wanted`, the sets, `enabled`, `pending`, `proven`, `failed` and the store's
garbage collection. The slot files are written under the update lock and
ext.lock. Lock order: the update lock, then ext.lock; whoever holds ext.lock
waits for the update lock only for a bounded time. GC keeps the images that
`wanted` ∪ core resolve to in the booted catalog and in both slot files, and
those mounted this boot; the sets that `enabled`, `pending` or the boot report
name; and the `proven` lines whose pair the booted catalog or a slot file
lists or this boot mounted. vos reads the line files (`wanted`, `proven`,
`failed`, a set's files) whole or not at all: one over 1 MiB is an error, and
a line of 4 KiB or more (longer than any valid one) is skipped.

**Boot** (the initramfs hook, after `/state`, `/var` and `/etc` are mounted;
never in live mode; never `vos_die`):
1. Before mounting vos_data (after e2fsck), it loads ext4 (`modprobe -q ext4`)
   and, only if the running kernel's ext4 has verity
   (`/sys/fs/ext4/features/verity`), sets the ext4 `verity` feature if missing
   (`tune2fs -O verity`). A kernel without it, or a failure (no `tune2fs`
   included), is reason `no-verity`, nothing more.
2. It picks the set: nothing with `vos.ext=0` or `skip-once` (removed first,
   whichever applies); else, while systemd-boot counts this boot
   (`LoaderBootCountPath-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f` in efivars: an OS
   trial), `enabled` at the booted catalog's digests, with `pending` left for
   later; else `pending` if its `tries` > 0 (decremented, temp + rename + sync,
   before anything mounts; if that fails, `enabled`); else `enabled`, mounting
   only images whose `<id> <fsverity>` line is in `proven`. A link counts only
   as exactly `sets/<n>` (`<n>` of 1 to 9 digits, no leading zero), resolved in
   the store on vos_data, to a set directory with an `ids` file, none of the
   store, `sets/`, the set and `ids` a symlink. Every other file the hook reads
   in the store (`tries`, `modprobe.conf`, `proven`, the images) counts only
   as a regular file, never through a symlink; `tries` other than one digit
   is 0, and a `tries.new` left over is removed before the new one is
   written. With no set to use, nothing mounts and the mode stays `enabled`
   (or `os-trial`) with reason `no-set`.
3. For each id of the set in catalog order (lines of `ids` that are not ids
   are ignored), the first check that fails is the skip reason: a requirement
   that did not mount (`requires`, also when it is not in the set),
   `images/<sha256>.raw` missing or a symlink (`missing`) or of another size
   (`size`), not in `proven` (`unproven`, `enabled` mode only), `fsverity
   measure` other than the catalog (`fsverity`), `mount -t erofs -o ro` failing, also through
   `losetup -r` (`mount`), no `usr/` directory (`no-usr`, unmounted). Ids the
   catalog does not list follow as `not-in-catalog`; an `ext` line whose
   sha256 or fsverity is not 64 lowercase hex digits, or whose size is not a
   decimal number, lists nothing. Each image mounts at the next
   `/run/vos/x/<n>` (n = 1, 2, ...).
4. With at least one mounted, `/usr` becomes `overlay -o ro,lowerdir=/run/vos/x/<k>/usr:...:<root>/usr`
   (no upper; mounted with `LIBMOUNT_FORCE_MOUNT2=always`), the images in
   reverse catalog order, so an extension sits above those it requires. If
   that fails, every image is unmounted and skipped as `overlay`.
5. `/run/modprobe.d/vos-ext.conf` gets the set's `modprobe.conf` lines that are
   exactly `options <module> <param>=<value>` (module and param of
   `[0-9A-Za-z_-]`, value of `[0-9A-Za-z_x.-]`, one param per line) and whose
   `<module> <param>` is a line of a mounted image's
   `usr/lib/vos/ext/<id>/module-options`. Nothing is written without one.
6. It writes `/run/vos/extensions.json`, one line of compact JSON:
   `{"mode":"pending|enabled|os-trial|off","set":"<n>","tries_left":N,"reason":"...","mounted":[{"id","sha256","fsverity"}],"skipped":[{"id","reason"}]}`.
   `set` is empty when none is used; `tries_left` is what the pending set has
   left after this boot (0 in other modes); `reason` is zero or more of
   `cmdline` (`vos.ext=0`), `skip-once`, `no-set`, `tries-used` (`pending` had
   no tries left), `tries-write` (its tries could not be written) and
   `no-verity`, space-separated; `mode` is `off` with `cmdline` or
   `skip-once`. Skip reasons: `requires`, `not-in-catalog`, `missing`, `size`,
   `fsverity`, `unproven`, `mount`, `no-usr`, `overlay`. A missing file reads
   as mode `off` with reason `no-report` (vos's own token). Readers test for a
   token, never compare the whole `reason`. Everything at runtime (generator,
   Steam settings, the control center) follows this file, not intent. The same
   goes to the serial console as one line (see "Serial lines").
7. On a trial boot it arms a hardware watchdog (see Trial and promotion).

**Trial and promotion:** a boot in mode `pending` is an extension trial. `vos
health` treats it like a counted boot with a fallback: a failure exits 1 (so
`FailureAction=reboot` tries again and, once the tries are used up, the next
boot uses `enabled`), and `vos.health.fail=1` applies. On success, under the
lock: every mounted `id fsverity` is added to `proven` (also on an OS trial);
then, if `pending` still points at the booted set and every id of the set
mounted or was skipped as `not-in-catalog`, `enabled` is replaced by that set
(rename, directory fsync) and `pending` removed. A set the user no longer
wants is never promoted: reconcile removes or replaces such a `pending`, also
on its own trial, and a promoter that knows the desired set's fingerprint
promotes only when what booted has it. On trial boots (mode `pending`, or an
OS trial) the generator gives `vos-health.service` a drop-in with
`JobTimeoutSec=10min` and `JobTimeoutAction=reboot-force`.

A trial that hangs the kernel or PID 1 must still reboot, so it uses up a try.
CachyOS blacklists the hardware watchdog drivers, which only stops their
aliases, so on trial boots (mode `pending`, or any boot systemd-boot counts,
also with `vos.ext=0` or `skip-once`) the initramfs, after the report, loads
them by name (`modprobe -q` `sp5100_tco`, `iTCO_wdt` and `wdat_wdt`, carried
in the initramfs where the kernel has them) and writes
`/run/systemd/system.conf.d/50-vos-trial.conf` (`[Manager]`
`RuntimeWatchdogSec=60s`), which PID 1 reads when it starts. Other boots load
no watchdog driver and leave `RuntimeWatchdogSec` as CachyOS ships it.

**Reconcile** (vosd, at start and after every change, under the lock): the
desired set is `wanted` ∪ core, with their requirements, as the booted catalog
lists them, plus the module options their settings render. Additions are
limited to images sealed in the store: an id whose image is missing stays in
the desired set only if it, or an id that requires it, mounted this boot or is
in the booted or the `enabled` set (a missing image never shrinks the set).
What booted is the mounted ids and digests with the booted set's options; a
boot that mounted nothing on purpose (mode `off`, or no report) counts as
`enabled` at the booted catalog's digests. Then, the first that applies:
1. A `pending` outside its own trial that names the booted or the `enabled`
   set (a promotion cut short) is removed, never failed.
2. A `pending` with `tries` 0 that this boot did not use moves to `failed`
   (fingerprint at the booted catalog's digests). On an OS trial it is only
   removed: its trials ran at the old image's digests, and the desired set gets
   a trial of its own at the new ones.
3. Desired equal to what booted: no `pending`, unless this boot is that
   `pending` set's trial and the set booted whole (every id mounted or skipped
   as `not-in-catalog`), which `vos health` promotes.
4. Equal to `pending` (ids and options): left alone.
5. Its fingerprint in `failed`: not proposed, and a `pending` is removed (the
   extension needs attention; "Try again" removes the fingerprint).
6. Otherwise a new set becomes `pending` with `tries` 2.

After a removal (1, 2, 3 or 5) reconcile runs again. A restart is needed (to
try `pending`) only while `pending` has tries left, this boot is neither its
trial nor one with reason `cmdline` or `skip-once`, and every image it names
is sealed.

**vosd** (installed systems only; `internal/extensions`). At start it removes
the temp files of downloads that stopped (`images/.tmp-*` untouched for an
hour) and writes `slots/<booted>.json` from the booted catalog (each `name`
`ext-<id>.raw`), under the update lock, unless the file already lists the same
images for the booted version. It reconciles at start, whenever a change asks
for it (wanted, a setting, the store), and, while an image it could fetch is
still missing or a reconcile failed, again after 1 minute, doubling up to
every 30 minutes. One reconcile:
1. under the lock, applies what the plan says to fail or clear;
2. without the lock, fetches and seals the plan's missing images for the
   booted version;
3. under the lock, plans again and acts in full (fail, clear, propose, keep or
   blocked, as above), planning again after each write;
4. fetches, best effort, the images `wanted` ∪ core need in the other slot
   file's version, when it names another;
5. runs GC, unless a slot file cannot be read.

Images come from `config.update.source` at the version: a registry at the tag
`<version>` (any tag in the source is replaced), by layer title, and when that
download fails, as the blob by digest, which still works once the tag is gone;
an HTTP or directory source by name. A source that served other bytes for an
image is not asked for it again until a reconcile is asked for, and on a disk
that cannot seal (boot reason `no-verity`, or a seal refused as unsupported)
nothing is fetched until the next boot. Once per boot, after the first
reconcile, vosd reads every mounted image through at idle I/O priority
(`ioprio_set`, class idle). A read that fails with `EIO` (fs-verity checks
every block) or bytes whose size or sha256 are not the booted catalog's delete
the image, at most once per digest per boot, and the next reconcile fetches it
again: the desired set keeps it meanwhile, so the next boot mounts the new
copy. A missing file is not damage. While vosd downloads, seals or re-reads an
image, idle shutdown counts it as busy (`adding an extension`).

**Install** (the `configure` step, after slot a is written and the target is
mounted): the installer runs the new image's own
`<target>/usr/bin/vos ext fetch --state-dir <target>/var/lib/vos --from SRC --version <ver> --seed`
(plus `--repair` on a repair), which seals the image's core extensions into
the new system's store. SRC is the install's source, or for the live medium
(which carries no extension images) the new system's `config.update.source`.
It is best effort and capped at 20 minutes: a failure is logged, and vosd
fetches what is missing once the system runs. Its progress lines show as
`configure`. Before it runs, a repair removes `slots/b.json` (slot b was
wiped), `pending` and `failed`.

## HTTP API (`vosd`, port 80, prefix `/api/v1`, JSON)

**Middleware, in order:**
1. Source IP must be loopback, private, link-local or ULA (else 403, unless `web.allow_public`).
2. `Host` must be in the allowlist (`localhost`, `127.0.0.1`, `<hostname>`, `<hostname>.local`, `vaporos-setup.local`, any current local IP, with or without a port), else 421.
3. Security headers (CSP `default-src 'self'; img-src 'self' data:; frame-ancestors 'none'`, nosniff, same-origin referrer).
4. Session cookie `vos_session` (HttpOnly, SameSite=Strict, 30-day sliding).
5. For every non-GET on `Authed` routes, header `X-VOS-CSRF` must equal the session's csrf.
6. Activity: every `Authed` or `Setup` request and every event-stream connect counts as web-UI activity, which idle shutdown treats as busy (`web UI in use`) for 5 minutes. A request with `X-VOS-Passive: 1`, or the query `passive=1` (for `EventSource`, which cannot send headers), does not count; pages send it on refreshes nobody asked for. Signing in and setting the setup cookie always count.

**Access levels:**
- `Public`: no auth.
- `Authed`: valid session.
- `Setup`: installer or first-run setup code, via header `X-VOS-Setup: <code>` or a `vos_setup` cookie set by `GET /setup?code=…`. `?code=` counts only on a first-party navigation (`Sec-Fetch-Site` none, same-origin or absent, and `Sec-Fetch-Dest` document or absent); otherwise `/setup` redirects to the same URL without it. In installer mode the code is waived while no monitor is attached (no connected DRM connector other than writeback or a `video=<C>:e` one; none listed yet does not count), checked on every request.
- `Local`: loopback only.

**Errors:** `{"error":"message"}` with a proper status.

**Events:** `GET /api/v1/events` (Authed or Setup) is an SSE stream: `event: <topic>`, `data: <json>`. A new stream starts with the latest event of each topic except `system.message` and `pairing.pending`, which are live-only. `session.begin` and `session.end` replace each other, so only the newer of the two is replayed. Signed out and without a setup code, it answers 403 (`setup code required`), since it is Authed-or-Setup.

| Topic | Data |
| --- | --- |
| `update.progress` | `{phase,percent,bytes,total,version,error?}`. A stage goes through `check`, `download` (kernel, initrd and extension images), `write` (root into the idle slot), `verify` (read back), `install` (ESP) and `done`; `percent` covers the whole stage, with `download` and `write` sharing 0-80 % by their bytes, while `bytes` and `total` count the current phase. Phase `error` carries why a stage stopped; `idle` ends a stage that found nothing to do (already up to date, or already staged); `cancelled` ends one that `POST /update/cancel` stopped |
| `update.state` | the update-state |
| `install.progress` | `{step,percent,message,state}` |
| `session.begin` | `{client,app?,mode,hdr,since}` (`app`: Sunshine's app, `Steam` or a game's name, omitted when Sunshine names none; `since`: when it was launched, RFC 3339 UTC; a resume keeps both) |
| `session.end` | `{}` |
| `pairing.pending` | `{name?}` |
| `pairing.state` | `{pairings:[{id,name,address}]}`: who waits for a PIN (as in GET `/sunshine` `pairings`), sent after vosd's first poll of Sunshine and whenever the list changes. An empty list ends any pairing prompt; Sunshine not answering counts as an empty list |
| `sunshine.state` | `{running}`: whether `vos-sunshine.service` is active (as in GET `/sunshine` `running`), sent after vosd's first poll of Sunshine and whenever it starts or stops |
| `display.changed` | `{}` |
| `power.idle` | `{idle_seconds,shutdown_in,busy}` (as in GET `/power`; `busy` is null while idle; sent every 15 s while idle and whenever the busy reason changes) |
| `system.message` | `{level,text}` (`level`: `info`, `warning` or `error`) |

| Method + path | Access | Request → Response |
| --- | --- | --- |
| GET `/ping` | Public | `{"ok":true,"mode":"os\|installer","version"}` |
| GET `/auth/me` | Public | `{"authenticated":bool,"csrf":"…","needs_setup":bool,"installer":bool}` |
| POST `/auth/login` | Public | `{"password"}` → `{"csrf"}` + cookie; 401 on a wrong password; 409 while no admin password is set yet (as on the installer); 429 after 5 failures per IP (backoff up to 15 min), and (Retry-After 1) while another check from the same client is running; 503 when too many sign-ins (8) are being checked |
| POST `/auth/logout` | Authed | → `{}` |
| POST `/auth/setup` | Setup | `{"password"}` → `{"csrf"}`; only when auth.json is missing (first run after a CLI install); 409 in installer mode |
| POST `/auth/password` | Authed | `{"current","new"}` → `{}`; 403 on a wrong current password; shares the login limit (429, 503) |
| GET `/system` | Authed | `{"hostname","version","channel","booted_slot","uptime_s","cpu","gpu":{"vendor","name","driver","supported"},"ips":["…"],"mdns":"vapor.local","disk":{"data_total","data_free"},"temps":[{"name","c"}]}` |
| GET `/status` | Authed | installed system only (404 on the installer). One summary for page loads that never waits on Sunshine or ethtool: `{"system":…,"sunshine":…,"stream":{"client","app"?,"mode","hdr","since"}\|null,"display":…,"update":…,"power":…,"restart":{"needed":bool,"reasons":[{"kind":"update\|rollback\|display","version"?}]}}`. `system`, `display` and `update`: as their GET routes. `sunshine`: GET `/sunshine` without `session`, from the 3 s poll (only `running` is asked now). `stream`: the session in progress as `session.begin` carries it, null without one. `power`: GET `/power` without `wol`; keep-awake is checked now, the other busy reasons come from the last 15 s policy pass. `restart.reasons`, in this order: `update` when `update.next_boot` is newer than the booted version, else `rollback` (both with `next_boot.version`), and `display` while `display.reboot_needed`; `needed` is true when there is one. A part that fails is null and the others still answer |
| PUT `/system/hostname` | Authed | `{"hostname"}` → `{}`; the name (trimmed and lower-cased first) is one RFC 1123 label (1-63 of `a-z`, `0-9`, `-`, not starting or ending with `-`, no dots) and not `localhost`, else 400 |
| POST `/system/reboot`, `/system/poweroff` | Authed | → `{}` |
| GET `/update` | Authed | update-state (with `held`) + `{"config":config.update,"booted_slot","other_slot":{"version","bootable","counting",…}\|null,"next_boot":{"slot","version"}\|null,"busy":bool,"progress":{…}\|null}` (`next_boot`: the entry systemd-boot starts next when it is not the running slot's: a staged update, or a rollback or downgrade waiting for a restart; null when a restart boots the running slot again or the ESP cannot be read) |
| POST `/update/check` | Authed | → `{"available":{…}\|null}` |
| POST `/update/stage` | Authed | `{"version"?}` → `{}` (progress via events); 409 while an update runs. Later refusals (on trial, rollback waiting for a restart, held, …) arrive as `update.progress` `{"phase":"error","error"}` |
| POST `/update/cancel` | Authed | → `{}`: stops the stage vosd runs (not a `vos update` from a shell). The outcome arrives as `update.progress`: `cancelled`, or `done` if it was already installing. 409 with the reason when nothing runs, when the running update is a `vos update` from a shell, or once the stage writes the ESP (phase `install`). A stage cancelled after writing began has already unhooked the idle slot (Write order 1), so no rollback target remains until the next stage. A cancel records no `last_error` and does not hold the version back: the next automatic check stages it again unless `auto` is `off` |
| POST `/update/activate` | Authed | → `{}` (reboots into the staged version) |
| POST `/update/rollback` | Authed | → `{}` (next boot = other slot; UI then offers reboot); 409 with the reason |
| PUT `/update/settings` | Authed | `{"channel","auto"}` → `{}` |
| GET `/sunshine` | Authed | `{"running":bool,"version","streaming":bool,"session":{"client","mode","hdr","app"?,"since"}\|null,"pending_pairing":bool,"pairings":[{"id","name","address"}]}` (`session.client`: the device that started the running app; `session.app` and `session.since` as in `session.begin`; `since` falls back to when vosd saw the `session.begin`) |
| POST `/sunshine/pair` | Authed | `{"pin","name","pairing_id"?}` → `{}` (Sunshine `POST /api/pin`); 409 when no device waits, or several wait and none is named |
| GET `/sunshine/clients` | Authed | `{"clients":[{"uuid","name","enabled":bool}]}` |
| DELETE `/sunshine/clients/{uuid}` | Authed | → `{}` |
| GET/PUT `/sunshine/settings` | Authed | `{"encoder","bitrate_kbps_max","audio_sink","gamepad"}`, a whitelisted subset (`audio_sink` `""` = Sunshine's default sink). Both answers add `"choices":{"encoder":["vulkan","vaapi","software"],"gamepad":["auto","xone","xseries","x360","ds4","ds5","switch","generic"],"bitrate_kbps_max":{"min":0,"max":1000000}}`: what the UI offers. PUT also accepts `nvenc`, ignores `choices` and answers the saved settings; 400 on any other value |
| GET `/sunshine/logs` | Authed | `text/plain`, last 2000 lines |
| POST `/sunshine/restart` | Authed | → `{}` |
| POST `/sunshine/end-stream` | Authed | → `{}`: closes the running Sunshine app (`POST /api/apps/close`), which ends every client's stream. Sunshine then runs the prep-cmd undo, so `session.end` follows. A Steam game the app started keeps running. 409 when nothing streams; 502/503 as the other Sunshine calls |
| GET `/display` | Authed | `{"profile":"amd\|none","virtual_connector","connectors":[{"name","status","physical":bool}],"available_connectors":["DP-2"],"modes":["WxH@R"],"current":"WxH@R"\|null,"hdr":bool,"learned":["WxH@R"],"added":["WxH@R"],"devices":[{"name","mode":"WxH@R","hdr":bool,"last_seen"}],"reboot_needed":bool,"state":"gaming\|welcome\|streaming\|none","planes":N}` (`physical`: connected and not the virtual connector; `available_connectors`: disconnected DP/HDMI ports of a supported GPU other than the current one, what the UI offers as `virtual_connector`; `planes`: fb-backed planes on the virtual connector's CRTC, 1 while gamescope composites; `added`: valid `display.extra_modes` beyond the built-in list; `devices`: clients.json, newest first, a device once per mode it asked for (its first entry is its last mode); `learned` stays the union of `added` and the valid client modes beyond the built-in list) |
| POST `/display/modes` | Authed | `{"mode":"WxH@R"}` → `{"reboot_needed":bool}`: false when the running kernel took the new EDID live |
| DELETE `/display/modes/{mode}` | Authed | → `{"reboot_needed":bool}`: removes `WxH@R` from `display.extra_modes` and every clients.json entry that asked for it, then rewrites the learned EDID (live where it can, as POST, else after a reboot). A device that asks for the mode again teaches it again. 400 for a malformed mode; 404 when neither list holds it |
| PUT `/display/settings` | Authed | `{"hdr":bool,"virtual_connector"?}` → `{}`; a new `virtual_connector` must be a DP/HDMI connector of the GPU |
| GET `/storage` | Authed | `{"disks":[{"path","model","size","uuid","label","fstype","mounted_at"?,"is_system":bool,"steam_library":bool,"library_dir"?,"adopted":bool,"missing"?:bool,"free"?,"registered":bool,"registration_pending"?:bool}]}` (`library_dir`: `.` or `SteamLibrary`; `missing`: adopted but not attached; `registered`: adopted, and Steam's library list has a library on it; `registration_pending`: queued in steam-libraries.json) |
| POST `/storage/libraries` | Authed | `{"uuid"}` → `{"mountpoint","library","registered":bool,"registration_pending":bool,"hint"}` (config + mount now + add `library` to Steam's list now, or once Steam is not running). Only ext2/3/4, btrfs, xfs, f2fs and NTFS (ntfs3), never exFAT/FAT (`storage.LibraryFS`, shared with the installer and generator); else 400. A disk with no library gets a new vapor-owned `SteamLibrary/` |
| DELETE `/storage/libraries/{uuid}` | Authed | → `{}` |
| GET/PUT `/power` | Authed | `{"idle_shutdown","idle_minutes","keep_awake_until"?,"wol":[{"iface","mac","enabled","supported","ipv4"?,"prefix"?,"broadcast"?}],"busy":{"reason","web"?}\|null,"web_until"?,"idle_seconds","shutdown_in"}`. PUT takes `idle_shutdown` and/or `idle_minutes` (1–1440) and answers the same document. `wol`: every wired NIC; `enabled`: magic-packet wake is armed; `supported`: the NIC can wake on a magic packet; `ipv4`, `prefix`, `broadcast`: its first IPv4 address that is neither loopback nor link-local, the prefix length and the subnet's broadcast address (all omitted without one; `broadcast` is also omitted on a /31 or /32). `busy`: why the machine stays awake, null when idle. `busy.web`: web-UI activity is the only reason. `web_until`: when web-UI activity stops keeping it awake (UTC, whole seconds), present whenever that activity counts, whatever the reason, and omitted otherwise. `idle_seconds` and `shutdown_in`: the idle timer in seconds (`0` and null while busy; `shutdown_in` is also null with idle shutdown off) |
| POST `/power/keep-awake` | Authed | `{"minutes"}` (0 = clear) → `{}` |
| GET/PUT `/ssh` | Authed | `{"enabled","keys":[…]}` |
| GET `/install/probe` | Setup | query `?source=&channel=` (optional); returns `"source","channel","version","min_size","source_error"` plus `{"disks":[{"path","model","size","transport","removable","is_live","has_vaporos","hostname"?,"steam_libraries":[{"uuid","label","path"}]}],"ips":[…],"timezone":"Europe/Brussels","gpu":{…}}`; `hostname` is the name the VaporOS install on that disk answers to (`<hostname>.local`, the one a repair keeps), read from `etc/upper/hostname` on its vos_data (mounted read-only without journal replay, never while an install runs); omitted when unknown |
| POST `/install` | Setup | `{"disk","mode":"erase\|repair","hostname","password","timezone","libraries":["uuid"],"source":"","channel":""}` → 202 `{"job":"id"}`; empty source means the live medium; `oci://` sources take `channel` (default: the live image's channel, then `main`); in repair, an empty hostname or timezone keeps the installed one; `hostname` (lower-cased first) follows PUT `/system/hostname`'s rule, so `localhost` is a 400 |
| GET `/install/status` | Setup | `{"state":"idle\|running\|done\|failed","step","percent","message","error"}`; `step` is one of `probe`, `partition`, `write`, `verify`, `bootloader`, `configure`, `done` (empty while `idle`) |
| POST `/install/reboot` | Setup | → `{}` |
| GET `/welcome` | Local | the welcome.json content without the setup code (`code` empty, `qr` cut before `setup?`) |

**Pages** (server-rendered shells plus JS modules that call the API): `/` home, `/devices`, `/screen`, `/system` with `/system/updates`, `/system/power`, `/system/storage`, `/system/settings`, `/system/logs` and `/system/about`, plus `/login` and `/setup` (the first-run password, or the installer wizard on the ISO). The old paths `/pair`, `/streaming`, `/display`, `/storage`, `/updates`, `/power` and `/advanced` answer 303 to their new pages (`/pair` goes to `/devices#pair`) and keep the query. On the ISO, every page answers 303 to `/setup`. There are no external assets (no CDN): everything is embedded. The installer's `/setup` adds `connect-src 'self' http://*.local`.

## Session protocol (`/run/vos/session.sock`)

Served by the display manager (`display.Manager.Run`), not by the daemon.

Newline-delimited JSON, one request and one response per connection.
- `{"op":"begin","client":"<SUNSHINE_CLIENT_NAME?>","app":"<SUNSHINE_APP_NAME>","width":W,"height":H,"fps":F,"hdr":bool}` → `{"ok":bool,"mode":"WxH@R","hdr":bool,"message":"…"}`
- `{"op":"end"}` → `{"ok":true}`

`vos session begin` reads `SUNSHINE_CLIENT_WIDTH/HEIGHT/FPS/HDR` and
`SUNSHINE_APP_NAME` from the environment. vosd gives a request 80 s and answers on its own if the handler overruns. It **always exits 0**, with a
hard timeout of 90 s.

vosd ends a session itself (as `end` would) when Sunshine's serverinfo has
reported `SUNSHINE_SERVER_FREE` for 30 s while no `begin` or `end` is running,
so a Sunshine that crashed mid-stream does not hold gamescope or keep the PC awake.

## Welcome screen (`/run/vos/welcome.json`, written by vosd, mode 0600)

```json
{"mode":"os|installer","hostname":"vapor","url":"http://vapor.local","ip_url":"http://192.168.1.50",
 "qr":"http://192.168.1.50/","code":"ABCD-EFGH","title":"VaporOS","status":"Ready to stream",
 "detail":"Open this address on your phone or computer","version":"…",
 "tone":"ready","attention":"pair","progress":42}
```
`qr` is `<base>/setup?code=<code>` while a setup code exists, else `<base>/pair` while a Moonlight device waits for its PIN, else `<base>/`; `<base>` is `ip_url`, or `url` when there is no address yet.

`tone` is the state word of `design/tokens.json` that the screen is coloured by: `ready`, `streaming`, `updating`, `restart-needed`, `asleep`, `fault` or `installing`; empty (omitted) is neutral, and a reader treats an unknown word as neutral. vosd sets it in the same place as `status`: `installing` for the installer, the setup code, a running or finished install; `fault` for no supported GPU, no network and a failed install; `restart-needed` while the virtual display waits for a restart; `streaming` during a session; `updating` while an update downloads or writes; `ready` otherwise, a staged update included. With no network, `tone` stays neutral for the first 15 s after vosd starts. `tone` is `asleep`, and `status` says so, while an idle shutdown is at most 2 minutes away (`power.idle` `shutdown_in` ≤ 120). `attention` is `pair` while a device waits for its PIN (the tone is unchanged). `progress` is the percent (0–100) of a running install or update, 100 once an install is done; omitted when 0. `status` and `detail` stay the words; `tone`, `attention` and `progress` only decide how the screen looks.

The virtual EDID's PNP id is `VOS` (not assigned in hwdata's pnp.ids, so gamescope falls back to the raw id) and its monitor name `VaporOS`, so gamescope's `modes.cfg` key is `VOS VaporOS`; vosd still reads the real key from `gamescopectl` at runtime instead of assuming it. vosd keeps gamescope compositing (the `composite_force` convar and the `GAMESCOPE_COMPOSITE_FORCE` root property, which Steam can reset) and re-asserts it every 5 s during sessions, because stock Sunshine's KMS capture loses the picture when gamescope scans a game out on its own plane.

`vos welcome` redraws whenever the file changes (poll 1 s). It lights every
connected physical connector with its preferred mode. It also lights the
virtual connector (if configured) with a 1920x1080 splash, because Sunshine
probes the encoder on the virtual connector before prep-cmd runs. While
running it keeps the VT keyboard off (`K_OFF`, echo off, input flushed) and VT
switching locked. On exit it flushes input, restores the previous keyboard
mode and unlocks switching, but leaves tty1 in `KD_GRAPHICS`. It never reads
input and exits cleanly on SIGTERM (releasing DRM master).

## Serial lines (harness contract; written to `/dev/ttyS0` if present)

- `VOS-READY mode=installer version=<v> ip=<ip> code=<code|->`: the installer API is up (`-` while the code is waived).
- `VOS-READY mode=os version=<v> ip=<ip> code=<code|->`: the installed system is up (the code appears only when auth.json is missing).
- `VOS-INSTALL state=<done|failed> message=<…>`
- `VOS-HEALTH result=<ok|degraded|failed> failures=<a; b|->`: written by `vos health` on every installed boot.
- `VaporOS: extensions mode=<mode> set=<n|-> mounted=<id,...|-> skipped=<id:reason,...|-> reason=<reason,...|->`: written by the initramfs on every installed boot, with `/run/vos/extensions.json`'s words (its space-separated reasons joined with commas, so every field is one word).

vosd re-emits `VOS-READY` whenever its IP changes.

## Units

**System**, in `/usr/lib/systemd/system`:
- `vosd.service`: `ExecStart=/usr/bin/vos daemon`, `Restart=always`
- `vos-welcome.service`: started and stopped by vosd only
- `vos-health.service`: `FailureAction=reboot` (see "Health")
- `vos-generator` (`/usr/lib/systemd/system-generators`), following `/run/vos/extensions.json` and never intent: for each mounted extension, the system units its shipped descriptor (`/usr/share/vos/extensions/<id>.json`) lists in `services` with scope `system` are wanted the way their `[Install]` would (`.service` by `multi-user.target`, `.socket`, `.timer` and `.path` by `sockets.target`, `timers.target` and `paths.target`; an instance links to its template's file); a unit file that is missing is logged and skipped. On a trial boot (mode `pending` or `os-trial`) it writes `vos-health.service.d/50-vos-trial.conf`: `[Unit]` `JobTimeoutSec=10min`, `JobTimeoutAction=reboot-force`. It always exits 0
- `seatd.service.d/vos.conf`
- `vos-firewall.service`: `nft -f /usr/lib/vos/nftables.nft`
- every `systemd-sysext*` and `systemd-confext*` unit is masked: only the initramfs merges extensions
- no `RuntimeWatchdogSec` in `/usr/lib/systemd/system.conf.d`: CachyOS blacklists the hardware watchdog drivers, and only trial boots load them and set it, in `/run/systemd/system.conf.d/50-vos-trial.conf` (see Extensions, Trial and promotion)

No keypress, local or from a Moonlight client, reboots or suspends the box: `ctrl-alt-del.target` is masked, `system.conf.d/vos.conf` sets `CtrlAltDelBurstAction=none`, `sysctl.d/99-vos.conf` sets `kernel.sysrq = 0`, and logind ignores the reboot, suspend and hibernate keys (and their long presses).

**User** (`vapor`), in `/usr/lib/systemd/user`, controlled by vosd via `systemctl --user -M vapor@`:
- `vos-gamescope.service`: env from `%t/vos/gamescope.env` (`VOS_OUTPUT`, `VOS_GS_EXTRA`); `GAMESCOPE_MODE_SAVE_FILE=%h/.config/gamescope/modes.cfg`
- `vos-sunshine.service`: `/usr/bin/sunshine %h/.config/sunshine/sunshine.conf`; ordered `After=` gamescope with no dependency on it; no `[Install]`. vosd starts it on every boot and again whenever it is inactive (checked every 10 s), only with a supported GPU. After each start or restart vosd reads that run's log until Sunshine's web UI is up: a run that logged `Platform failed to initialize` (its KMS capture found no lit plane, e.g. it came up before gamescope's first modeset) never recovers by itself and fails every stream with error 503, so vosd restarts it while idle, 5 s after the failure and then backing off to 2 min.

Both user units carry `ConditionKernelCommandLine=!vos.mode=live`.

**Display policy (vosd):**
- **No GPU profile:** no gamescope. Show the welcome screen if any connector is connected.
- **GPU and no physical monitor:** gamescope and Steam run permanently, and Sunshine runs.
- **GPU and a monitor:** the welcome screen runs while idle. `session begin` stops it, starts gamescope and applies the mode. `session end` plus 60 s idle (no game or download) stops gamescope and starts the welcome screen again.
- **HDR:** a `begin` whose HDR differs from gamescope's restarts gamescope only when no Steam game runs; otherwise the session keeps the current HDR (the mode still switches).

Sunshine renders from `/usr/share/vos/sunshine.conf.tmpl` into `~vapor/.config/sunshine/sunshine.conf`:
- `capture = kms`, `encoder = vulkan`, `adapter_name = <render node of the virtual connector's card>`, `output_name = <virtual connector name, e.g. DP-1>` (Sunshine's KMS capture matches connector names; a number would mean "n-th active plane", which shifts while the welcome screen lights other outputs)
- `origin_web_ui_allowed = pc`, `upnp = disabled`, `system_tray = disabled`, `gamepad = xone`
- `global_prep_cmd = [{"do":"/usr/bin/vos session begin","undo":"/usr/bin/vos session end","elevated":false}]`. Sunshine runs prep commands on launch only, so a resumed stream keeps the mode and HDR it was launched with; vosd publishes a `system.message` when it sees a resume (a client connects with no `session.begin` since the last one left).

`~vapor/.config/sunshine/apps.json`: `"Steam"` (no command), then per installed game `{"name","detached":["/usr/bin/vos session launch steam://rungameid/<id>"]}`, never `cmd`.

**Firewall** (nftables, input policy drop):
- accept lo, established, ICMP/ICMPv6, udp 5353, udp 67-68;
- tcp 80 (and 443 if HTTPS) from private ranges;
- Sunshine tcp 47984, 47989, 48010 and udp 47998-48000;
- tcp 22 only while SSH is enabled.

47990 is never reachable from outside.

**Network** (NetworkManager, `/usr/lib/NetworkManager/conf.d/50-vos.conf`; Wi-Fi through iwd; DNS through systemd-resolved):
- NetworkManager, not networkd, because Steam's SteamOS first-run setup and its network settings list and join networks over NM's D-Bus API.
- Wired ports get NM's automatic DHCP profile; route metric 100 wired, 600 Wi-Fi.
- `ipv4.dhcp-client-id=mac`, so the installer and the installed system get the same address (networkd's `ClientIdentifier=mac` before it sent the same one).
- `hostname-mode=none` (vosd owns `/etc/hostname`), `ethernet.wake-on-lan=ignore` (`50-vos-wol.link` sets it), no connectivity check.
- Networks joined in Steam are saved in `/etc/NetworkManager/system-connections/`, kept by the `/etc` overlay.
- `/usr/share/polkit-1/rules.d/50-vos-networkmanager.rules` grants every `org.freedesktop.NetworkManager.*` action to `vapor`, which has no seat session.

**SteamOS helpers** Steam runs with `-steamos3`, as stubs in `/usr/bin`: `steamos-update` exits 7 (no update; VaporOS updates through vos) and `steamos-select-branch -c` prints `stable`.
