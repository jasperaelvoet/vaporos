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
| `vos ext validate DESCRIPTOR...` | build: load and check each source descriptor (`extension.json`: schema, every field, no `build` section); one problem per line on stderr as `<file>: <problem>`. Exit 0 when every one passes, 1 on any problem, 2 without a descriptor |
| `vos ext check-tree --id ID --descriptor FILE --tree DIR --base DIR [--other DIR]... [--json OUT]` | build: check an extension's image tree against its descriptor, the base and the extensions built before it; one problem per line on stderr (`<id>: <problem>`, warnings as `<id>: warning: <text>`), exit 1 on any. `--json` (on success) writes `{"permissions","runs_as_root","warnings"}` |
| `vos ext catalog --stage DIR --out DIR` | build: from `ext-<id>.raw`, `<id>.json`, `<id>.build.json` (check-tree's `--json`) and the optional `<id>.key` and `<id>.packages.txt` in DIR, write `extensions.list`, `extensions.json` (the manifest's `extensions` object) and `descriptors/<id>.json` (with the `build` section) |
| `vos ext digest FILE...` | prints `<fs-verity digest>  <file>` per file |
| `vos ext fetch [--from SRC] [--version V] [--state-dir DIR] [--seed [--repair]] [ids...]` | fetch and seal into the store the extension images of version V (default: the booted image's) from SRC (default: config.json's `update.source`; a registry at the tag V, replacing any tag in SRC). It reads and verifies V's signed manifest as `vos update` does and refuses a source that serves another version, then fetches the images the store lacks of `ids` (default: `wanted` ∪ the manifest's core; with `--seed` core is always added), with their requirements, as vosd does (see Extensions). It stops at a disk that cannot seal (fs-verity unsupported) or a source that cannot be reached, and without `--state-dir` fetches nothing when the boot report has reason `no-verity`; the images left are reported as not sealed. `--state-dir` uses DIR as `/var/lib/vos` (the installer's target). `--seed` (needs `--from`) then, under the update lock and then the store lock, writes `slots/a.json` from the manifest, `wanted` (the ids given less core; without ids an existing `wanted` stays, else it is written empty) and a new `pending` set with `tries` 2 of core and the ids given (with `--repair`, core only), with their requirements, as far as their images sealed (none when none did). It never writes `enabled`: the first boot is that set's trial, which `vos health` promotes. `--repair` first removes `slots/b.json`, `enabled`, `pending` and `failed` (`wanted` stays, and vosd proposes the rest of it through a trial). Prints `{"bytes":N,"total":N}` lines on stdout (the bytes of the run's missing images, never going down, ending at the total); exit 0 when every image is sealed, 1 when one is not or anything else fails (reasons on stderr; `--seed` still seeds what sealed), 2 on bad arguments |
| `vos ext launch [--app N\|--shortcut ID/KEY] [--] CMD [ARGS...]` | Steam launch dispatcher, run as `vapor` from the launch options `vos steam prepare` writes (see Extensions, Steam). Its options end at `--` or at the first other word, CMD, after which nothing is read: N is a Steam app id (decimal, 1–4294967295), ID/KEY an extension id and one of its shortcut keys, and at most one of them is given. What starts is, in order: `--app` or `--shortcut`; the `AppId=` of Steam's reaper line when CMD is one (as in Units, Display policy); `SteamAppId`, then `SteamGameId` in its environment. An id with the top bit set, or a shortcut's game id (`(appid << 32) \| 0x02000000`), names an extension's shortcut when `/var/lib/vos/ext/steam.json` lists one with that app id (`crc32("<owner>/<key>") \| 0x80000000`) or prepare's record holds it, and nothing otherwise. For an app it runs the launch hooks of the extensions steam.json lists in that app's `hooks` and `/run/vos/extensions.json` names as mounted, in that file's (catalog) order; for a shortcut, its extension's hook, refusing when that extension is not mounted (also when the report cannot be read). A hook is Go in `vos` (`Helper.LaunchHook`) that may rewrite the command and add to its environment; the programs a hook starts run without `LD_PRELOAD`, `LD_LIBRARY_PATH` and the `STEAM_RUNTIME*` and `PRESSURE_VESSEL*` variables, while the command keeps Steam's environment. It then execs the command (looked up in `PATH` unless absolute; `argv[0]` as given); with no hook to run that is CMD with ARGS and the environment unchanged. A refusal, a hook's error or a hook that leaves no command is exit 1, with the extension, the code and the detail on stderr and in a record for vosd: `$XDG_RUNTIME_DIR/vos/ext-messages/<unix nanoseconds>.json` (`/run/user/<uid>` without an absolute `XDG_RUNTIME_DIR`), `{"code":"not-mounted\|hook-failed\|<helper code>","id":"<extension id>","detail":"..."}` (`not-mounted`: a shortcut whose extension is not mounted; `hook-failed`: a hook's error, or a hook that left no command; a helper's code: a hook's refusal its helper words, see Extensions, Dispatcher messages), written to a temp file and renamed, its `detail` on one line and cut to 300 characters so the record always fits what vosd reads. Once Steam stopped the launch (SIGTERM or SIGINT), which it checks after each hook, whatever the hook returned, and before the exec, it is exit 1 with no record, and nothing more runs. Exit 2 on bad arguments, 1 when it refuses or CMD cannot be run |
| `vos ext action ID NAME [--args JSON]` | runs action NAME of extension ID through its helper (Go in `vos`, `Helper.Action`) in this process, with its shipped descriptor, its settings and its data areas; vosd runs it as `vapor` for every action whose `run_as` is not `root` (see Extensions, Control center). `--args` (or `--args=JSON`) is a JSON object. It refuses an extension `/run/vos/extensions.json` does not name as mounted, and an action with `run_as` `root` when it does not run as root. Exit 0 when the action ran, 1 when it failed (why, as the last line on stderr), 2 on bad arguments, 3 when the extension has no such action |
| `vos ext coolercontrol prepare` / `fans snapshot` / `fans restore` | the steps of the CoolerControl extension's `coolercontrold.service`, as root (see Extensions, CoolerControl): exit 0, 1 on failure (a failed `prepare` stops the start), 2 on bad arguments |
| `vos ext star-citizen fetch-installer --prefix DIR` | as `vapor` (it refuses as root), run by the star-citizen helper's install: downloads the RSI Launcher's installer that the publisher's `latest.yml` names into `DIR/installer/`, resuming and checking it, and prints `{"installer","version"}` (see Extensions, Star Citizen). DIR must be a Star Citizen prefix on its own drive. The last line on stderr says what went wrong, for the card. Exit 0, 1 on failure, 2 on bad arguments |
| `vos ext truckersmp sync\|mp ets2\|ats\|handoff ets2\|ats ID NONCE\|setup\|copy-profiles` | TruckersMP's own commands, as `vapor` only (exit 2 as root or on bad arguments; see Extensions, TruckersMP): `sync` brings the mod's files up to date and prints `{"bytes":N,"total":N}` lines (exit 3 while another sync runs, 4 when the version API does not vouch for a wanted game's core library, 5 when the files do not fit, after a `{"short":N}` line); `mp` starts multiplayer through Steam; `handoff` is what `mp` runs in the transient unit; `setup` copies the injector into the home data area; `copy-profiles` copies the Linux builds' profiles into the Proton prefixes. Exit 0, or 1 with the reason as the last line on stderr |
| `vos index IMAGE` | writes `IMAGE.idx`, the block index of a root image (the build runs it; see "Block index") |
| `vos steam prepare [--unwrap]` | as `vapor`, before every start of Steam and after every stop (`vos-gamescope.service`), and from vosd when `dispatcher` turns false while that unit is down: brings Steam's files in line with `/var/lib/vos/ext/steam.json` (compatibility tools, launch options through `vos ext launch`, shortcuts and their art, branches) within 5 s; `--unwrap` takes the dispatcher and VaporOS's compatibility tools back out (see Extensions, Steam). Does nothing as root; exit 0, 2 on bad arguments |
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
| `/var/lib/vos/steam-libraries.json` | vosd | `{"pending":["/var/mnt/<label>[/SteamLibrary]"]}`: adopted libraries still to be added to Steam's library list, which vosd changes only while Steam is not running (no `steam`, `steam.sh`, `steamwebhelper` or `gamescope*` process of `vapor`, and `vos-gamescope.service` inactive or failed) and only holding the Steam lock (`/run/user/1000/vos-steam.lock`, see Extensions, Steam; a registration that cannot have it within 2 s, or that finds Steam or gamescope's unit up once it has it, waits here for the next round). Without `/run/user/1000` (`vapor`'s manager is not running, so nobody can hold the lock or start Steam) it edits the list without the lock |
| `/var/lib/vos/firmware/edid/vaporos.bin` | vosd | EDID with learned modes; overrides the image one via `firmware_class.path=/var/lib/vos/firmware`. vosd also hands each new version to the running kernel, best effort: it writes `/sys/kernel/debug/dri/<minor or PCI address>/<C>/edid_override`, re-probes the connector by switching its sysfs `status` to `on-digital` and back to `on`, and sends a `change` uevent with `HOTPLUG=1`. It counts only when the connector's sysfs `edid` then matches. vosd skips this during a stream (it applies at `session.end`) and when the new EDID drops the mode on screen. If the kernel refuses, or gamescope does not reach a mode added this way within 15 s, vosd stops trying until the next boot and the mode applies after a reboot |
| `/var/lib/vos/health-ok` | `vos health` | JSON `{"gpu":bool,"stream":bool,"lan":bool,"lan_mac":"<address>"}` from the last good boot; `lan_mac` is the `address` of the network device that had the LAN, recorded only when it is the hardware's own (`addr_assign_type` 0, not random or set by software; omitted otherwise or when unknown) |
| `/usr/lib/vos/extensions.list` | build | the image's extension catalog (see Extensions) |
| `/usr/share/vos/extensions/<id>.json` | build | each extension's descriptor, with what the build verified |
| `/var/lib/vos/ext/` | vosd, initramfs, `vos health`, `vos ext fetch` | the extension store, sets and trial state (see Extensions) |
| `/run/vos/extensions.json` | initramfs | which extensions this boot mounted, and why others were skipped |
| `/var/lib/vos/ext/steam.json` | vosd | what the mounted extensions want in Steam, which `vos steam prepare` applies (see Extensions, Steam) |
| `/var/lib/vos/ext/ports` | vosd | the network ports of the running extensions, which `vos-firewall` opens to the local network (see Firewall) |
| `/run/vos/coolercontrol-fans.json` | `vos ext coolercontrol fans snapshot` | each fan's control mode (and a manual one's duty) and each amdgpu fan curve before CoolerControl took them, this boot (see Extensions, CoolerControl) |
| `~vapor/.local/state/vaporos/steam.json` | `vos steam prepare` | prepare's record of what VaporOS owns in Steam's files; vosd reads it through gamerfs, for display only (see Extensions, Steam) |
| `/run/user/1000/vos-steam.lock` | vosd, `vos steam prepare` | the Steam lock (flock) every writer of Steam's files holds (see Extensions, Steam) |
| `/run/user/1000/vos/ext-messages/` | `vos ext launch`, `vos ext truckersmp mp\|handoff` | the dispatcher's and helper commands' refusals for vosd, which publishes and deletes them (see Extensions, Steam) |
| `/run/user/1000/vos/truckersmp-mp.json` | `vos ext truckersmp mp` | the multiplayer flag TruckersMP's launch hook takes once (see Extensions, TruckersMP) |
| `~vapor/.local/share/vaporos/ext/truckersmp/` | `vos ext truckersmp` | TruckersMP's home data area: `bin/truckersmp-cli.exe`, the mod in `files/`, `manifest.json`, `partial/`, `.sync.lock` (see Extensions, TruckersMP) |
| `/var/lib/vos/ext/data/truckersmp/branch.json` | vosd | the Steam branch each game stays on for TruckersMP (see Extensions, TruckersMP) |
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
              "initrd":{"name":"initramfs.img","size":123,"sha256":"…"},
              "index":{"name":"root.erofs.idx","size":123,"sha256":"…"}}}
```
- `version` is the UTC build or commit time, `YYYYMMDD.HHMMSS`.
- `index` is optional (older updaters ignore it; `min_updater` stays 1): the block index of `root.erofs`, see "Block index". The build always writes it.
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
  3. blobs by `org.opencontainers.image.title` annotation: `manifest.json`, `manifest.json.sig`, `root.erofs`, `vmlinuz`, `initramfs.img`, `root.erofs.idx` when the manifest names it, and each extension image `ext-<id>.raw`. Follow the 307 redirect; resume with `Range`. Block downloads (see "Block index") send `Range: bytes=A-B` and need a 206; they may reuse a storage URL for up to 2 minutes, and ask the registry again when it answers anything else.
  4. an extension image also with no tag at all, as the blob `/v2/<repo>/blobs/sha256:<sha256>`, which works while any tag still holds it (token, redirect and resume as above).
- `http(s)://host/dir/` or a local dir: the same files by name.

**Acceptance:**
- The signature verifies against any key in `/usr/lib/vos/keys`.
- `schema` = 1 and `min_updater` ≤ 1.
- `version` ≠ booted version, unless `--force`.
- `rollback_index` > booted image's, unless `--force` or `--allow-downgrade`.
- Without a picked version or `--force`: `rollback_index` > `held.rollback_index`.
- The version is not in `failed`, unless `--force`.
- Every artifact's size and sha256 match (for OCI, also its layer's digest and size), and every extension image's size, sha256 and fs-verity digest.
- Staging refuses (unless `--force`) while the running entry is on trial, or while it is marked bad and the idle slot's entry is bootable (a rollback waiting for a restart). Neither is recorded as `last_error`.

**Write order:**
0. Fetch kernel and initrd into `/var/tmp`, verified; a file the ESP already holds for an installed version (same size and sha256) is copied from there instead of downloaded. Then fetch and seal (`store.Put`) the extension images the new image needs (`wanted` ∪ its core, with requirements, as its manifest lists them) that the store lacks, before anything is unhooked. Before fetching, the data partition must have those images' bytes plus 2 GiB free, else the stage fails. On a boot whose report has reason `no-verity` no image is fetched, counted or given space (they could not be sealed), and a seal refused as unsupported stops the fetching: either way the new image then starts without them (logged). Any other failure fails the stage.
1. Under ext.lock: remove the idle slot's entries, then write `ext/slots/<idle>.json` from the new manifest (an empty `extensions` object when it has none). An image of step 0 that GC removed in between (no slot file named it yet) is fetched again.
2. Fill the idle slot partition with root: from the block index when the manifest has one (see "Block index"), else by streaming the whole file while hashing.
3. Re-read and verify.
4. Kernel and initrd to `/efi/vos/<ver>/` (tmp + rename).
5. Write entry `vos-<ver>+3.conf` last. The running entry stays a fallback: `+0-1` for a downgrade or the same version, re-blessed if it was at `+0` and the new version is newer. systemd-boot's NVRAM overrides (`LoaderConfigTimeout`, `LoaderEntryDefault`, `LoaderEntryPreferred`) are cleared; the installer clears them after `bootctl install` too.
6. Record `staged` in update-state (and `held` for a downgrade).

