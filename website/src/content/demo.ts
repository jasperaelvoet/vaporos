// The live demo (spec-website §8, MASTER-PLAN §3.6): the real control
// center, exported by TestExportDemo (internal/web/export_test.go) into
// public/demo/ui and running there on made-up data, and every word the
// website puts around it.
//
// `enabled` turns it on. pages.yml then exports the demo before the build
// (VAPOROS_DEMO_REQUIRED=1 fails a build without it), the /demo/ page is
// built, the nav, the footer, the 404 page and the sitemap link to it, the
// home page gets its beat and its hero button, and the TV stills' QR codes
// open it. Off, nothing links to it and scripts/drop-demo.mjs removes the
// /demo/ page from the export, so turning it back off is this one line.
//
// `uiLabels` is the control center the site describes: 'legacy' (the eight
// pages) or 'next' (the four tabs: Home, Devices, Screen, System). The copy
// that names the control center follows it through ccCopy(), and the
// [[label]] check reads tests/fixtures/ui-strings.<uiLabels>.json.
//
// pages.yml and scripts/drop-demo.mjs import this file with Node's own
// TypeScript support, so it stays plain data with type-only imports.
//
// Import from '@/content/demo' (not re-exported by '@/content').
import type { BeatHead } from './home';

export type UiLabels = 'legacy' | 'next';

export const demo = {
  enabled: false,
  uiLabels: 'legacy' as UiLabels,
};

/**
 * The words for the control center the site describes (`demo.uiLabels`).
 * Only the chosen set is exported, so the [[label]] check sees only the
 * labels that are on the site.
 */
export function ccCopy<T>(v: Record<UiLabels, T>): T {
  return v[demo.uiLabels];
}

/** The demo's pages (TestExportDemo), under the site's base path. */
export const DEMO_UI = '/demo/ui/';

/**
 * The tabs and pages `/demo/?open=<page>` may open: the control center's own
 * paths without the leading slash (demo-manifest.json `pages`, less the
 * sign-in and the installer, which the demo leaves out).
 */
export const DEMO_OPEN = ['devices', 'screen', 'system', 'system/updates', 'system/power', 'system/storage', 'system/settings', 'system/logs', 'system/about'] as const;
export type DemoOpen = (typeof DEMO_OPEN)[number];

/** /demo/: the page. */
export const demoPage = {
  headline: ['Live demo'],
  lead: 'The real VaporOS control center, running in your browser on made-up data. Nothing here touches a real PC.',
};

/** B4 on the home page: the demo, in a phone frame (the home page's #demo). */
export const demoBeat: BeatHead = {
  id: 'demo',
  heat: 0.42,
  heatLabel: 'ready',
  title: {
    lines: ['The real thing,', 'on made-up data.'],
    narrow: ['The real thing,', 'on made-up', 'data.'],
    fitK: 10.625,
    fitKNarrow: 7.868,
  },
  lead: "This is the control center itself, running in your browser with a pretend PC behind it. Start a stream, pair a Steam Deck or install an update: nothing here reaches a real PC.",
};

/** The phone-frame embed, its scenario panel and the Moonlight next to it. */
export const demoStage = {
  /** The iframe's title. */
  frameTitle: 'VaporOS control center, live demo with made-up data',
  /** Before the frame: jumps past it (the frame holds a whole page). */
  skip: 'Skip the demo',
  starting: 'Starting the demo…',
  /** No `ready` from the frame after 8 s. */
  failed: "The demo didn't start.",
  openAlone: 'Open it in its own tab',
  /** A build with the demo on but no export in public/demo/ui (local only; CI fails instead). */
  missing: 'This build has no demo in it. TestExportDemo writes it into public/demo/ui.',
  noscript: 'The live demo needs JavaScript.',

  panelTitle: 'Make something happen',
  panelLead: 'Each button plays a moment on the pretend PC. Watch the phone.',
  moreLabel: 'More',
  /** One button per scenario (demo-protocol.ts SCENARIO_IDS): its label and what it does. */
  scenarios: {
    stream: { label: 'Start a stream', help: 'The Living room TV starts streaming Steam at 3840 × 2160, 60 Hz, HDR.' },
    'stream-end': { label: 'End the stream', help: 'The Living room TV stops playing.' },
    pair: { label: 'A device wants to pair', help: 'A Steam Deck asks to pair. Moonlight shows the PIN; type it on the phone.' },
    update: { label: 'An update arrives', help: 'A newer version turns up. Download it on the phone, then restart.' },
    'power-off': { label: 'Power it off', help: 'The PC powers off. The phone notices within seconds and shows how to wake it.' },
    wake: { label: 'Wake it', help: 'Moonlight wakes the PC, and the phone reconnects by itself.' },
    reset: { label: 'Start over', help: 'Back to an idle PC, ready to stream.' },
  },

  /** The Moonlight app on the Steam Deck: where the PIN shows up. */
  moonlight: {
    label: 'Moonlight on Steam Deck',
    app: 'Moonlight',
    host: 'vapor',
    idle: 'Nothing is pairing. Press “A device wants to pair” to start.',
    waiting: 'Enter this PIN on the VaporOS page:',
    /** Where the PIN goes, on phones (the sheet is closed by then). */
    waitingShort: 'Moonlight PIN',
    paired: 'Paired. The Steam Deck can play now.',
  },

  notes: {
    title: 'What’s real, what’s made up',
    real: '**Real:** the control center’s pages, words and behaviour, the same files the PC serves.',
    fake: '**Made up:** the PC behind them. Its names, addresses and versions are invented, and nothing you type leaves this page.',
  },

  /** Polite announcements of what happened ({client}, {device}, {pin}, {text}). */
  announce: {
    streaming: 'Streaming to {client}.',
    streamEnded: 'The stream ended.',
    pairing: '{device} wants to pair. The PIN is {pin}.',
    paired: '{device} is paired.',
    updating: 'VaporOS is installing an update.',
    restart: 'VaporOS needs a restart.',
    asleep: 'VaporOS is off.',
    awake: 'VaporOS is back on.',
    notice: '{text}',
  },

  /** /demo/ on phones and touch screens: the frame fills the screen under a bar. */
  mobile: {
    title: 'Live demo',
    scenarios: 'Scenarios',
    exit: 'Exit',
    close: 'Close',
  },

  /** B4 on phones and touch screens: a card instead of the frame. */
  card: {
    title: 'Try the live demo',
    text: 'The real control center on made-up data. It opens full screen.',
    cta: 'Open the live demo',
  },
};
