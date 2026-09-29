// Build-time lookup of the latest VaporOS release. Any failure (no release
// yet, rate limit, offline build) yields null and the site shows its
// "first release coming soon" state instead of a broken link.
import { readFile } from 'node:fs/promises';
import { LATEST_API, parseRelease, type Release } from './release-shape';

let cached: Promise<Release | null> | undefined;

async function load(): Promise<Release | null> {
  const fixture = process.env.VAPOROS_RELEASE_FIXTURE;
  try {
    if (fixture) return parseRelease(JSON.parse(await readFile(fixture, 'utf8')));
    const headers: Record<string, string> = {
      Accept: 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28',
      'User-Agent': 'vaporos-website',
    };
    if (process.env.GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`;
    const res = await fetch(LATEST_API, { headers, signal: AbortSignal.timeout(10_000) });
    if (!res.ok) {
      console.warn(`release: ${LATEST_API} answered ${res.status}; showing the no-release state`);
      return null;
    }
    return parseRelease(await res.json());
  } catch (err) {
    console.warn(`release: ${fixture ?? LATEST_API}: ${(err as Error).message}; showing the no-release state`);
    return null;
  }
}

export function getLatestRelease(): Promise<Release | null> {
  cached ??= load();
  return cached;
}