**Block index** (`root.erofs.idx`, made by `vos index root.erofs`): a 32-byte
header, then one hash per 4096-byte block of `root.erofs`, in order (the last
block may be shorter). Header: `VOSBIDX1`, the block size (u32 LE, 4096), the
hash size (u32 LE, 16), the image size (u64 LE, = `artifacts.root.size`),
8 zero bytes. A hash is the first 16 bytes of the block's SHA-256. erofs puts
every file's data on block boundaries, so files that did not change between
two images are the same blocks, only at other offsets. With the index, step 2 is:
1. Download the index (verified against the manifest) and read the slots: the
   idle slot's blocks where the new image puts them, and the booted slot's
   blocks up to the size its erofs superblock gives (`blocks << blkszbits` at
   byte 1024; the new image's size when it has none).
2. Every block of the new image the idle slot already holds stays (so a stage
   that stopped part way resumes there); one the booted slot holds anywhere is
   copied from it; the rest is downloaded. Runs of missing blocks are joined
   across gaps of up to 64 KiB, cut at 8 MiB, and fetched 8 at a time as
   `Range` requests of root.
3. Every block is checked against the index before it is written, and step 3
   still checks the whole image.

The updater streams the whole root instead when the manifest has no index, the
index is malformed, more than 3/4 of the image would be downloaded, the source
answers a range with the whole file, or a downloaded block or the read-back
does not match. A download that fails (network, cancel) fails the stage; the
next stage keeps the blocks already in the idle slot. While blocks are
fetched, `update.progress` `write` counts the bytes to download in `bytes` and
`total`; while the slots are read and blocks copied, `total` is 0.

**Update state** (`/var/lib/vos/update-state.json`):
```json
{"booted":"<ver>","staged":{"version":"<ver>","slot":"b","at":"RFC3339"},"failed":["<ver>"],
 "available":{"version":"<ver>","size":123,"checked":"RFC3339"},"checked":"RFC3339","last_error":"",
 "held":{"version":"<ver>","rollback_index":N}}
```
`available.size` is the most a stage downloads: the root plus the extension
images this machine would still have to fetch for it (Write order step 0; none
on a boot with reason `no-verity`). With the block index, less of the root is
downloaded.
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
- if `health-ok.lan`, the LAN is up within 60 s of the start (the check runs alongside the others): some network device (an interface with `/sys/class/net/<if>/device`; not `lo`, bridges or tunnels) that is up (`IFF_UP` in its `flags`; without `flags`, a readable `carrier`) with `carrier` 1 has an IPv4 address that is neither loopback nor link-local, or a global or unique local IPv6 one. If none has by the deadline, it fails when no network device has the `address` `health-ok.lan_mac` (the device is gone; not checked without `lan_mac`), when a wired one (`type` 1, without `wireless` or `phy80211`, and neither `DEVTYPE=wlan` nor `DEVTYPE=wwan` in its `uevent`) or the one with the `address` `lan_mac` is down (NetworkManager brings up every wired device, also without a cable, while a radio or modem may be down for a switch or a missing SIM), or when a network device has a link without such an address; when every network device is up with `carrier` 0, or down and neither wired nor the `lan_mac` one, or there is none (nothing is plugged in), it passes and keeps `health-ok.lan` and `lan_mac`;
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
extension trial that passes, it first writes the booted set's number (if
the report names one) to `/run/vos/ext-trial-ok` (temp + rename), so vosd
can still promote the set should the next step not get the lock. After a
good boot whose report mounted anything on purpose (mode other than `off`),
it takes ext.lock (waiting up to 30 s) and records the boot (proven images,
promotion; see "Trial and promotion"); a problem there is logged and never
fails the boot.

## Extensions

Extensions add software that is not part of the VaporOS image (Proton,
CoolerControl, TruckersMP, ...). They are curated: each is a directory in
`extensions/<id>/` of this repository, built by the same build as the image it
belongs to, against that image's exact packages, and listed in its signed
manifest. Each extension is itself immutable: one sealed, read-only image.

**Source** (`extensions/<id>/`): `extension.json`, the descriptor (schema 1,
unknown fields rejected; `internal/extensions/descriptor`), and `files/usr/...`,
copied into the image. A `steam.hooks` entry is `{"apps":[<app id>...]}`
and nothing more: the hooks of several extensions on one app run in catalog
order. A setting may be `"required": true` only when its `type` is `disk`:
a drive the extension cannot do without (see Control center). Integration
logic is Go in `vos`
(`internal/extensions/<id>`). Non-goals: no `/opt` or `/usr/local` payloads, no
AUR or DKMS, no `.ko`, no `sysusers.d` or `tmpfiles.d`, no confext, no plugin
stores that run code as root.

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
directory of theirs but has another mode, or with a directory of the base
that is not root's (the merged directory takes the image's mode and owner,
and every directory of an image is root's, `--all-root`; root is the owner of
the base's `/`), so a base directory not owned by root cannot be extended;
writes under
`usr/lib/systemd`, `usr/lib/udev`, `usr/share/dbus-1`, `usr/share/polkit-1`,
`usr/lib/security`, `usr/share/vulkan`, any other `*.d/` hook directory (one
not reviewed as harmless) or `usr/lib/vos/**` except
`usr/lib/vos/ext/<id>/**`, or anything under `usr/share/vos`, beyond the categories its
descriptor declares in `permissions` (`service`, `user-service`, `udev`,
`sysctl`, `modules`, `polkit`, `dbus`, `compat-tool`), with any
difference between declared and found failing too; ships `sysusers.d`,
`tmpfiles.d` (`usr/lib/tmpfiles.d` or `usr/share/user-tmpfiles.d`: a service
gets its directories from `StateDirectory=`, `RuntimeDirectory=`,
`CacheDirectory=` and `LogsDirectory=`, and vosd creates the data areas), `*.ko`,
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
extension sets; has a unit, alias, drop-in or dependency directory whose name ends
in `-` before `@` or its type (a systemd prefix drop-in applies to every unit
with that prefix), or that is, or is for, a `.mount`, `.automount`, `.swap`,
`.slice`, `.scope` or `.device` unit (systemd and generators name these at
runtime: `efi.mount`, `var-lib-vos.mount`, `var-log-journal.mount`,
`user-1000.slice`, ...); a unit or alias the base has (in
`usr/lib/systemd/<scope>` or `etc/systemd/<scope>`) by name or template, a
template the base has an instance of, a drop-in that is a symlink, or two
drop-ins of the same name for one unit in directories whose order systemd
leaves open (an instance's hides its template's); ships a system unit that
runs commands (a service, or a socket with `Exec*=` commands) whose last
`TimeoutStartSec=` or `TimeoutSec=` (only `TimeoutSec=` in `[Socket]`) is not
a finite one set by a drop-in of its own; has a unit or drop-in whose
`[Unit]` `Before=`, `Conflicts=` (except `shutdown.target`, which default
dependencies add anyway), `OnFailure=`, `OnSuccess=`, `PropagatesStopTo=`,
`PropagatesReloadTo=`, `Upholds=` or `JoinsNamespaceOf=` names a unit of the
base (of its scope, by name or template), one of those runtime types, or a
name with a specifier outside its instance (`PartOf=` and
`StopPropagatedFrom=` may name a unit of the base or a runtime type, and
`BindsTo=` too unless it is a system-state unit or a name with a specifier:
they only make the extension's own unit follow it); a `.upholds` entry for
such a unit; a timer, path or socket that may start one (every non-empty
`Unit=` or `Service=`, although systemd keeps a timer's or path's first and a
socket's last, and always the service of its own name, a template's with
`Accept=yes`, which systemd uses when it ignores them all); a `FailureAction=`,
`SuccessAction=`, `StartLimitAction=` or `JobTimeoutAction=` other than
`none` (in any section: `[Service]` still reads the old `FailureAction=` and
`StartLimitAction=`); a `[Unit]` `OnFailureJobMode=` or `OnSuccessJobMode=`
of `isolate` or `flush` (they stop or cancel other units' jobs),
`OnFailureIsolate=yes` or `AllowIsolate=yes`; a `[Unit]` `Wants=`,
`Requires=`, `Requisite=`, `Upholds=` or `BindsTo=` (or the old `BindTo=`,
`RequiresOverridable=`, `RequisiteOverridable=`), or a `.wants`, `.requires`
or `.upholds` entry, that names a unit changing the system's state (the
`reboot`, `poweroff`, `halt`, `kexec`, `soft-reboot`, `exit`, `emergency`,
`rescue`, `shutdown`, `final`, `ctrl-alt-del`, `sleep`, `suspend`,
`hibernate`, `hybrid-sleep` and `suspend-then-hibernate` targets,
`systemd-{reboot,poweroff,halt,kexec,soft-reboot,suspend,hibernate,hybrid-sleep,suspend-then-hibernate,exit}.service`,
`emergency.service` and `rescue.service`), also through an alias the base
has (`runlevel6.target`), or a name with a specifier outside its instance;
or drop-ins and
`.wants`/`.requires`/`.upholds` for a unit it does not ship (a unit's drop-ins
are those of `<unit>.d/*.conf`, its template's and its aliases', merged by
file name before these checks); or has an ELF (outside
`elf_exempt`) with a `DT_NEEDED` that resolves nowhere: not through its
`DT_RUNPATH` (else `DT_RPATH`, with `$ORIGIN`) in the image or the base, not
in the loader's default directories (`usr/lib` and `usr/lib/x86_64-linux-gnu`,
or `usr/lib32` for 32-bit) of the image or the base, and not in the base's
`ld.so.conf` directories (in the
base's `ld.so.cache`, which never lists the image's libraries). `vos ext
check-tree` also checks that `strip` paths are gone, that the `services` units
exist, and that `usr/lib/vos/ext/<id>/` holds the source descriptor, the
descriptor's exact `module_options` pairs and every `fetch` file. Its
`runs_as_root` is true when a system unit runs a command as root: a service,
or a socket with commands, without a `User=` other than root
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
`^[a-z][a-z0-9-]{0,31}$` and are not `skip-once`, a word vosd's routes use
where an id goes (`/extensions/skip-once`; the descriptor, the catalog and the
manifest all refuse it); `requires` must name extensions of the same manifest,
without cycles. The OCI artifact carries each image as a layer titled
`ext-<id>.raw` (media type `application/vnd.vaporos.extension.v1.erofs`); http
and directory sources serve it by that name next to `manifest.json`.

