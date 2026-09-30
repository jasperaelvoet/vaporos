// out/sitemap.xml (served at /vaporos/sitemap.xml), as the Astro site had.
import type { MetadataRoute } from 'next';
import { demo } from '@/content/demo';
import { routes } from '@/content/site';
import { absoluteUrl } from '@/lib/base-path';

export const dynamic = 'force-static';

export default function sitemap(): MetadataRoute.Sitemap {
  // The live demo's page, while content/demo.ts has it on (its control center, /demo/ui/, is never listed).
  const pages = [routes.home, routes.download, routes.install, routes.faq, ...(demo.enabled ? [routes.demo] : [])];
  return pages.map((p) => ({ url: absoluteUrl(p) }));
}
