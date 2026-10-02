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
| `internal/brand` | The design tokens in Go (states, palette, heat ramps, the TV's type and look), the mark rasterizer for the TV and the PNG icons, the screen-shape readout and the TV-to-phone handshake mark. Its tests generate every file made from `design/` except the fonts; it imports nothing from this repo. |
| `internal/config` | Paths (package variables so tests can redirect them), `config.json` schema and defaults, shared helpers. |
| `internal/daemon` | vosd: wires every service to the HTTP server; web installer in live mode; VOS-READY announcer; setup code; config watcher. |
| `internal/display` | GPU profiles, connector state, gamescope control, display policy (welcome vs gamescope, follow the client's mode), learned modes. |
| `internal/display/drm` | Pure-Go DRM/KMS ioctls (no libdrm): dumb buffers, scanout mode queries, uevents. |
| `internal/display/edid` | Generator and decoder for the virtual display's EDID (`vos edid`). |
| `internal/display/welcome` | `vos welcome`: draws URL, QR, status and setup code on physical monitors as DRM master. |
| `internal/events` | In-process pub/sub behind `GET /api/v1/events` (SSE) and the welcome screen. |
| `internal/extensions` | vosd's extensions service (`service.go`, `pass.go`, `rehash.go`): the booted slot file, fetching and sealing images, reconcile, GC and the once-per-boot re-read; `Status` is provisional until the API. And `vos ext`: one `register` per command file (`cli.go`); the build's `check-tree`, `catalog` and `digest` in `cli_build.go`, `fetch` in `cli_fetch.go`, the `launch` dispatcher in `cli_launch.go`. |
| `internal/extensions/buildcheck` | The build's side: checks an extension's image tree against its descriptor, the base and the other extensions; writes the catalog, manifest entries and shipped descriptors. |
| `internal/extensions/catalog` | `/usr/lib/vos/extensions.list`, the image's extension catalog the initramfs trusts (line format, closure, from a manifest). |
| `internal/extensions/descriptor` | `extension.json` (schema 1, unknown fields rejected), from the source tree and as shipped in the image. |
| `internal/extensions/fsverity` | Pure-Go fs-verity file digest (sha256, 4096-byte blocks, no salt): what the kernel measures. |
| `internal/extensions/store` | The store in `/var/lib/vos/ext`: sealed images, sets, the `enabled`/`pending` links, `wanted`/`proven`/`failed`, slot catalogs, the boot report, the pure reconcile plan, promotion and GC, under `ext.lock`. |
| `internal/install` | `vos install` and the web installer (shared `Install`): probe, partition (erase or repair), write and verify slot a, bootloader, first-boot config. |
| `internal/manifest` | Signed update manifest (ed25519). Verify the bytes first, parse second. |
| `internal/power` | Idle shutdown policy, keep-awake, Wake-on-LAN status. |
| `internal/session` | Sunshine prep-cmd protocol: `vos session begin` / `end` to `/run/vos/session.sock`. Always exits 0. |
| `internal/steamprep` | `vos steam prepare [--unwrap]`, as vapor before every Steam start (never in vosd): applies `/var/lib/vos/ext/steam.json` to Steam's files (compat tools, launch-option wrapping, shortcuts and art, branches) under the Steam lock within 5 s, recording what VaporOS owns in `~/.local/state/vaporos/steam.json`. |
| `internal/storage` | Disk and Steam library discovery, library adoption, the systemd generator (mount units, SSH). |
| `internal/storage/steam` | Reads `libraryfolders.vdf`, `appmanifest_*.acf` and library markers. Edits Steam's files without touching other bytes (`vdfedit.go` splices text KeyValues; `config.vdf` compat tools, `localconfig.vdf` launch options, an ACF's `BetaKey`), reads and writes binary `shortcuts.vdf`, wraps launch options for `vos ext launch` and derives shortcut ids. Never talks to Steam. |
| `internal/sunshine` | Renders `sunshine.conf`/`apps.json`, local API credentials, pairing/clients/logs proxy, KMS plane-loss watchdog. |
| `internal/sysd` | Thin wrappers around `systemctl` and processes (no D-Bus). |
| `internal/system` | `/system/*` and `/ssh`: machine info, hostname, reboot/poweroff, SSH toggle and keys. |
| `internal/update` | `vos update/rollback/status/health/sign/keygen` and vosd's update service: OCI/HTTP/dir sources, streaming into the idle slot, update-state. |
| `internal/web` | Embedded web UI, the control center (go:embed): server-rendered page shells in `templates/`; ES modules and a Tailwind-built CSS file in `static/`; CSS input in `styles/`; the dev server's fake API data in `fixtures/`; no external assets. The routes are the registry in `web.go`, and `activeSet` picks the UI vosd serves. The previous eight-page UI stays in `templates/legacy/` and `static/legacy/` until it is removed. |
| `rootfs/` | Overlay onto the image: systemd system/user units, initramfs hook (`usr/lib/initcpio`), nftables, cmdline, sysusers, tmpfiles, os-release, avahi, NetworkManager config and its polkit rule, the `steamos-*` stubs Steam calls. |
| `iso/airootfs/` | Empty placeholder tree. Nothing in `build/` or `scripts/` uses it. |
| `extensions/` | The curated extensions, one directory per id: `extension.json` (the descriptor) and optional `files/usr/...`. Built with the image and signed in its manifest; see CONTRACTS "Extensions". |
| `packages.txt` | Every package in the image. Repos are set up in `build/pacman.conf`. |
| `build/` | The Arch build container (`Dockerfile`) and `build.sh`, which pacstraps, overlays and makes erofs, kernel, initramfs, manifest and ISO; `extensions.sh` makes the extension images (`ext-<id>.raw`, never on the ISO) and their catalog; `lib.sh` holds the helpers `selftest.sh` tests. |
| `scripts/` | `dev.sh` (the dev loop behind `make`), `build.sh` (runs the build on the Proxmox builder), `serial.py`. |
| `tests/` | `qemu-smoke.sh` (the CI install-and-boot test), `vm-checks.sh` (in-VM checks for `make test`), QMP helpers. |
| `keys/` | `release.pub` only. See `keys/README.md`. |
| `design/` | The one source for how VaporOS looks on the control center, the website and the TV: `tokens.json` (colours, the state vocabulary, type, motion), `logo.svg`, the UI icons in `icons/`, the font manifest and OFL licences in `fonts/`, the copy voice (`voice.md`) and test vectors (`*-vectors.json`). Everything made from it is generated (see Commands); `design/README.md` has the schema. |
| `tools/web/` | Node tooling for the control center, never in the OS build: `css.mjs` (the pinned Tailwind CLI that builds `internal/web/static/app.css` and `static/pages/*.css`), `dev.mjs` (dev server and CSS watch) and `e2e/` (the headless browser harness, the no-poll check, runtime budgets and flow parity). A stub `go.mod` keeps it out of `./...`. |
| `tools/fonts/` | Python (fontTools, hash-pinned in `requirements.txt`) that builds the committed fonts from `design/fonts/fonts.json`: WOFF2 for the control center, static TTF for the TV. |
| `website/` | Project site (Next.js, static export), deployed to https://jasperaelvoet.github.io/vaporos/ with base `/vaporos`. |

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
- `VOS_WEB_STRICT=1 go test ./internal/web/... ./internal/brand/...` also runs the checks plain `go test` skips
  and `web.yml` runs in CI: the fake API against the contract, and the inactive UI set.
- `VOS_GEN_DESIGN=1 go test ./internal/brand -run TestGenerateDesign` regenerates everything made from `design/`:
  the Go tokens and mark, `internal/web/styles/tokens.css`, the app icons and manifest, the logo partials,
  `internal/web/icons_gen.go`, and the website's `*.gen.*` files and icons. `TestDesignOutputsFresh` fails on a stale one.
- `python3 tools/fonts/fonts.py` rebuilds the fonts from `design/fonts/fonts.json` (`--check` compares only).
- Control center tooling needs Node 22 or newer and `npm ci --prefix tools/web` once:
  - `npm --prefix tools/web run css` rebuilds `internal/web/static/app.css` and `static/pages/*.css`;
    `run css:check` compares only. Commit the rebuilt CSS with the template, script or style change that
    needs it: `TestAppCSSFresh` fails on a stale one.
  - `npm --prefix tools/web run dev` runs the control center against a fake API with live reload and the CSS
    watch (`-- --port=8081 --preset=streaming`). Without Node:
    `VOS_WEB_DEV=127.0.0.1:8081 VOS_WEB_PRESET=idle go test ./internal/web -run '^TestDevServer$' -timeout 0`.
    Presets are `internal/web/fixtures/presets/*.json`; flags are in `internal/web/devserver_test.go`.
  - `node tools/web/e2e/run.mjs --matrix=smoke --out=DIR` is the headless browser check (every preset and page:
    console, CSP, overflow, focus, axe, and the flows in `e2e/specs/`); `--matrix=full` for every viewport and
    scheme, `--only=<spec>` for one page. `e2e/nopoll.mjs` checks that pages only keep the event stream open,
    `npm --prefix tools/web run perf` the runtime budgets, and `e2e/parity.mjs --report=DIR/report.json --strict`
    that every flow ID has a passing flow.
  - `node --test tools/web/test/*.test.mjs` tests the tooling itself.
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
- Web UI: no external assets or CDNs (the CSP is `default-src 'self'`). Vanilla ES modules only (no framework,
  no bundler). CSS is built by the pinned Tailwind CLI and committed; never edit `internal/web/static/app.css`
  or `static/pages/*.css` by hand. The CSP also rules out inline `<script>` and `<style>`, `style=` and `on*=`
  attributes; text goes into the page through `textContent` only (`TestCSPCompliance`).
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
- `.github/workflows/web.yml` runs the control center's Node checks on changes under `internal/**`, `design/**`,
  `tools/web/**` or `docs/CONTRACTS.md`: `css:check`, the strict Go tests (`VOS_WEB_STRICT=1`), the tooling's
  unit tests and the headless smoke matrix. It never blocks an OS release.
- `.github/workflows/pages.yml` builds `website/` and deploys it to GitHub Pages. It runs on pushes to `main` that
  touch `website/**`, `keys/release.pub` or the workflow, daily, and on manual dispatch. Only runs on `main` deploy.
  Pages must be enabled once with the Actions source (`gh api -X POST repos/jasperaelvoet/vaporos/pages -f build_type=workflow`)
  before the first run. Scheduled runs stop after 60 days without repository activity; the site's in-browser
  release refresh covers that.
