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
| `vos-generator` (argv[0], systemd generator symlink) | mount units and SSH from config.json |
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
| `/var/lib/vos/clients.json` | vosd | learned Moonlight client modes `{name: {w,h,fps,hdr,last_seen}}`; entries go when DELETE `/display/modes/{mode}` removes their mode |
| `/var/lib/vos/sunshine-api.json` | vosd | `{"user","password"}` for Sunshine's local API, mode 0600 |
| `/var/lib/vos/cmdline` | installer, vosd | machine-specific kernel args (boot disk, virtual connector + EDID) |
| `/var/lib/vos/steam-libraries.json` | vosd | `{"pending":["/var/mnt/<label>[/SteamLibrary]"]}`: adopted libraries still to be added to Steam's library list, which vosd changes only while Steam is not running |
| `/var/lib/vos/firmware/edid/vaporos.bin` | vosd | EDID with learned modes; overrides the image one via `firmware_class.path=/var/lib/vos/firmware` |
| `/var/lib/vos/health-ok` | `vos health` | JSON `{"gpu":bool,"stream":bool}` from the last good boot |
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
- **Test knobs:** `vos.health.fail=1` makes `vos health` fail on a counted boot (ignored on a blessed entry). `vos.debug=1` is reserved.

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
`/var` bind, `/etc` overlay `index=off`). It finds the partitions by GPT name
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
  3. blobs by `org.opencontainers.image.title` annotation: `manifest.json`, `manifest.json.sig`, `root.erofs`, `vmlinuz`, `initramfs.img`. Follow the 307 redirect; resume with `Range`.
- `http(s)://host/dir/` or a local dir: the same five files by name.

**Acceptance:**
- The signature verifies against any key in `/usr/lib/vos/keys`.
- `schema` = 1 and `min_updater` ≤ 1.
- `version` ≠ booted version, unless `--force`.
- `rollback_index` > booted image's, unless `--force` or `--allow-downgrade`.
- Without a picked version or `--force`: `rollback_index` > `held.rollback_index`.
- The version is not in `failed`, unless `--force`.
- Every artifact's size and sha256 match.
- Staging refuses (unless `--force`) while the running entry is on trial, or while it is marked bad and the idle slot's entry is bootable (a rollback waiting for a restart). Neither is recorded as `last_error`.

**Write order:**
1. Remove the idle slot's entries.
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
- `vos.health.fail=1` forces a failure.

It exits non-zero only on a counted boot when another entry would boot once
this one runs out of tries; otherwise a failing check is logged as degraded
and it exits 0. It writes `health-ok` unless it fails, and prints the
`VOS-HEALTH` serial line on every outcome. `FailureAction=reboot`, and
systemd-boot counting does the rest.

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
| `update.progress` | `{phase,percent,bytes,total,version,error?}` (phase `error` carries why a stage stopped; `idle` ends a stage that found nothing to do (already up to date, or already staged); `cancelled` ends one that `POST /update/cancel` stopped) |
| `update.state` | the update-state |
| `install.progress` | `{step,percent,message,state}` |
| `session.begin` | `{client,app?,mode,hdr,since}` (`app`: Sunshine's app, `Steam` or a game's name, omitted when Sunshine names none; `since`: when it was launched, RFC 3339 UTC; a resume keeps both) |
| `session.end` | `{}` |
| `pairing.pending` | `{name?}` |
| `pairing.state` | `{pairings:[{id,name,address}]}`: who waits for a PIN (as in GET `/sunshine` `pairings`), sent after vosd's first poll of Sunshine and whenever the list changes. An empty list ends any pairing prompt; Sunshine not answering counts as an empty list |
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
| GET `/display` | Authed | `{"profile":"amd\|none","virtual_connector","connectors":[{"name","status","physical":bool}],"available_connectors":["DP-2"],"modes":["WxH@R"],"current":"WxH@R"\|null,"hdr":bool,"learned":["WxH@R"],"added":["WxH@R"],"devices":[{"name","mode":"WxH@R","hdr":bool,"last_seen"}],"reboot_needed":bool,"state":"gaming\|welcome\|streaming\|none","planes":N}` (`physical`: connected and not the virtual connector; `available_connectors`: disconnected DP/HDMI ports of a supported GPU other than the current one, what the UI offers as `virtual_connector`; `planes`: fb-backed planes on the virtual connector's CRTC, 1 while gamescope composites; `added`: valid `display.extra_modes` beyond the built-in list; `devices`: clients.json, newest first; `learned` stays the union of `added` and the valid client modes beyond the built-in list) |
| POST `/display/modes` | Authed | `{"mode":"WxH@R"}` → `{"reboot_needed":true}` |
| DELETE `/display/modes/{mode}` | Authed | → `{"reboot_needed":bool}`: removes `WxH@R` from `display.extra_modes` and every clients.json entry that asked for it, then rewrites the learned EDID (applies after a reboot). A device that asks for the mode again teaches it again. 400 for a malformed mode; 404 when neither list holds it |
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

vosd re-emits `VOS-READY` whenever its IP changes.

## Units

**System**, in `/usr/lib/systemd/system`:
- `vosd.service`: `ExecStart=/usr/bin/vos daemon`, `Restart=always`
- `vos-welcome.service`: started and stopped by vosd only
- `vos-health.service`: `FailureAction=reboot` (see "Health")
- `seatd.service.d/vos.conf`
- `vos-firewall.service`: `nft -f /usr/lib/vos/nftables.nft`

No keypress, local or from a Moonlight client, reboots or suspends the box: `ctrl-alt-del.target` is masked, `system.conf.d/vos.conf` sets `CtrlAltDelBurstAction=none`, `sysctl.d/99-vos.conf` sets `kernel.sysrq = 0`, and logind ignores the reboot, suspend and hibernate keys (and their long presses).

**User** (`vapor`), in `/usr/lib/systemd/user`, controlled by vosd via `systemctl --user -M vapor@`:
- `vos-gamescope.service`: env from `%t/vos/gamescope.env` (`VOS_OUTPUT`, `VOS_GS_EXTRA`); `GAMESCOPE_MODE_SAVE_FILE=%h/.config/gamescope/modes.cfg`
- `vos-sunshine.service`: `/usr/bin/sunshine %h/.config/sunshine/sunshine.conf`; ordered `After=` gamescope with no dependency on it; no `[Install]`. vosd starts it on every boot and again whenever it is inactive (checked every 10 s), only with a supported GPU.

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
