// The install: the three-step strip on the home page, and the full guide at
// /install/. Ported from components/InstallSteps.astro and pages/install.astro.
import { requirements, type Requirement } from './requirements';
import { links, routes } from './site';
import type { Callout, Code, Headline, IconName, Item, LinkItem, Rich } from './types';

// ---------------------------------------------------------------------------
// The strip: three steps, then a link to the guide.

export const installStrip = {
  steps: [
    {
      icon: 'usb',
      title: 'Flash a USB stick',
      body: 'Download the ISO and write it to a USB stick with balenaEtcher, Rufus or `dd`.',
      short: 'Write the ISO to a USB stick.',
    },
    {
      icon: 'monitor',
      title: 'Boot the PC from it',
      body: 'Start in UEFI mode with Secure Boot off. The screen shows an address, a QR code and a setup code.',
      short: 'UEFI mode, Secure Boot off.',
    },
    {
      icon: 'phone',
      title: 'Finish on your phone',
      body: 'Scan the code, pick a drive, set a password, and install. Then pair Moonlight at `vapor.local`.',
      short: 'Scan the code and install. Then pair Moonlight.',
    },
  ] satisfies (Item & { icon: IconName })[],
  more: { label: 'Read the full install guide', href: routes.install, icon: 'arrow' } satisfies LinkItem,
};

// ---------------------------------------------------------------------------
// Commands. Exact: these are what the copy buttons copy.

export const DD_LINUX = `# Find the USB stick, e.g. /dev/sdb
lsblk
sudo dd if=vaporos-<version>.iso of=/dev/sdX bs=4M status=progress oflag=sync`;

export const DD_MAC = `# Find the USB stick, e.g. /dev/disk4
diskutil list
diskutil unmountDisk /dev/diskN
sudo dd if=vaporos-<version>.iso of=/dev/rdiskN bs=4m`;

// ---------------------------------------------------------------------------
// The installer's wizard, the Moonlight stores, the closing note.

/** The web installer's steps, in order (numbered 1 to 6). */
export const wizard: Item[] = [
  {
    title: 'Setup code',
    body: 'Enter the code from the screen. Scanning the QR code fills it in for you. It keeps other people on your network from installing over your PC. With no monitor connected, the installer skips this step.',
    short: 'Enter the code from the screen, or scan the QR code.',
  },
  {
    title: 'Drive',
    body: "Pick the drive to install on. The USB stick you started from isn't listed. If the drive already has VaporOS, you can repair it: games, settings and paired devices stay, and the PC keeps its name and time zone.",
    short: 'Pick the drive. One that already has VaporOS can be repaired instead.',
  },
  {
    title: 'Name',
    body: 'Choose the name on your network (vapor by default, so the address is vapor.local) and an admin password of at least 8 characters. On a repair the name stays and the password is optional: leave it empty to keep the current one.',
    short: 'A network name (vapor by default) and an admin password.',
  },
  {
    title: 'Games',
    body: 'Pick your time zone, and tick any Steam libraries found on other drives to keep playing those games. VaporOS adds the ticked libraries to Steam for you. A repair keeps the time zone.',
    short: 'Your time zone, and Steam libraries on other drives.',
  },
  {
    title: 'Install',
    body: 'Check the summary. If the drive will be erased, type ERASE to confirm. The install takes a few minutes; keep the USB stick in.',
    short: 'Check the summary and install. It takes a few minutes.',
  },
  {
    title: 'Restart',
    body: 'Remove the USB stick and press Restart now. VaporOS starts in about a minute, and the page moves on by itself.',
    short: 'Remove the USB stick and restart. VaporOS starts in about a minute.',
  },
];

export interface Store {
  /** The devices it covers. */
  name: string;
  /** The store's name. */
  where: string;
  url: string;
  icon: IconName;
}

/** Where to get Moonlight. */
export const stores: Store[] = [
  {
    name: 'iPhone, iPad, Apple TV',
    where: 'App Store',
    url: 'https://apps.apple.com/app/moonlight-game-streaming/id1000551566',
    icon: 'phone',
  },
  {
    name: 'Android, Google TV',
    where: 'Google Play',
    url: 'https://play.google.com/store/apps/details?id=com.limelight',
    icon: 'phone',
  },
  { name: 'Windows, Mac, Linux', where: 'moonlight-stream.org', url: links.moonlight, icon: 'monitor' },
];

export const done = {
  icon: 'gamepad' as IconName,
  title: "That's it. Enjoy your games.",
  text: 'Stuck somewhere? The [FAQ](/faq/) covers the common questions.' as Rich,
};

// ---------------------------------------------------------------------------
// The guide: nine sections, each a list of typed blocks. A renderer switches
// on block.type; everything a block needs is inside it.

