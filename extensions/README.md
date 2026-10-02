# Extensions

An extension is software VaporOS adds on top of its read-only system:
CachyOS Proton, CoolerControl, TruckersMP, the Star Citizen launcher. Each one
lives in a directory here, is built by the same build as the OS image it
belongs to, and reaches a PC as one sealed, read-only image. How it is built,
checked, stored, merged at boot, tried and promoted is in
[`docs/CONTRACTS.md`](../docs/CONTRACTS.md) under "Extensions"; this file is
how to add or change one.

## What an extension is made of

```
extensions/<id>/extension.json      the descriptor (schema 1; unknown fields are an error)
extensions/<id>/files/usr/...       files copied into the image as they are
internal/extensions/<id>/           its Go helper, built into vos (optional)
```

- **The image** holds only `usr/`: the descriptor's `packages` (prebuilt
  CachyOS and Arch packages, resolved from the same package snapshot as the OS
  image), its pinned `fetch` downloads, its `files/`, and
  `usr/lib/vos/ext/<id>/`. The build names it by its fs-verity digest and lists
  that digest in the OS image's catalog, so a PC mounts exactly what was built.
- **The descriptor** says what the extension is and what it may do:
  permissions, services, kernel module options, network ports, a web page,
  what it changes in Steam, its data areas, what it downloads, its settings
  and actions. The control center's card is made from it, so a new extension
  needs no page code. `internal/extensions/descriptor` is the schema.
- **The helper** does what a descriptor cannot say: download a mod's files,
  place a game's prefix on a drive, copy a password. It implements
  `extensions.Helper` (embed `extensions.NopHelper`), registers itself with
  `extensions.RegisterHelper` and any `vos ext <id> …` commands with
  `extensions.RegisterCommand` from an `init` function, and is linked into vos
  by a blank import in `internal/extensions/all`. vosd calls it as root; work
  in vapor's home or a game drive goes through a `vos ext <id> …` subprocess
  run as vapor (`sysd.AsGamer`), never as root.

## Rules

The build checks every image (`vos ext check-tree`) and fails one that:

- ships anything outside `usr/`, or a file the base or another extension
  ships;
- puts files where the system would run them, beyond the permissions its
  descriptor declares (`service`, `user-service`, `udev`, `sysctl`, `modules`,
  `polkit`, `dbus`, `compat-tool`); the declared list must match what the
  image holds exactly;
- ships a system unit without a VaporOS drop-in (a finite `TimeoutStartSec=`),
  or a unit that reaches into the base's units or the whole system (see
  CONTRACTS for the full list);
- has a program whose libraries the base cannot provide.

Not supported, on purpose:

- `/opt` or `/usr/local` payloads (both are on the data partition here);
- AUR or DKMS packages, and kernel modules (`*.ko`);
- `sysusers.d` (users go in the base's `sysusers.d/vos.conf`, or the unit uses
  `DynamicUser=yes`) and `tmpfiles.d` (use `StateDirectory=` and friends; vosd
  makes the data areas);
- confext, and plugin stores that download and run code as root;
- plain Wine: Windows software runs through the core CachyOS Proton.

## Adding one

1. Write `extensions/<id>/extension.json`. Start from an existing descriptor;
   `go test ./internal/extensions/descriptor/` validates every descriptor in
   this directory.
2. Add `files/usr/...` for what the packages lack (a VaporOS drop-in for its
   service, art for a Steam shortcut).
3. If it needs logic, add `internal/extensions/<id>/` with its helper and tests,
   and import it in `internal/extensions/all`.
4. Say in the copy (`copy.install`, `copy.remove`, `caveats`, `downloads`) what
   it does in plain words: what it changes, what it downloads and whether
   VaporOS can check those files. See `design/voice.md`.
5. Build (`make build`) and boot it on the dev VM (`make`, `make test`): the
   build checks the image, and `tests/vm-checks.sh extensions` checks what was
   mounted.

A change to a descriptor, its files, its packages or its helper ships with the
next OS version; the build reuses an unchanged image (by its input key), so a
PC only downloads an extension again when it changed.
