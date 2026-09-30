// The home page's beats in detail: what each picture shows and says. The
// pictures are illustrations of real behaviour, with made-up names and
// versions; every claim they carry comes from features.ts, install.ts,
// faq.ts or the code named next to it.
//
// Import from '@/content/story' (not re-exported by '@/content').
import { links, routes } from './site';
import type { LinkItem, Rich } from './types';

// ---------------------------------------------------------------------------
// The hero's thermal view.

export const heroView = {
  /** The HUD's caption: a thermal camera's readout (ε is the emissivity it assumes). */
  hud: ['thermal view', 'palette inferno', 'ε 0.95'],
  /** The spot meter on the GPU, before and after the scroll starts a game. */
  spot: {
    label: 'GPU',
    ready: 'Ready to stream',
    hot: 'Streaming to Living room TV',
    hotMode: '3840 × 2160 · 60 Hz · HDR',
  },
};

// ---------------------------------------------------------------------------
// B2: the screen follows the device. Each device asks for its own mode; the
// virtual display takes it (docs/CONTRACTS.md: "The virtual display follows
// each Moonlight client's resolution, fps and HDR"). Every mode here is in
// the EDID's built-in list (internal/display/edid/mode.go Catalogue), so it
// streams natively on the first try. 3840 × 2160 at 120 Hz is not: it needs
// more than the 600 MHz pixel clock the EDID allows.

export interface Device {
  name: string;
  w: number;
  h: number;
  hz: number;
  hdr: boolean;
}

export const devices: Device[] = [
  { name: 'Living room TV', w: 3840, h: 2160, hz: 60, hdr: true },
  { name: 'iPhone', w: 2796, h: 1290, hz: 120, hdr: false },
  { name: 'Steam Deck', w: 1280, h: 800, hz: 90, hdr: false },
  { name: 'Ultrawide monitor', w: 3440, h: 1440, hz: 100, hdr: false },
];

export const screenStory = {
  pickLabel: 'Pick a device',
  stageLabel: 'The virtual display, sized for the selected device',
  display: 'virtual display',
  hz: 'Hz',
  hdr: 'HDR',
  pps: 'million pixels a second',
  /** The note under the picker. */
  note: { strong: 'Heat here is pixels per second.', text: 'The more a device asks for, the harder the GPU works for it.' },
  /** FAQ N-03, one tap away. */
  switching: { label: "Switched devices and the picture didn't change?", href: `${routes.faq}#switching` } satisfies LinkItem,
  /** The pipeline under the head, from how-it-works.ts. */
  pipelineLabel: 'How a frame gets to you',
};

// ---------------------------------------------------------------------------
// B3 and B4: the phone runs it; a monitor only shows the welcome screen.
// The TV's words are the welcome screen's own (internal/display/status.go
// buildWelcome, "Ready to stream"); the QR here opens this website.

export const remoteStory = {
  /** What the control center does, one line each (the current pages). */
  itemsLabel: 'What you do from your phone',
  items: [
    { label: 'Pair', text: 'Moonlight devices with a 4-digit PIN.' },
    { label: 'Display', text: 'Modes and HDR, learned from each device.' },
    { label: 'Storage', text: "Game drives you already have. VaporOS adds them to Steam's library list; nothing on them is changed." },
    { label: 'Updates', text: 'Signed, in the background, with a rollback to the previous version.' },
    { label: 'Power', text: 'Idle power-off, Stay awake and Wake-on-LAN.' },
  ],
  /** The demo is off until the next release (content/demo.ts): a line under the picture instead of it. */
  demoLater: 'The live demo arrives with the next VaporOS release.',
  tvCaption:
    "A monitor is optional, even to install. If one is connected, it only ever shows this: the address and a QR code. Never a terminal, never a desktop.",
  figureLabel: 'The control center on a phone, ready to stream, and the screen connected to the PC showing its address',
  /**
   * The phone: the control center's home page as it is today, ready to
   * stream (internal/web/templates/legacy/pages/dashboard.html and
   * static/legacy/js/pages/dashboard.js renderStatus: the host, "Ready to
   * stream", its detail, the display's mode, the live pill and the quick
   * actions). Drawn in the site's look; the words are the page's own.
   */
  phone: {
    host: 'vapor.local',
    live: 'Live',
    status: { verb: 'Ready', rest: 'to stream' },
    detail: 'Open Moonlight on any device and pick this PC.',
    mode: '3840 × 2160 · 60 Hz',
    actionsLabel: 'Quick actions',
    actions: ['Pair a device', 'Stay awake 1 h', 'Restart', 'Power off'],
  },
};

/**
 * A welcome screen's hostname mark (internal/brand HandshakeFor): the 5×5
 * cells, '#' on, and its two colours as heat stops. The TV draws it before
 * "Scan to open"; these are the marks of the hostnames drawn here.
 */