export type GuideBlock =
  | { type: 'p'; text: Rich; muted?: boolean }
  | { type: 'ul'; items: Rich[] }
  | { type: 'ol'; items: Rich[] }
  | { type: 'callout'; callout: Callout }
  | { type: 'code'; blocks: Code[] }
  | { type: 'requirements'; items: Requirement[] }
  /** The welcome screen on the PC, drawn from installerScreen (a real render replaces it once the site has stills). */
  | { type: 'figure'; screen: 'installer'; caption: string }
  | { type: 'wizard'; steps: Item[] }
  | { type: 'stores'; stores: Store[] }
  | { type: 'done'; icon: IconName; title: string; text: Rich };

export interface GuideSection {
  /** The anchor: /install/#pair. */
  id: string;
  /** '01' … '09' */
  num: string;
  /** Also the table of contents label. */
  title: string;
  /** One line: what the section is about. */
  summary: string;
  blocks: GuideBlock[];
}

// ---------------------------------------------------------------------------
// The welcome screen while the installer waits, as the PC draws it
// (internal/display/status.go: the installer's status and detail;
// internal/display/welcome: the code label and the QR caption). The IP
// address and the setup code are examples: every PC shows its own.

export const installerScreen = {
  status: 'Ready to install',
  detail: 'Open this address on a phone or computer to install VaporOS',
  url: 'http://vaporos-setup.local',
  ip: 'http://192.168.1.50',
  or: 'or',
  codeLabel: 'Setup code',
  code: 'K7QF-3M2P',
  qrLabel: 'Scan to open',
};

