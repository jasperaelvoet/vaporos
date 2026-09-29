'use client';
// The browser half of the release lookup. The build bakes in what
// /releases/latest said (release.ts); a new release doesn't rebuild the site,
// so the page re-reads the GitHub API once it runs.
//
//   const { release, origin } = useLatestRelease(initial);
//
// `release` starts as `initial` (so the server HTML and the first render
// match) and only ever upgrades: a failed or empty answer keeps the build's
// state, never downgrading a download link to "coming soon".
import { useEffect, useState } from 'react';
import { LIST_API, latestFromList, parseRelease, pickNewer, type Release } from './release-shape';

export interface LatestRelease {
  /** What to show: the newest release known so far, or null. */
  release: Release | null;
  /** 'build' until the browser found a newer (or refreshed) release. */
  origin: 'build' | 'live';
  /** True once the browser check has finished, whatever it found. */
  settled: boolean;
}

// The unauthenticated API allows 60 requests an hour per visitor, so one
// answer (or failure) is reused across pages for ten minutes.
const CACHE_KEY = 'vaporos-latest-release';
const CACHE_MS = 10 * 60 * 1000;

function cached(): { latest: unknown } | null {
  try {
    const c = JSON.parse(sessionStorage.getItem(CACHE_KEY) ?? 'null');
    return c && Date.now() - c.at < CACHE_MS ? c : null;
  } catch {
    return null;
  }
}

function remember(latest: unknown) {
  try {
    sessionStorage.setItem(CACHE_KEY, JSON.stringify({ at: Date.now(), latest }));
  } catch {}
}

// Every component on a page (and every client-side navigation within the
// cache window) shares one request.
let inflight: { at: number; latest: Promise<unknown> } | null = null;

function latestRaw(): Promise<unknown> {
  if (inflight && Date.now() - inflight.at < CACHE_MS) return inflight.latest;
  const hit = cached();
  const latest: Promise<unknown> = hit
    ? Promise.resolve(hit.latest)
    : fetch(LIST_API, { headers: { Accept: 'application/vnd.github+json' } })
        .then(async (res) => {
          // Errors (rate limits, outages) keep what the build found.
          if (!res.ok) throw new Error(String(res.status));
          const l = latestFromList(await res.json());
          remember(l);
          return l;
        })
        .catch(() => {
          remember(null);
          return null;
        });
  inflight = { at: Date.now(), latest };
  return latest;
}

export function useLatestRelease(initial: Release | null): LatestRelease {
  const [state, setState] = useState<LatestRelease>({ release: initial, origin: 'build', settled: false });

  useEffect(() => {
    let alive = true;
    latestRaw().then((l) => {
      if (!alive) return;
      const live = parseRelease(l);
      setState((s) => {
        const next = pickNewer(s.release, live);
        return next === s.release ? { ...s, settled: true } : { release: next, origin: 'live', settled: true };
      });
    });
    return () => {
      alive = false;
    };
  }, []);

  return state;
}