export interface TvHandshake {
  rows: string[];
  on: 'h6' | 'h7' | 'h8' | 'h9' | 'ink-ready';
  ground: 'h0' | 'h1' | 'h2';
}

/** The welcome screen as the PC draws it once VaporOS is installed. */
export const tvReady = {
  /** The version beside the wordmark (a made-up one, as in the updates beat). */
  version: '20260929.172712',
  /** The mark of the hostname "vapor". */
  handshake: { rows: ['#.#.#', '..#..', '##.##', '##.##', '#.#.#'], on: 'h8', ground: 'h0' } satisfies TvHandshake,
  status: 'Ready to stream',
  detail: 'Open this address on a phone or computer to pair Moonlight',
  url: { scheme: 'http://', host: 'vapor.local' },
  ip: 'http://192.168.1.50',
  ipLead: 'or',
  qrCaption: 'Scan to open',
};

/** The welcome screen while the installer waits (status.go: "Ready to install"). */
export const tvInstaller = {
  version: 'installer 20260929.172712',
  /** The mark of the hostname "vaporos-setup". */
  handshake: { rows: ['##.##', '#...#', '#####', '.....', '#####'], on: 'h6', ground: 'h0' } satisfies TvHandshake,
  status: 'Ready to install',
  detail: 'Open this address on a phone or computer to install VaporOS',
  url: { scheme: 'http://', host: 'vaporos-setup.local' },
  ip: 'http://192.168.1.50',
  ipLead: 'or',
  code: { label: 'Setup code', value: 'K7QF-3M2P' },
  qrCaption: 'Scan to open',
};

/**
 * A real QR code (rsc.io/qr, level M, as the welcome screen draws its own)
 * for the website's address, so scanning the picture opens this site. 29
 * modules; `d` is the dark modules as SVG path data.
 */
