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
  /** The H1 as it is set, line by line. */
  headline: ['Questions, answered.'],
  /** The label of the topic links under the lead. */
  topicsLabel: 'Topics',
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
      'Not even to install, if the PC already boots from the USB stick in UEFI mode with Secure Boot off: with no monitor connected, the installer skips the setup code. A monitor or TV makes the firmware settings and the code easy.',
      "After that you don't need one. VaporOS makes its own virtual display at the resolution, frame rate and HDR setting of the device you stream to, so you don't need a dummy plug either.",
      'If a monitor is connected, it only ever shows the welcome screen: the address of the PC, a QR code, and a status line such as “Ready to stream”. It never shows a terminal or a desktop.',
    ],
    short: 'No. With no monitor the installer skips the setup code, and afterwards VaporOS makes its own virtual display.',
  },
  {
    id: 'headless',
    q: 'Can I install it without a monitor?',
    a: [
      "Yes, if the PC already boots from the USB stick in UEFI mode with Secure Boot off. With no monitor connected, the installer doesn't ask for the setup code: open `http://vaporos-setup.local` on your phone. Anyone on your network could open it while it waits, so do this on a network you trust.",
      "If the PC's graphics card isn't recognised yet, the installer still asks for the code, which a monitor shows.",
    ],
    short: "Yes: with no monitor connected, the installer doesn't ask for the setup code.",
  },
  {
    id: 'dual-boot',
    q: 'Can I dual boot it with Windows?',
    a: [
      'No. VaporOS installs to a whole drive and erases it. Other drives in the PC are left alone, and if they hold Steam libraries you can pick them during setup (or later, under [[Storage]]) and keep playing those games. Nothing on them is changed.',
    ],
    short: 'No. VaporOS installs to a whole drive and erases it. Other drives are left alone.',
  },
  {
    id: 'drives',
    q: 'Which drives can hold my Steam games?',
    a: [
      "Drives formatted ext2, ext3, ext4, btrfs, XFS, F2FS or NTFS. exFAT and FAT drives can't be used for games. VaporOS mounts the drive, adds it to Steam's library list, and never changes the files on it; a drive without a library gets an empty SteamLibrary folder.",
    ],
    short: 'ext2/3/4, btrfs, XFS, F2FS or NTFS; not exFAT or FAT.',
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
      "After installing, open `http://vapor.local` on a phone or computer on the same network. If you picked another name during setup, it's that name plus `.local`. A connected monitor also shows the address, the PC's IP address and a QR code. With no monitor, your router's list of devices shows its IP address.",
      'While you install from the USB stick, the address is `http://vaporos-setup.local`.',
    ],
    short: '`http://vapor.local` once installed; `http://vaporos-setup.local` while you install.',
  },
  {
    id: 'updates',
    q: 'Where do updates come from?',
    a: [
      "From this project's GitHub container registry, `ghcr.io/jasperaelvoet/vaporos`. By default VaporOS downloads new versions in the background and prepares them; they start on the next restart. You can turn that off and update by hand under [[Updates]].",
      'Every update has to carry a valid ed25519 signature from the release key, before VaporOS writes anything, and every file has to match its size and SHA-256 before the new version can start. The `main` channel is the stable one; other channels are test builds.',
    ],
    short: 'From `ghcr.io/jasperaelvoet/vaporos`, signed with the release key.',
  },
  {
    id: 'rollback',
    q: 'What if an update breaks something?',
    a: [
      "VaporOS keeps two copies of the system. An update goes to the one you aren't running, and the old one stays as it was. If the new version doesn't start cleanly, the PC goes back to the previous version by itself and won't install that version again.",
      "You can also go back by hand: open [[Updates]], choose [[Roll back]], and restart. VaporOS then holds back the version you left: it won't install it again by itself, only a newer one.",
    ],
    short: "If a new version doesn't start cleanly, the PC goes back to the previous one by itself.",
  },
  {
    // Idle power-off: off by default, turned on by a new install only when a
    // wired network adapter wakes on a magic packet (internal/install/target.go).
    id: 'power',
    q: 'Does it stay on all the time?',
    a: [
      'It depends on the PC. When a wired network adapter can wake it from Moonlight (Wake-on-LAN), the installer turns on idle power-off: after 15 minutes of nobody playing it powers off, but never while it streams, runs a game, downloads or updates. Otherwise it stays on. You can change this, or keep it awake for 1 or 4 hours, under [[Power]].',
    ],
    short: 'It powers off when idle only if Moonlight can wake it again.',
  },
  {
    // The Power page lists each wired adapter's MAC address (GET /power wol[]).
    // The control center is served by the PC itself, so it can't be opened
    // while the PC is off, and browsers can't send the UDP magic packet.
    id: 'wake',
    q: 'Can I wake it from my phone?',
    a: [
      'Yes, when the PC is on a network cable and Wake-on-LAN works. In Moonlight, pick the PC and choose **Wake**; this works for a PC Moonlight has paired with.',
      'Any Wake-on-LAN app on the same network works too, with the MAC address shown under [[Power]]. Note it down while the PC is on.',
      "The VaporOS web page itself can't wake the PC: the PC serves that page, and a web page can't send the wake packet.",
    ],
    short: 'Yes: Moonlight wakes it, and so does any Wake-on-LAN app with the MAC address shown under [[Power]].',
  },
  {
    id: 'wifi',
    q: 'Can it use Wi-Fi?',
    a: [
      'Not from the web page: the installer and the control center only set up a network cable, and Wake-on-LAN needs the cable.',
    ],
    short: 'Setup and Wake-on-LAN need a network cable.',
  },
  {
    id: 'switching',
    q: "I switched devices and the picture didn't change.",
    a: [
      'The resolution, refresh rate and HDR are set when a game starts in Moonlight. Resuming it from another device keeps them. Quit the game in Moonlight first, then start it on the new device.',
    ],
    short: 'Quit the game in Moonlight first, then start it on the new device.',
  },
  {
    id: 'internet',
    q: 'Can other people on the internet reach it?',
    a: [
      "The web page only answers devices on private networks, such as your home network, and asks for your admin password. Sunshine's own admin page is never reachable from other devices. SSH is off unless you turn it on under [[Advanced]], and then only with keys you add.",
    ],
    short: 'The web page only answers devices on private networks, and asks for your admin password.',
  },
  {
    id: 'apps',
    q: 'Can I install other software on it?',
    a: [
      // The root is a read-only erofs image (build/build.sh); pacman is in it
      // (base pulls it in) but has nothing it could write to.
      'No. The system is one read-only image: nothing can be installed on it, which is what makes updates and rollback reliable. Games come from Steam, as usual.',
    ],
    short: 'No. The system is one read-only image: nothing can be installed on it.',
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

/**
 * The FAQ page's topics, each an <h2> over its questions (by id, in order).
 * Every question in `faq` is in exactly one topic (tests/unit checks it).
 */
export interface FaqGroup {
  /** The anchor: /faq/#installing. */
  id: string;
  title: string;
  ids: string[];
}

export const faqGroups: FaqGroup[] = [
  { id: 'hardware', title: 'Basics and hardware', ids: ['what', 'gpu', 'monitor', 'headless', 'drives'] },
  { id: 'installing', title: 'Installing', ids: ['dual-boot', 'secure-boot', 'address'] },
  { id: 'safety', title: 'Updates and safety', ids: ['updates', 'rollback', 'verify', 'internet', 'apps'] },
  { id: 'everyday', title: 'Everyday use', ids: ['power', 'wake', 'switching', 'wifi'] },
];

/** faq entries by id, in the order given. */
export function faqByIds(ids: readonly string[]): QA[] {
  return ids.map((id) => faq.find((f) => f.id === id)).filter((f): f is QA => !!f);
}
