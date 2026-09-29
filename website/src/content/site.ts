// Site identity, URLs, per-page meta, navigation and footer.
// Ported from the Astro site's layouts/Base.astro and lib/paths.ts.
import { RELEASES_URL, REPO_URL } from '@/lib/release-shape';
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
  /** public/og.png: the Astro site's share card, 1200x630. Replace it if the look changes. */
  ogImage: { path: '/og.png', width: 1200, height: 630, alt: 'VaporOS: your gaming PC, as a streaming console.' },
  themeColor: '#060a0f',
  /** The wordmark is Vapor + OS, with OS in the accent color, as the product draws it. */
  wordmark: { text: 'Vapor', accent: 'OS' },
} as const;

/** External places. */
export const links = {
  repo: REPO_URL,
  releases: RELEASES_URL,
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
} as const;

export const pageMeta = {
  home: { path: routes.home, title: null, description: site.description },
  download: {
    path: routes.download,
    title: 'Download',
    description:
      'Download the latest VaporOS ISO, check the requirements, and verify the download with SHA256SUMS and the release signature.',
  },
  install: {
    path: routes.install,
    title: 'Install guide',
    description:
      'Install VaporOS step by step: write the USB stick, set the firmware, run the web installer from your phone and pair Moonlight.',
  },
  faq: {
    path: routes.faq,
    title: 'FAQ',
    description:
      'Answers about VaporOS: supported graphics cards, monitors, dual boot, Secure Boot, updates, rollback and power.',
  },
  // GitHub Pages serves the 404 page at any missing path: no canonical, noindex.
  notFound: { path: '/404/', title: 'Page not found', description: "This page doesn't exist.", noindex: true },
} satisfies Record<string, PageMeta>;

export type NavKey = 'home' | 'download' | 'install' | 'faq';

export const nav = {
  skipLink: 'Skip to content',
  /** aria-label of the logo link. */
  homeLabel: 'VaporOS home',
  /** aria-label of the mobile menu button. */
  menuLabel: 'Menu',
  /** aria-labels of the two navs. */
  mainLabel: 'Main',
  mobileLabel: 'Main menu',
  /** The page links. On wide screens the Astro site showed Download as the CTA button instead of a link. */
  items: [
    { key: 'download', label: 'Download', href: routes.download },
    { key: 'install', label: 'Install guide', href: routes.install },
    { key: 'faq', label: 'FAQ', href: routes.faq },
  ] satisfies (LinkItem & { key: NavKey })[],
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
  license: 'License: TBD',
  disclaimer: 'Not affiliated with Valve, Moonlight or Sunshine. Steam is a trademark of Valve Corporation.',
} as const;
