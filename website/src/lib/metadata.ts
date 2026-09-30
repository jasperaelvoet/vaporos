// <head> metadata for every page, from src/content/site.ts.
//
// Use pageMetadata() in each page.tsx:  export const metadata = pageMetadata(pageMeta.faq);
// Next merges metadata SHALLOWLY: a page that sets `openGraph: { title }` drops
// the layout's og:image. pageMetadata() always returns the whole set.
import type { Metadata, Viewport } from 'next';
import { site } from '@/content/site';
import type { PageMeta } from '@/content/types';
import { withBase } from './base-path';

/** A share card as og:image and twitter:image (Next emits width, height and both alts). */
function card(og?: PageMeta['og']) {
  return {
    url: withBase(og?.path ?? site.ogImage.path),
    width: site.ogImage.width,
    height: site.ogImage.height,
    alt: og?.alt ?? site.ogImage.alt,
  };
}
const ogImage = card();

/** For the root layout. metadataBase makes '/vaporos/…' URLs absolute. */
export const rootMetadata: Metadata = {
  metadataBase: new URL(site.origin),
  title: { default: site.title, template: site.titleTemplate },
  description: site.description,
  applicationName: site.name,
  // metadata icons do NOT get the base path added: spell it out.
  icons: {
    // The SVG first; the PNG for browsers without SVG favicons.
    icon: [
      { url: withBase('/favicon.svg'), type: 'image/svg+xml' },
      { url: withBase('/icon-192.png'), type: 'image/png', sizes: '192x192' },
    ],
    apple: withBase('/apple-touch-icon.png'),
  },
  openGraph: { type: 'website', siteName: site.name, title: site.title, description: site.description, images: [ogImage] },
  twitter: { card: 'summary_large_image', title: site.title, description: site.description, images: [ogImage] },
  // <meta name="vaporos-commit">: the commit a deployed site was built from
  // (pages.yml sets VAPOROS_SITE_COMMIT), for checking what is live.
  ...(process.env.VAPOROS_SITE_COMMIT ? { other: { 'vaporos-commit': process.env.VAPOROS_SITE_COMMIT } } : {}),
};

export const rootViewport: Viewport = {
  themeColor: site.themeColor,
  colorScheme: 'dark',
  viewportFit: 'cover',
};

export function pageMetadata({ path, title, description, noindex, og }: PageMeta): Metadata {
  const fullTitle = title ? site.titleTemplate.replace('%s', title) : site.title;
  const url = withBase(path);
  const image = card(og);
  return {
    title: { absolute: fullTitle },
    description,
    ...(noindex ? { robots: { index: false } } : { alternates: { canonical: url } }),
    openGraph: {
      type: 'website',
      siteName: site.name,
      title: fullTitle,
      description,
      ...(noindex ? {} : { url }),
      images: [image],
    },
    twitter: { card: 'summary_large_image', title: fullTitle, description, images: [image] },
  };
}