export const siteQR = {
  text: 'https://jasperaelvoet.github.io/vaporos/',
  size: 29,
  d: 'M0 0h7v1h-7zM9 0h1v1h-1zM12 0h1v1h-1zM15 0h1v1h-1zM18 0h2v1h-2zM22 0h7v1h-7zM0 1h1v1h-1zM6 1h1v1h-1zM8 1h3v1h-3zM12 1h1v1h-1zM15 1h2v1h-2zM18 1h2v1h-2zM22 1h1v1h-1zM28 1h1v1h-1zM0 2h1v1h-1zM2 2h3v1h-3zM6 2h1v1h-1zM11 2h1v1h-1zM14 2h2v1h-2zM19 2h1v1h-1zM22 2h1v1h-1zM24 2h3v1h-3zM28 2h1v1h-1zM0 3h1v1h-1zM2 3h3v1h-3zM6 3h1v1h-1zM9 3h1v1h-1zM11 3h2v1h-2zM14 3h1v1h-1zM18 3h1v1h-1zM22 3h1v1h-1zM24 3h3v1h-3zM28 3h1v1h-1zM0 4h1v1h-1zM2 4h3v1h-3zM6 4h1v1h-1zM8 4h1v1h-1zM10 4h1v1h-1zM13 4h3v1h-3zM17 4h4v1h-4zM22 4h1v1h-1zM24 4h3v1h-3zM28 4h1v1h-1zM0 5h1v1h-1zM6 5h1v1h-1zM9 5h1v1h-1zM11 5h1v1h-1zM14 5h2v1h-2zM17 5h1v1h-1zM19 5h1v1h-1zM22 5h1v1h-1zM28 5h1v1h-1zM0 6h7v1h-7zM8 6h1v1h-1zM10 6h1v1h-1zM12 6h1v1h-1zM14 6h1v1h-1zM16 6h1v1h-1zM18 6h1v1h-1zM20 6h1v1h-1zM22 6h7v1h-7zM9 7h2v1h-2zM12 7h4v1h-4zM17 7h4v1h-4zM0 8h1v1h-1zM2 8h1v1h-1zM4 8h1v1h-1zM6 8h1v1h-1zM9 8h1v1h-1zM11 8h1v1h-1zM13 8h1v1h-1zM17 8h1v1h-1zM24 8h1v1h-1zM27 8h1v1h-1zM2 9h2v1h-2zM8 9h2v1h-2zM13 9h2v1h-2zM16 9h3v1h-3zM20 9h3v1h-3zM25 9h1v1h-1zM28 9h1v1h-1zM1 10h3v1h-3zM5 10h2v1h-2zM9 10h1v1h-1zM11 10h4v1h-4zM21 10h2v1h-2zM24 10h1v1h-1zM26 10h3v1h-3zM1 11h2v1h-2zM7 11h1v1h-1zM9 11h2v1h-2zM13 11h3v1h-3zM17 11h1v1h-1zM19 11h2v1h-2zM23 11h1v1h-1zM27 11h1v1h-1zM1 12h4v1h-4zM6 12h3v1h-3zM10 12h2v1h-2zM13 12h2v1h-2zM16 12h2v1h-2zM20 12h4v1h-4zM25 12h1v1h-1zM27 12h2v1h-2zM2 13h1v1h-1zM4 13h1v1h-1zM11 13h2v1h-2zM16 13h1v1h-1zM20 13h1v1h-1zM22 13h1v1h-1zM25 13h1v1h-1zM28 13h1v1h-1zM1 14h7v1h-7zM12 14h2v1h-2zM16 14h1v1h-1zM18 14h1v1h-1zM22 14h2v1h-2zM25 14h1v1h-1zM27 14h2v1h-2zM1 15h3v1h-3zM10 15h1v1h-1zM13 15h2v1h-2zM16 15h2v1h-2zM20 15h4v1h-4zM25 15h1v1h-1zM27 15h1v1h-1zM2 16h2v1h-2zM6 16h1v1h-1zM9 16h3v1h-3zM14 16h1v1h-1zM17 16h1v1h-1zM19 16h5v1h-5zM25 16h1v1h-1zM27 16h2v1h-2zM4 17h2v1h-2zM7 17h1v1h-1zM9 17h3v1h-3zM16 17h3v1h-3zM20 17h3v1h-3zM25 17h2v1h-2zM28 17h1v1h-1zM0 18h1v1h-1zM6 18h6v1h-6zM13 18h1v1h-1zM20 18h1v1h-1zM22 18h3v1h-3zM27 18h2v1h-2zM1 19h5v1h-5zM7 19h1v1h-1zM16 19h3v1h-3zM20 19h1v1h-1zM23 19h1v1h-1zM25 19h1v1h-1zM27 19h1v1h-1zM0 20h1v1h-1zM2 20h11v1h-11zM15 20h3v1h-3zM20 20h5v1h-5zM8 21h2v1h-2zM13 21h1v1h-1zM17 21h1v1h-1zM20 21h1v1h-1zM24 21h1v1h-1zM26 21h3v1h-3zM0 22h7v1h-7zM9 22h2v1h-2zM12 22h1v1h-1zM14 22h1v1h-1zM17 22h4v1h-4zM22 22h1v1h-1zM24 22h2v1h-2zM27 22h2v1h-2zM0 23h1v1h-1zM6 23h1v1h-1zM11 23h1v1h-1zM13 23h2v1h-2zM16 23h2v1h-2zM20 23h1v1h-1zM24 23h2v1h-2zM27 23h1v1h-1zM0 24h1v1h-1zM2 24h3v1h-3zM6 24h1v1h-1zM8 24h5v1h-5zM14 24h5v1h-5zM20 24h5v1h-5zM0 25h1v1h-1zM2 25h3v1h-3zM6 25h1v1h-1zM11 25h1v1h-1zM14 25h1v1h-1zM16 25h1v1h-1zM19 25h1v1h-1zM23 25h2v1h-2zM26 25h3v1h-3zM0 26h1v1h-1zM2 26h3v1h-3zM6 26h1v1h-1zM8 26h1v1h-1zM10 26h5v1h-5zM17 26h1v1h-1zM19 26h1v1h-1zM23 26h3v1h-3zM28 26h1v1h-1zM0 27h1v1h-1zM6 27h1v1h-1zM10 27h1v1h-1zM14 27h2v1h-2zM17 27h1v1h-1zM19 27h2v1h-2zM23 27h2v1h-2zM27 27h1v1h-1zM0 28h7v1h-7zM8 28h2v1h-2zM12 28h2v1h-2zM15 28h3v1h-3zM19 28h4v1h-4zM27 28h2v1h-2z',
};

// ---------------------------------------------------------------------------
// B6: the install. The phone shows the installer's first step as it is today
// (internal/web/templates/legacy/pages/setup.html: "Where should VaporOS go?",
// steps Drive, Name, Games, Install).

export const setupStory = {
  tvLabel: 'The screen connected to the PC shows the installer’s address, a QR code and the setup code; the phone shows the installer.',
  caption: 'The screen on the PC shows the address, a QR code and the setup code. Your phone does the rest.',
  headless: 'No monitor? The installer skips the code: open `vaporos-setup.local` on your phone.' as Rich,
  phone: {
    host: 'vaporos-setup.local',
    step: '1 of 4',
    steps: 4,
    title: 'Where should VaporOS go?',
    hint: "Pick the drive to install on. The USB stick you started from isn't listed.",
    drives: [
      { name: 'NVMe SSD · 1 TB', meta: 'nvme0n1 · empty', selected: true },
      { name: 'SATA SSD · 2 TB', meta: 'sda · NTFS', tag: 'Steam library' },
    ],
    next: 'Next',
  },
};

