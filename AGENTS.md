# AGENTS.md

Guide for coding agents working in this repository. Users read README.md.

## What this is

VaporOS is an immutable, CachyOS-based appliance. It turns a PC with an AMD GPU into a
headless Steam streaming box (Sunshine to Moonlight). The root is a read-only
erofs image in one of two A/B slots. `/etc` is an overlay, and `/var` lives on the data
partition. One static Go binary, `vos`, is the whole control plane. The product
name is **VaporOS**. The repo is `github.com/jasperaelvoet/vaporos`.

**`docs/CONTRACTS.md` is the source of truth** for every interface: paths,
config.json, kernel cmdline, disk layout, update format and acceptance, the HTTP
API, the session protocol, the welcome file, serial lines and units. Change a
contract there first, then in code, and keep the two in sync.

## Map

| Path | What |
| --- | --- |
| `cmd/vos/main.go` | Multi-call entry point: `daemon`, `welcome`, `session`, `edid`, `install`, `update/rollback/status/health/sign/keygen`, `generator` (also via argv[0] `vos-generator`), `version`. |
| `internal/api` | vosd's HTTP server: routing, access levels (Public/Authed/Setup/Local), sessions, CSRF, setup codes, Host and source-IP checks, rate limit, SSE. |
| `internal/auth` | Web admin password (argon2id, PHC string) in `/var/lib/vos/auth.json`. |
| `internal/boot` | ESP management: systemd-boot entries per slot with boot counting, kernels under `/vos/<ver>/`, loader.conf, cmdline assembly. |
| `internal/config` | Paths (package variables so tests can redirect them), `config.json` schema and defaults, shared helpers. |
| `internal/daemon` | vosd: wires every service to the HTTP server; web installer in live mode; VOS-READY announcer; setup code; config watcher. |
| `internal/display` | GPU profiles, connector state, gamescope control, display policy (welcome vs gamescope, follow the client's mode), learned modes. |
| `internal/display/drm` | Pure-Go DRM/KMS ioctls (no libdrm): dumb buffers, scanout mode queries, uevents. |
| `internal/display/edid` | Generator and decoder for the virtual display's EDID (`vos edid`). |
| `internal/display/welcome` | `vos welcome`: draws URL, QR, status and setup code on physical monitors as DRM master. |
| `internal/events` | In-process pub/sub behind `GET /api/v1/events` (SSE) and the welcome screen. |
| `internal/install` | `vos install` and the web installer (shared `Install`): probe, partition (erase or repair), write and verify slot a, bootloader, first-boot config. |
| `internal/manifest` | Signed update manifest (ed25519). Verify the bytes first, parse second. |
| `internal/power` | Idle shutdown policy, keep-awake, Wake-on-LAN status. |
| `internal/session` | Sunshine prep-cmd protocol: `vos session begin` / `end` to `/run/vos/session.sock`. Always exits 0. |
| `internal/storage` | Disk and Steam library discovery, library adoption, the systemd generator (mount units, SSH). |
| `internal/storage/steam` | Reads `libraryfolders.vdf`, `appmanifest_*.acf` and library markers. Never talks to Steam. |
| `internal/sunshine` | Renders `sunshine.conf`/`apps.json`, local API credentials, pairing/clients/logs proxy, KMS plane-loss watchdog. |
| `internal/sysd` | Thin wrappers around `systemctl` and processes (no D-Bus). |
| `internal/system` | `/system/*` and `/ssh`: machine info, hostname, reboot/poweroff, SSH toggle and keys. |
| `internal/update` | `vos update/rollback/status/health/sign/keygen` and vosd's update service: OCI/HTTP/dir sources, streaming into the idle slot, update-state. |
| `internal/web` | Embedded web UI (go:embed): server-rendered page shells in `templates/`, vanilla JS and CSS in `static/`, no external assets. |
| `rootfs/` | Overlay onto the image: systemd system/user units, initramfs hook (`usr/lib/initcpio`), nftables, cmdline, sysusers, tmpfiles, os-release, avahi, networkd. |
| `iso/airootfs/` | Empty placeholder tree. Nothing in `build/` or `scripts/` uses it. |
| `packages.txt` | Every package in the image. Repos are set up in `build/pacman.conf`. |
| `build/` | The Arch build container (`Dockerfile`) and `build.sh`, which pacstraps, overlays and makes erofs, kernel, initramfs, manifest and ISO. |
| `scripts/` | `dev.sh` (the dev loop behind `make`), `build.sh` (runs the build on the Proxmox builder), `serial.py`. |
| `tests/` | `qemu-smoke.sh` (the CI install-and-boot test), `vm-checks.sh` (in-VM checks for `make test`), QMP helpers. |
| `keys/` | `release.pub` only. See `keys/README.md`. |
| `website/` | Project site (Astro, static), deployed to https://jasperaelvoet.github.io/vaporos/ with base `/vaporos`. |

## Commands

Go checks, exactly as CI runs them (Go version from `go.mod`):

```sh
gofmt -l .                 # must print nothing
go vet ./...
go test -race ./...
```

The static binary, as CI builds it:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v3 \
  go build -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.commit=$GITHUB_SHA" \
  -o .build/vos ./cmd/vos
```

CI then fails unless `file` reports it as statically linked and `vos version` prints `$VERSION`.

Other commands:
- `make go-test` runs vet and test locally, with no VM and no builder.
- `VOS_WEB_DEV=127.0.0.1:8080 go test ./internal/web -run TestDevServer -timeout 0` runs the web UI
  against a fake API, with no machine. Flags are in `internal/web/devserver_test.go`.
- `VOS_GEN_ICONS=1 go test ./internal/web -run TestGenerateIcons` regenerates the PNG icons from `static/icon.svg`.
- JS unit tests: `node --test internal/web/jstest/*.test.mjs`. They also run under `go test` when `node` is installed.
- Website: `cd website && npm ci && npm run dev` (or `npm run build`).
- `make` (the dev loop) and `make test` (end to end) drive Proxmox. Run them only when the user asks.

## Hard rules

- **Never build the OS image or compile Rust on the Mac.** x86-64-v3 packages need AVX2, which Rosetta doesn't have.
  Images are built on the Proxmox builder LXC 9010 (`vaporos-builder`) through `make` or `scripts/build.sh`.
  The only local compilation is the Go `vos` binary and Go tests.
- **Verify OS changes by booting them** on the dev VM (`make`, then `make test`). Unit tests alone aren't enough.
- **Only one VaporOS VM, ever:** the dev VM (VMID 9000, `vaporos-dev`). Never create a second one, and never
  take over a VM the user is using. The builder LXC 9010 is not a VM, so using it is fine.
- **Never open browser windows or GUI apps.** The VM display is `make console` (native Screen Sharing), and only
  when asked. Use `CONSOLE=0` for unattended runs. Browser automation runs headless only.
- **Never commit `*.key` or any private key.** `keys/release.pub` is the only key in git. The release private key
  is the `VOS_SIGNING_KEY` secret, and the dev key never leaves the builder.
- **Don't push or merge without the user's go-ahead.** When `VOS_SIGNING_KEY` is set, every push to any branch
  that touches OS files publishes a signed release and an OCI tag.
- **The version and channel scheme is fixed:** version `YYYYMMDD.HHMMSS` (commit UTC time), tag `v<version>`,
  channel = sanitized branch name. Release assets are `vaporos-<ver>.iso`, `manifest.json`, `manifest.json.sig`
  and `SHA256SUMS`. Don't rename assets without updating `website/src/lib/release.ts` and the docs.
- The product is VaporOS. Never call it WaterVaporOS (that's only the local folder name).

## Style

- Match the surrounding Go: stdlib first, small packages, and tests beside the code (`*_test.go`, fakes over mocks).
  Dependencies are limited to `golang.org/x/*` and `rsc.io/qr`. Don't add new ones lightly.
- Keep the binary static: no cgo and no libraries that need it (DRM goes through raw ioctls in `internal/display/drm`).
- Keep comments few: say why, not what. Package doc comments point to `docs/CONTRACTS.md`.
- Paths go through `internal/config` variables so tests can redirect them. Never hard-code `/var/lib/vos` elsewhere.
- Web UI: no external assets or CDNs (the CSP is `default-src 'self'`). Vanilla JS only.
- Commit messages: one plain, imperative sentence that says what changed and why.

## Skills

`.claude/skills/` holds vetted Agent Skills for the website and the web UI. Sources, pinned commits, licences
and the security review are in `.claude/skills/SOURCES.md`.
- `frontend-design` leads art direction. `build-awwwards-quality-sites` sets the bar for motion and WebGL on the
  website. The `gsap-*` skills cover GSAP, the website's only animation library (pin the exact `gsap` version).
- `fixing-accessibility` and `fixing-metadata` are the review checklists.
- `playwright-cli` runs headless only: never `--headed`, `show`, `attach` or `--extension`.
- The web UI's CSP rules out most website tooling (no inline scripts, no CDNs, no WebAssembly). Check a skill's
  advice against the Style rules above before using it in `internal/web`.

## CI

- `.github/workflows/build.yml` runs on every push, PR and manual dispatch, except changes that touch only
  `website/**`, `**.md`, `.claude/**` or `pages.yml`. It has four jobs:
  - `go`: gofmt, vet, `test -race` and the static `vos` binary;
  - `image`: `build/build.sh` in the builder container, which produces the OS files and the ISO (the ISO must stay under 1.95 GiB);
  - `vm-test`: `tests/qemu-smoke.sh` installs the ISO through the web installer in QEMU and boots it;
  - `publish`: pushes only, and only with `VOS_SIGNING_KEY`. It signs the manifest, checks the signature
    against `keys/release.pub`, writes `SHA256SUMS`, and pushes to `ghcr.io/jasperaelvoet/vaporos:<version>,<channel>`.
    It also creates the GitHub release: `--latest` on `main`, a prerelease on other branches.
- `.github/workflows/pages.yml` builds `website/` and deploys it to GitHub Pages. It runs on pushes to `main` that
  touch `website/**`, `keys/release.pub` or the workflow, daily, and on manual dispatch. Only runs on `main` deploy.
  Pages must be enabled once with the Actions source (`gh api -X POST repos/jasperaelvoet/vaporos/pages -f build_type=workflow`)
  before the first run. Scheduled runs stop after 60 days without repository activity; the site's in-browser
  release refresh covers that.
