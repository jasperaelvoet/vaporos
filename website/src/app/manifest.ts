// out/manifest.webmanifest (Next links it from every page's <head>): the name,
// colours and icons a browser uses when the site is saved to a home screen.
// The icons are generated from design/logo.svg (internal/brand site-icons);
// URLs carry the base path, which metadata routes don't add.
import type { MetadataRoute } from 'next';
import { site } from '@/content/site';
import { withBase } from '@/lib/base-path';

export const dynamic = 'force-static';

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: site.name,
    short_name: site.name,
    description: site.description,
    start_url: withBase('/'),
    scope: withBase('/'),
    display: 'browser',
    background_color: site.themeColor,
    theme_color: site.themeColor,
    icons: [
      { src: withBase('/icon-192.png'), sizes: '192x192', type: 'image/png' },
      { src: withBase('/icon-512.png'), sizes: '512x512', type: 'image/png' },
      { src: withBase('/icon-maskable-512.png'), sizes: '512x512', type: 'image/png', purpose: 'maskable' },
    ],
  };
}
