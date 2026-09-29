'use client';
// The browser half of the release lookup. The build bakes in what
// /releases/latest said (release.ts); a new release doesn't rebuild the site,
// so the page re-reads the GitHub API once it runs.
//
//   const { lookup, origin } = useLatestRelease(initial);
//
// `lookup` starts as `initial` (so the server HTML and the first render
// match) and only ever upgrades: a newer release replaces an older one, and a
// successful answer settles an `unknown` build (to `ready`, or to `none` when
// GitHub lists no stable release). A failed answer keeps the build's state,
// and a `ready` card is never downgraded.
import { useEffect, useState } from 'react';
import { LIST_API, latestFromList, parseRelease, pickNewer, type ReleaseLookup } from './release-shape';

export interface LatestRelease {
  /** What to show: the best answer known so far. */
  lookup: ReleaseLookup;
  /** 'build' until the browser's answer changed what the card shows. */
  origin: 'build' | 'live';
  /** True once the browser check has finished, whatever it found. */
  settled: boolean;
}

// What the browser learned: ok=false when the request failed; latest is the
// newest stable release in the list, or null when there is none.
interface Answer {
  ok: boolean;
  latest: unknown;
}

// The unauthenticated API allows 60 requests an hour per visitor, so one
// answer (or failure) is reused across pages for ten minutes.
const CACHE_KEY = 'vaporos-latest-release';
const CACHE_MS = 10 * 60 * 1000;

function cached(): Answer | null {
  try {
    const c = JSON.parse(sessionStorage.getItem(CACHE_KEY) ?? 'null');
    return c && typeof c.ok === 'boolean' && Date.now() - c.at < CACHE_MS ? { ok: c.ok, latest: c.latest } : null;
  } catch {
    return null;
  }
}

function remember(a: Answer) {
  try {
    sessionStorage.setItem(CACHE_KEY, JSON.stringify({ at: Date.now(), ...a }));
  } catch {}
}

// Every component on a page (and every client-side navigation within the
// cache window) shares one request.
let inflight: { at: number; answer: Promise<Answer> } | null = null;

function latestAnswer(): Promise<Answer> {
  if (inflight && Date.now() - inflight.at < CACHE_MS) return inflight.answer;
  const hit = cached();
  const answer: Promise<Answer> = hit
    ? Promise.resolve(hit)
    : fetch(LIST_API, { headers: { Accept: 'application/vnd.github+json' } })
        .then(async (res) => {
          // Errors (rate limits, outages) keep what the build found.
          if (!res.ok) throw new Error(String(res.status));
          const json: unknown = await res.json();
          if (!Array.isArray(json)) throw new Error('not a list');
          const a = { ok: true, latest: latestFromList(json) };
          remember(a);
          return a;
        })
        .catch(() => {
          const a = { ok: false, latest: null };
          remember(a);
          return a;
        });
  inflight = { at: Date.now(), answer };
  return answer;
}

/** What the card shows after the browser's answer; the same object when nothing changes. */
export function settle(current: ReleaseLookup, answer: Answer): ReleaseLookup {
  if (!answer.ok) return current;
  const live = parseRelease(answer.latest);
  if (current.state === 'ready') {
    const next = pickNewer(current.release, live);
    return next === current.release || !next ? current : { state: 'ready', release: next };
  }
  if (live) return { state: 'ready', release: live };
  // A successful list with no stable release in it: GitHub has none. A stable
  // release without an ISO stays unknown.
  if (current.state === 'unknown' && answer.latest === null) return { state: 'none' };
  return current;
}

export function useLatestRelease(initial: ReleaseLookup): LatestRelease {
  const [state, setState] = useState<LatestRelease>({ lookup: initial, origin: 'build', settled: false });

  useEffect(() => {
    let alive = true;
    latestAnswer().then((a) => {
      if (!alive) return;
      setState((s) => {
        const next = settle(s.lookup, a);
        return next === s.lookup ? { ...s, settled: true } : { lookup: next, origin: 'live', settled: true };
      });
    });
    return () => {
      alive = false;
    };
  }, []);

  return state;
}