export const installGuide = {
  eyebrow: 'Install guide',
  title: { text: 'From USB stick', accent: 'to first game.' } satisfies Headline,
  /** The H1 as it is set, line by line. */
  headline: ['From USB stick', 'to first game.'],
  lead: 'A few minutes of your time, most of it spent waiting. You need the PC, a USB stick, a monitor or TV for the firmware settings (the installer itself runs without one), and a phone or computer on the same network.' as Rich,
  tocLabel: 'On this page',
  /** The TOC's summary on phones, where it starts collapsed: `${tocLabel} · ${n} ${tocUnit}`. */
  tocUnit: 'sections',
  /** Under each step of the installer's wizard: `${n} ${of} ${total}`. */
  of: 'of',
  sections: [
    {
      id: 'before',
      num: '01',
      title: 'Before you start',
      summary: 'Check the requirements and download the ISO.',
      blocks: [
        { type: 'p', muted: true, text: 'Check the PC against this list, and [download the ISO](/download/).' },
        { type: 'requirements', items: requirements },
        {
          type: 'callout',
          callout: {
            icon: 'alert',
            tone: 'warn',
            text: '**The drive you install on is erased** (unless you repair one that already has VaporOS). Back up anything on it first. Other drives are left alone.',
          },
        },
      ],
    },
    {
      id: 'flash',
      num: '02',
      title: 'Write the USB stick',
      summary: 'Write the ISO to a USB stick with balenaEtcher, Rufus or dd.',
      blocks: [
        { type: 'p', text: 'The ISO is a disk image, not a file to copy. Write it to the stick with one of these:' },
        {
          type: 'ul',
          items: [
            '[balenaEtcher](https://etcher.balena.io/) (Windows, Mac, Linux): pick the ISO, pick the stick, **Flash**.',
            '[Rufus](https://rufus.ie/) (Windows): pick the ISO and the stick. If Rufus asks how to write the image, choose **DD Image mode**.',
            '`dd` on Linux or macOS. Double-check the device name: `dd` overwrites whatever you point it at.',
          ],
        },
        {
          type: 'code',
          blocks: [
            { title: 'Linux', lang: 'sh', code: DD_LINUX },
            { title: 'macOS', lang: 'sh', code: DD_MAC },
          ],
        },
      ],
    },
    {
      id: 'firmware',
      num: '03',
      title: 'Firmware settings',
      summary: 'Boot mode UEFI, Secure Boot off.',
      blocks: [
        {
          type: 'p',
          text: "Open the PC's firmware setup (usually ++Del++ or ++F2++ right after power on) and check two things:",
        },
        {
          type: 'ul',
          items: [
            "**Boot mode: UEFI.** Turn off CSM or legacy boot if it's on. The installer only boots in UEFI mode.",
            "**Secure Boot: off.** The VaporOS kernel isn't signed for Secure Boot.",
          ],
        },
        {
          type: 'p',
          text: "Plug in a network cable. A connected monitor or TV shows the address and the setup code; you can unplug it afterwards. With no monitor, the installer doesn't ask for a code, so anyone on your network could open it: install that way only on a network you trust.",
        },
      ],
    },
    {
      id: 'boot',
      num: '04',
      title: 'Boot the installer',
      summary: 'Start the PC from the USB stick; the screen shows where to go.',
      blocks: [
        {
          type: 'p',
          text: 'Start the PC from the USB stick (the one-time boot menu is often ++F11++, ++F12++ or ++F8++). After a moment the screen says **Ready to install** and shows:',
        },
        {
          type: 'ul',
          items: [
            "the address `http://vaporos-setup.local`, and the PC's IP address;",
            'a **setup code**;',
            'a QR code that opens the installer with the code filled in.',
          ],
        },
        {
          type: 'p',
          text: 'If the screen says **Waiting for the network**, plug in the network cable. No monitor? Open `http://vaporos-setup.local` on your phone.',
        },
        { type: 'figure', screen: 'installer', caption: 'Illustration of the welcome screen while the installer runs.' },
      ],
    },
    {
      id: 'installer',
      num: '05',
      title: 'Install from your phone',
      summary: 'The web installer walks you through up to six steps.',
      blocks: [
        {
          type: 'p',
          muted: true,
          text: 'Scan the QR code, or open the address in a browser on the same network. The installer walks you through it:',
        },
        { type: 'wizard', steps: wizard },
      ],
    },
    {
      id: 'first-run',
      num: '06',
      title: 'Sign in',
      summary: 'Open http://vapor.local and sign in with your admin password.',
      blocks: [
        {
          type: 'p',
          text: "Open `http://vapor.local` (or the name you chose, plus `.local`) and sign in with your admin password. The dashboard shows whether VaporOS is **Ready to stream**, what it's doing, and quick actions to pair a device, stay awake, restart or power off.",
        },
        {
          type: 'p',
          text: "If `.local` names don't work on your network, use the IP address from the welcome screen instead, or find it in your router's list of devices.",
        },
      ],
    },
    {
      id: 'pair',
      num: '07',
      title: 'Pair Moonlight',
      summary: 'Get Moonlight and pair it with a 4-digit PIN.',
      blocks: [
        { type: 'p', text: 'Moonlight is the free app that plays your games from the PC. Get it for your device:' },
        { type: 'stores', stores },
        {
          type: 'ol',
          items: [
            "Open Moonlight on the same network. It usually finds the PC by itself; tap it. If it doesn't, tap **+** (Add PC) and enter the PC's address.",
            "Moonlight shows a 4-digit PIN. On the VaporOS page, open [[Pair a device]], type the PIN (the device's name is filled in), and press [[Pair]]. A connected monitor also says when a device wants to pair.",
            "Pick Steam, or one of your games, in Moonlight and play. The virtual display switches to that device's resolution, frame rate and HDR.",
          ],
        },
      ],
    },
    {
      id: 'updates',
      num: '08',
      title: 'Updates and rollback',
      summary: "Updates prepare in the background by default; if one doesn't start cleanly, the PC goes back by itself.",
      blocks: [
        {
          type: 'p',
          text: 'VaporOS checks for new versions and, by default, downloads and prepares them in the background. A prepared update starts on the next restart; you can also restart right away from the [[Updates]] page. Turn off [[Download updates automatically]] to decide yourself.',
        },
        {
          type: 'p',
          text: "Each update is written to the copy of the system you aren't running. If the new version doesn't start cleanly, the PC returns to the previous version by itself, and the [[Updates]] page lists the version that didn't start. To go back by hand, choose [[Roll back]] and restart. VaporOS then holds back the version you left: it won't install it again by itself, only a newer one.",
        },
      ],
    },
    {
      // Idle power-off: off by default, turned on by a new install only when a
      // wired network adapter wakes on a magic packet (internal/install/target.go).
      id: 'power',
      num: '09',
      title: 'Power and waking',
      summary: 'When Wake-on-LAN works, it powers off when idle; Moonlight wakes it.',
      blocks: [
        {
          type: 'p',
          text: 'If the PC has a wired network adapter that can wake it (Wake-on-LAN), the installer turns on idle power-off: after 15 minutes of nobody playing, VaporOS powers off, but never while it streams, runs a game, downloads or updates. Without one it stays on. Change this under [[Power]], or keep it awake for 1 or 4 hours with [[Stay awake]].',
        },
        {
          type: 'p',
          text: "To turn it back on, select the PC in Moonlight and choose **Wake**. Wake-on-LAN needs a wired connection, and may need to be enabled in the PC's firmware. Any Wake-on-LAN app works too, with the MAC address shown under [[Power]].",
        },
        { type: 'done', ...done },
      ],
    },
  ] satisfies GuideSection[],
};

/** [id, label] pairs for a table of contents. */
export const installToc = installGuide.sections.map((s) => ({ id: s.id, num: s.num, label: s.title }));
