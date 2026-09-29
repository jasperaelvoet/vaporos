// "What you need": the hardware and setup requirements.
// Ported from components/Requirements.astro. Shown on /download/ and at the
// start of the install guide.
import { anchors } from './site';
import type { IconName, Item } from './types';

export interface Requirement extends Item {
  id: string;
  icon: IconName;
  /** A two-to-four word spec-sheet label. */
  label: string;
}

export const requirementsSection = {
  id: anchors.requirements,
  eyebrow: 'Requirements',
  title: { text: 'What you need' },
};

export const requirements: Requirement[] = [
  {
    id: 'gpu',
    icon: 'hdr',
    label: 'AMD Radeon GPU',
    title: 'An AMD Radeon graphics card',
    body: "VaporOS streams with AMD GPUs on the `amdgpu` driver. NVIDIA and Intel graphics aren't supported yet.",
    short: 'AMD GPUs on the `amdgpu` driver. No NVIDIA or Intel yet.',
  },
  {
    id: 'cpu',
    icon: 'cpu',
    label: 'x86-64-v3 CPU',
    title: 'An x86-64-v3 processor',
    body: 'Intel Haswell, AMD Zen, or newer.',
  },
  {
    id: 'firmware',
    icon: 'lock',
    label: 'UEFI, Secure Boot off',
    title: 'UEFI, with Secure Boot off',
    body: "The kernel isn't signed for Secure Boot. Legacy BIOS boot isn't supported.",
    short: 'No legacy BIOS boot.',
  },
  {
    id: 'drive',
    icon: 'drive',
    label: '24.5 GiB drive',
    title: 'A drive of at least 24.5 GiB',
    body: 'About 26.3 GB; 32 GB or larger is recommended. VaporOS takes the whole drive. Steam libraries on other drives can stay and be used.',
    short: 'VaporOS takes the whole drive; 32 GB or larger is recommended.',
  },
  {
    id: 'network',
    icon: 'ethernet',
    label: 'Network cable',
    title: 'A network cable',
    body: 'Setup happens over your home network, and Wake-on-LAN needs a wired connection.',
    short: 'Wake-on-LAN needs a wired connection.',
  },
  {
    id: 'install-kit',
    icon: 'usb',
    label: 'USB stick and a screen',
    title: 'A USB stick and a screen, to install',
    body: "Everything on the stick is erased when you write the ISO. A monitor or TV shows the setup code during the install; you don't need it afterwards.",
    short: 'You only need the screen during the install.',
  },
];
