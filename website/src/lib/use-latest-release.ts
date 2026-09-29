'use client';
// The browser half of the release lookup. The build bakes in what
// /releases/latest said (release.ts); a new release doesn't rebuild the site,
// so the page re-reads the GitHub API once it runs.
//
//   const { lookup, origin } = useLatestRelease(initial);
//
// `lookup` starts as `initial` (so the server HTML and the first render
// match) and only ever upgrades (settleLookup in release-shape.ts): a newer
// release replaces an older one, and a successful answer settles an `unknown`
// build. A failed answer keeps the build's state, and a `ready` card is never
// downgraded.
import { useEffect, useState } from 'react';
import { LIST_API, latestFromList, settleLookup, type ListAnswer, type ReleaseLookup } from './release-shape';

export interface LatestRelease {
  /** What to show: the best answer known so far. */
  lookup: ReleaseLookup;
  /** 'build' until the browser's answer changed what the card shows. */
  origin: 'build' | 'live';
  /** True once the browser check has finished, whatever it found. */
  settled: boolean;
}

// The unauthenticated API allows 60 requests an hour per visitor, so one
// answer (or failure) is reused across pages for ten minutes.
const CACHE_KEY = 'vaporos-latest-release';
const CACHE_MS = 10 * 60 * 1000;

function cached(): ListAnswer | null {
  try {
    const c = JSON.parse(sessionStorage.getItem(CACHE_KEY) ?? 'null');
    return c && typeof c.ok === 'boolean' && Date.now() - c.at < CACHE_MS ? { ok: c.ok, latest: c.latest } : null;
  } catch {
    return null;
  }
}

function remember(a: ListAnswer) {
  try {
    sessionStorage.setItem(CACHE_KEY, JSON.stringify({ at: Date.now(), ...a }));
  } catch {}
}

// Every component on a page (and every client-side navigation within the
// cache window) shares one request.
let inflight: { at: number; answer: Promise<ListAnswer> } | null = null;

function latestAnswer(): Promise<ListAnswer> {
  if (inflight && Date.now() - inflight.at < CACHE_MS) return inflight.answer;
  const hit = cached();
  const answer: Promise<ListAnswer> = hit
    ? Promise.resolve(hit)
    : fetch(LIST_API, { headers: { Accept: 'application/vnd.github+json' } })
        .then(async (res) => {
          // Errors (rate limits, outages) keep what the build found.
          if (!res.ok) throw new Error(String(res.status));
          const json: unknown = await res.json();
          if (!Array.isArray(json)) throw new Error('not a list');
          const a: ListAnswer = { ok: true, latest: latestFromList(json) };
          remember(a);
          return a;
        })
        .catch(() => {
          const a: ListAnswer = { ok: false, latest: null };
          remember(a);
          return a;
        });
  inflight = { at: Date.now(), answer };
  return answer;
}

export function useLatestRelease(initial: ReleaseLookup): LatestRelease {
  const [state, setState] = useState<LatestRelease>({ lookup: initial, origin: 'build', settled: false });

  useEffect(() => {
    let alive = true;
    latestAnswer().then((a) => {
      if (!alive) return;
      setState((s) => {
        const next = settleLookup(s.lookup, a);
        return next === s.lookup ? { ...s, settled: true } : { lookup: next, origin: 'live', settled: true };
      });
    });
    return () => {
      alive = false;
    };
  }, []);

  return state;
}
