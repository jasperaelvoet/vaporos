// Base-aware links: the site lives under /vaporos on GitHub Pages.
const base = import.meta.env.BASE_URL.replace(/\/$/, '');

export function href(path = '/'): string {
  const p = path.startsWith('/') ? path : `/${path}`;
  return `${base}${p}`;
}

export const SITE_NAME = 'VaporOS';
