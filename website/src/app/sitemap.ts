// out/sitemap.xml (served at /vaporos/sitemap.xml), as the Astro site had.
import type { MetadataRoute } from 'next';
import { routes } from '@/content/site';
import { absoluteUrl } from '@/lib/base-path';

export const dynamic = 'force-static';

export default function sitemap(): MetadataRoute.Sitemap {
  return [routes.home, routes.download, routes.install, routes.faq].map((p) => ({ url: absoluteUrl(p) }));
}
