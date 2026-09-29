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
| `vos install [flags]` | CLI install (dev and tests); the web installer uses the same code |
| `vos update [--from SRC] [--force] [--stage-only]` | fetch, verify and write the idle slot |
| `vos rollback` | next boot uses the other slot |
| `vos status [--json]` | version, booted slot, both slots, staged/failed |
| `vos health` | boot health check (vos-health.service) |
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
| `/var/lib/vos/clients.json` | vosd | learned Moonlight client modes `{name: {w,h,fps,hdr,last_seen}}` |
| `/var/lib/vos/sunshine-api.json` | vosd | `{"user","password"}` for Sunshine's local API, mode 0600 |
| `/var/lib/vos/cmdline` | installer, vosd | machine-specific kernel args (virtual connector + EDID) |
| `/var/lib/vos/firmware/edid/vaporos.bin` | vosd | EDID with learned modes; overrides the image one via `firmware_class.path=/var/lib/vos/firmware` |
| `/var/lib/vos/health-ok` | `vos health` | JSON `{"gpu":bool,"stream":bool}` from the last good boot |
| `/run/vos/session.sock` | vosd | session protocol, mode 0660 root:vapor |
| `/run/vos/welcome.json` | vosd | what the welcome screen shows (below) |
| `/run/vos/medium/vos/` | initramfs (live) | ISO contents: root.erofs, vmlinuz, initramfs.img, manifest.json(.sig) |
| `/efi` | fstab automount | ESP (systemd-boot, `/vos/<ver>/{vmlinuz,initramfs.img}`, `loader/entries/vos-<ver>[+N[-M]].conf`) |
| `/var/home/vapor` | tmpfiles | gaming user home (Steam, Sunshine config/state) |
| `/var/mnt/<label>` | generator | adopted game library disks (`/mnt` → `var/mnt`) |

Tests override these through the package variables in `internal/config`.

## Users

- `vapor` (uid/gid 1000) is created by sysusers. It is in groups `input render video seat audio`, has no password, lingers, and its home is `/var/home/vapor`. It runs gamescope, Steam, Sunshine and PipeWire as user units.
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
A missing file or field means the default. `power.idle_shutdown` defaults to false; the installer sets it to true when a wired NIC supports Wake-on-LAN (magic packet), so a PC is never switched off with no way to wake it remotely. Services change the shared config only through `config.Mutate` and read it through `Snapshot`/`View`. `update.channel` defaults to
`image.json.channel`. `auto` is `"stage"` (download and stage; applies on
next boot) or `"off"`.

## Kernel command line

A boot entry's `options` are built as: `vos.slot=<a|b>` + image cmdline + machine cmdline.
- **Image cmdline** (`/usr/lib/vos/cmdline`, and `manifest.cmdline` for a new image):
  `quiet loglevel=3 rd.udev.log_level=3 systemd.show_status=false rd.systemd.show_status=false vt.global_cursor_default=0 systemd.getty_auto=0 panic=10 console=ttyS0,115200`
- **Machine cmdline** (`/var/lib/vos/cmdline`): on a machine with a supported GPU,
  `video=<C>:e drm.edid_firmware=<C>:edid/vaporos.bin firmware_class.path=/var/lib/vos/firmware`
- **Live ISO entry:** `vos.mode=live vos.label=VOS_LIVE` + the image cmdline.
- **Test knobs:** `vos.health.fail=1` makes `vos health` fail. `vos.debug=1` is reserved.

`vos update` writes the new entry with the *new* manifest's cmdline. `vosd`
rewrites both slots' entries when the machine cmdline changes
(`boot.RewriteOptions`).

## Disk layout (unchanged from bash `vos`, with slot sizing updated)

GPT:
1. `vos_esp` (vfat, 512 MiB, label `VOS_ESP`)
2. `vos_a`
3. `vos_b`
4. `vos_data` (ext4, label `vos_data`, rest of the disk)

Slot size: `clamp(3 × image size, 8 GiB, 16 GiB)`. The minimum disk is
`512 MiB + 2×slot + 8 GiB`. On `vos_data`: `var/`, `etc/{upper,work}`.
Mounting is done by the initramfs hook (erofs slot ro at `/`, data at `/state`,
`/var` bind, `/etc` overlay `index=off`).

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
- The version is not in `failed`, unless `--force`.
- Every artifact's size and sha256 match.

