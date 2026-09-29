// Build-time lookup of the latest VaporOS release, for server components.
// Any failure (no release yet, rate limit, offline build) yields null and the
// site shows its "first release coming soon" state instead of a broken link.
// The browser re-checks later (use-latest-release.ts), because a new release
// doesn't rebuild the site.
//
//   GITHUB_TOKEN=…                    authenticated request (pages.yml sets it)
//   VAPOROS_RELEASE_FIXTURE=path.json read a saved API answer instead: a single
//                                     release (/releases/latest) or a list
//                                     (/releases). A path that doesn't parse,
//                                     e.g. /dev/null, gives the no-release state.
import 'server-only';
import { readFile } from 'node:fs/promises';
import { LATEST_API, latestFromList, parseRelease, type Release } from './release-shape';

// One lookup per build process (Next may render pages in a few workers).
let cached: Promise<Release | null> | undefined;

async function load(): Promise<Release | null> {
  const fixture = process.env.VAPOROS_RELEASE_FIXTURE;
  try {
    if (fixture) {
      const json: unknown = JSON.parse(await readFile(fixture, 'utf8'));
      const r = parseRelease(Array.isArray(json) ? latestFromList(json) : json);
      console.info(`release: ${r ? r.version : 'no release with an ISO'} (fixture ${fixture})`);
      return r;
    }
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
    const r = parseRelease(await res.json());
    console.info(`release: ${r ? r.version : 'latest release has no ISO; showing the no-release state'}`);
    return r;
  } catch (err) {
    console.warn(`release: ${fixture ?? LATEST_API}: ${(err as Error).message}; showing the no-release state`);
    return null;
  }
}

export function getLatestRelease(): Promise<Release | null> {
  cached ??= load();
  return cached;
}
