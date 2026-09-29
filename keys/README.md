# Update-signing keys

VaporOS only installs an update whose `manifest.json` carries a valid ed25519
signature (`manifest.json.sig`) from a key the running image trusts. The
image trusts every `*.pub` file in `/usr/lib/vos/keys/`, which the build fills
from here. See "Update format" in [docs/CONTRACTS.md](../docs/CONTRACTS.md).

## Formats

- **Public key** (`*.pub`): one line, the base64 of the raw 32-byte ed25519
  public key.
- **Private key**: the base64 of the 64-byte ed25519 private key (seed and
  public key, as Go's `crypto/ed25519` stores it).
- **Signature** (`manifest.json.sig`): the base64 of the ed25519 signature over
  the exact bytes of `manifest.json`.

`vos keygen --out PREFIX` writes `PREFIX.key` and `PREFIX.pub`;
`vos sign --key FILE|env:VAR manifest.json` writes `manifest.json.sig`.

## The keys

| Key | Private half | Public half | Trusted by |
| --- | --- | --- | --- |
| release | GitHub secret `VOS_SIGNING_KEY` | `keys/release.pub` (this directory) | every image |
| dev | the builder only: `/var/lib/vos-build/keys/dev.key` on the Proxmox host (`/keys/dev.key` in the build container) | next to it, `dev.pub` | debug images (`VOS_DEBUG=1`) only |

- **release.pub** is committed here once the release key exists. A build
  without it produces images that trust no release key.
- **The dev key** is created on the builder by `scripts/build.sh` (through
  `vos keygen`) the first time a debug build runs, and every debug build is
  signed with it. It never leaves the builder and is never committed:
  `.gitignore` refuses `*.key` everywhere. Release images do not trust it, so a
  dev build can never update a release install.

Private keys never go in this directory.
