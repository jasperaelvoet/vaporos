// Frequently asked questions. Every answer follows from the code; see
// docs/CONTRACTS.md in the repo for the underlying behaviour. Ported from the
// Astro site's lib/faq.ts (its HTML turned into Rich paragraphs).
import { links, routes } from './site';
import type { Headline, IconName, Rich } from './types';

export interface QA {
  /** The anchor: /faq/#rollback opens that question. */
  id: string;
  q: string;
  /** The answer, one Rich string per paragraph. */
  a: Rich[];
  /** Same facts, one line. */
  short: Rich;
}

export const faqPage = {
  eyebrow: 'FAQ',
  title: { text: 'Questions,', accent: 'answered.' } satisfies Headline,
  lead: 'What VaporOS runs on, what it does to your PC, and how it keeps itself up to date.' as Rich,
  ask: {
    icon: 'question' as IconName,
    text: `Something else? [Open an issue on GitHub](${links.issues}).` as Rich,
  },
};

export const faq: QA[] = [
  {
    id: 'what',
    q: 'What is VaporOS?',
    a: [
      'An immutable, CachyOS-based operating system that turns a PC with an AMD GPU into a headless Steam streaming box. Steam runs on a virtual display on the PC, Sunshine streams it, and you play with [Moonlight](https://moonlight-stream.org/) on a TV, phone, tablet or laptop. You install and manage it from a web page on your phone.',
    ],
    short: 'An immutable, CachyOS-based OS that turns a PC with an AMD GPU into a headless Steam streaming box.',
  },
  {
    id: 'gpu',
    q: 'Does it work with NVIDIA or Intel graphics?',
    a: [
      "Not yet. VaporOS streams with AMD Radeon GPUs on the `amdgpu` driver. On other graphics it still installs and runs, but the dashboard and the welcome screen say there's no supported graphics card, and it won't stream. Very old AMD cards that use the legacy `radeon` driver aren't supported either.",
    ],
    short: 'Not yet. VaporOS streams with AMD Radeon GPUs on the `amdgpu` driver.',
  },
  {
    id: 'monitor',
    q: 'Do I need a monitor? Can I use one?',
    a: [
      "Only while you install. The installer's setup code appears on the PC's screen, so connect a monitor or TV for the install.",
      "After that you don't need one. VaporOS makes its own virtual display at the resolution, frame rate and HDR setting of the device you stream to, so you don't need a dummy plug either.",
      'If a monitor is connected, it only ever shows the welcome screen: the address of the PC, a QR code, and a status line such as “Ready to stream”. It never shows a terminal or a desktop.',
    ],
    short: 'Only while you install. After that, VaporOS makes its own virtual display.',
  },
  {
    id: 'dual-boot',
    q: 'Can I dual boot it with Windows?',
    a: [
      'No. VaporOS installs to a whole drive and erases it. Other drives in the PC are left alone, and if they hold Steam libraries you can pick them during setup (or later, under Storage) and keep playing those games. Nothing on them is changed.',
    ],
    short: 'No. VaporOS installs to a whole drive and erases it. Other drives are left alone.',
  },
  {
    id: 'secure-boot',
    q: 'Why does Secure Boot have to be off?',
    a: [
      "The VaporOS kernel isn't signed for Secure Boot, so firmware with Secure Boot on refuses to start it. Turn Secure Boot off and boot in UEFI mode. Legacy (CSM/BIOS) boot isn't supported.",
    ],
    short: "The VaporOS kernel isn't signed for Secure Boot.",
  },
  {
    id: 'address',
    q: 'Where do I find the web page?',
    a: [
      "After installing, open `http://vapor.local` on a phone or computer on the same network. If you picked another name during setup, it's that name plus `.local`. A connected monitor also shows the address, the PC's IP address and a QR code.",
      'While you install from the USB stick, the address is `http://vaporos-setup.local`.',
    ],
    short: '`http://vapor.local` once installed; `http://vaporos-setup.local` while you install.',
  },
  {
    id: 'updates',
    q: 'Where do updates come from?',
    a: [
      "From this project's GitHub container registry, `ghcr.io/jasperaelvoet/vaporos`. By default VaporOS downloads new versions in the background and prepares them; they start on the next restart. You can turn that off and update by hand under Updates.",
      'Every update has to carry a valid ed25519 signature from the release key, before VaporOS writes anything, and every file has to match its size and SHA-256 before the new version can start. The `main` channel is the stable one; other channels are test builds.',
    ],
    short: 'From `ghcr.io/jasperaelvoet/vaporos`, signed with the release key.',
  },
  {
    id: 'rollback',
    q: 'What if an update breaks something?',
    a: [
      "VaporOS keeps two copies of the system. An update goes to the one you aren't running, and the old one stays as it was. If the new version doesn't start cleanly, the PC goes back to the previous version by itself and won't install that version again.",
      'You can also go back by hand: open Updates, choose **Roll back**, and restart.',
    ],
    short: "If a new version doesn't start cleanly, the PC goes back to the previous one by itself.",
  },
  {
    // STALE? Commit 0382231 (after the Astro copy was checked) made idle shutdown
    // default to OFF; the installer turns it on when a wired NIC supports
    // Wake-on-LAN (docs/CONTRACTS.md, power.idle_shutdown). Reword before launch.
    id: 'power',
    q: 'Does it stay on all the time?',
    a: [
      "By default it powers off after 15 minutes of nobody playing, but never while it's streaming, downloading or updating. You can change the time or turn this off under Power, or keep it awake for an hour or four. Moonlight wakes a paired PC over Wake-on-LAN, which needs a wired connection.",
    ],
    short: 'By default it powers off after 15 minutes of nobody playing. Moonlight wakes it.',
  },
  {
    id: 'internet',
    q: 'Can other people on the internet reach it?',
    a: [
      "The web page only answers devices on private networks, such as your home network, and asks for your admin password. Sunshine's own admin page is never reachable from other devices. SSH is off unless you turn it on under Advanced, and then only with keys you add.",
    ],
    short: 'The web page only answers devices on private networks, and asks for your admin password.',
  },
  {
    id: 'apps',
    q: 'Can I install other software on it?',
    a: [
      'No. The system is one read-only image with no package manager, which is what makes updates and rollback reliable. Games come from Steam, as usual.',
    ],
    short: 'No. The system is one read-only image with no package manager.',
  },
  {
    id: 'verify',
    q: 'How do I know the ISO is genuine?',
    a: [
      `Every release comes with \`SHA256SUMS\` and a signed \`manifest.json\`. The [download page](${routes.verify}) shows how to check both. They prove different things: \`SHA256SUMS\` catches a damaged download, and the signature proves that \`manifest.json\`, the list of system image files, came from the release key. Neither signs the ISO file itself, because \`SHA256SUMS\` is not signed.`,
      "The installer on the ISO only installs a system image whose manifest carries a valid signature. Download from this site or the project's GitHub releases.",
    ],
    short: `Check \`SHA256SUMS\` and the signed \`manifest.json\`: the [download page](${routes.verify}) shows how.`,
  },
];

/** faq entries by id, in the order given. */
export function faqByIds(ids: readonly string[]): QA[] {
  return ids.map((id) => faq.find((f) => f.id === id)).filter((f): f is QA => !!f);
}
