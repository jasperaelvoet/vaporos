// "How it works": the PC → network → Moonlight diagram and three steps.
// Ported from components/HowItWorks.astro.
import { anchors } from './site';
import type { IconName, Item, SectionHead } from './types';

export interface DiagramNode {
  icon: IconName;
  title: string;
  detail: string;
}

export interface HowItWorks {
  /** The whole diagram in words: its accessible description. */
  diagramCaption: string;
  /** Left half: what happens on the PC, left to right. */
  pc: DiagramNode & { pipeline: DiagramNode[]; monitorNote: string };
  /** The link between the two halves. */
  network: { icon: IconName; label: string };
  /** Right half: where you play. */
  client: DiagramNode & { devices: DiagramNode[] };
  steps: Item[];
}

export const howItWorksSection: SectionHead = {
  id: anchors.how,
  eyebrow: 'How it works',
  title: { text: 'Steam runs on the PC.', accent: 'You play wherever you are.' },
  lead: 'Steam runs on a virtual display that nobody has to plug in. Sunshine captures it and encodes it on the GPU, and Moonlight plays it on the screen in front of you.',
};

export const howItWorks: HowItWorks = {
  diagramCaption:
    'The VaporOS PC runs Steam in gamescope on a virtual display. Sunshine captures and encodes it and sends it over your home network to Moonlight on a TV, phone or laptop. A monitor on the PC, if there is one, shows only the welcome screen with its address and a QR code.',
  pc: {
    icon: 'cpu',
    title: 'Your PC',
    detail: 'VaporOS · AMD GPU',
    pipeline: [
      { icon: 'gamepad', title: 'Steam', detail: 'in gamescope' },
      { icon: 'hdr', title: 'Virtual display', detail: "your device's mode" },
      { icon: 'zap', title: 'Sunshine', detail: 'GPU encode' },
    ],
    monitorNote:
      "A monitor is optional, even to install; it helps with the PC's firmware settings. If one is connected, it shows only the welcome screen: the address and a QR code.",
  },
  network: { icon: 'ethernet', label: 'Home network' },
  client: {
    icon: 'gamepad',
    title: 'Moonlight',
    detail: 'on the screen in front of you',
    devices: [
      { icon: 'tv', title: 'TV', detail: 'Apple TV, Google TV' },
      { icon: 'phone', title: 'Phone and tablet', detail: 'iPhone, iPad, Android' },
      { icon: 'laptop', title: 'Computer', detail: 'Windows, Mac, Linux' },
    ],
  },
  steps: [
    {
      title: 'Always ready',
      body: 'With no monitor attached, Steam and Sunshine run all the time, so a game is one tap away in Moonlight.',
      short: 'With no monitor attached, a game is one tap away in Moonlight.',
    },
    {
      title: 'Fits every screen',
      body: "When a stream starts, VaporOS switches the virtual display to that device's resolution, refresh rate and HDR.",
      short: "The virtual display switches to each device's resolution, refresh rate and HDR.",
    },
    {
      title: 'Nothing to maintain',
      body: 'The system is one read-only image. Updates arrive whole and signed, and the old version stays as a fallback.',
      short: 'One read-only image. Signed updates, with the old version as a fallback.',
    },
  ],
};