// ---------------------------------------------------------------------------
// B5: updates. Two slots play an update, and a bad one. The versions are
// made up, in the release format (vYYYYMMDD.HHMMSS).

export const updatesStory = {
  signed: {
    strong: 'Signed and checked.',
    text: 'Every update carries an ed25519 signature, checked before anything is written, and each file is verified before the new version can start. The system itself is read-only.',
  },
  play: 'Play an update',
  playBad: 'Play a bad update',
  slotsLabel: 'The two copies of the system',
  versions: { previous: '20260927.190212', running: '20260929.172712', next: '20261006.081544' },
  roles: { running: 'Running', previous: 'Previous', next: 'Next', failed: 'Didn’t start' },
  status: {
    running: 'Running since yesterday',
    kept: 'Kept as it was',
    downloading: 'Downloading',
    checked: 'Checked · ready',
    started: 'Started cleanly',
    failed: 'Failed to start',
    back: 'Back by itself',
  },
  /** What the log says at each step; {running}, {previous} and {next} are the versions, set in bold. */
  log: {
    rest: 'Slot A runs {running}. Slot B keeps {previous}.',
    downloading: 'Downloading {next} into slot B. A keeps running.',
    restart: 'Restart. The PC starts {next} from slot B.',
    started: 'Running {next} · slot B. Slot A keeps {running} as the fallback.',
    failed: '{next} didn’t start cleanly…',
    back: 'Back on {running} · slot A, by itself. VaporOS won’t install {next} again.',
  },
  more: { label: 'What if an update breaks something?', href: `${routes.faq}#rollback` } satisfies LinkItem,
};

// ---------------------------------------------------------------------------
// B5: power. One evening as heat: asleep until Moonlight wakes the PC, a
// stream, then idle power-off 15 minutes after the last player left.

export interface EveningSpan {
  from: number;
  to: number;
  state: string;
}

export const evening = {
  label:
    'One evening as heat: asleep until Moonlight wakes the PC at 19:10, streaming to the TV from 19:25 to 22:05, then idle power-off at 22:20.',
  /** Hours on the axis. */
  start: 18,
  end: 24,
  axis: ['18:00', '19:00', '20:00', '21:00', '22:00', '23:00', '24:00'],
  /** Events above (up) and below (down) the ribbon; `at` in hours. */
  up: [
    { at: 19.17, time: '19:10', text: 'Moonlight wakes it' },
    { at: 22.33, time: '22:20', text: 'Idle power-off' },
  ],
  down: [
    { at: 19.42, time: '19:25', text: 'Streaming to the TV' },
    { at: 22.08, time: '22:05', text: 'Nobody playing' },
  ],
  spans: [
    { from: 18.0, to: 19.17, state: 'Asleep' },
    { from: 19.17, to: 19.42, state: 'Ready to stream' },
    { from: 19.42, to: 22.08, state: 'Streaming to Living room TV · 3840 × 2160 · 60 Hz · HDR' },
    { from: 22.08, to: 22.33, state: 'Ready · nobody playing' },
    { from: 22.33, to: 24.0, state: 'Asleep · powered off after 15 minutes without anyone playing' },
  ] satisfies EveningSpan[],
  /** Where the readout rests without scrolling (and without JavaScript). */
  rest: 20.67,
};

/** The three ways it comes back and stays on (faq.ts power and wake). */
export const powerFacts = [
  {
    title: 'Moonlight wakes it',
    body: 'In Moonlight, pick the PC and choose **Wake**. Any Wake-on-LAN app works too, with the MAC address shown under Power.' as Rich,
  },
  {
    title: 'Never mid-game',
    body: 'It never powers off while you stream, play, download or update.' as Rich,
  },
  {
    title: 'Stay awake',
    // A Steam download already keeps it on (internal/power/power.go busyReason).
    body: 'Keep it on for 1 or 4 hours whenever you want it on anyway, or turn idle power-off off under Power.' as Rich,
  },
];

export const powerMore = { label: 'Can I wake it from my phone?', href: `${routes.faq}#wake` } satisfies LinkItem;

// ---------------------------------------------------------------------------
// B7: download. The release comes from the build (release.ts) and the
// browser (use-latest-release.ts).

export const getStory = {
  /** The ISO button: its label, then the file type and size (formatSize) as a tag. */
  button: 'Download VaporOS',
  fileType: 'ISO',
  released: 'Released',
  allReleases: { label: 'All releases', href: links.releases } satisfies LinkItem,
};