**Store** (`/var/lib/vos/ext/`, root's):

| Path | What |
| --- | --- |
| `images/<sha256>.raw` | A sealed image: written to a temp file, fsynced, sha256-checked, closed, reopened read-only, `FS_IOC_ENABLE_VERITY` (sha256, 4096, no salt), `FS_IOC_MEASURE_VERITY` compared with the catalog, then renamed into place and the directory fsynced. A file under its final name is always sealed; one without fs-verity, or with another digest, is deleted and fetched again. `images/` is a real directory: vosd never uses a symlinked one (the initramfs takes it for missing) but replaces the link with a real directory (the parent fsynced), so its images are fetched again |
| `wanted` | the ids the user added, one per line (core ids are always wanted) |
| `sets/<n>/ids`, `sets/<n>/modprobe.conf`, `sets/<n>/tries` | one attempt at a set of extensions: ids (one per line, catalog order), the module options it sets (`options <module> <param>=<value>` lines, module and param `[A-Za-z0-9_-]+`, value `[0-9A-Za-z_x.-]+`; other lines are dropped), boots left to try it (one digit; anything else reads as 0). `<n>` is a decimal number that is never used twice: the larger of `sets/.next` and one more than any set in `sets/` or named by a link or the boot report. Written into `sets/.tmp-<n>`, fsynced, renamed |
| `sets/.next` | the high-water mark: the next set number (one decimal line), written atomically after each new set is renamed into place and never lowered; anything else reads as 0 |
| `enabled`, `pending` | symlinks `sets/<n>` (relative): the last good set, and the set on trial. Renaming `pending` into place is the commit of a change |
| `proven` | `<id> <fsverity>` lines: images that passed a boot, pruned by GC |
| `failed` | `<fingerprint>` lines: sets whose trial failed. The fingerprint is the hex sha256 of the set's sorted, unique `<id> <fsverity>` lines (digests from the booted catalog) followed by its sorted, unique option lines, each ending in `\n` |
| `skip-once` | present: the next boot mounts no extension, then the initramfs removes it (vosd writes it for POST `/extensions/skip-once` and removes it for DELETE) |
| `slots/<a\|b>.json` | `{"version","extensions":{...manifest extensions...}}`: the catalog of each slot's image, from its signed manifest (`vos update` writes the idle slot's at Write order 1, the installer `a.json` through `vos ext fetch --seed`), or, for the booted slot, from the booted catalog (vosd) |
| `settings/<id>.json` | the extension's settings (never in config.json): one JSON object of its descriptor's setting keys, written whole (temp + rename) when it is added and on each change. A key missing, or with a value its setting does not take, reads as the default: the descriptor's `default` when the setting takes it, else `false`, the first choice, or `""` (no drive picked yet); a disk setting's `/state` (the system drive's folder earlier control centers offered) reads as `/var`. Purging the extension deletes it |
| `settings/<id>.installed` | present: the extension's helper finished its `Install` and no `Remove` came after (see Control center) |
| `steam.json` | what the mounted extensions want in Steam, written by vosd for `vos steam prepare` (see Steam) |
| `steam-owned.json` | `{"ids":["<id>",...]}` (sorted, at most 256): every extension that was wanted (with core and requirements) or mounted on this box at some point, so the only ones that may have set something in Steam (`release`, see Steam). vosd adds to it (atomically, before it writes `steam.json`) and never removes from it; ids that are not extension ids are dropped when read, and a file that cannot be read counts as empty |
| `ports` | `<proto> <port>` lines (`tcp` or `udp`, 1024-65535, sorted, each once), `tcp <port> upstream <upstream port>` for a `proxied` one (its `upstream`'s port on 127.0.0.1, which only root may connect to; see Firewall): the descriptors' `network.ports` of the extensions this boot mounted that `wanted` ∪ core (with their requirements) still wants, never 47990, also not as an upstream. vosd writes it (0644, atomically) at start, after every reconcile and when the control center adds or removes an extension, only when its bytes change, and then reloads `vos-firewall.service` (a reload that failed is tried again at the next of those moments). A missing file opens nothing, as an empty one (see Firewall) |
| `autorestart.json` | `{"restarts":[{"at","set","fingerprint","version"}]}`: the auto-restarts vosd made (RFC 3339 UTC; the pending set's number and fingerprint; the VaporOS version it restarted from), the last 32, written atomically. A file that cannot be read counts as empty |
| `data/<id>/` | its `system` data area (`home` ones are in `/var/home/vapor/.local/share/vaporos/ext/<id>/`, `library` ones in `<library>/VaporOS/<id>`) |

Images are sealed without the lock (a final name is always sealed), and may
be fetched before the `wanted`, set or slot file that keeps them, so GC
leaves an image, sealed or temp (`images/.tmp-*`), alone for an hour after
its last write (mtime). An image is fetched only while it fits on the data
partition with 2 GiB to spare (checked before its download starts).
`/run/vos/ext.lock` (flock) serialises every write to
`wanted`, the sets, `enabled`, `pending`, `proven`, `failed` and the store's
garbage collection. The slot files are written under the update lock and
ext.lock. Lock order: the update lock, then ext.lock; whoever holds ext.lock
waits for the update lock only for a bounded time. GC (one run, which reads
the slot files under ext.lock) keeps the images that
`wanted` ∪ core resolve to in the booted catalog and in both slot files, and
those mounted this boot; the sets that `enabled`, `pending` or the boot report
name; and the `proven` lines whose pair the booted catalog or a slot file
lists or this boot mounted: every run prunes `proven`. vos reads the line files (`wanted`, `proven`,
`failed`, a set's files) whole or not at all: one over 1 MiB is an error, and
a line of 4 KiB or more (longer than any valid one) is skipped. Two writers
of `proven` read it whatever its size: GC, which rewrites it without the
lines it drops (those too long included), and recording a good boot, which
rewrites one over 1 MiB from its valid lines (from the new ones alone if
those do not fit either).

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
   `images/<sha256>.raw` missing, a symlink or in an `images/` that is a
   symlink (`missing`) or of another size (`size`), not in `proven` (`unproven`, `enabled` mode only), `fsverity
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
then, even when `proven` could not be written, if `pending` still points at the booted set and every id of the set
mounted or was skipped as `not-in-catalog`, `enabled` is replaced by that set
(rename, directory fsync) and `pending` removed. A set the user no longer
wants is never promoted: reconcile removes or replaces such a `pending`, also
on its own trial, and a promoter that knows the desired set's fingerprint
promotes only when what booted has it, or the trial set's own while
reconcile keeps that set because the desired set only adds to it (see
Reconcile, below rule 6). On trial boots (mode `pending`, or any
boot systemd-boot counts, also with `vos.ext=0` or `skip-once`) the generator
gives `vos-health.service` a drop-in with `JobTimeoutSec=10min` and
`JobTimeoutAction=reboot-force` (see Units).

A trial that hangs the kernel or PID 1 must still reboot, so it uses up a try.
CachyOS blacklists the hardware watchdog drivers, which only stops their
aliases, so on trial boots (mode `pending`, or any boot systemd-boot counts,
also with `vos.ext=0` or `skip-once`) the initramfs, after the report, loads
them by name (`modprobe -q` `sp5100_tco`, `iTCO_wdt` and `wdat_wdt`, carried
in the initramfs where the kernel has them; the build fails an initramfs
without `modprobe` or without any of the three) and writes
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
   (fingerprint at the booted catalog's digests). It is only removed on an OS
   trial (its trials ran at the old image's digests, and the desired set gets
   a trial of its own at the new ones) and when the image of an id of it that
   the booted catalog lists is not sealed now (its trials could not mount it;
   reconcile proposes it again once the image is there).
3. Desired equal to what booted: no `pending`, unless this boot is that
   `pending` set's trial and the set booted whole (every id mounted or skipped
   as `not-in-catalog`), which `vos health` promotes.
4. Equal to `pending` (ids and options): left alone.
5. Its fingerprint in `failed`: not proposed, and a `pending` is removed (the
   extension needs attention; "Try again" removes the fingerprint).
6. Otherwise a new set becomes `pending` with `tries` 2.

Rules 5 and 6 wait while this boot is the trial of the `pending` set, that
set booted whole (every id the booted catalog lists mounted, with the
options its ids' settings render now) and the desired set only adds to it
(it holds every id of the set the booted catalog lists): `pending` is kept,
so `vos health` promotes it and the box has it to fall back on (an install's
seeded core set among them), and the pass after the promotion acts.

After a removal (1, 2, 3 or 5) reconcile runs again. A restart is needed (to
try `pending`) only while `pending` has tries left, this boot is neither its
trial nor one with reason `cmdline`, `skip-once`, `tries-write` or `no-report`
(after those the next boot would not try it either), and every image it names
is sealed.

**vosd** (installed systems only; `internal/extensions`). At start it removes
the temp files of downloads that stopped (`images/.tmp-*` untouched for an
hour). It reconciles at start, whenever a change asks
for it (wanted, a setting, the store), and, while something is left that a
later reconcile could do (an image it could fetch still missing, a slot file
or a reconcile that failed, a trial `vos health` has not passed yet), again
after 1 minute, doubling up to every 30 minutes. One reconcile:
1. writes `slots/<booted>.json` from the booted catalog (each `name`
   `ext-<id>.raw`), under the update lock (waiting at most 10 s) and then the
   lock, unless the file already lists the same images for the booted version;
2. under the lock, when this boot is the trial of the set `pending` still
   names and `/run/vos/ext-trial-ok` names it too (`vos health` passed it but
   may not have recorded it), first records the boot as `vos health` does
   (the mounted images proven, best effort: a failure is logged and the
   pass goes on), promoting only when what booted is the desired set, or
   the trial set itself while rules 5 and 6 wait for it (a promotion that
   fails and leaves `pending` naming the set ends the pass); then applies what
   rules 1 and 2 say (a `pending` left over or out of tries), and nothing
   else: a `pending` whose image is still to come is not removed for it;
3. without the lock, fetches and seals the plan's missing images for the
   booted version;
4. under the lock, plans again and acts in full (fail, clear, propose, keep or
   blocked, as above), planning again after each write;
5. fetches, best effort, the images `wanted` ∪ core need in the other slot
   file's version, when it names another;
6. runs GC, unless a slot file cannot be read.

Images come from `config.update.source` at the version: a registry at the tag
`<version>` (any tag in the source is replaced), by layer title, and when the
registry answered otherwise (no such tag or layer, other bytes), as the blob
by digest, which still works once the tag is gone or moved; an HTTP or
directory source by name. A network failure (one the download gave up on), a
failed write to the disk or a failed seal is never followed by the blob.
Before its first download, a reconcile asks the source for its manifest: a
source it cannot reach, or that stops answering during a download, is asked
for nothing else in that reconcile (only a registry or an HTTP(S) source
stops answering: a file of a directory source that ends early fails that
image alone). A source that served other bytes for an
image is not asked for it again until a reconcile is asked for; an image that
does not fit (2 GiB to spare, also a disk that filled up during its download
or its seal: the fsync, close and rename, or the Merkle tree fs-verity writes)
is not fetched again until more space is free or a reconcile is asked for, and
its card says so; on a disk that cannot seal (boot reason `no-verity`, or a
seal refused as unsupported) nothing is fetched until the next boot. Once per
boot, after the first reconcile and beside the later ones, vosd reads every
mounted image through at idle I/O priority
(`ioprio_set`, class idle). A read that fails with `EIO` (fs-verity checks
every block) or bytes whose size or sha256 are not the booted catalog's delete
the image, at most once per digest per boot, and a reconcile follows that
fetches it again: the desired set keeps it meanwhile, so the next boot mounts
the new copy. A missing file is not damage. Idle shutdown counts as busy
(`adding an extension`) a download from its first bytes until it and its seal
end, unless no bytes came for 2 minutes, the re-read, and every helper call
(`Install`, `Remove`, an action, `PasswordChanged`; see Control center); and, with its own
reason, a helper's own work while the helper says so (`Busier`; TruckersMP's
sync: `updating TruckersMP`).

**Install** (the `configure` step, after slot a is written and the target is
mounted): the installer runs the new image's own
`<target>/usr/bin/vos ext fetch --state-dir <target>/var/lib/vos --from SRC --version <ver> --seed`
(plus `--repair` on a repair), which seals the image's core extensions into
the new system's store as the `pending` set (tries 2): the first boot is an
extension trial that `vos health` proves and promotes, and a new install has
no `enabled` before that. A repair tries core only; the rest of `wanted`
(kept, its images fetched too) vosd proposes through a trial of its own
once the core set's trial is promoted. SRC
is the install's source (a directory, http(s) or a registry, which the
command asks at the tag `<ver>`), or for the live medium (which carries no
extension images) the new system's `config.update.source`. It is best effort
and capped at 20 minutes: a failure is logged, and vosd fetches what is
missing once the system runs. Its progress lines (bytes of the whole run)
show as `configure`: the first one, then whenever the percent moves, at least
once a second while bytes come in, and the last. Before it runs, a repair
removes `slots/b.json` (slot b was wiped), `enabled`, `pending` and `failed`,
and keeps `wanted`: a repair that cannot run the command (offline) boots
without extensions rather than with the old set, and vosd proposes the wanted
ones through a trial once it can.

**Steam** (installed systems only; `vos steam prepare` is
`internal/steamprep`, vosd's side `internal/extensions`; the files' formats
are Steam's). Extensions change Steam's own files only through
`vos steam prepare`, which runs as `vapor` while Steam is down: before
every start of Steam and after every stop. vosd says what is wanted and
reads what was done; it never edits those files for an extension (it
only starts prepare as `vapor`, see `dispatcher`).

*Desired state,* `/var/lib/vos/ext/steam.json` (root's, 0644), written by
vosd atomically and only when its bytes change: at start before its first
reconcile, after every reconcile, when the control center adds or
removes an extension, changes its settings or runs one of its actions,
when what the other slot boots may have changed (a stage of vosd's once it
recorded the idle slot's `slots/<slot>.json`, and again when that stage
ends; a rollback; an activation, before its restart), and about once a
minute (which also catches `vos update` and `vos rollback` run from a
shell). The minute's check, the launch messages and the account check run
on their own in vosd, apart from the reconciles. When the file changes
vosd asks for a Steam restart (see Units, Display policy).
```json
{"set":"<n>","dispatcher":true,"default_compat_tool":"proton-cachyos-slr",
 "apps":[{"app":227300,"compat_tool":"proton-cachyos-slr","hooks":["truckersmp"],"beta":{"branch":"temporary_1_61","request":"<id>"}}],
 "shortcuts":[{"owner":"star-citizen","key":"launcher","name":"Star Citizen","exe":"/var/mnt/<label>/VaporOS/star-citizen/installer/<file>","start_dir":"/var/mnt/<label>/VaporOS/star-citizen","compat_tool":"proton-cachyos-slr","art":""},
              {"owner":"truckersmp","key":"ets2","name":"TruckersMP (ETS2)","exe":"/usr/bin/vos","start_dir":"/var/home/vapor/.local/share/vaporos/ext/truckersmp","args":["ext","truckersmp","mp","ets2"],"compat_tool":"","art":""}],
 "release":[{"app":227300}],
 "owners":["proton","star-citizen","truckersmp"]}
```
- `set`: the boot report's `set` (`""` when it has none). prepare applies
  the file only while it names this boot's set, and otherwise changes
  nothing (vosd writes it anew and restarts Steam).
- Only extensions this boot mounted contribute (`owners` aside), and of those only the ones
  `wanted` ∪ core (with their requirements, in the booted catalog) still
  wants, so one removed until the restart drops out at once: each from its
  shipped descriptor and its helper's `SteamParts` (settings from
  `settings/<id>.json` over the descriptor's defaults). A tool
  (`default_compat_tool`, `compat_tool`) is named only when
  `/usr/share/steam/compatibilitytools.d/<tool>/compatibilitytool.vdf`
  exists, and is `""` otherwise.
- `default_compat_tool`: the `steam.default_compat_tool` of the first
  mounted core extension that has one.
- `apps`: one entry per app id. `compat_tool` is `steam.compat_tool` of the
  extension whose `steam.force_compat_tool` lists the app; `hooks` are the
  extensions whose `steam.hooks` name it, in catalog order; `beta` is the
  request a helper's `SteamParts.Beta` makes, `null` when none does:
  `branch` the branch to switch to (`""` the public branch) and `request`
  the helper's id for this request (`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`),
  the same while the request stands and new only for a new switch (a
  helper's `Steam` gives the same parts for the same state, so steam.json
  changes only when something did). prepare applies
  each id once (step 5.5), so a branch the user picks in Steam afterwards
  stays theirs until the helper makes a request with a new id. A request
  vosd would not take (a branch or id of another form, app 0) is left out,
  with a log line.
- `shortcuts`: each descriptor `steam.shortcuts` entry whose helper's
  `SteamParts.Shortcuts` gives it a target (`exe`, `start_dir`: canonical
  absolute paths; one without a target yet is left out; `args`, omitted
  when there are none: up to 16 words of `[A-Za-z0-9._/:=+-]`, at most 128
  characters each, else the shortcut is left out), with `compat_tool`
  the extension's `steam.compat_tool` when the shortcut sets `compat_tool`,
  and `art` its `art` directory in the mounted image
  (`/usr/lib/vos/ext/<owner>/<art>`) when that directory exists, else `""`.
- `release`: apps a removed extension used to force: those the shipped
  descriptors of the catalog's extensions no longer wanted force, of the
  extensions `steam-owned.json` lists (once wanted or mounted on this box,
  so one never added releases nothing), unless a listed extension forces
  them (empty while `wanted` cannot be read). prepare hands over only the
  mappings its record owns: such a mapping stays, and VaporOS no longer
  owns it; an app whose mapping VaporOS does not own is left alone.
- `owners`: the extensions whose shortcuts stay while this boot lists
  none of theirs, sorted: `wanted` as it is (ids the booted catalog lacks
  included), `wanted` ∪ core with their requirements in the booted
  catalog, and every id this boot mounted (one removed stays until the
  boot that no longer mounts it). `null` while `wanted` cannot be read,
  and then prepare removes no shortcut (step 5.3).
- `dispatcher`: true only when every VaporOS the box can boot has
  `vos ext launch`: the booted catalog has `dispatcher` 1 or more, and the
  other slot has no boot entry, or its `slots/<other>.json` is for its
  entry's version and lists extensions (only updaters that know extensions
  write slot files, images built before extensions list none, and every
  image built with them has the dispatcher). The minute's check does not
  read the ESP each time: vosd reads the other slot's entry again only
  when `slots/<other>.json` or `update-state.json` changed (size or
  mtime; a stage writes both around its change of the entries, also
  from a shell), 30 minutes after the last reading, and when its own
  update service changed the slots. An ESP that cannot be read keeps
  `dispatcher` as `steam.json` has it, and a second failed read in a row
  makes it false. While it is false no launch options are wrapped, and
  those that were are unwrapped. So staging an image built before
  extensions (a downgrade) turns it false at once, and the next prepare
  unwraps them before that image can boot: the Steam restart that follows
  (at the next quiet moment), or gamescope's stop
  (`ExecStopPost=-/usr/bin/vos steam prepare`, at shutdown too). When it
  turns false (from true in the file it replaces) while
  `vos-gamescope.service` is inactive or failed, no stop or restart is
  coming, so vosd runs `vos steam prepare` as `vapor` (`runuser`, as it
  runs vapor's actions) at once.
- prepare ignores, with a log line, entries that are not well formed: ids
  and keys `^[a-z][a-z0-9-]{0,31}$`, tools, branches and branch request
  ids `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, app ids above 0 and listed once,
  paths absolute and clean without `"`, `args` as vosd writes them, `art`
  under `/usr/lib/vos/ext/<owner>/`, `owners` ids. A `beta` that is not such an object goes
  alone: the rest of its app applies, and that app's branch and its record
  are left as they are. An extension an ignored entry names still counts
  as named (step 5.3).

*prepare's record,* `/var/home/vapor/.local/state/vaporos/steam.json`
(vapor's, 0644), written only by `vos steam prepare`, atomically, right
after each of Steam's files it replaces. vosd reads it through gamerfs and
trusts it for display only:
```json
{"fingerprint":"<hex>","vos":"<version>","accounts":["<accountid>"],
 "default":{"wrote":"proton-cachyos-slr","before":null,"suspended":false},
 "apps":{"227300":{"mapping":{"wrote":"proton-cachyos-slr","before":{"name":"proton_9","config":"","priority":"250"},"suspended":false},
                   "launch":{"<accountid>":{"wrote":"<launch options VaporOS wrote>","before":"<the user's>"}},
                   "beta":{"wrote":"temporary_1_61","before":"temporary_1_53","request":"<id>"}}},
 "shortcuts":{"<accountid>":{"star-citizen/launcher":{"appid":3799105208,"gameid":"16317032622456832000","deleted":false,
                                                     "art":{"3799105208p.png":{"size":123456,"mtime":1759400000000000000}}}}},
 "skipped":"","error":""}
```
- A mapping's `wrote` is the tool VaporOS wrote (`""`: it owns no entry),
  `before` the entry before it (`null`: none), and `suspended` that VaporOS
  put `before` back for now and still owns the entry (below). `apps` also
  holds the mappings of shortcut app ids.
- `launch.<accountid>` may instead be
  `{"wrote":"","before":"<the user's>","conflict":true}`: the user's
  options have several `%command%`, so they only lost any dispatcher
  tokens; the card says the extension needs attention.
- `beta.request`: the id of the request VaporOS applied (step 5.5).
- `shortcuts.<accountid>.<owner>/<key>.deleted`: the user removed a
  shortcut VaporOS added. It is not added again, and the card says so.
  `art` (omitted when empty): the grid files VaporOS wrote for the
  shortcut, by name, with their size and mtime (Unix nanoseconds) as it
  wrote them (step 4).
- `accounts`: the accounts in loginusers.vdf at the last run, one that
  skipped included (step 3); a run that cannot read loginusers.vdf keeps
  them as they were. vosd checks every 15 s and asks for a Steam
  restart when `~/.local/share/Steam/config/loginusers.vdf` lists an
  account that is missing here, once per such set of accounts (never
  without a record). Both sides count the same accounts (step 5.2:
  individual ones, never anonymous or id 0), by SteamID64 & 0xffffffff.
- `skipped`: why the last run changed nothing (step 3): `steam-running`,
  `no-steam-json`, `bad-steam-json` or `other-set`; `""` when it went ahead
  or found nothing to do.
- `error`: what failed, `; `-separated (a file that does not parse or could
  not be written, `out of time`), and `""` after a clean run.

*`vos steam prepare [--unwrap]`* (`ExecStartPre=-` and `ExecStopPost=-`
of `vos-gamescope.service`, and vosd's run when `dispatcher` turns false):
1. As root it does nothing: it never writes vapor's files as root. It
   always exits 0 (2 on bad arguments) and logs to stderr (the journal).
2. It has a 5 s budget on its own timer. The work stops between files at
   4.5 s and records where it stopped; the command returns at 5 s whatever
   a step is doing.
3. It changes nothing when `~/.steam/root` exists and does not resolve to
   `~/.local/share/Steam` (where vosd reads); when a `steam`, `steam.sh` or
   `steamwebhelper` process runs as vapor; when it cannot take the Steam
   lock in time; without `steam.json` (unless `--unwrap`); or when
   `steam.json`'s `set` is not the boot report's. The Steam lock
   (`internal/steamlock`) is flock(`LOCK_EX`) on
   `/run/user/1000/vos-steam.lock`, opened through gamerfs and made empty,
   `vapor`'s and 0600 when missing; prepare holds it for the whole run, and
   vosd's library registration while it edits Steam's library lists,
   waiting at most 2 s (see Paths, `steam-libraries.json`). All of these
   but `~/.steam/root` and the lock still write the record, so a skipped
   run does not make vosd restart Steam for the same accounts again:
   `skipped` (`steam-running`, `no-steam-json`, `bad-steam-json` for one
   that cannot be read or parsed, with the reason in `error`, `other-set`)
   and `accounts` as loginusers.vdf has them (unchanged when it cannot be
   read), the rest as it was. Without the lock another run may be writing
   the record, so that one writes nothing.
4. The fingerprint is the sha256 of `steam.json`'s bytes, the vos version,
   `--unwrap`, whether the boot report's mode is `off` (step 5.3),
   whether each tool `steam.json` names or VaporOS owns an entry
   for is installed, loginusers.vdf's bytes, and the size, mtime and mode of
   config.vdf, `steamapps/libraryfolders.vdf`, each account's localconfig.vdf
   and shortcuts.vdf and, in every library, the appmanifests of the apps a
   branch is asked or recorded for, and whether each library's `steamapps`
   is there (with such apps only). When it
   equals the record's and the record has no error, there is nothing to do
   (and `skipped` becomes `""`). A run that ends without an error records
   the fingerprint as it is after its writes; any other records `""`.
5. Then, in this order, it edits Steam's files without changing any other
   byte in them. Each file is replaced only when its bytes change,
   atomically (a temp file in its directory, fsync, rename, directory
   fsync) and with its mode kept. A file that does not parse, is larger
   than Steam's ever are (4 MiB; localconfig.vdf 64 MiB), or whose edited
   bytes would not parse again within that size, is not touched and goes
   into `error`. config.vdf and each account's localconfig.vdf and
   shortcuts.vdf are parsed once a run, and all their edits are made on
   that parse in one pass (an entry the same pass set and then removes
   is not written); an appmanifest gets one edit. Lookups follow
   Steam's on Linux: keys match in any case, the first of repeated keys
   counts, and an entry whose conditional is false on Linux (`[$WIN32]`,
   `[$WINDOWS]`, `[$OSX]`, `[$X360]`, `[$PS3]`, `[!$LINUX]`, `[!$POSIX]`,
   and `||`/`&&` of them) is not there, so it is neither read nor changed;
   a conditional stays with its entry. Taking an entry out from between two
   unquoted tokens leaves a space between them.
   1. **config.vdf**, `InstallConfigStore/Software/Valve/Steam/CompatToolMapping`.
      prepare never creates config.vdf, but Steam does on its first start,
      so `"0"` is set before anyone signs in. A missing `CompatToolMapping`
      block is added. Entries VaporOS writes are
      `{"name":<tool>,"config":"","priority":<p>}`.
      - `"0"`, every Windows game's default: `default_compat_tool` at 75,
        written only where `"0"` is missing or still holds the tool VaporOS
        last wrote. Any other value is the user's choice, and VaporOS stops
        owning it.
      - Each app's `compat_tool` at 250, forced: written whatever the entry
        holds, its first value kept as `before`.
      - Each VaporOS shortcut's `compat_tool` at 250, under the app id it has
        in shortcuts.vdf (step 3).
      - Never Steam's tool apps (`steam.App.IsTool`), by id: Proton
        Experimental 1493710, Hotfix 2180100, 11.0 4628710, 10.0 3658110,
        9.0 2805730, 8.0 2348590, 7.0 1887720, 6.3 1580130, 5.13 1420170,
        5.0 1245040, 4.11 1113280, 4.2 1054830, 3.16 961940 and 3.7 858280,
        the Steam Linux Runtimes 1070560, 1391110, 1628350 and 4183110, the
        EAC and BattlEye runtimes 1826330 and 1161040, the redistributables
        228980; or by the name in the app's installed appmanifest (`Proton`,
        `Proton …`, `Steam Linux Runtime…`, `Steamworks Common
        Redistributables…`). Such an app is not mapped, wrapped or switched
        to a branch.
      - No dangling mappings: while the tool of an entry VaporOS owns is not
        installed (its compatibilitytool.vdf is missing), the entry holds
        `before` (deleted when `null` or when it names that missing tool)
        and is `suspended`. VaporOS keeps owning it, and whatever the entry
        becomes meanwhile counts as Steam's, not the user's. Once the tool
        is back and `steam.json` asks for it, VaporOS writes its value
        again, with what the entry held then as the new `before`.
      - An owned entry `steam.json` no longer asks for is kept as it is (and
        owned) while its tool is installed, as is the entry of a VaporOS
        shortcut that stays without being listed (step 3). A shortcut's
        entry goes with its shortcut, never while which shortcuts an
        account has is not known: its shortcuts.vdf or loginusers.vdf
        cannot be read, or the record holds shortcuts of an account this
        run does not plan (not in loginusers.vdf, or without its
        `config/` directory). A `release` app's entry stays and is no
        longer owned, unless `apps` still forces a tool on it.
      - An entry that already holds the tool `steam.json` asks for is taken
        as VaporOS's, with `before` `null`.
   2. **localconfig.vdf** of each account in loginusers.vdf (individual
      accounts, never an anonymous one or id 0; the account id is the
      SteamID64's low 32 bits),
      `userdata/<accountid>/config/localconfig.vdf`, skipped while it is
      missing: `UserLocalConfigStore/Software/Valve/Steam/apps/<app>/LaunchOptions`.
      With `dispatcher` true, each app with `hooks` is wrapped as below.
      Every other app that carries a dispatcher token, and every app while
      `dispatcher` is false, is unwrapped. A dispatcher token is
      `/usr/bin/vos ext launch --app <digits>` with the spaces and tabs
      after it, anywhere in the options, ending them or followed by a space,
      a tab or `%command%`. Wrapping and unwrapping first take every token
      out, whatever the app and the number of `%command%`. The user's
      options stay around the token; when they change them in Steam, the new
      ones, less the tokens, become `before`. Unwrapping options nobody
      changed since VaporOS wrote them gives back `before` exactly (with
      none, the key goes, and the app's block too when that leaves it
      empty); changed ones lose only the tokens.

      | User's launch options, less the tokens | Written as |
      | --- | --- |
      | none, or no `%command%` | `/usr/bin/vos ext launch --app N %command% <whole string>` |
      | one `%command%` | `<prefix>/usr/bin/vos ext launch --app N %command%<suffix>`, so wrapping is idempotent |
      | several `%command%` | without the tokens and otherwise left alone, recorded as `conflict` |
   3. **shortcuts.vdf** (binary) of each account that has a
      `userdata/<accountid>/config/` directory, made when missing and there
      is a shortcut to add. A VaporOS shortcut is the entry whose
      `LaunchOptions` carry
      `/usr/bin/vos ext launch --shortcut <owner>/<key> %command%` (after
      `--unwrap` took that off, the entry with the app id the record holds),
      never one matched by name. Its `LaunchOptions` are those words,
      then, when the shortcut has `args`, a space and the `args` joined by
      spaces (Steam passes them to `Exe` after `%command%`). For each
      shortcut in `steam.json`:
      - an existing one gets its `AppName`, `Exe` and `StartDir` (in double
        quotes, as Steam keeps them), those `LaunchOptions` and the tag
        `VaporOS`, and its `icon` when it has none (below); its other keys,
        tags and app id stay;
      - a missing one is added with the app id
        `crc32(IEEE, "<owner>/<key>") | 0x80000000` (stored as an int32),
        the overlay and desktop controller configuration allowed, the
        `icon` and the tags `["VaporOS"]`,
      - unless the record has it and the file existed: the user deleted it,
        so it is recorded as `deleted` and not added again. A missing
        shortcuts.vdf (Steam's data was reset) gets every shortcut again.

      The `icon` is the full path of the copy of the extension's `icon.png`
      in the account's grid (step 4), when its `art` has one.

      A VaporOS shortcut `steam.json` does not list is removed only when
      its extension is not in `owners` and `steam.json` names it nowhere
      else (in no shortcut and no `apps[].hooks`, entries prepare ignores
      included); never while `owners` is `null` or missing, and never on
      a boot whose report has mode `off` (`vos.ext=0`, starting once
      without extensions), which says nothing about what stays. Until
      then it stays as it is with its record, and its mapping is kept
      like an app's no longer asked for, so a run without it (a trial that
      fell back, a boot without extensions, an install with no target for
      it yet) neither removes it nor adds it again. A removed shortcut's
      record and mapping go (step 1) and its art (step 4); the record
      stays only when it says `deleted`: those always stay, so a shortcut
      the user deleted is not added again after its extension was away.
      The app id Steam keeps for each shortcut is read back into the
      record, with its game id `(appid << 32) | 0x02000000`, the one
      `steam://rungameid/<gameid>` takes.
   4. **Grid art:** for each VaporOS shortcut with `art`, the files that
      exist there are copied into `userdata/<accountid>/config/grid/` where
      the account has none yet, so art the user picked stays:
      `capsule.png` (the portrait capsule, 600×900) to `<appid>p.png`,
      `capsule-wide.png` (the wide capsule, 920×430) to `<appid>.png`,
      `hero.png` to `<appid>_hero.png`, `logo.png` to `<appid>_logo.png`
      and `icon.png` to `<appid>_icon.png`. Each file VaporOS writes goes
      into the shortcut's record with its size and mtime, and a removed
      shortcut takes along only those that still have both: art the user
      put in their place, or that VaporOS has no record of, stays.
   5. **appmanifest_\<app>.acf**, `AppState/UserConfig/BetaKey`, in the
      first library that has the manifest. An app that is not installed
      waits, and loses its branch record once no library has its
      manifest while every library's `steamapps` is there (a drive that
      is away may hold it), so a reinstall gets the request that still
      stands. Each `beta` request is applied once (`""` removes the key:
      the public branch) and its `request` recorded; while the record
      holds that id the manifest is left as it is, so a branch the user
      picks in Steam afterwards stays. `before` is the manifest's value
      when VaporOS first wrote one, or the user's when a new request comes
      after they changed it; a manifest that already asks for the branch
      with no record of VaporOS's is taken over with `before` `""`. A
      branch no longer asked for goes back to `before` if the manifest
      still asks for the one VaporOS wrote.
6. `--unwrap`, which nothing in VaporOS runs (a plain run unwraps the
   launch options while `dispatcher` is false; this is for a shell, before
   booting a system without `vos steam prepare`), takes
   every dispatcher token out of every app's launch options (as above) and
   out of the VaporOS shortcuts' `LaunchOptions` (the shortcuts stay, with
   `%command%` and their `args`). Every CompatToolMapping entry VaporOS owns gets `before`
   back and is `suspended`; one the user changed is theirs for `"0"` and
   stays as it is otherwise. Branches and art are left alone. The next run
   without `--unwrap` applies everything again.

*Dispatcher messages:* `vos ext launch` (as vapor), and a helper's own
command that runs as vapor where nobody sees its output (`vos ext truckersmp
mp` and `handoff`), write a record of each refusal to
`$XDG_RUNTIME_DIR/vos/ext-messages/<unix nanoseconds>.json`
(`/run/user/<uid>` without an absolute `XDG_RUNTIME_DIR`;
`{"code":"<code>","id":"<extension id>","detail":"..."}`, a temp file
renamed, `detail` on one line and cut to 300 characters before it is
written, so any record fits the 4 KiB vosd reads; see Binary). The
dispatcher's codes are `not-mounted` and
`hook-failed`; a helper's command uses codes of its own
(`^[a-z][a-z0-9-]{0,31}$`) that its helper words (`MessageWords`, Go in
`vos`), and so does a launch hook whose error is a refusal with such a code
(`extensions.Refuse`), which the dispatcher records with that code instead of
`hook-failed`. Every 3 s vosd reads the ones in
`/run/user/1000/vos/ext-messages/` (names of 1 to 20 digits and `.json`,
read through gamerfs, at most 4 KiB each; of more than 5 at once only the
newest 5), deletes them all, logs each record's id, code and detail (on one
line, at most 300 characters) to the journal, and publishes its own words,
never the record's, as `system.message` `{"level":"warning","text","welcome":false}`
with `<name>` the shipped descriptor's name: for `not-mounted` "<name>
didn't start because its extension isn't active. Open Extensions in VaporOS
to check it.", for `hook-failed` "<name> couldn't start the game. Try
again, or check its card in Extensions." (without a shipped descriptor: "A
game didn't start because its extension isn't active. Open Extensions in
VaporOS to check it." and "An extension couldn't start the game. Try again,
or check Extensions in VaporOS."), and for a helper's code the sentence its
helper has for it. A file that is not such a record (an unknown code, one
the extension's helper has no words for, an id that is not an extension id)
is deleted unread.

**Control center** (vosd, installed systems only; the `/extensions` routes in
HTTP API). GET `/extensions` lists the booted catalog's extensions in its
order, then the wanted ids it lacks, each card from the shipped descriptor:
`permissions` and `runs_as_root` are what the build verified (its `build`
section; none without one), never the descriptor's own words; `size` is the
image's (the catalog's); `web` is the descriptor's web UI, listed whether or
not it runs (vosd serves it only while the extension is mounted, still
wanted and its services run; see HTTP API, Extension web UIs), and
`web_running` whether vosd serves its port now; `status`
is its helper's lines (`tone` `warning` or `error`, or none); `requires` its
direct requirements; `required_by` the wanted or core extensions that
require it, directly or not; `needs_password` whether adding it takes the
admin password again (it, or a requirement not wanted or core yet, directly
or not, runs as root or sets kernel module options); `module_options`
whether it can set kernel module options (its descriptor lists
`module_options`), a setting's `needs_password` whether changing it
takes the admin password (a `module_options` entry names it) and its
`required` whether it is a drive the extension cannot do without (its
descriptor says so; adding the extension asks for it, and its `value` ""
means none is picked yet); `steam` what
it changes in Steam, null when none of these: `compat_tool` (its
`steam.compat_tool`, the tool its forced apps and shortcuts run with, or
""), `forces` (the apps of `steam.force_compat_tool`) and `hooks` (the apps
of `steam.hooks`, each once) as `{"app","name"}`, and `shortcuts` (each
`steam.shortcuts` entry as `{"name"}`); an app's `name` is the one in its
`steamapps/appmanifest_<app>.acf` in Steam's own library
(`~vapor/.local/share/Steam`), else in the first library on an adopted game
drive (config.json `storage.libraries` whose `mountpoint` is
`/var/mnt/<name>`) that has one: the drive's top, its `SteamLibrary`, then
each library `steamapps/libraryfolders.vdf` lists on that drive (a path
vapor wrote only picks a directory below a drive's folder, never a place to
read from); read through gamerfs, again only once the file's mtime or size
changed, on one line, at most 128 characters; else ""; `wanted` whether
the user added it or it is core. The document's `skip_once` says whether the next
boot mounts no extension (`skip-once` exists). `state` is the first of these
that applies, with `reason` (at most two sentences: what happened, then what
to do) only for `needs-attention` and `not-in-this-version`:
1. `installing` while its image downloads or seals (`progress`: its bytes so
   far), or while its helper's `Install` runs, waits for its turn or, after
   a try that failed, comes again by itself (this boot mounted it, it is
   wanted or core, without `settings/<id>.installed` and with tries left;
   `progress` null);
2. `needs-attention` when it is wanted or core (one removed since the last
   reconcile goes on to 3 or 6) and its image cannot be had
   (the source failed, no space, a disk that cannot seal, damaged), the
   desired set it is in failed its trial (its fingerprint is in `failed`;
   "Try again"), or its trial or this boot could not start it (a skip
   reason); when its helper's `Install` did not finish after its last try
   this boot, or its `Remove` did not finish, while it is wanted, core or
   mounted ("Setting up <name> didn't finish. Try again, or remove it."
   without "or remove it" for core, or "Removing <name> didn't finish. Try
   removing it again."; or, when the error is the helper's refusal with a
   code its helper words (`extensions.Refuse`, `MessageWords`), that
   sentence; the error goes to the journal); when a `required`
   disk setting has no drive ("Pick a game drive for <name>, then select
   Try again.");
   while it runs and is wanted or core, when one of its helper's status
   lines has tone `error` (a Steam shortcut the user deleted, say); and when
   it is wanted or core and not running and what it waits for will not come
   by itself: a requirement whose image cannot be had, or a boot with
   `vos.ext=0` or `skip-once`, or with reason `tries-write` or `no-report`
   (after those a restart is not needed, see Reconcile);
3. `restart-needed` when a restart changes it: the pending set adds it (it
   is wanted or core), it is mounted and no longer wanted, or the module
   options its settings render differ from those this boot started it with;
4. `installed` when this boot mounted it;
5. `not-in-this-version` when it is wanted and the booted catalog lacks it;
6. `not-installed` when it is neither wanted, core nor mounted (also one
   removed before the restart that would have added it).

A wanted or core extension of the booted catalog that none of these fit is
`installing` too (`progress` null): its image is still to come, or the
reconcile that proposes it still to run. One that cannot get there by itself
needs attention (2), so a card never stays `installing`, and a core
extension is never `not-installed`. `restart.needed` is `/status`'s
restart kind `extensions` (a restart would try `pending`, see Reconcile),
false while `skip-once` exists (the next start mounts no extension);
`restart.reason` says what a restart finishes, by name ("Restart to finish
adding CoolerControl."), "" while none is needed; `restart.auto` whether
VaporOS may still restart by itself for it (below). One change to `wanted`
or the settings runs at a time; each answers the document and asks for a
reconcile. Add, remove and settings fail closed: when a shipped descriptor
they check (of the extension, of what an add brings with it, or of a wanted
or core extension an add is checked against) cannot be read, they change
nothing (409).
- **Add** (POST `/extensions/{id}`) refuses an id the booted catalog lacks
  (404, or 409 when it is wanted: not in this version), a core one (409),
  `options` that are not values of its settings (400), adding an extension
  (with what it requires) that names a wanted or core extension, or a
  capability one provides, in `conflicts`, is named so by one, or provides a
  capability one of them provides (409), an extension it adds with a
  `required` disk setting and no drive for it, in `options` for the
  extension itself, else in its `settings/<id>.json` (400: "<name> needs a
  game drive. Pick one to add it.", or for a requirement "<name> needs
  <requirement>, which needs a game drive. Add <requirement> first."),
  images that do not fit with 2 GiB to
  spare or a disk that cannot seal (409), and, when it adds an extension that
  runs as root or sets module options, or an option feeds module options,
  a missing or wrong admin password (403; checked under the sign-in limit as
  POST `/auth/password`: 429, 503). Then, under ext.lock, it and what it
  requires (less core) join `wanted`, and each of them that has settings but
  no `settings/<id>.json` gets one (the `options`, for it, and the defaults).
  One still mounted, removed before the restart, gets back, once its helper
  lock is free (a removal under way holds it) and if it is still wanted
  then, its `system` and `home` data areas (a purge deleted them) and the
  units its removal stopped (`systemctl start`, or `systemctl --user -M
  vapor@ start`, template instances by name), and its helper's `Install`
  runs again.
- **Remove** (DELETE `/extensions/{id}`, `purge` `0` or `1`, else 400)
  refuses core and an extension a wanted or core one requires (409). It
  leaves `wanted` (under ext.lock); then it takes the extension's helper
  lock, stopping (cancelling) its helper's `Install` under way and waiting
  for it, and, unless it was added back meanwhile: when this boot mounted
  it, its `services` stop now (`systemctl stop`, or `systemctl --user -M
  vapor@ stop`; every instance of a template), noting first which of them
  run (`systemctl list-units --state=active,activating,reloading`; when that
  fails, its units that are not templates); its helper's `Remove` runs (with
  `purge`, unless it was added back by then) only when this boot mounted it
  or `settings/<id>.installed` exists; `settings/<id>.installed` goes; and,
  with `purge`, whether or not the helper ran, its system data area, its
  home data area (as vapor) and `settings/<id>.json` are deleted (library
  areas are its helper's to delete), under the lock of the changes to
  `wanted` and the settings, unless it was added back by then. When it was
  added back while all this ran, the removal ends by making its `system` and
  `home` data areas and starting the units it stopped (what it stops after
  that the add starts); the next reconcile has its helper's `Install` run
  again. Its files stay mounted until the restart. A helper that fails
  becomes the card's `reason` (not when it was added back); the removal
  stands.
- **Settings** (PUT `/extensions/{id}/settings`): every key one of its
  settings and every value one it takes (`bool` true or false, `choice` one of
  `choices`, `disk` `/var` for the system drive, `/var/mnt/<name>` for a
  game drive's folder, `<name>` of 1 to 255 `[A-Za-z0-9._-]` and not `.` or
  `..`, or "" for none, which a `required` one does not take), else 400; changing a
  setting a `module_options` entry names takes the admin password (403, 429,
  503). They are saved under ext.lock. The module options of a set are what
  its extensions' helpers render from their settings, less any line for a
  `module param` its descriptor does not list, so changing one is a new
  `pending` set, which the next restart tries.
- **Actions** (POST `/extensions/{id}/actions/{name}`, `args` an object of
  at most 64 KiB, or absent, else 400) run one of its descriptor's actions
  (else 404) through its helper, only while this boot mounted it (409),
  under its helper lock, within 10 minutes (the wait for the lock included)
  and even when the page goes away: one with `run_as` `root` in vosd, every
  other as `vapor` through `vos ext action <id> <name> [--args <json>]`
  (exit 3 is 404). A failure is 500 "<label> didn't finish. Try again.", or
  "<label> didn't finish in time. Try again." when the 10 minutes ran out
  (the action's `label`); why (that command's last line) and the `args` go
  to the journal only. After one that worked, `steam.json` is written again
  (a helper's Steam parts may follow it).
- **Try again** (POST `/extensions/{id}/retry`) removes the desired set's
  fingerprint from `failed` and lets its helper's `Install` run again.
- **Start without extensions** (POST `/extensions/skip-once`) writes
  `skip-once`; DELETE `/extensions/skip-once` removes it.

After each reconcile vosd has the helper's `Install` run (as root, within 10
minutes) for every extension this boot mounted that is wanted or core and
has no `settings/<id>.installed`, which it writes after; on an extension
trial, only once `vos health` passed it. These run one at a time, beside the
reconciles (which go on fetching and proposing), and a reconcile that asks
while they run has them looked at again after. A failure is tried again
after 1 minute (2 after the second), the card `installing` meanwhile, at
most 3 times a boot; after the third it is the card's `reason`, and only
"Try again" tries more. Each extension's helper calls (`Install`,
`Remove`, actions, `PasswordChanged`) take its helper lock, held across
the call, so they run one at a time, and count as busy `adding an
extension`. An `Install` checks under the lock that the extension is still
wanted and not set up, then that each `required` disk setting has a drive:
without one it does not run, the card's `reason` is "Pick a game drive for
<name>, then select Try again." and nothing tries again by itself; else
vosd makes its `system` and `home` data areas first. One that ends with the
extension no longer wanted writes no marker, and its helper's `Remove`
undoes it unless a removal waits to.

When the admin password changes (POST `/auth/password`, or POST
`/auth/setup` on an installed system), vosd calls, beside the request, the
optional `PasswordChanged` of the helper of each extension this boot
mounted that is still wanted (CoolerControl's sign-in keeps a copy), as
root, under its helper lock and within 10 minutes; a failure goes to the
journal.
`extensions.state` carries the document whenever it changes (vosd also
builds it every 5 s, for its helpers' status lines), at most every 250 ms
and never the same one twice in a row.

**Auto-restart:** while a restart would try `pending`, `restart.auto` holds
and `skip-once` does not exist, vosd restarts the PC at the end of a pass of
the idle-shutdown policy that saw the PC idle (every busy reason checked in
that pass) for 2 minutes, when `vos-health.service` is active (it passed
this boot; asked within 5 s) or the boot is older than 5 minutes, with no
idle shutdown on its way or due within 5 minutes (the wake boot tries the
set anyway). Under the update lock and then ext.lock (each waited for at
most 5 s, the second from when the first was had), it checks again that a
restart would try `pending` and `skip-once` does not exist, records the
restart in `autorestart.json` and reboots through the guard of POST
`/system/reboot` (asked within 30 s; nothing while a restart or power off is
on its way, and the record is then dropped). It first publishes the
`system.message` the welcome screen shows: "Restarting to finish adding
<name>" (or removing, or changing extension settings), or, when the restart
starts another VaporOS first (GET `/update` `next_boot`), "Restarting to
install VaporOS <v>; <name> is added after the next restart" for a newer
one and "Restarting to go back to VaporOS <v>; <name> is added after the
next restart" for any other (as `/status` tells `update` from `rollback`).
Like idle shutdown and wake, it thus also boots a staged update or rollback.
`restart.auto`, the circuit breaker: none yet for the pending set's
fingerprint, or one from another VaporOS version (an OS trial does not try
`pending`), and fewer than 3 in the last 24 hours (one timed in the future
counts); it is false while `skip-once` exists. After that only a restart
from the control center applies it.

**CoolerControl** (`extensions/coolercontrol/`, helper
`internal/extensions/coolercontrol`; a system extension that runs as root).
Packages `cachyos/coolercontrold` and `extra/liquidctl`, less liquidctl's
`usr/lib/udev/rules.d/71-liquidctl.rules` (a `uaccess` rule, with no seat to
act on); provides `fan-control.hwmon` and `fan-control.amdgpu`; permissions
`service` (`coolercontrold.service`, system) and `modules`
(`modules-load.d/vos-coolercontrol.conf`: `drivetemp`). Its web UI is port
11987, proxied to `127.0.0.1:11986` (HTTP API, Extension web UIs). Its
system data area holds `config/` (`CC_CONFIG_DIR`: `config.toml`,
`.passwd`), `data/` (`CC_DATA_DIR`) and `vaporos.json`
(`{"passwd_sha256","daemon"}`, `prepare`'s record).
- The drop-in `coolercontrold.service.d/vos.conf` sets `CC_CONFIG_DIR`,
  `CC_DATA_DIR`, `CC_PLUGINS_DIR=/usr/lib/vos/ext/coolercontrol/plugins`
  (empty and in the image: no plugin runs), `CC_HOST_IP4=127.0.0.1`,
  `CC_HOST_IP6=::1`, `CC_PORT=11986`, `CC_TLS=OFF` (`false` would turn TLS
  on) and `CC_SERVICE_MANAGER=OFF`; `StateDirectory=vos/ext/data/coolercontrol`,
  `RuntimeDirectory=coolercontrold`, `ProtectSystem=strict`,
  `ProtectHome=yes`, `PrivateTmp=yes`, `ReadWritePaths=` the data area,
  `NoExecPaths=/var /run /tmp`, `ExecPaths=/usr`,
  `CapabilityBoundingSet=CAP_SYS_MODULE CAP_SYS_RAWIO CAP_DAC_OVERRIDE
  CAP_DAC_READ_SEARCH CAP_FOWNER CAP_CHOWN CAP_SYS_NICE` (no
  `CAP_SYS_ADMIN`; no `ProtectKernel*` or `PrivateDevices`: it writes
  `/sys`, reads `/dev/port` and hidraw, loads drivers) and
  `TimeoutStartSec=300s`; then `ExecStartPre=` `/usr/bin/vos ext
  coolercontrol prepare`, `-/usr/bin/coolercontrold detect --load` (`-`:
  a board with no driver to load still starts the daemon) and
  `+/usr/bin/vos ext coolercontrol fans snapshot`, in that order, and
  `ExecStopPost=+/usr/bin/vos ext coolercontrol fans restore`, which runs
  after any stop (`+`: outside the sandbox, for `/run/vos`). Its `[Unit]`
  has `StartLimitIntervalSec=30min` and `StartLimitBurst=5`: upstream's
  `Restart=always` stops after five starts in 30 minutes (which holds five
  that each run out `TimeoutStartSec`), and the daemon stays down, its fans
  put back, until the next restart.
- `prepare` (in the sandbox, `CC_CONFIG_DIR` and `CC_DATA_DIR` from the
  environment when absolute) makes `config/` and `data/` (0700), then:
  1. copies auth.json's argon2id PHC string, as it is (no newline;
     coolercontrold verifies it with the parameters in it), to `.passwd`
     (0600) when that file is missing or still holds the bytes it wrote last
     (their sha256 is `passwd_sha256`); one changed in CoolerControl stays.
     Without auth.json, or with a hash that is not argon2id, it fails, so
     the daemon never starts with its default password. A new VaporOS
     password reaches CoolerControl at once through its helper's
     `PasswordChanged` (below), else at the daemon's next start;
  2. when `usr/lib/vos/ext/coolercontrol/packages.txt` names a
     `coolercontrold` version other than `daemon`, runs `coolercontrold
     backup` (in the data area, at most 2 minutes) if `config.toml` exists,
     and records the version; a failed backup is logged and tried again at
     the next start, and without `config.toml` the version is only
     recorded;
  3. writes `config.toml` (the file made when missing, everything else in
     it kept): an empty `[devices]`, `[legacy690]` and `[device-settings]`
     table where the file lacks one (coolercontrold stops on a file without
     them), and in `[settings]` `ipv4_address = "127.0.0.1"`,
     `ipv6_address = "::1"`, `port = 11986` and `tls_enabled = false` at
     every start; `poll_rate = 1.0` and `drivetemp_suspend = true` only
     where the table lacks them, so a choice made in CoolerControl stays. A
     `config.toml` it cannot change (settings as dotted keys or an inline
     table, a table or key twice, a value never closed) fails the start.
- `fans snapshot` records, in `/run/vos/coolercontrol-fans.json`
  (`{"fans":{"<key>":"<mode>"},"duty":{"<key>":"<duty>"},"curves":{"<path>":"<sha256>"}}`,
  0600, atomically): the value of every
  `/sys/class/hwmon/hwmon*/pwm<N>_enable` under the key `<device>/pwm<N>_enable`
  (`<device>`: the realpath of `hwmon*/device`, else of `hwmon*` itself);
  for one that is 0 (full speed) or 1 (manual), `pwm<N>`'s duty (0-255)
  under `<device>/pwm<N>`; and the sha256 of every amdgpu OverDrive fan
  curve, `<realpath of hwmon*/device>/gpu_od/fan_ctrl/fan_curve`, under its
  path. It never replaces a key it has: a restart of the daemon, or a chip
  whose driver loads later, keeps what the firmware set. `fans restore`
  first writes `r` (reset), then `c` (commit), to every recorded fan curve
  whose sha256 differs now, which gives the fan back to the card's
  firmware; then, wherever the chip is numbered now, it writes back every
  recorded mode (1 to 3 digits) that differs, and a recorded duty that
  differs only while the fan is in manual mode, the one mode every driver
  takes a duty in: after the mode when that is 1, before it when it is 0
  and the fan is in 1. USB devices (liquidctl's) are not put back: they
  keep their last setting until the PC turns off, as the card says.
- Its helper's status: nothing while it is not mounted or no longer
  wanted; "CoolerControl is starting." (no tone) while the unit activates;
  nothing while the unit is active and `GET
  http://<proxied upstream>/handshake` answers 200; otherwise
  "CoolerControl isn't running. Restart VaporOS, or remove it."
  (`warning`). It looks at most every 5 s. Its module options: setting
  `gpu_fan_curves` gives `options amdgpu ppfeaturemask=0x<hex>`, the running
  `/sys/module/amdgpu/parameters/ppfeaturemask` (hex or decimal) with
  `0x4000` (OverDrive) added, so this boot's own option renders the same
  line again. Before amdgpu has loaded (that file missing) it is the value
  of the first such line of `/run/modprobe.d/vos-ext.conf`, else of the
  booted set's `modprobe.conf`, with `0x4000` added; without one, the
  kernel's default `0xfff7bfff` with `0x4000` added when a PCI device under
  `/sys/bus/pci/devices` has `vendor` `0x1002` and a `class` of
  `0x03xxxx` (an AMD display controller), and nothing without; never a
  line when the mask would be `0xffffffff`. So the set's options, and with
  them its fingerprint, do not change with when the driver loads.
  `it87_conflicts` gives `options it87 ignore_resource_conflict=1`. Its
  `Remove` stops `coolercontrold.service` while it is mounted (whose
  `ExecStopPost` puts the fans back), runs `fans restore` itself in case
  that could not, and with `purge` deletes the data area. Its optional
  `PasswordChanged`, when the VaporOS password changes, copies the new
  hash to `.passwd` on `prepare`'s terms (only while it is still the copy
  `passwd_sha256` names, updating that; one changed in CoolerControl
  stays), and does nothing while there is no `.passwd` (the next start
  writes it); coolercontrold reads `.passwd` again when its mtime changes.

**TruckersMP** (`extensions/truckersmp`, `internal/extensions/truckersmp`;
requires `proton`). Its image holds the injector alone,
`usr/lib/vos/ext/truckersmp/truckersmp-cli.exe` (truckersmp-cli 0.11.0, MIT,
pinned by its release tarball's sha256). In Steam it forces
`proton-cachyos-slr` on ETS2 (227300) and ATS (270880) and hooks both, so
single-player and multiplayer share each game's own prefix
(`<library>/steamapps/compatdata/<app>`); its shortcuts `ets2` "TruckersMP
(ETS2)" and `ats` "TruckersMP (ATS)" run vos, without a compatibility tool
(`exe` `/usr/bin/vos`, `start_dir` its home data area, `args` `ext
truckersmp mp <key>`). Sunshine lists `/usr/bin/vos ext truckersmp mp
ets2|ats` under the shortcuts' names, standing for them. Both shortcuts and
entries stay whether or not their game is installed (Steam keeps a
shortcut's id and art only while it stays listed); `mp` says when a game is
missing. Its descriptor's `downloads` name the mod (about 640 MiB, at
install, and each new version) from `download.ets2mp.com`, the file list
from `update.ets2mp.com` and the version API from `api.truckersmp.com`.
- *Files* (the home data area, `vapor`'s): `bin/truckersmp-cli.exe` (a copy
  of the image's, which Proton's container cannot see; `setup` and the
  launch hook make it equal), `files/` (MODDIR, laid out as files.json
  says), `manifest.json`
  (`{"version","checked","games","files":[{"path","type","md5","size","mtime"}]}`,
  `mtime` in unix nanoseconds: written by a sync that finished, and deleted
  first by one that changes files, so it always describes whole files),
  `partial/<md5>.part` (downloads to resume) and `.sync.lock` (flock).
- *Sync* (`vos ext truckersmp sync`, as `vapor`, one at a time): it asks
  `https://api.truckersmp.com/v2/version` and
  `https://update.ets2mp.com/files.json` (`{"Files":[{"Md5","Type","FilePath"}]}`),
  takes the `system` files and those of each game a library has
  (`appmanifest_<app>.acf`) or the manifest holds, and refuses a list with a
  path that leaves `files/`. For each of those games the API must give the
  MD5 of `core_ets2mp.dll` (`core_atsmp.dll`) as `ets2mp_checksum.dll`
  (`atsmp_checksum.dll`), and files.json the same one; otherwise the sync
  downloads nothing and exits 4 (it tries again later). A file
  the manifest recorded with that MD5, size and mtime, or whose MD5 matches,
  stays; the others come from `https://download.ets2mp.com/files<FilePath>`
  (HEAD for the size; where HEAD gives none, the GET's `Content-Length`, or
  its `Content-Range` total when resumed; then GET, resumed with `Range`
  where a part exists, a file of no bytes included; at most 2 GiB a file
  and 8 GiB in all, the bytes downloaded counted against it as they come;
  given up after 60 s without data), are MD5-checked and renamed into
  place. Before downloading, the home data area's filesystem must have the
  bytes still to come (the sizes known, less what `partial/` holds) plus
  2 GiB free, checked again for each size only the GET gives; otherwise the
  sync prints `{"short":N}` (the bytes missing) and exits 5. Files of the
  old manifest no longer listed go.
- *vosd* runs the sync as `vapor` (runuser; its progress lines read as they
  come; within 4 hours; exit 3 counts as done, exits 4 and 5 the card words
  itself) from `Install` (after `setup`), and, while this boot
  mounted the extension and `settings/truckersmp.installed` exists, when it
  builds the card and a sync is due: no manifest, the API's `name` (asked at
  most hourly, every 10 minutes while unanswered; 15 s, 64 KiB) other than
  the manifest's `version`, a manifest a day old, or a game installed that
  it lacks; never two at once, nor two it starts by itself within an hour.
  A running sync is busy `updating TruckersMP` until no bytes came for 2
  minutes. `Remove` stops it.
- *Multiplayer:* `vos ext truckersmp mp ets2|ats` (as `vapor`: from the
  shortcut, through `vos ext launch --shortcut`, or from Sunshine) refuses,
  with a message (see Dispatcher messages: codes `running-<key>`,
  `starting`, `not-installed-<key>`, `no-files-<key>`, `updating`,
  `handoff-failed` and, from `handoff`, `steam-silent`, each with its
  sentence in the helper), while a reaper of 227300 or 270880 runs
  (`running-`), while `vos-ext-handoff.service` is loaded (`starting`),
  when no library has the game (`not-installed-`), or when the game's
  files fail the quick check: `no-files-` when the manifest lacks the
  game, or without a manifest `files/` lacks its core library, else
  `updating`. Otherwise it writes the flag
  `$XDG_RUNTIME_DIR/vos/truckersmp-mp.json` (`{"game","created","nonce"}`,
  0600; valid for 15 minutes, and up to 1 minute in the future) and runs
  `systemd-run --user --collect --quiet --unit=vos-ext-handoff -- /usr/bin/vos
  ext truckersmp handoff <game> <id> <nonce>` without `LD_PRELOAD`,
  `LD_LIBRARY_PATH`, `STEAM_RUNTIME*` and `PRESSURE_VESSEL*` (Steam's
  overlay and runtime, as for a hook's programs), `<id>`
  the AppId of the nearest reaper above it (0 when none is); when that fails
  the flag goes. `handoff` waits until no reaper with that AppId (as app id
  or game id) is left, at most 30 s, then 2 s more (with 0, not at all), and hands
  `steam://rungameid/<227300|270880>` to Steam as `vos session launch` does;
  when Steam never takes it, the flag goes and a message says so.
- *Launch hook* (as `vapor`, in `vos ext launch` for 227300 and 270880):
  only when the command holds an absolute `.../bin/win_x64/eurotrucks2.exe`
  (`amtrucks.exe`; any case) and a valid flag for that game exists does it
  take the flag (renamed away first: once), deleting one that is not valid
  and leaving one for the other game. Then the quick check (each `system`
  and game file has the manifest's size and mtime, the core library its
  MD5): stale files refuse the start ("its files are updating. Try again in
  a few minutes."), never turning it into single-player. Otherwise the
  executable becomes `<home data area>/bin/truckersmp-cli.exe GAMEDIR
  MODDIR` (GAMEDIR two levels above the executable's folder), followed by
  Steam's arguments for the game or, without any, `-rdevice gl -nointro
  -64bit`. A command that holds the Linux build instead
  (`.../bin/linux_x64/eurotrucks2`, `amtrucks`) while a valid flag for
  that game exists takes the flag and refuses the start ("ETS2 isn't set to
  run with Proton. Restart VaporOS and try again."). Every other start
  passes untouched. For its shortcuts it adds
  `ext truckersmp mp <key>` when the command ends in `/usr/bin/vos` (a
  prepare that wrote no `args`).
- *Card* (no lines while it is not mounted and set up, but for a sync that
  runs): the game versions the API supports, each installed game's version
  (the last `init ver.` in `game.log.txt` of its prefix's
  `Documents/<game>`, else of `~/.local/share/<game>`, read through
  gamerfs) against them or the branch it is held on (and, once the API
  supports a newer version than that, whether that one is the game's
  latest), and the files (downloading n %, ready, out of date, or, while
  they are behind, a sync that failed: for want of free space, with how
  much to free; for want of the API's checksum; or otherwise).
- *Actions:* `copy-profiles` (as `vapor`, through `vos ext action`) copies each folder of
  `~/.local/share/<game>/profiles` that the prefix's
  `Documents/<game>/profiles` lacks (through a hidden folder renamed whole;
  no symbolic links), for each game whose prefix exists; a copy that fails
  says "Couldn't copy the <game> profiles. Try again." and logs why.
  `switch-branch` (as `root`) writes `branch.json` in its system data area
  (`{"apps":{"<app>":{"branch","request","latest"}}}`): an installed game
  whose logged version is newer than the API supports gets
  `temporary_<major>_<minor>` of the supported version, with `latest` that
  logged version; a game held on another branch than the supported one
  gets the supported one's, or `""` (its latest version again) once the
  API supports `latest` or newer (the newer of `latest` and its logged
  version). Each game it changes gets a new `request` id (the switch's UTC
  time, `YYYYMMDDTHHMMSS.nnnnnnnnn`); every other entry stays as it was, id
  included. The helper's `SteamParts.Beta` passes each entry to prepare as
  `{"branch","request"}`, so prepare applies each switch once and a branch
  picked in Steam afterwards stays. `latest-branch` (as `root`) deletes
  the file, so prepare puts back the branch from before.
- *Removal* stops a sync and deletes `branch.json`; steam.json then
  releases 227300 and 270880 (they stay on Proton, the user's from now on)
  and prepare resets the branch VaporOS set. Purging deletes the home data
  area as `vapor`.

**Star Citizen** (`extensions/star-citizen`, helper `internal/extensions/starcitizen`;
requires `proton`, no packages). A Steam shortcut "Star Citizen" (`launcher`,
run with `proton-cachyos-slr`) whose target is the RSI Launcher's installer
in a Proton prefix on the drive the person picks; the launch hook turns it
into the launcher's start. The image ships one udev rule,
`usr/lib/udev/rules.d/70-vos-star-citizen-hotas.rules` (`udev` permission):
`SUBSYSTEM=="hidraw", ATTRS{idVendor}=="231d|3344|044f", GROUP="input", MODE="0660"`,
so vapor (group `input`) opens the hidraw nodes of VKB, Virpil and
Thrustmaster sticks on USB. No sysctl: the base's `vm.max_map_count` is
already 1048576.
- *Drive.* The setting `disk` ("Game drive", `"required": true`: the
  install dialog asks for it, and vosd refuses an install without one) is
  the drive's folder: `""` is no drive picked yet; `/var` is the system
  drive (a stored `/state`, which earlier control centers offered, is
  accepted and read as `/var`), and the prefix is its home data area,
  `/var/home/vapor/.local/share/vaporos/ext/star-citizen`;
  `/var/mnt/<name>` (`/mnt/<name>` reads the same) is a game drive, and the
  prefix is `/var/mnt/<name>/VaporOS/star-citizen` (its `library` data
  area). Any other value is refused ("VaporOS doesn't know the drive picked
  for Star Citizen. Pick another one on its card, then select Try
  again."). The prefix is fixed at install: a drive picked later applies
  only after removing Star Citizen and adding it again (its card says so).
  `state.json`'s `prefix` names the place; its `disk` is the setting the
  install used, normalized (`/var` for the system drive).
- *Out of reach.* One sentence per situation, the same on the card's status,
  as Install's and fetch-installer's reason and as the launch hook's
  refusal: a game drive that is not mounted exactly at its folder, with
  nothing mounted between it and the prefix (`/proc/self/mountinfo`; an
  empty mount point lies on the system drive, where 100+ GB must never land
  by accident), or that holds another filesystem than the recorded one (the
  marker's, for vapor's checks), is "Star Citizen's drive, <name>, isn't
  connected. Connect it, then try again."; files gone from a drive that is
  there (no marker, a symlink in the prefix), and anything wrong on the
  system drive, are "Star Citizen's files on <drive> are missing. Remove
  Star Citizen and add it again." (<drive> is "the system drive" or the
  game drive's name).
- *Install* (the helper's `Install`, root, after the restart that mounts
  it). Before it touches any drive it refuses while none is picked ("Pick a
  game drive for Star Citizen on its card, then select Try again.") and an
  unknown one (above). Then it refuses, as the card's reason: a game drive
  out of reach (above), a filesystem not in the `games` area's `fs` (ext4,
  btrfs, xfs, f2fs), a prefix path with anything but `[A-Za-z0-9/._-]` (the
  LUG: no special characters), a filesystem without a UUID (the link in
  `/dev/disk/by-uuid` that resolves to the mount's source device), and,
  unless the launcher is already in the prefix, less than `min_free_gb`
  (150 GB, 10^9 bytes) free (`statfs` of the mount point). Then it makes the
  prefix through gamerfs (vapor's, 0755, no symlink followed), writes the
  marker `<prefix>/.vaporos-drive` (the filesystem's UUID, one line) and
  `/var/lib/vos/ext/data/star-citizen/state.json`
  (`{"disk","prefix","uuid","installer","version"}`, root's, atomic; vosd
  trusts only this file; an earlier `installer` and `version` for the same
  prefix are kept), and runs `vos ext star-citizen fetch-installer --prefix
  <prefix>` as vapor (`sysd.AsGamer`). On success `installer` and `version`
  are recorded; a failed download keeps the prefix recorded, so a removal
  finds it. When the launcher is already in the prefix and the recorded
  installer is in `installer/` (looked at through gamerfs), nothing is
  downloaded: the installer only runs on a first start. Its other reasons
  are plain sentences too; Go's errors go to the journal only.
- *Memory.* A PC with less than 16 GiB of memory, or less than 32 GiB of
  memory and swap together (`MemTotal` and `SwapTotal` in `/proc/meminfo`;
  swap counts zram, which CachyOS sizes as the memory), gets a warning
  line, never a refusal: "This PC has N GB of memory, and Star Citizen needs
  16 GB. It may not start, or it may close while you play." or "This PC has
  N GB of memory and swap together, and Star Citizen wants 32 GB. It may
  stutter or close in busy places." (N rounded to whole GiB). The limits
  allow 1 GiB and 2 GiB less: firmware and integrated graphics keep up to a
  GiB of a PC's memory, and zram sized as the memory counts that twice.
- *fetch-installer* (vapor) checks the prefix as the launch hook does (below),
  reads `https://install.robertsspaceindustries.com/rel/2/latest.yml`
  (electron-builder's feed, at most 64 KiB, read line by line: top-level
  `version`, `path`, `sha512`, and the `files` list's `url`, `sha512`,
  `size`): the entry whose `url` is `path` (the first without one), its
  `sha512` (else the top-level one: base64 of 64 bytes) and `size` (1 byte
  to 4 GiB); `path` must match `RSI Launcher-Setup-[0-9A-Za-z._+-]{1,40}.exe`.
  It downloads `https://install.robertsspaceindustries.com/rel/2/<path,
  URL-escaped>` (https only, also through redirects) into
  `<prefix>/installer/<path>.part`, asking with `Range` for the bytes it
  lacks (a whole answer starts over), up to 3 times; checks size and
  SHA-512, deletes a file that does not match, renames it to `<path>`, and
  deletes the partial downloads (`RSI Launcher-Setup-*.exe.part`) there;
  other installers stay for the launch hook. A file already there with the
  right size and hash is kept, with its modification time set to now. Its
  last line on stderr is a sentence for the person (an out-of-reach one
  above, or that the feed or the download failed); Install shows it, and
  its own sentence for anything else.
- *Steam.* Once `state.json` names an installer, `SteamParts` gives the
  shortcut `exe` `<prefix>/installer/<installer>` (always there) and
  `start_dir` `<prefix>`.
- *Launch hook* (vapor, `vos ext launch --shortcut star-citizen/launcher`).
  It acts on the last argument that is an absolute
  `<prefix>/installer/RSI Launcher-Setup-<v>.exe`; without one the command
  passes untouched. It refuses (exit 1; the person reads the dispatcher's
  sentence, this one goes to Steam's log and the journal), changing
  nothing, when `<prefix>` is not one of the two places above ("Star
  Citizen's files aren't where VaporOS put them. Remove Star Citizen and add
  it again."), or is out of reach (above): a game drive must be mounted
  exactly at its folder, and the prefix must resolve to itself (no symlink)
  and hold a marker equal to the UUID of the filesystem the mount table
  puts it on. Then it sets `STEAM_COMPAT_DATA_PATH=<prefix>` (Proton's
  prefix is `<prefix>/pfx`) and `UMU_ID=umu-starcitizen` (protonfixes then
  add PowerShell and the Visual C++ runtime on the first start) in the
  game's environment, and replaces that argument:
  - when `<prefix>/pfx/drive_c/Program Files/Roberts Space Industries/RSI Launcher/RSI Launcher.exe`
    is a file, with that path and `--in-process-gpu --disable-gpu` (the
    launcher's Electron window can stay blank under gamescope otherwise; it
    draws in software). It also makes `.../Roberts Space Industries/StarCitizen/LIVE`
    and its `USER.cfg` (`pl_pit.forceSoftwareCursor = 1`, CRLF) when the
    folder has no `USER.cfg` in any case;
  - otherwise (the first start) with `C:\windows\system32\cmd.exe`, `/c`
    and `Z:<prefix, / as \>\installer\first-start.bat`, after writing
    `first-start.bat`, `vaporos.reg` and `USER.cfg` into `installer/`
    (CRLF; each replaced only when it differs). The batch file waits for
    each step (`start "" /wait`): the setup with `/S` (electron-builder's
    silent install, per machine into `C:\Program Files\Roberts Space
    Industries\RSI Launcher`), `reg import` of `vaporos.reg` (REGEDIT4:
    `HKCU\Software\Microsoft\Windows\CurrentVersion\Policies\Explorer`
    `NoTrayItemsDisplay`=1, so closing the launcher quits it rather than
    hiding it in a tray gamescope does not show), `StarCitizen\LIVE` and
    its `USER.cfg` when missing, then the launcher with the flags above
    (exit 1 when the setup left no launcher). One Proton run does it all.
    electron-builder's own `--force-run` is not used: it starts the
    launcher through the Explorer shell as a non-elevated user and returns
    at once, which under Wine and gamescope may start nothing, and it
    leaves no step for the registry and USER.cfg.

  The first start's setup is the installer Steam's shortcut names or, when
  that one is gone (a newer download replaced it before Steam picked up the
  new target), the newest `RSI Launcher-Setup-*.exe` in `installer/` (by
  modification time); with none there it refuses ("Star Citizen's installer
  is missing. Remove Star Citizen and add it again."). Once the shortcut
  names the newest installer there, the hook deletes the others; until then
  they stay, as the shortcut may still name one.
- *Status* (root; files in the prefix only through gamerfs): "Its files are
  on <drive>, which has N GB free.", then "The RSI Launcher is installed."
  or "Start Star Citizen in Steam: its first start installs the RSI
  Launcher."; with tone `warning`, the out-of-reach sentence (above; the
  recorded filesystem must be mounted there and the marker hold its UUID),
  "You picked another drive. Star Citizen stays on <drive> until you remove
  it and add it again." when the setting names another drive than the
  recorded one (not when it is empty), and the memory line (also before it
  is installed).
- *Remove:* without `purge` nothing goes (the shortcut leaves steam.json).
  With `purge`, the recorded prefix on a game drive goes: vapor runs
  `rm -rf --one-file-system --` on it only while the filesystem mounted at
  its folder has the recorded UUID; otherwise its files stay and the card's
  reason is "Star Citizen's drive, <name>, isn't connected, so its files
  stay on it.". Then vapor removes `<drive>/VaporOS` when nothing else is
  in it (`rmdir --ignore-fail-on-non-empty`; a failure, such as a drive
  whose top folder is root's, is only logged). On the system drive the
  prefix is the home data area, which vosd's purge deletes; a prefix the
  state does not record is left alone. The remove dialog says the game can
  be more than 100 GB.
- *Not done:* the umu fallback for an Easy Anti-Cheat that fails under
  Steam's runtime (a shortcut without a compatibility tool whose hook runs
  `umu-run` with `PROTONPATH=/usr/share/steam/compatibilitytools.d/proton-cachyos-slr`,
  `GAMEID=umu-starcitizen` and `WINEPREFIX=<prefix>`) is not built: it
  needs a second shortcut (another app id, so its art and Sunshine entry
  change), a Steam Runtime seeded for umu (it otherwise downloads one into
  vapor's home), and a test on hardware that umu shares Steam's `pfx`. The
  launcher's own close-to-quit setting is not seeded either.

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
| `update.progress` | `{phase,percent,bytes,total,version,error?}`. A stage goes through `check`, `download` (kernel, initrd and extension images), `write` (root into the idle slot), `verify` (read back), `install` (ESP) and `done`; `percent` covers the whole stage, with `download` and `write` sharing 0-80 % by their bytes (the files `download` fetches, and root's size for `write`), while `bytes` and `total` count the current phase, and in `write` only what it downloads (see "Block index"). Phase `error` carries why a stage stopped; `idle` ends a stage that found nothing to do (already up to date, or already staged); `cancelled` ends one that `POST /update/cancel` stopped |
| `update.state` | the update-state |
| `install.progress` | `{step,percent,message,state}` |
| `session.begin` | `{client,app?,mode,hdr,since}` (`app`: Sunshine's app, `Steam` or a game's name, omitted when Sunshine names none; `since`: when it was launched, RFC 3339 UTC; a resume keeps both) |
| `session.end` | `{}` |
| `pairing.pending` | `{name?}` |
| `pairing.state` | `{pairings:[{id,name,address}]}`: who waits for a PIN (as in GET `/sunshine` `pairings`), sent after vosd's first poll of Sunshine and whenever the list changes. An empty list ends any pairing prompt; Sunshine not answering counts as an empty list |
| `sunshine.state` | `{running}`: whether `vos-sunshine.service` is active (as in GET `/sunshine` `running`), sent after vosd's first poll of Sunshine and whenever it starts or stops |
| `display.changed` | `{}` |
| `power.idle` | `{idle_seconds,shutdown_in,busy}` (as in GET `/power`; `busy` is null while idle; sent every 15 s while idle and whenever the busy reason changes) |
| `system.message` | `{level,text,welcome?}` (`level`: `info`, `warning` or `error`; `welcome` false: for the control center alone, the welcome screen does not show it) |
| `extensions.state` | the whole document (as in GET `/extensions`), sent whenever it changes, at most every 250 ms (see Extensions, Control center) |

| Method + path | Access | Request → Response |
| --- | --- | --- |
| GET `/ping` | Public | `{"ok":true,"mode":"os\|installer","version"}` |
| GET `/auth/me` | Public | `{"authenticated":bool,"csrf":"…","needs_setup":bool,"installer":bool}` |
| POST `/auth/login` | Public | `{"password"}` → `{"csrf"}` + cookie; 401 on a wrong password; 409 while no admin password is set yet (as on the installer); 429 after 5 failures per IP (backoff up to 15 min), and (Retry-After 1) while another check from the same client is running; 503 when too many sign-ins (8) are being checked |
| POST `/auth/logout` | Authed | → `{}` |
| POST `/auth/setup` | Setup | `{"password"}` → `{"csrf"}`; only when auth.json is missing (first run after a CLI install); 409 in installer mode |
| POST `/auth/password` | Authed | `{"current","new"}` → `{}`; 403 on a wrong current password; shares the login limit (429, 503) |
| GET `/system` | Authed | `{"hostname","version","channel","booted_slot","uptime_s","cpu","gpu":{"vendor","name","driver","supported"},"ips":["…"],"mdns":"vapor.local","disk":{"data_total","data_free"},"temps":[{"name","c"}]}` |
| GET `/status` | Authed | installed system only (404 on the installer). One summary for page loads that never waits on Sunshine or ethtool: `{"system":…,"sunshine":…,"stream":{"client","app"?,"mode","hdr","since"}\|null,"display":…,"update":…,"power":…,"restart":{"needed":bool,"reasons":[{"kind":"update\|rollback\|display\|extensions","version"?}]}}`. `system`, `display` and `update`: as their GET routes. `sunshine`: GET `/sunshine` without `session`, from the 3 s poll (only `running` is asked now). `stream`: the session in progress as `session.begin` carries it, null without one. `power`: GET `/power` without `wol`; keep-awake is checked now, the other busy reasons come from the last 15 s policy pass. `restart.reasons`, in this order: `update` when `update.next_boot` is newer than the booted version, else `rollback` (both with `next_boot.version`), `display` while `display.reboot_needed`, and `extensions` while a restart would try the extensions' `pending` set (GET `/extensions` `restart.needed`); `needed` is true when there is one. Readers show a general 'restart to finish' for a kind they do not know. A part that fails is null and the others still answer |
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
| GET `/extensions` | Authed | installed system only. `{"extensions":[{"id","name","summary","category":"runtime\|system\|app","core":bool,"upstream":{"name","url","license"},"caveats":["…"],"state":"installed\|not-installed\|installing\|restart-needed\|needs-attention\|not-in-this-version","wanted":bool,"mounted":bool,"size","progress":null\|{"bytes","total"},"reason","permissions":["…"],"runs_as_root":bool,"downloads":[{"what","from","checked":"pinned\|publisher-hash\|none","runs_code":bool,"when":"install\|update\|launch"}],"settings":[{"key","type":"bool\|choice\|disk","label","help","restart":bool,"choices":["…"],"value","needs_password":bool,"required":bool}],"actions":[{"name","label","confirm":{"title","body","button","tone"}\|null}],"web":{"port","label"}\|null,"web_running":bool,"status":[{"text","tone"?}],"copy":{"install","remove"},"requires":["…"],"required_by":["…"],"needs_password":bool,"module_options":bool,"steam":{"compat_tool","forces":[{"app","name"}],"hooks":[{"app","name"}],"shortcuts":[{"name"}]}\|null}],"restart":{"needed":bool,"auto":bool,"reason"},"skip_once":bool}` (every list is a list, never null; `value` is a bool, or a string for `choice` and `disk`; the states, `reason`, `steam`, `restart` and `skip_once`: see Extensions, Control center) |
| POST `/extensions/{id}` | Authed | `{"options"?:{"<setting>":value},"password"?}` → the GET `/extensions` document: adds the extension and what it requires. 400 for a bad body or options, or no drive for a `required` disk setting of what it adds; 403 for a missing or wrong password when it needs one (`needs_password`, or an option whose setting `needs_password`), 429 and 503 as POST `/auth/password`; 404 for an unknown id; 409 with the reason when this version lacks it, it is core, it conflicts with a wanted or core extension, its images do not fit, or a descriptor it checks cannot be read |
| DELETE `/extensions/{id}` | Authed | query `purge=0\|1` (else 400) → the GET `/extensions` document: stops its units, undoes what its helper set up (`purge`: and deletes its data) and removes it at the next restart. 404 for an unknown id; 409 for core, while a wanted extension requires it, or when its descriptor cannot be read |
| PUT `/extensions/{id}/settings` | Authed | `{"settings":{"<setting>":value},"password"?}` → the GET `/extensions` document. 400 for an unknown setting or a value it does not take; 403, 429, 503 for the password a changed setting that `needs_password` needs; 404 for an unknown id; 409 when its descriptor cannot be read |
| POST `/extensions/{id}/actions/{name}` | Authed | `{"args"?:{…}}` → the GET `/extensions` document, once the action ran. 400 when `args` is not an object or is larger than 64 KiB; 404 for an unknown id or action; 409 while the extension is not running (mounted); 500 when the action failed ("<label> didn't finish. Try again.", or "… in time …") |
| POST `/extensions/{id}/retry` | Authed | → the GET `/extensions` document ("Try again": forgets the failed set, sets the extension up again). 404 for an unknown id |
| POST `/extensions/skip-once` | Authed | → `{}`: the next boot mounts no extension |
| DELETE `/extensions/skip-once` | Authed | → `{}`: the next boot mounts the extensions again (takes POST's flag back) |
| GET `/install/probe` | Setup | query `?source=&channel=` (optional); returns `"source","channel","version","min_size","source_error"` plus `{"disks":[{"path","model","size","transport","removable","is_live","has_vaporos","hostname"?,"steam_libraries":[{"uuid","label","path"}]}],"ips":[…],"timezone":"Europe/Brussels","gpu":{…}}`; `hostname` is the name the VaporOS install on that disk answers to (`<hostname>.local`, the one a repair keeps), read from `etc/upper/hostname` on its vos_data (mounted read-only without journal replay, never while an install runs); omitted when unknown |
| POST `/install` | Setup | `{"disk","mode":"erase\|repair","hostname","password","timezone","libraries":["uuid"],"source":"","channel":""}` → 202 `{"job":"id"}`; empty source means the live medium; `oci://` sources take `channel` (default: the live image's channel, then `main`); in repair, an empty hostname or timezone keeps the installed one; `hostname` (lower-cased first) follows PUT `/system/hostname`'s rule, so `localhost` is a 400 |
| GET `/install/status` | Setup | `{"state":"idle\|running\|done\|failed","step","percent","message","error"}`; `step` is one of `probe`, `partition`, `write`, `verify`, `bootloader`, `configure`, `done` (empty while `idle`) |
| POST `/install/reboot` | Setup | → `{}` |
| GET `/welcome` | Local | the welcome.json content without the setup code (`code` empty, `qr` cut before `setup?`) |

**Pages** (server-rendered shells plus JS modules that call the API): `/` home, `/devices`, `/screen`, `/system` with `/system/updates`, `/system/power`, `/system/storage`, `/system/extensions`, `/system/settings`, `/system/logs` and `/system/about`, plus `/login` and `/setup` (the first-run password, or the installer wizard on the ISO). The old paths `/pair`, `/streaming`, `/display`, `/storage`, `/updates`, `/power` and `/advanced` answer 303 to their new pages (`/pair` goes to `/devices#pair`) and keep the query. On the ISO, every page answers 303 to `/setup`. There are no external assets (no CDN): everything is embedded. The installer's `/setup` adds `connect-src 'self' http://*.local`. `/login`'s `?next=` returns to a path on vosd's own origin, or to an extension's web UI: an `http` URL with the sign-in page's own host name, no user info and another explicit port of 1024 or more.

**Extension web UIs** (installed systems only; a second listener per port, `internal/extensions`): for each `network.ports` entry with mode `proxied` (never one whose port or upstream port is 47990) of an extension this boot mounted that `wanted` ∪ core (with their requirements) still wants, vosd listens on `:<port>` (every address) while each of its `services` that is not a template is active. It looks every 5 s and whenever the ports file is written; a unit that is activating or reloading keeps a port already served, and a port that cannot be bound is logged and tried again, never stopping vosd. Each request passes, in order:
1. the source IP is loopback, private, link-local or ULA, whatever `web.allow_public` says (else 403);
2. `Host` is in vosd's allowlist (else 421);
3. a request other than GET or HEAD is same-origin, as on vosd (else 403);
4. it carries a live `vos_session` (else GET and HEAD answer 303 to `http://<host>[:<vosd's port>]/login?next=<URL-escaped http://<host>:<port><request URI>>`, the port the one it came in on, and the rest 401).

None of vosd's headers are added (no CSP). Only a request other than GET or HEAD, or a navigation (`Sec-Fetch-Mode: navigate`, or, without that header, which browsers send only to secure origins, an `Accept` with `text/html`), counts as web UI activity. The request then goes to the entry's `upstream` on loopback (`httputil.ReverseProxy`: `Rewrite` with `SetXForwarded`, so `X-Forwarded-For` is the peer and a client's own forwarding headers are dropped; `Host` as the browser sent it; flushed as it comes, for event streams) without `vos_*` cookies or `X-VOS-*` headers, and the answer loses any `Set-Cookie` of a `vos_*` name (cookies are per host, not per port). An upstream that does not answer is 502 "`<name>` isn't answering. Try again in a moment.". The firewall opens the port to the local network only (see Firewall).

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
- `vos-generator` (`/usr/lib/systemd/system-generators`), following `/run/vos/extensions.json` and never intent: for each mounted extension, the system units its shipped descriptor (`/usr/share/vos/extensions/<id>.json`) lists in `services` with scope `system` are wanted by `multi-user.target` (`.service`), `sockets.target`, `timers.target` or `paths.target` by suffix (an instance links to its template's file); a unit file that is missing is logged and skipped. On a trial boot (mode `pending`, or any boot systemd-boot counts: `LoaderBootCountPath-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f` in efivars, the variable `vos health` reads, also when `vos.ext=0` or `skip-once` left mode `off` or the report cannot be read) it writes `vos-health.service.d/50-vos-trial.conf`: `[Unit]` `JobTimeoutSec=10min`, `JobTimeoutAction=reboot-force`. It always exits 0
- `seatd.service.d/vos.conf`
- `vos-firewall.service`: `/usr/lib/vos/vos-firewall`, which loads `/usr/lib/vos/nftables.nft` with the optional rules of config.json and `/var/lib/vos/ext/ports` in one transaction (see Firewall); vosd reloads it when either changes
- every `systemd-sysext*` and `systemd-confext*` unit is masked: only the initramfs merges extensions
- no `RuntimeWatchdogSec` in `/usr/lib/systemd/system.conf.d`: CachyOS blacklists the hardware watchdog drivers, and only trial boots load them and set it, in `/run/systemd/system.conf.d/50-vos-trial.conf` (see Extensions, Trial and promotion)

No keypress, local or from a Moonlight client, reboots or suspends the box: `ctrl-alt-del.target` is masked, `system.conf.d/vos.conf` sets `CtrlAltDelBurstAction=none`, `sysctl.d/99-vos.conf` sets `kernel.sysrq = 0`, and logind ignores the reboot, suspend and hibernate keys (and their long presses).

**User** (`vapor`), in `/usr/lib/systemd/user`, controlled by vosd via `systemctl --user -M vapor@`:
- `vos-gamescope.service`: env from `%t/vos/gamescope.env` (`VOS_OUTPUT`, `VOS_GS_EXTRA`); `GAMESCOPE_MODE_SAVE_FILE=%h/.config/gamescope/modes.cfg`; `ExecStartPre=-/usr/bin/vos steam prepare` first, so every start of Steam follows the mounted extensions, and `ExecStopPost=-/usr/bin/vos steam prepare`, so every stop (at shutdown too) applies the latest `steam.json` while Steam is down (see Extensions, Steam)
- `vos-sunshine.service`: `/usr/bin/sunshine %h/.config/sunshine/sunshine.conf`; ordered `After=` gamescope with no dependency on it; no `[Install]`. vosd starts it on every boot and again whenever it is inactive (checked every 10 s), only with a supported GPU. After each start or restart vosd reads that run's log until Sunshine's web UI is up: a run that logged `Platform failed to initialize` (its KMS capture found no lit plane, e.g. it came up before gamescope's first modeset) never recovers by itself and fails every stream with error 503, so vosd restarts it while idle, 5 s after the failure and then backing off to 2 min.

Both user units carry `ConditionKernelCommandLine=!vos.mode=live`.

**Display policy (vosd):**
- **No GPU profile:** no gamescope. Show the welcome screen if any connector is connected.
- **GPU and no physical monitor:** gamescope and Steam run permanently, and Sunshine runs.
- **GPU and a monitor:** the welcome screen runs while idle. `session begin` stops it, starts gamescope and applies the mode. `session end` plus 60 s idle (no game or download) stops gamescope and starts the welcome screen again.
- **HDR:** a `begin` whose HDR differs from gamescope's restarts gamescope only when no Steam game runs; otherwise the session keeps the current HDR (the mode still switches).
- **A Steam game runs** (here and for idle shutdown: one probe, `internal/gameproc`) while a process of `vapor` (the real uid in `/proc/<pid>/status`) is Steam's reaper for an app (`argv[0]`'s base name exactly `reaper`, `argv[1]` `SteamLaunch`, and a non-zero decimal `AppId=`, up to 64 bits, before `--`; gamescope's `gamescopereaper` is not one), or has `SteamAppId` > 0 in its environment, or while `vapor`'s manager has `vos-ext-handoff.service` loaded (`/run/user/1000/systemd/transient/vos-ext-handoff.service` is a regular file, opened through gamerfs: a symlink in any component counts as no unit).
- **Steam restart** (`display.Manager.RestartSteam`): when what `vos steam prepare` applies changes under a running Steam (see Extensions, Steam), vosd restarts Steam, only in the gaming state on a box without a monitor (with one, the welcome screen replaces gamescope at the next idle moment, and its next start applies the change), with no session and nothing busy (a game, a download, a Sunshine client), only while `vos-gamescope.service` is active (while it is activating, its prepare step perhaps reading the old file, or deactivating, it looks again later; inactive or failed, its next start runs prepare, so nothing is restarted, though a `dispatcher` that turned false has vosd run prepare at once, see Extensions, Steam), and only when the display policy is not switching (it never waits for it); not at all when gamescope's unit last started (its start job, `InactiveExitTimestamp`) after the change. With a process holding `~/.steam/steam.pipe` it runs `steam -shutdown` as `vapor` with that process's `DISPLAY`, `WAYLAND_DISPLAY`, `GAMESCOPE_WAYLAND_DISPLAY`, `XAUTHORITY` and `DBUS_SESSION_BUS_ADDRESS`, for at most 10 s and without holding the display policy: a session that begins meanwhile waits, within its own time limit, until the restart is over and then switches the mode of the gamescope that came back. Steam counts as restarted once that holder's pid or the unit's `ExecMainStartTimestamp` changes (gamescope exits with Steam and `Restart=always` starts both again); after 30 s, without a holder, or when `steam -shutdown` fails or runs out of time, it restarts `vos-gamescope.service` (when the display policy is not switching and the rules above still allow it). Requests made before the restart are one restart; vosd's own come at most once every 10 minutes, one a person asks for whenever the box is quiet. vosd's first gamescope start waits up to 5 s for `/var/lib/vos/ext/steam.json` to name the boot report's set, so prepare sees this boot's extensions.

Sunshine renders from `/usr/share/vos/sunshine.conf.tmpl` into `~vapor/.config/sunshine/sunshine.conf`:
- `capture = kms`, `encoder = vulkan`, `adapter_name = <render node of the virtual connector's card>`, `output_name = <virtual connector name, e.g. DP-1>` (Sunshine's KMS capture matches connector names; a number would mean "n-th active plane", which shifts while the welcome screen lights other outputs)
- `origin_web_ui_allowed = pc`, `upnp = disabled`, `system_tray = disabled`, `gamepad = xone`
- `global_prep_cmd = [{"do":"/usr/bin/vos session begin","undo":"/usr/bin/vos session end","elevated":false}]`. Sunshine runs prep commands on launch only, so a resumed stream keeps the mode and HDR it was launched with; vosd publishes a `system.message` when it sees a resume (a client connects with no `session.begin` since the last one left).

`~vapor/.config/sunshine/apps.json`: `"Steam"` (no command), then per installed game `{"name","detached":["/usr/bin/vos session launch steam://rungameid/<id>"]}`, then the apps of the extensions mounted and still wanted (as steam.json has them, see Extensions, Steam): per shortcut whose game id `vos steam prepare` recorded and did not mark deleted (its record, read through gamerfs; where accounts differ, the id VaporOS gave the shortcut, else the lowest account's) `{"name":<the shortcut's name>,"detached":["/usr/bin/vos session launch steam://rungameid/<game id>"]}` with the game id as an unsigned 64-bit decimal (unless its extension's helper adds an entry of the same name, case aside, which stands for it), then the entries their helpers add (`SteamParts.SunshineApps`, such as TruckersMP's multiplayer start), never `cmd`. Names stay unique (a game's gains ` (<id>)`, an extension's ` (2)`, ` (3)`, …), and `$` is written `$$` in names and in the extensions' commands.

**Firewall** (nftables, input policy drop):
- accept lo, established, ICMP/ICMPv6, udp 5353, udp 67-68;
- tcp 80 (and 443 if HTTPS) from private ranges;
- Sunshine tcp 47984, 47989, 48010 and udp 47998-48000;
- tcp 22 only while SSH is enabled;
- the running extensions' ports (`/var/lib/vos/ext/ports`, see Extensions)
  from the private ranges only (`allow_lan`: the `lan4`/`lan6` sets), whatever
  `web.allow_public` says. Each line must be exactly `tcp <port>`,
  `udp <port>` or `tcp <port> upstream <upstream port>`, both 1024-65535
  without a leading zero and never 47990, at most 64 lines; one that is not
  (a blank line too), and the file opens nothing and adds no upstream rule.
  The upstream is never opened.

Output (policy accept): what leaves through `lo` passes the `upstream`
chain, where for each `upstream <port>` of the ports file a TCP packet to
that port from a socket whose owner (`meta skuid`) is not root is rejected
with a TCP reset. Only root (vosd, which guards the web UI it proxies)
reaches an extension's own server on loopback; a game or anything else
running as `vapor` does not. Packets without a socket are not matched.

47990 is never reachable from outside.

**Network** (NetworkManager, `/usr/lib/NetworkManager/conf.d/50-vos.conf`; Wi-Fi through iwd; DNS through systemd-resolved):
- NetworkManager, not networkd, because Steam's SteamOS first-run setup and its network settings list and join networks over NM's D-Bus API.
- Wired ports get NM's automatic DHCP profile; route metric 100 wired, 600 Wi-Fi.
- `ipv4.dhcp-client-id=mac`, so the installer and the installed system get the same address (networkd's `ClientIdentifier=mac` before it sent the same one).
- `hostname-mode=none` (vosd owns `/etc/hostname`), `ethernet.wake-on-lan=ignore` (`50-vos-wol.link` sets it), no connectivity check.
- Networks joined in Steam are saved in `/etc/NetworkManager/system-connections/`, kept by the `/etc` overlay.
- `/usr/share/polkit-1/rules.d/50-vos-networkmanager.rules` grants every `org.freedesktop.NetworkManager.*` action to `vapor`, which has no seat session.

**SteamOS helpers** Steam runs with `-steamos3` and calls SteamOS's helpers, which VaporOS ships as stubs in `/usr/bin` and `/usr/bin/steamos-polkit-helpers/` (`polkit-helpers/` below). None of them goes through pkexec as SteamOS's do; each runs as `vapor`.
- `steamos-update` exits 7 (no update; VaporOS updates through vos) and `steamos-select-branch -c` prints `stable`. Steam checks for and applies OS updates through `polkit-helpers/steamos-update`, which runs `/usr/bin/steamos-update` with the same arguments; without it, first-run setup stops at "Unable to download the required update (2)".
- `polkit-helpers/steamos-set-timezone <Area/City>` runs `timedatectl set-timezone`, as on SteamOS, so a zone picked in Steam's first-run setup or settings replaces the installer's and is kept by the `/etc` overlay. `/usr/share/polkit-1/rules.d/50-vos-timedate.rules` grants `vapor` `org.freedesktop.timedate1.set-timezone` and no other timedated action (not the clock, NTP or the RTC).
- Steam Deck firmware, which Steam checks on every update check ("Error: YieldingCheckForUpdateBIOS: update check error" without the stubs): `polkit-helpers/jupiter-biosupdate` exits 0 and `polkit-helpers/jupiter-dock-updater` exits 7, each its updater's "no update" (Valve's BIOS updater exits 7 when it has one, the dock updater 0). `jupiter-initial-firmware-update` exits 0: no day-one firmware update, which only the Steam Deck OLED needs.
- `polkit-helpers/steamos-devkit-mode --disable` exits 0, since there is no devkit service to stop; `--enable` exits 1.