**Write order:**
1. Remove the idle slot's entries.
2. Stream root into the idle slot partition while hashing.
3. Re-read and verify.
4. Kernel and initrd to `/efi/vos/<ver>/` (tmp + rename).
5. Write entry `vos-<ver>+3.conf` last.
6. Record `staged` in update-state.

**Update state** (`/var/lib/vos/update-state.json`):
```json
{"booted":"<ver>","staged":{"version":"<ver>","slot":"b","at":"RFC3339"},"failed":["<ver>"],
 "available":{"version":"<ver>","size":123,"checked":"RFC3339"},"last_error":""}
```
On daemon start: if `staged.version` ≠ booted, and the staged entry has no
tries left (or is gone), append it to `failed` and clear `staged`. If it
equals booted, clear `staged`.

## Health

`vos-health.service` is `RequiredBy=boot-complete.target`, `Before=boot-complete.target`, `Type=oneshot`. It runs `vos health`, which exits non-zero if any check fails:
- `/state` is mounted rw and `/etc` is an overlay;
- `vosd` answers `GET http://127.0.0.1/api/v1/ping` within 60 s;
- `user@1000.service` is active;
- if `health-ok.gpu`, a DRM card with an amdgpu (or other supported) driver exists;
- `vos.health.fail=1` forces a failure.

On success it writes `health-ok`. Its `OnFailure=` reboots, and systemd-boot
counting does the rest.

## HTTP API (`vosd`, port 80, prefix `/api/v1`, JSON)

**Middleware, in order:**
1. Source IP must be loopback, private, link-local or ULA (else 403, unless `web.allow_public`).
2. `Host` must be in the allowlist (`localhost`, `127.0.0.1`, `<hostname>`, `<hostname>.local`, `vaporos-setup.local`, any current local IP, with or without a port), else 421.
3. Security headers (CSP `default-src 'self'; img-src 'self' data:; frame-ancestors 'none'`, nosniff, same-origin referrer).
4. Session cookie `vos_session` (HttpOnly, SameSite=Strict, 30-day sliding).
5. For every non-GET on `Authed` routes, header `X-VOS-CSRF` must equal the session's csrf.

**Access levels:**
- `Public`: no auth.
- `Authed`: valid session.
- `Setup`: installer or first-run setup code, via header `X-VOS-Setup: <code>` or a `vos_setup` cookie set by `GET /setup?code=…`.
- `Local`: loopback only.

**Errors:** `{"error":"message"}` with a proper status.

**Events:** `GET /api/v1/events` (Authed or Setup) is an SSE stream: `event: <topic>`, `data: <json>`.

| Topic | Data |
| --- | --- |
| `update.progress` | `{phase,percent,bytes,total,version}` |
| `update.state` | the update-state |
| `install.progress` | `{step,percent,message,state}` |
| `session.begin` | `{client,mode,hdr}` |
| `session.end` | `{}` |
| `pairing.pending` | `{name?}` |
| `display.changed` | `{}` |
| `power.idle` | `{idle_seconds,shutdown_in}` |
| `system.message` | `{level,text}` |

