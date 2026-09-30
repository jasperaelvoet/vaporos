// The home page, one heat cycle from top to bottom: the hero, then each
// beat's head (the words set at the beat's temperature), the white-hot
// download block and a few questions. The beats' own details (the devices,
// the A/B play, the evening, the TV and the phone) live in story.ts; the
// facts they draw on stay in features.ts, how-it-works.ts, install.ts and
// faq.ts.
//
// Headline lines: `lines` is the set from 901 px up (each kept on one
// line), `narrow` the set below. `fitK`/`fitKNarrow` are the fits the
// Headline measured at its hottest width (its data-fit-k), so the first
// paint is already fitted; re-measure them when a line changes.
import { anchors, routes } from './site';
import type { ButtonItem, IconName, Rich } from './types';

export interface BeatHead {
  /** In-page anchor. */
  id: string;
  /** The heat scale's reading while the beat is in view (0..1) and its label. */
  heat: number;
  heatLabel: string;
  title: {
    lines: string[];
    narrow?: string[];
    fitK?: number;
    fitKNarrow?: number;
  };
  lead?: Rich;
}

export const hero = {
  /** The plain line above the headline: what this is. */
  kicker: 'A Steam streaming OS for your gaming PC',
  title: {
    text: 'Leave the heat in the other room.',
    lines: ['Leave the heat', 'in the other room.'],
    narrow: ['Leave', 'the heat in', 'the other room.'],
    fitK: 13.032,
    fitKNarrow: 9.928,
  },
  lead: 'VaporOS turns a PC with an AMD GPU into a headless Steam box. It runs hot where it stands while you play on your TV, phone or laptop with Moonlight. You set it up and run it from your phone. No terminal, ever.' as Rich,
  leadShort: 'A headless Steam box for a PC with an AMD GPU. Play on your TV, phone or laptop with Moonlight, and run it all from your phone.' as Rich,
  ctas: [
    { label: 'Download VaporOS', href: `#${anchors.get}`, icon: 'download', primary: true },
    { label: 'How it works', href: `#${anchors.how}` },
  ] satisfies ButtonItem[],
  /** aria-label of the facts list. */
  factsLabel: 'At a glance',
  facts: [
    { icon: 'layers', label: 'Built on CachyOS' },
    { icon: 'shield', label: 'Signed A/B updates' },
    { icon: 'moon', label: 'Wakes from Moonlight' },
  ] satisfies { icon: IconName; label: string }[],
};

/** B2: the virtual display takes each device's mode. The hero's "How it works" lands here. */
export const screenBeat: BeatHead = {
  id: anchors.how,
  heat: 0.86,
  heatLabel: 'streaming',
  title: {
    lines: ['Every screen gets', 'its own picture.'],
    fitK: 14.554,
  },
  lead: "**No dummy plug.** When Moonlight starts a stream, VaporOS switches its virtual display to that device's resolution, refresh rate and HDR. The TV gets 4K HDR; your phone gets its own odd shape." as Rich,
};

/** B3 and B4: run it from your phone; a monitor only ever shows where to go. */
export const remoteBeat: BeatHead = {
  id: 'phone',
  heat: 0.42,
  heatLabel: 'ready',
  title: {
    lines: ['Your console,', 'in your pocket.'],
    fitK: 5.846,
  },
  lead: 'Open `vapor.local` on your phone and everything is there. The screen on the PC, if you have one, just tells you where to go.' as Rich,
};

/** B6: the install, in three steps. */
export const setupBeat: BeatHead = {
  id: 'setup',
  heat: 0.18,
  heatLabel: 'cold boot',
  title: {
    lines: ['Three steps.', 'The last one is', 'on your phone.'],
    fitK: 6.119,
  },
  lead: "Once the PC boots from the USB stick, the rest happens in your phone's browser. The PC itself only shows where to go.",
};

/** B5, first chapter: updates that undo themselves. */
export const updatesBeat: BeatHead = {
  id: 'updates',
  heat: 0.55,
  heatLabel: 'updating',
  title: {
    lines: ['Updates that', 'undo themselves.'],
    fitK: 7.412,
  },
  lead: "VaporOS keeps two copies of the system. A new version goes to the copy you aren't running and starts on the next restart. **If it doesn't start cleanly, the PC goes back to the previous version by itself**, and won't install that version again." as Rich,
};

/** B5, second chapter: idle power-off and Wake-on-LAN. */
export const powerBeat: BeatHead = {
  id: 'power',
  heat: 0.06,
  heatLabel: 'asleep',
  title: {
    lines: ['Off when nobody plays.', 'On when you do.'],
    fitK: 5.694,
  },
  // Idle power-off is off by default; a new install turns it on only when a
  // wired network adapter wakes on a magic packet (internal/config/config.go
  // Defaults, internal/install/target.go writeConfig). Busy means a stream, a
  // game, a Steam download or an update (internal/power/power.go busyReason).
  lead: "When a wired network adapter can wake the PC, VaporOS powers off after 15 minutes of nobody playing. Never while you stream, play, download or update. **In Moonlight, pick the PC and choose Wake**, and it's back." as Rich,
};

/** B7: the white-hot download block. The hero's Download lands here. */
export const getBeat = {
  id: anchors.get,
  heat: 1,
  heatLabel: 'white-hot',
  title: {
    lines: ['Turn it into a', 'console tonight.'],
    narrow: ['Turn it', 'into a console', 'tonight.'],
    fitK: 12.58,
    fitKNarrow: 9.913,
  },
  /** downloadPage.lead plus downloadPage.channelsShort, in one breath. */
  lead: 'One ISO. Write it to a USB stick, boot the PC from it, and finish the setup on your phone. An installed PC updates itself, so you only download this once.' as Rich,
  /** The chip before the requirements line. */
  needs: 'Needs',
  requirementsLabel: 'Requirements',
  verify: { label: 'Verify your download', href: routes.verify },
  requirementsLink: { label: 'All the requirements', href: routes.requirements },
};

/** B8: a few questions. */
export const faqTeaser = {
  id: 'questions',
  heat: 0.42,
  heatLabel: 'ready',
  title: { lines: ['Good', 'to know'], fitK: 4.359 },
  /** Which faq.ts entries, in this order. */
  ids: ['gpu', 'monitor', 'headless', 'rollback', 'wake', 'dual-boot'],
  more: { label: 'All questions', href: routes.faq, icon: 'arrow' as IconName },
};
