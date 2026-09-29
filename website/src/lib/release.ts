// Build-time lookup of the latest VaporOS release, for server components.
// It answers in three states, so the site never claims more than it knows:
//
//   ready     a release with an ISO: the download card links to it
//   none      GitHub says there is no release (404 on /releases/latest, or an
//             empty list): the "first release coming soon" card
//   unknown   the lookup failed (network, rate limit, 5xx, a release without
//             an ISO): the card points at the releases page on GitHub instead
//             of claiming there is no release
//
// The browser re-checks later (use-latest-release.ts), because a new release
// doesn't rebuild the site.
//
//   GITHUB_TOKEN=…                    authenticated request (pages.yml sets it)
//   VAPOROS_RELEASE_REQUIRED=1        an `unknown` answer fails the build, so a
//                                     degraded card is never deployed (pages.yml)
//   VAPOROS_RELEASE_FIXTURE=path.json read a saved API answer instead: a single
//                                     release (/releases/latest) or a list
//                                     (/releases). An empty or non-JSON file
//                                     (e.g. /dev/null), [] or a "Not Found"
//                                     answer gives `none`; any other error
//                                     message gives `unknown`.
import 'server-only';
import { readFile } from 'node:fs/promises';
import { LATEST_API, latestFromList, parseRelease, type Release, type ReleaseLookup } from './release-shape';

export type { ReleaseLookup };

const NONE: ReleaseLookup = { state: 'none' };
const UNKNOWN: ReleaseLookup = { state: 'unknown' };

function ready(r: Release | null, from: string): ReleaseLookup {
  if (r) {
    console.info(`release: ${r.version} (${from})`);
    return { state: 'ready', release: r };
  }
  console.warn(`release: the latest release from ${from} has no ISO; showing the unknown state`);
  return UNKNOWN;
}

// A saved API answer: a release, a list, an error object, or nothing at all.
function fromFixture(text: string, fixture: string): ReleaseLookup {
  let json: unknown;
  try {
    json = JSON.parse(text);
  } catch {
    console.info(`release: none (fixture ${fixture} is not JSON)`);
    return NONE;
  }
  if (Array.isArray(json)) {
    const latest = latestFromList(json);
    if (!latest) {
      console.info(`release: none (fixture ${fixture} lists no stable release)`);
      return NONE;
    }
    return ready(parseRelease(latest), `fixture ${fixture}`);
  }
  const o = json as Record<string, unknown> | null;
  if (o && typeof o === 'object' && typeof o.message === 'string' && !('tag_name' in o)) {
    const none = o.message === 'Not Found';
    console.info(`release: ${none ? 'none' : 'unknown'} (fixture ${fixture}: ${o.message})`);
    return none ? NONE : UNKNOWN;
  }
  return ready(parseRelease(json), `fixture ${fixture}`);
}

async function fromApi(): Promise<ReleaseLookup> {
  const headers: Record<string, string> = {
    Accept: 'application/vnd.github+json',
    'X-GitHub-Api-Version': '2022-11-28',
    'User-Agent': 'vaporos-website',
  };
  if (process.env.GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`;
  try {
    const res = await fetch(LATEST_API, { headers, signal: AbortSignal.timeout(10_000) });
    if (res.status === 404) {
      console.info(`release: none (${LATEST_API} answered 404)`);
      return NONE;
    }
    if (!res.ok) {
      console.warn(`release: ${LATEST_API} answered ${res.status}; showing the unknown state`);
      return UNKNOWN;
    }
    return ready(parseRelease(await res.json()), LATEST_API);
  } catch (err) {
    console.warn(`release: ${LATEST_API}: ${(err as Error).message}; showing the unknown state`);
    return UNKNOWN;
  }
}

async function load(): Promise<ReleaseLookup> {
  const fixture = process.env.VAPOROS_RELEASE_FIXTURE;
  let r: ReleaseLookup;
  if (fixture) {
    let text = '';
    try {
      text = await readFile(fixture, 'utf8');
    } catch (err) {
      throw new Error(`release: VAPOROS_RELEASE_FIXTURE=${fixture} can't be read: ${(err as Error).message}`);
    }
    r = fromFixture(text, fixture);
  } else {
    r = await fromApi();
  }
  if (r.state === 'unknown' && process.env.VAPOROS_RELEASE_REQUIRED === '1') {
    throw new Error('release: the latest release could not be read and VAPOROS_RELEASE_REQUIRED=1; not building a degraded download card');
  }
  return r;
}

// One lookup per build process (Next may render pages in a few workers).
let cached: Promise<ReleaseLookup> | undefined;

export function getLatestRelease(): Promise<ReleaseLookup> {
  cached ??= load();
  return cached;
}
