// Site identity, URLs, per-page meta, navigation and footer.
// Ported from the Astro site's layouts/Base.astro and lib/paths.ts.
import { RELEASES_URL, REPO_URL } from '@/lib/release-shape';
import { tokens } from '@/lib/tokens.gen';
import { demo } from './demo';
import type { IconName, LinkItem, PageMeta } from './types';

export const site = {
  name: 'VaporOS',
  /** The site is served at origin + basePath. */
  origin: 'https://jasperaelvoet.github.io',
  basePath: '/vaporos',
  url: 'https://jasperaelvoet.github.io/vaporos/',
  /** The home page's <title>; other pages use titleTemplate. */
  title: 'VaporOS: your gaming PC, as a streaming console',
  titleTemplate: '%s · VaporOS',
  description:
    'VaporOS turns a PC with an AMD GPU into a headless Steam streaming box. Play on your TV, phone or laptop with Moonlight. Set up and manage it from your phone.',
  /** One line about the product (the footer's text). */
  blurb:
    'An immutable, CachyOS-based appliance that turns a PC with an AMD GPU into a headless Steam streaming box.',
  tagline: 'Your gaming PC, as a streaming console.',
  /** The two lines under the headline on og.png. */
  ogTagline: ['Headless Steam streaming for PCs with an AMD GPU.', 'Play anywhere with Moonlight.'],
  /**
   * The home page's share card, out/og.png (1200×630, app/og.png/route.tsx),
   * and the default for pages without their own. The alt says what the card
   * says: the hero's kicker and headline.
   */
  ogImage: {
    path: '/og.png',
    width: 1200,
    height: 630,
    alt: 'VaporOS: Leave the heat in the other room. A Steam streaming OS for your gaming PC.',
  },
  /** The browser's chrome colour: the page's ash (design/tokens.json, colour role canvas). */
  themeColor: tokens.color.dark.canvas,
  /**
   * The wordmark as text (for alt text and OG images): "Vapor" in the hot cut,
   * "OS" in the cold cut, as the drawn wordmark in src/lib/logo.gen.ts.
   */
  wordmark: { text: 'Vapor', accent: 'OS' },
} as const;

/** External places. */
export const links = {
  repo: REPO_URL,
  releases: RELEASES_URL,
  /** Always the newest stable release's page on GitHub (GitHub redirects it). */
  latestRelease: `${RELEASES_URL}/latest`,
  issues: `${REPO_URL}/issues`,
  buildFromSource: `${REPO_URL}#development`,
  keyInRepo: `${REPO_URL}/blob/main/keys/release.pub`,
  moonlight: 'https://moonlight-stream.org/',
  etcher: 'https://etcher.balena.io/',
  rufus: 'https://rufus.ie/',
} as const;

/** In-page anchors other pages link to. Keep the element ids in sync. */
export const anchors = {
  /** /download/: the verify section. */
  verify: 'verify',
  /** /download/: the requirements section (the Astro site used #req-title). */
  requirements: 'requirements',
  /** /: the how-it-works section (the hero's second button). */
  how: 'how',
  /** /: the download section. */
  get: 'get',
} as const;

/** Site routes and files, without the base path (next/link and withBase() add it). */
export const routes = {
  home: '/',
  download: '/download/',
  install: '/install/',
  faq: '/faq/',
  verify: `/download/#${anchors.verify}`,
  requirements: `/download/#${anchors.requirements}`,
  releaseKey: '/release.pub',
  /** The live demo (content/demo.ts turns it on; hidden from nav, footer and sitemap until then). */
  demo: '/demo/',
} as const;

/** The live demo's link: in the nav, the menu and the footer once content/demo.ts turns it on. */
const demoLink = { key: 'demo', label: 'Live demo', href: routes.demo } satisfies LinkItem & { key: NavKey };
const withDemo = <T,>(...items: T[]): T[] => (demo.enabled ? items : []);