| Method + path | Access | Request → Response |
| --- | --- | --- |
| GET `/ping` | Public | `{"ok":true,"mode":"os\|installer","version"}` |
| GET `/auth/me` | Public | `{"authenticated":bool,"csrf":"…","needs_setup":bool,"installer":bool}` |
| POST `/auth/login` | Public | `{"password"}` → `{"csrf"}` + cookie; 401 on a wrong password; 429 after 5 failures per IP (backoff up to 15 min) |
| POST `/auth/logout` | Authed | → `{}` |
| POST `/auth/setup` | Setup | `{"password"}` → `{"csrf"}`; only when auth.json is missing (first run after a CLI install); 409 in installer mode |
| POST `/auth/password` | Authed | `{"current","new"}` → `{}` |
| GET `/system` | Authed | `{"hostname","version","channel","booted_slot","uptime_s","cpu","gpu":{"vendor","name","driver","supported"},"ips":["…"],"mdns":"vapor.local","disk":{"data_total","data_free"},"temps":[{"name","c"}]}` |
| PUT `/system/hostname` | Authed | `{"hostname"}` → `{}` |
| POST `/system/reboot`, `/system/poweroff` | Authed | → `{}` |
| GET `/update` | Authed | update-state + `{"config":config.update,"booted":…,"other_slot":{"version"}}` |
| POST `/update/check` | Authed | → `{"available":{…}\|null}` |
| POST `/update/stage` | Authed | `{"version"?}` → `{}` (progress via events) |
| POST `/update/activate` | Authed | → `{}` (reboots into the staged version) |
| POST `/update/rollback` | Authed | → `{}` (next boot = other slot; UI then offers reboot) |
| PUT `/update/settings` | Authed | `{"channel","auto"}` → `{}` |
| GET `/sunshine` | Authed | `{"running":bool,"version","streaming":bool,"session":{"client","mode","hdr"}\|null,"pending_pairing":bool}` |
| POST `/sunshine/pair` | Authed | `{"pin","name"}` → `{}` (Sunshine `POST /api/pin`) |
| GET `/sunshine/clients` | Authed | `{"clients":[{"uuid","name"}]}` |
| DELETE `/sunshine/clients/{uuid}` | Authed | → `{}` |
| GET/PUT `/sunshine/settings` | Authed | `{"encoder","bitrate_kbps_max","audio_sink"?,"gamepad"}`, a whitelisted subset |
| GET `/sunshine/logs` | Authed | `text/plain`, last 2000 lines |
| POST `/sunshine/restart` | Authed | → `{}` |
| GET `/display` | Authed | `{"profile":"amd\|none","virtual_connector","connectors":[{"name","status","physical":bool}],"modes":["WxH@R"],"current":"WxH@R"\|null,"hdr":bool,"learned":["WxH@R"],"reboot_needed":bool,"state":"gaming\|welcome\|streaming\|none","planes":N}` (`planes`: fb-backed planes on the virtual connector's CRTC; 1 while gamescope composites) |
| POST `/display/modes` | Authed | `{"mode":"WxH@R"}` → `{"reboot_needed":true}` |
| PUT `/display/settings` | Authed | `{"hdr":bool,"virtual_connector"?}` → `{}` |
| GET `/storage` | Authed | `{"disks":[{"path","model","size","uuid","label","fstype","mounted_at","is_system":bool,"steam_library":bool,"adopted":bool,"free"}]}` |
| POST `/storage/libraries` | Authed | `{"uuid"}` → `{}` (config + mount now + Steam library registration hint) |
| DELETE `/storage/libraries/{uuid}` | Authed | → `{}` |
| GET/PUT `/power` | Authed | `{"idle_shutdown","idle_minutes","keep_awake_until"?,"wol":[{"iface","mac","enabled"}],"busy":{"reason"}\|null}` |
| POST `/power/keep-awake` | Authed | `{"minutes"}` (0 = clear) → `{}` |
| GET/PUT `/ssh` | Authed | `{"enabled","keys":[…]}` |
| GET `/install/probe` | Setup | query `?source=&channel=` (optional); returns `"source","channel","version","min_size","source_error"` plus `{"disks":[{"path","model","size","transport","removable","is_live","has_vaporos","steam_libraries":[{"uuid","label","path"}]}],"ips":[…],"timezone":"Europe/Brussels","gpu":{…}}` |
| POST `/install` | Setup | `{"disk","mode":"erase\|repair","hostname","password","timezone","libraries":["uuid"],"source":"","channel":""}` → 202 `{"job":"id"}`; empty source means the live medium; `oci://` sources take `channel` (default: the live image's channel, then `main`) |
| GET `/install/status` | Setup | `{"state":"idle\|running\|done\|failed","step","percent","message","error"}` |
| POST `/install/reboot` | Setup | → `{}` |
| GET `/welcome` | Local | the welcome.json content |

**Pages** (server-rendered shells plus vanilla JS that calls the API):
`/` dashboard, `/login`, `/setup` (first-run password or installer wizard), `/pair`, `/streaming`, `/display`, `/storage`, `/updates`, `/power`, `/advanced`. There are no external assets (no CDN): everything is embedded.

## Session protocol (`/run/vos/session.sock`)

Served by the display manager (`display.Manager.Run`), not by the daemon.

Newline-delimited JSON, one request and one response per connection.
- `{"op":"begin","client":"<SUNSHINE_CLIENT_NAME?>","app":"<SUNSHINE_APP_NAME>","width":W,"height":H,"fps":F,"hdr":bool}` → `{"ok":bool,"mode":"WxH@R","hdr":bool,"message":"…"}`
- `{"op":"end"}` → `{"ok":true}`

`vos session begin` reads `SUNSHINE_CLIENT_WIDTH/HEIGHT/FPS/HDR` and
`SUNSHINE_APP_NAME` from the environment. vosd gives a request 80 s and answers on its own if the handler overruns. It **always exits 0**, with a
hard timeout of 90 s.

## Welcome screen (`/run/vos/welcome.json`, written by vosd)

```json
{"mode":"os|installer","hostname":"vapor","url":"http://vapor.local","ip_url":"http://192.168.1.50",
 "qr":"http://192.168.1.50/","code":"ABCD-EFGH","title":"VaporOS","status":"Ready to stream",
 "detail":"Open this address on your phone or computer","version":"…"}
```
The virtual EDID's PNP id is `VOS` (not assigned in hwdata's pnp.ids, so gamescope falls back to the raw id) and its monitor name `VaporOS`, so gamescope's `modes.cfg` key is `VOS VaporOS`; vosd still reads the real key from `gamescopectl` at runtime instead of assuming it. vosd keeps gamescope compositing (the `composite_force` convar and the `GAMESCOPE_COMPOSITE_FORCE` root property, which Steam can reset) and re-asserts it every 5 s during sessions, because stock Sunshine's KMS capture loses the picture when gamescope scans a game out on its own plane.

`vos welcome` redraws whenever the file changes (poll 1 s). It lights every
connected physical connector with its preferred mode. It also lights the
virtual connector (if configured) with a 1920x1080 splash, because Sunshine
probes the encoder on the virtual connector before prep-cmd runs. It owns
tty1 in `KD_GRAPHICS` mode, never reads input, and exits cleanly on SIGTERM
(releasing DRM master).

## Serial lines (harness contract; written to `/dev/ttyS0` if present)

- `VOS-READY mode=installer version=<v> ip=<ip> code=<code>`: the installer API is up.
- `VOS-READY mode=os version=<v> ip=<ip> code=<code|->`: the installed system is up (the code appears only when auth.json is missing).
- `VOS-INSTALL state=<done|failed> message=<…>`

vosd re-emits `VOS-READY` whenever its IP changes.

## Units

**System**, in `/usr/lib/systemd/system`:
- `vosd.service`: `ExecStart=/usr/bin/vos daemon`, `Restart=always`
- `vos-welcome.service`: started and stopped by vosd only
- `vos-health.service`
- `seatd.service.d/vos.conf`
- `vos-firewall.service`: `nft -f /usr/lib/vos/nftables.nft`

**User** (`vapor`), in `/usr/lib/systemd/user`, controlled by vosd via `systemctl --user -M vapor@`:
- `vos-gamescope.service`: env from `%t/vos/gamescope.env` (`VOS_OUTPUT`, `VOS_GS_EXTRA`); `GAMESCOPE_MODE_SAVE_FILE=%h/.config/gamescope/modes.cfg`
- `vos-sunshine.service`: `/usr/bin/sunshine %h/.config/sunshine/sunshine.conf`; `Wants=`, not `Requires=`, gamescope

Both user units carry `ConditionKernelCommandLine=!vos.mode=live`.

**Display policy (vosd):**
- **No GPU profile:** no gamescope. Show the welcome screen if any connector is connected.
- **GPU and no physical monitor:** gamescope and Steam run permanently, and Sunshine runs.
- **GPU and a monitor:** the welcome screen runs while idle. `session begin` stops it, starts gamescope and applies the mode. `session end` plus 60 s idle (no game or download) stops gamescope and starts the welcome screen again.

Sunshine renders from `/usr/share/vos/sunshine.conf.tmpl` into `~vapor/.config/sunshine/sunshine.conf`:
- `capture = kms`, `encoder = vulkan`, `adapter_name = <render node of the virtual connector's card>`, `output_name = <virtual connector name, e.g. DP-1>` (Sunshine's KMS capture matches connector names; a number would mean "n-th active plane", which shifts while the welcome screen lights other outputs)
- `origin_web_ui_allowed = pc`, `upnp = disabled`, `system_tray = disabled`, `gamepad = xone`
- `global_prep_cmd = [{"do":"/usr/bin/vos session begin","undo":"/usr/bin/vos session end","elevated":false}]`

**Firewall** (nftables, input policy drop):
- accept lo, established, ICMP/ICMPv6, udp 5353, udp 67-68;
- tcp 80 (and 443 if HTTPS) from private ranges;
- Sunshine tcp 47984, 47989, 48010 and udp 47998-48000;
- tcp 22 only while SSH is enabled.

47990 is never reachable from outside.
