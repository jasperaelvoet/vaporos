// Base-aware URLs: the site lives under /vaporos on GitHub Pages.
//
// next/link, next/image's static imports and the metadata API add the base
// path by themselves. Everything else that the browser fetches does NOT:
// plain <a href>, <img src>, <video src>, CSS url(), fetch(), a Three.js
// texture or model path, a font URL. Run those through withBase().

/** '/vaporos', from next.config.ts. Inlined at build time. */
export const BASE_PATH: string = process.env.NEXT_PUBLIC_BASE_PATH ?? '';

/** The deployed site's origin; the site itself is at SITE_ORIGIN + BASE_PATH. */
export const SITE_ORIGIN = 'https://jasperaelvoet.github.io';

const EXTERNAL = /^(?:[a-z][a-z\d+.-]*:|\/\/|#|\?)/i;

/**
 * withBase('/release.pub') → '/vaporos/release.pub'. External URLs, hashes and
 * paths that already carry the base path are returned unchanged.
 */
export function withBase(path = '/'): string {
  if (EXTERNAL.test(path)) return path;
  const p = path.startsWith('/') ? path : `/${path}`;
  if (BASE_PATH && (p === BASE_PATH || p.startsWith(`${BASE_PATH}/`))) return p;
  return `${BASE_PATH}${p}`;
}

/** absoluteUrl('/download/') → 'https://jasperaelvoet.github.io/vaporos/download/'. */
export function absoluteUrl(path = '/'): string {
  return EXTERNAL.test(path) ? path : `${SITE_ORIGIN}${withBase(path)}`;
}

/** True for a site route ('/faq/', '/download/#verify'), false for files ('/release.pub') and external URLs. */
export function isRoute(href: string): boolean {
  if (!href.startsWith('/') || href.startsWith('//')) return false;
  const path = href.split(/[?#]/, 1)[0];
  return !/\.[a-z\d]+$/i.test(path);
}