export const pageMeta = {
  home: { path: routes.home, title: null, description: site.description },
  download: {
    path: routes.download,
    title: 'Download',
    description:
      'Download the latest VaporOS ISO, check the requirements, and verify the download with SHA256SUMS and the release signature.',
    og: {
      path: '/download/og.png',
      alt: 'Get VaporOS. One ISO. Write it to a USB stick, boot the PC from it, and finish the setup on your phone.',
    },
  },
  install: {
    path: routes.install,
    title: 'Install guide',
    description:
      'Install VaporOS step by step: write the USB stick, set the firmware, run the web installer from your phone and pair Moonlight.',
    og: {
      path: '/install/og.png',
      alt: 'VaporOS install guide: From USB stick to first game. Flash a USB stick, boot the PC from it, finish on your phone.',
    },
  },
  faq: {
    path: routes.faq,
    title: 'FAQ',
    description:
      'Answers about VaporOS: supported graphics cards, monitors, dual boot, Secure Boot, updates, rollback and power.',
    og: {
      path: '/faq/og.png',
      alt: 'VaporOS FAQ: Questions, answered. What VaporOS runs on, what it does to your PC, and how it keeps itself up to date.',
    },
  },
  demo: {
    path: routes.demo,
    title: 'Live demo',
    description:
      'Try the VaporOS control center in your browser: start a stream, pair a device and install an update, on made-up data.',
  },
  // GitHub Pages serves the 404 page at any missing path: no canonical, noindex.
  notFound: { path: '/404/', title: 'Page not found', description: "This page doesn't exist.", noindex: true },
} satisfies Record<string, PageMeta>;

export type NavKey = 'home' | 'download' | 'install' | 'faq' | 'demo';

export const nav = {
  skipLink: 'Skip to content',
  /** aria-label of the logo link. */
  homeLabel: 'VaporOS home',
  /** aria-label of the mobile menu button. */
  menuLabel: 'Menu',
  /** aria-labels of the two navs. */
  mainLabel: 'Main',
  mobileLabel: 'Main menu',
  /**
   * The header's links from 901 px, before the Download button (always last and
   * primary). The live demo leads them when content/demo.ts turns it on.
   */
  items: [
    ...withDemo(demoLink),
    { key: 'install', label: 'Install guide', href: routes.install },
    { key: 'faq', label: 'FAQ', href: routes.faq },
    { label: 'GitHub', href: links.repo, icon: 'github' },
  ] satisfies (LinkItem & { key?: NavKey })[],
  /** The menu below 901 px (Download stays in the bar next to it). */
  menu: [
    { key: 'home', label: 'Home', href: routes.home },
    { key: 'download', label: 'Download', href: routes.download },
    ...withDemo(demoLink),
    { key: 'install', label: 'Install guide', href: routes.install },
    { key: 'faq', label: 'FAQ', href: routes.faq },
    { label: 'GitHub', href: links.repo, icon: 'github' },
  ] satisfies (LinkItem & { key?: NavKey })[],
  /** The live demo's link (in items and menu while content/demo.ts has it on). */
  demo: demoLink,
  /** First item of the mobile menu. */
  home: { key: 'home', label: 'Home', href: routes.home } satisfies LinkItem & { key: NavKey },
  github: { label: 'GitHub', href: links.repo, icon: 'github' as IconName },
  cta: { label: 'Download', href: routes.download, icon: 'download' as IconName },
} as const;

export const footer = {
  blurb: site.blurb,
  groups: [
    {
      title: 'Product',
      links: [
        { label: 'Download', href: routes.download },
        { label: 'Install guide', href: routes.install },
        ...withDemo({ label: 'Live demo', href: routes.demo }),
        { label: 'FAQ', href: routes.faq },
        { label: 'Verify a download', href: routes.verify },
      ],
    },
    {
      title: 'Project',
      links: [
        { label: 'Source on GitHub', href: links.repo },
        { label: 'Releases', href: links.releases },
        { label: 'Report an issue', href: links.issues },
        { label: 'Build from source', href: links.buildFromSource },
      ],
    },
  ] satisfies { title: string; links: LinkItem[] }[],
  /** The licence line; null until the repository has a licence (no LICENSE file yet). */
  license: null as string | null,
  disclaimer: 'Not affiliated with Valve, Moonlight or Sunshine. Steam is a trademark of Valve Corporation.',
} as const;
