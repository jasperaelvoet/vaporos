// The home page: hero, and the heads of the sections that pull in other
// modules (features.ts, how-it-works.ts, install.ts, download.ts, faq.ts).
// Ported from the Astro site's pages/index.astro and components/Hero.astro.
import { anchors, routes } from './site';
import type { ButtonItem, IconName, Rich, SectionHead } from './types';

export const hero = {
  badge: 'Steam streaming appliance',
  kicker: 'for PCs with an AMD GPU',
  title: { text: 'Your gaming PC,', accent: 'as a streaming console.' },
  lead: "VaporOS turns a PC into a headless Steam box. Play on your TV, phone or laptop with Moonlight, at each device's own resolution, frame rate and HDR. You set it up and run it from your phone. No terminal, ever." as Rich,
  leadShort: 'A headless Steam box you run from your phone. Play on your TV, phone or laptop with Moonlight.' as Rich,
  ctas: [
    { label: 'Download VaporOS', href: routes.download, icon: 'download', primary: true },
    { label: 'How it works', href: `#${anchors.how}`, icon: 'arrow' },
  ] satisfies ButtonItem[],
  /** aria-label of the facts list. */
  factsLabel: 'At a glance',
  facts: [
    { icon: 'layers', label: 'Built on CachyOS' },
    { icon: 'shield', label: 'Signed A/B updates' },
    { icon: 'moon', label: 'Wakes from Moonlight' },
  ] satisfies { icon: IconName; label: string }[],
  /**
   * Status chips floating around the hero art. Decorative examples of what
   * the product shows (a Moonlight session, the web UI), not claims about a
   * particular setup.
   */
  chips: [
    { title: 'Streaming to Living room TV', detail: '3840x2160@60 HDR', live: true },
    { title: 'vapor.local', detail: 'Ready to stream', icon: 'phone' as IconName },
  ],
};

/** "Setup": the three-step strip (install.ts → installStrip) on the home page. */
export const setupSection: SectionHead = {
  eyebrow: 'Setup',
  title: { text: 'Three steps.', accent: "Then it's all phone." },
  lead: "Once the PC boots from the USB stick, the rest happens in your phone's browser. The PC itself only shows where to go.",
};

/** "Control center": what the web UI at vapor.local does. */
export const controlCenter = {
  eyebrow: 'Control center',
  title: { text: 'Your console,', accent: 'in your pocket.' },
  lead: 'Open `vapor.local` on your phone and everything is there. The screen on the PC, if you have one, just tells you where to go.' as Rich,
  /** `text` is the full line as the Astro site set it; `label` + `detail` split it for layouts that need to. */
  items: [
    { icon: 'phone', label: 'Pair', detail: 'Moonlight devices with a 4-digit PIN.', text: '**Pair** Moonlight devices with a 4-digit PIN.' },
    { icon: 'hdr', label: 'Display', detail: 'Modes and HDR, learned from each device.', text: '**Display** modes and HDR, learned from each device.' },
    { icon: 'drive', label: 'Storage', detail: 'For game drives you already have.', text: '**Storage** for game drives you already have.' },
    { icon: 'update', label: 'Updates', detail: 'And one-tap rollback.', text: '**Updates** and one-tap rollback.' },
    { icon: 'power', label: 'Power', detail: 'Idle shutdown, stay awake and Wake-on-LAN.', text: '**Power**: idle shutdown, stay awake and Wake-on-LAN.' },
  ] satisfies { icon: IconName; label: string; detail: string; text: Rich }[],
  /** Caption under the device illustration (see mock.ts). */
  mockNote: 'Illustration of the VaporOS web UI and the welcome screen on a connected monitor.',
};

/** "Download": the compact download card on the home page. */
export const getSection: SectionHead = {
  id: anchors.get,
  eyebrow: 'Download',
  title: { text: 'Turn it into a', accent: 'console tonight.' },
};

/** "Questions": a few FAQ entries on the home page. */
export const faqTeaser = {
  eyebrow: 'Questions',
  title: { text: 'Good to know' },
  /** Which faq.ts entries, in this order. */
  ids: ['gpu', 'monitor', 'dual-boot', 'rollback'],
  more: { label: 'All questions', href: routes.faq, icon: 'arrow' as IconName },
};
