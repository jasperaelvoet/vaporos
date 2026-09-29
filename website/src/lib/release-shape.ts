// The part of a GitHub release the site shows. Shared by the build-time
// fetch (release.ts) and the in-browser refresh (use-latest-release.ts), so
// both read the API the same way. Pure: safe in server and client code.

export const REPO = 'jasperaelvoet/vaporos';
export const REPO_URL = `https://github.com/${REPO}`;
export const RELEASES_URL = `${REPO_URL}/releases`;
export const LATEST_API = `https://api.github.com/repos/${REPO}/releases/latest`;
// The browser reads the release list instead: it answers 200 with [] before
// the first release, where /releases/latest answers 404, which browsers log
// as a console error on every visit. Every push to a non-main branch adds a
// prerelease, so read a full page (100) to reach the newest stable release.
// A list without one never downgrades the card (see use-latest-release.ts).
export const LIST_API = `https://api.github.com/repos/${REPO}/releases?per_page=100`;

// latestFromList picks what /releases/latest would: the newest published
// release that isn't a prerelease (the list is sorted newest first).
export function latestFromList(json: unknown): unknown {
  if (!Array.isArray(json)) return null;
  return (
    json.find(
      (r: Record<string, unknown> | null) => r && typeof r === 'object' && !r.draft && !r.prerelease,
    ) ?? null
  );
}

// Asset names come from .github/workflows/build.yml (publish job):
// vaporos-<version>.iso, SHA256SUMS, manifest.json, manifest.json.sig.
export const ISO_PATTERN = /^vaporos-.*\.iso$/;

export interface Asset {
  name: string;
  url: string;
  size: number;
}

export interface Release {
  /** '20260928.101500': the tag without its leading v. */
  version: string;
  /** 'v20260928.101500' */
  tag: string;
  /** The release page on GitHub (release notes). */
  url: string;
  /** ISO 8601 timestamp, or '' when GitHub gave none. */
  published: string;
  prerelease: boolean;
  iso: Asset;
  sums: Asset | null;
  manifest: Asset | null;
  sig: Asset | null;
}

interface ApiAsset {
  name?: unknown;
  browser_download_url?: unknown;
  size?: unknown;
}

function asset(a: ApiAsset | undefined): Asset | null {
  if (!a || typeof a.name !== 'string' || typeof a.browser_download_url !== 'string') return null;
  if (!/^https:\/\//.test(a.browser_download_url)) return null;
  return { name: a.name, url: a.browser_download_url, size: typeof a.size === 'number' ? a.size : 0 };
}

// parseRelease turns a /releases/latest response into a Release, or null
// when it has no ISO (then there is nothing to download).
export function parseRelease(json: unknown): Release | null {
  if (!json || typeof json !== 'object') return null;
  const r = json as Record<string, unknown>;
  const assets = (Array.isArray(r.assets) ? r.assets : []) as ApiAsset[];
  const byName = (test: (n: string) => boolean) =>
    asset(assets.find((a) => typeof a.name === 'string' && test(a.name)));
  const iso = byName((n) => ISO_PATTERN.test(n));
  if (!iso) return null;
  const tag = typeof r.tag_name === 'string' ? r.tag_name : '';
  const version = tag.replace(/^v/, '') || iso.name.replace(/^vaporos-|\.iso$/g, '');
  const htmlUrl = typeof r.html_url === 'string' && r.html_url.startsWith('https://') ? r.html_url : RELEASES_URL;
  return {
    version,
    tag,
    url: htmlUrl,
    published: typeof r.published_at === 'string' ? r.published_at : '',
    prerelease: r.prerelease === true,
    iso,
    sums: byName((n) => n === 'SHA256SUMS'),
    manifest: byName((n) => n === 'manifest.json'),
    sig: byName((n) => n === 'manifest.json.sig'),
  };
}

// pickNewer decides what the page shows when the browser finds a release:
// the state only ever upgrades. No live release (rate limits, outages, a page
// full of branch prereleases) keeps what the build found, and so does a live
// answer older than the build's (a stale sessionStorage entry, say).
export function pickNewer(current: Release | null, candidate: Release | null): Release | null {
  if (!candidate) return current;
  if (!current) return candidate;
  const was = Date.parse(current.published);
  const now = Date.parse(candidate.published);
  if (Number.isNaN(was) || Number.isNaN(now)) return candidate;
  return now >= was ? candidate : current;
}

/** 3221225472 → '3.2 GB'; 0 → ''. */
export function formatSize(bytes: number): string {
  if (!bytes) return '';
  const gb = bytes / 1e9;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  return `${Math.round(bytes / 1e6)} MB`;
}

/** '2026-09-28T11:02:41Z' → 'Sep 28, 2026' (UTC, so server and browser agree). */
export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric', timeZone: 'UTC' });
}
