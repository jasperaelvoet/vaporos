// "Check your download": the same checks the release pipeline runs
// (.github/workflows/build.yml, "Check the signature against
// keys/release.pub"), written out for people. Ported from
// components/VerifySteps.astro and pages/download.astro.
//
// The signature command embeds the release key, which only server code can
// read (src/lib/key.ts). So the parts that need it are functions:
//
//   import { RELEASE_KEY } from '@/lib/key';          // server component
//   const steps = verifySteps(RELEASE_KEY);            // pass down as props
import { anchors, links, routes } from './site';
import type { Callout, Code, Headline, IconName, LinkItem, Rich } from './types';

export const verifySection = {
  id: anchors.verify,
  eyebrow: 'Verify',
  title: { text: 'Check your download' } satisfies Headline,
  lead: 'Optional, and quick. Download `SHA256SUMS`, `manifest.json` and `manifest.json.sig` from the same release as the ISO, then run these in that folder.' as Rich,
};

/** Step 1's command. Exact. */
export const CHECKSUM_COMMAND = 'sha256sum -c --ignore-missing SHA256SUMS';

/** Step 2's commands, with the key filled in. Exact (the \\x escapes are literal backslashes for printf). */
export function signatureCommand(key: string): string {
  return `# The VaporOS release key (keys/release.pub)
echo '${key}' > release.pub

# An Ed25519 public key in DER form: a fixed 12-byte header, then the raw key
printf '\\x30\\x2a\\x30\\x05\\x06\\x03\\x2b\\x65\\x70\\x03\\x21\\x00' > release.der
base64 -d < release.pub >> release.der
base64 -d < manifest.json.sig > manifest.sig

openssl pkeyutl -verify -pubin -keyform DER -inkey release.der -rawin \\
  -in manifest.json -sigfile manifest.sig`;
}

export interface VerifyStep {
  num: number;
  title: string;
  body: Rich;
  code: Code;
  /** What a good run prints. */
  expect: string;
  /** What the step needs to run (already part of `body`). */
  needs: Rich;
}

export function verifySteps(key: string): VerifyStep[] {
  return [
    {
      num: 1,
      title: 'Check the download',
      body: 'Put `SHA256SUMS` next to the ISO and run this, on Linux or macOS. It should print `OK` for the ISO. A mismatch means the file is damaged: download it again.',
      code: { title: 'Checksum', lang: 'sh', code: CHECKSUM_COMMAND },
      expect: 'OK',
      needs: 'Linux or macOS.',
    },
    {
      num: 2,
      title: 'Check the signature',
      body: '`manifest.json` lists every file of the system image with its size and SHA-256, and `manifest.json.sig` is its ed25519 signature. This is the check every installed VaporOS runs before it accepts an update. It needs OpenSSL 3 (on macOS, from Homebrew: the built-in LibreSSL lacks it) and bash or zsh, and it should print `Signature Verified Successfully`.',
      code: { title: 'Signature', lang: 'sh', code: signatureCommand(key) },
      expect: 'Signature Verified Successfully',
      needs: 'OpenSSL 3 (on macOS, from Homebrew: the built-in LibreSSL lacks it) and bash or zsh.',
    },
  ];
}

/** The note under the steps: what the two checks do and don't cover. */
export const verifyProof = {
  icon: 'info',
  text: '**What these prove.** The checksum catches a damaged download. The signature proves that `manifest.json` came from the release key; it covers the system image files, not the ISO file itself, and `SHA256SUMS` is not signed. The installer on the ISO only installs a system image whose manifest carries a valid signature.',
} satisfies Callout;

export function releaseKeyCard(key: string) {
  return {
    icon: 'key' as IconName,
    title: 'Release public key',
    detail: 'ed25519, base64 of the raw 32-byte key',
    code: { title: 'release.pub', lang: 'text', code: key } satisfies Code,
    /** A site file: link it with withBase() and the download attribute. */
    download: { label: 'Download release.pub', href: routes.releaseKey, icon: 'download' } satisfies LinkItem,
    compare: { label: 'Compare it with the repository', href: links.keyInRepo, icon: 'external' } satisfies LinkItem,
  };
}
