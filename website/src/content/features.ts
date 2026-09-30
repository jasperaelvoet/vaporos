// "What you get": the feature cards. Ported from components/FeatureGrid.astro.
import { ccCopy } from './demo';
import type { IconName, Item, SectionHead } from './types';

export interface Feature extends Item {
  id: string;
  icon: IconName;
  /** The Astro bento grid gave these cards two columns. */
  wide?: boolean;
  /** Decorative chips on the card: the kinds of screen Moonlight runs on. */
  devices?: { icon: IconName; label: string }[];
  /** Decorative example display modes; `active` was highlighted. */
  modes?: { size: string; rate: string; hdr?: boolean; active?: boolean }[];
}

export const featuresSection: SectionHead = {
  eyebrow: 'What you get',
  title: { text: 'A console experience,', accent: 'from the PC you already own.' },
  lead: 'VaporOS is an immutable, CachyOS-based appliance. It does one job: it keeps Steam ready to stream, and it stays out of your way.',
};

export const features: Feature[] = [
  {
    id: 'stream',
    icon: 'gamepad',
    title: 'Stream to any screen',
    body: 'Play your Steam library on a TV, phone, tablet or laptop with Moonlight. Sunshine runs on the PC, already set up for you.',
    short: 'Your Steam library on a TV, phone, tablet or laptop, with Moonlight.',
    wide: true,
    devices: [
      { icon: 'tv', label: 'TV' },
      { icon: 'phone', label: 'Phone' },
      { icon: 'laptop', label: 'Laptop' },
      { icon: 'monitor', label: 'Desktop' },
    ],
  },
  {
    id: 'display',
    icon: 'hdr',
    title: 'The display follows your device',
    body: "No dummy plug. A virtual display takes each Moonlight client's resolution, frame rate and HDR, so every screen gets its own picture.",
    short: "No dummy plug: a virtual display takes each device's resolution, frame rate and HDR.",
    wide: true,
    modes: [
      { size: '2796x1290', rate: '@120' },
      { size: '3840x2160', rate: '@60', hdr: true, active: true },
      { size: '1920x1080', rate: '@60' },
    ],
  },
  {
    id: 'phone',
    icon: 'phone',
    title: 'Run it from your phone',
    body: 'Install from your phone at `vaporos-setup.local`, then pair, update and power off at `vapor.local`. The PC never shows a terminal.',
    short: 'Install at `vaporos-setup.local`; pair, update and power off at `vapor.local`.',
  },
  {
    id: 'rollback',
    icon: 'rollback',
    title: 'Updates that undo themselves',
    body: "A new version goes to the slot you aren't running. If it doesn't start cleanly, VaporOS goes back to the previous one by itself.",
    short: "If a new version doesn't start cleanly, VaporOS goes back to the previous one by itself.",
  },
  {
    id: 'signed',
    icon: 'shield',
    title: 'Signed, checked, read-only',
    body: 'Every update carries an ed25519 signature, checked before anything is written, and each file is verified before the new version can start. The system itself is read-only.',
    short: 'Every update is ed25519-signed and every file verified. The system is read-only.',
  },
  {
    id: 'cachyos',
    icon: 'zap',
    title: 'Tuned by CachyOS',
    body: "The CachyOS kernel and x86-64-v3 packages, with CachyOS's zram swap, I/O scheduler rules and process priorities built in.",
    short: 'The CachyOS kernel and x86-64-v3 packages.',
  },
  {
    id: 'libraries',
    icon: 'drive',
    title: 'Bring your Steam libraries',
    body: ccCopy({
      legacy: "Already have games on another drive? Pick it during setup, or later under Storage, and keep playing. VaporOS mounts it and adds it to Steam's library list; nothing on it is changed.",
      next: "Already have games on another drive? Pick it during setup, or later under [[System]] › [[Storage]], and keep playing. VaporOS mounts it and adds it to Steam's library list; nothing on it is changed.",
    }),
    short: 'Keep playing the games on your other drives: VaporOS adds them to Steam. Nothing on them is changed.',
    wide: true,
  },
  {
    // Idle power-off is off by default; a new install turns it on only when a
    // wired network adapter wakes on a magic packet, and a repair keeps the old
    // setting (internal/config/config.go Defaults, internal/install/target.go
    // writeConfig). Busy means a stream, a game, a Steam download or an update
    // (internal/power/power.go busyReason, internal/daemon/daemon.go).
    id: 'power',
    icon: 'moon',
    title: 'Off when idle, awake on demand',
    body: "When the PC's wired network adapter supports Wake-on-LAN, VaporOS powers off after 15 minutes of nobody playing, and Moonlight wakes it again (Wake-on-LAN may need turning on in the firmware). It never powers off while you stream, play, download or update. On a PC that can't be woken that way, it stays on until you turn this on.",
    short: 'Powers off when idle, if Moonlight can wake it again.',
    wide: true,
  },
];
