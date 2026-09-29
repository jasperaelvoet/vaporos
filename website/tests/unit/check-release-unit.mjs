#!/usr/bin/env node
// Unit checks for src/lib/release.ts and release-shape.ts, run straight from
// the TypeScript source (npm test):
//
//   node --conditions=react-server --import ./tests/unit/check-ts-hooks.mjs \
//     tests/unit/check-release-unit.mjs [projectDir=.] [fixture=tests/fixtures/release-latest.json]
//
// The fixture is GitHub's real answer for v20260929.172712
// (gh api repos/jasperaelvoet/vaporos/releases/latest). Scratch files go to
// the system temp directory, never into the repo.
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const [PROJECT = '.', FIXTURE = 'tests/fixtures/release-latest.json'] = process.argv.slice(2);
const lib = pathToFileURL(resolve(PROJECT, 'src/lib') + '/');
const fx = resolve(FIXTURE);
const one = JSON.parse(readFileSync(fx, 'utf8'));
const TAG = 'v20260929.172712';
const ISO = 'vaporos-20260929.172712.iso';

let failures = 0;
const check = (n, c, d = '') => {
  console.log(`${c ? 'PASS' : 'FAIL'}  ${n}${d ? `  (${d})` : ''}`);
  if (!c) failures++;
};
const tmp = mkdtempSync(join(tmpdir(), 'vaporos-release-unit-'));
const file = (name, body) => {
  const p = join(tmp, name);
  writeFileSync(p, typeof body === 'string' ? body : JSON.stringify(body));
  return p;
};

// Each scenario gets a fresh module instance (the lookup is memoized per process).
let n = 0;
const lookup = async () => (await import(new URL(`release.ts?v=${n++}`, lib).href)).getLatestRelease();
const env = (o) => {
  for (const k of ['VAPOROS_RELEASE_FIXTURE', 'VAPOROS_RELEASE_REQUIRED', 'GITHUB_TOKEN']) delete process.env[k];
  Object.assign(process.env, o);
};
const throws = async (f) => {
  try {
    await f();
    return false;
  } catch {
    return true;
  }
};
const realFetch = globalThis.fetch;
let seen;
const fakeFetch = (res) => async (url, init) => {
  seen = { url: String(url), headers: init?.headers ?? {} };
  if (res instanceof Error) throw res;
  return res;
};
console.info = () => {};
console.warn = () => {};

try {
  check('the fixture is the real v20260929.172712 answer', one.tag_name === TAG && one.assets.some((a) => a.name === ISO), one.tag_name);

  // ---------------------------------------------------------- fixtures
  env({ VAPOROS_RELEASE_FIXTURE: fx });
  let r = await lookup();
  check(
    'fixture (single release) is ready',
    r.state === 'ready' && r.release.version === '20260929.172712' && r.release.iso.name === ISO && r.release.iso.size === 1721292800,
    r.state === 'ready' ? `${r.release.iso.name} ${r.release.iso.size}` : r.state,
  );
  check('fixture: sums, manifest and signature found', r.state === 'ready' && !!r.release.sums && !!r.release.manifest && !!r.release.sig);

  env({ VAPOROS_RELEASE_FIXTURE: file('list.json', [{ ...one, tag_name: 'v2', draft: true }, { ...one, tag_name: 'v1', prerelease: true }, one]) });
  r = await lookup();
  check('fixture (list) picks the newest stable release', r.state === 'ready' && r.release.tag === TAG, r.release?.tag);

  env({ VAPOROS_RELEASE_FIXTURE: file('pre.json', [{ ...one, prerelease: true }]) });
  check('fixture (list of prereleases only) is none', (await lookup()).state === 'none');
  env({ VAPOROS_RELEASE_FIXTURE: file('empty-list.json', []) });
  check('fixture [] is none', (await lookup()).state === 'none');
  env({ VAPOROS_RELEASE_FIXTURE: '/dev/null' });
  check('fixture /dev/null is none', (await lookup()).state === 'none');
  env({ VAPOROS_RELEASE_FIXTURE: file('404.json', { message: 'Not Found' }) });
  check('fixture "Not Found" is none', (await lookup()).state === 'none');
  env({ VAPOROS_RELEASE_FIXTURE: file('403.json', { message: 'API rate limit exceeded' }) });
  check('fixture with another error message is unknown', (await lookup()).state === 'unknown');
  env({ VAPOROS_RELEASE_FIXTURE: file('noiso.json', { ...one, assets: one.assets.filter((a) => !a.name.endsWith('.iso')) }) });
  check('fixture release without an ISO is unknown', (await lookup()).state === 'unknown');
  env({ VAPOROS_RELEASE_FIXTURE: join(tmp, 'missing.json') });
  check('an unreadable fixture path fails the build', await throws(lookup));

  env({ VAPOROS_RELEASE_REQUIRED: '1', VAPOROS_RELEASE_FIXTURE: join(tmp, '403.json') });
  check('VAPOROS_RELEASE_REQUIRED=1 fails the build on unknown', await throws(lookup));
  env({ VAPOROS_RELEASE_REQUIRED: '1', VAPOROS_RELEASE_FIXTURE: '/dev/null' });
  check('VAPOROS_RELEASE_REQUIRED=1 accepts none', (await lookup()).state === 'none');
  env({ VAPOROS_RELEASE_REQUIRED: '1', VAPOROS_RELEASE_FIXTURE: fx });
  check('VAPOROS_RELEASE_REQUIRED=1 accepts ready', (await lookup()).state === 'ready');

  // ---------------------------------------------------------- the API
  env({ GITHUB_TOKEN: 'tkn' });
  globalThis.fetch = fakeFetch(new Response(JSON.stringify(one), { status: 200 }));
  r = await lookup();
  check('API: sends Authorization with GITHUB_TOKEN', seen.headers.Authorization === 'Bearer tkn' && seen.url.endsWith('/releases/latest'));
  check('API: parses the release', r.state === 'ready' && r.release.iso.name === ISO);

  env({});
  globalThis.fetch = fakeFetch(new Response('{}', { status: 200 }));
  await lookup();
  check('API: no Authorization without GITHUB_TOKEN', !('Authorization' in seen.headers));

  const api = async (res) => {
    globalThis.fetch = fakeFetch(res);
    return (await lookup()).state;
  };
  check('API: 404 is none', (await api(new Response('{"message":"Not Found"}', { status: 404 }))) === 'none');
  check('API: 403 (rate limit) is unknown', (await api(new Response('{"message":"rate limited"}', { status: 403 }))) === 'unknown');
  check('API: 502 is unknown', (await api(new Response('bad gateway', { status: 502 }))) === 'unknown');
  check('API: a network error is unknown', (await api(new TypeError('fetch failed'))) === 'unknown');
  check('API: bad JSON is unknown', (await api(new Response('not json', { status: 200 }))) === 'unknown');
  check(
    'API: a release without an ISO is unknown',
    (await api(new Response(JSON.stringify({ ...one, assets: one.assets.filter((a) => !a.name.endsWith('.iso')) }), { status: 200 }))) === 'unknown',
  );
  env({ VAPOROS_RELEASE_REQUIRED: '1' });
  globalThis.fetch = fakeFetch(new TypeError('fetch failed'));
  check('API: VAPOROS_RELEASE_REQUIRED=1 fails the build on a network error', await throws(lookup));
  globalThis.fetch = realFetch;
  env({});

  // ---------------------------------------------------------- release-shape
  const shape = await import(new URL('release-shape.ts', lib).href);
  check('ISO regex', shape.ISO_PATTERN.test(ISO) && !shape.ISO_PATTERN.test('vaporos.img') && !shape.ISO_PATTERN.test('x-vaporos-1.iso'));
  check('LIST_API per_page=100', shape.LIST_API === 'https://api.github.com/repos/jasperaelvoet/vaporos/releases?per_page=100');
  check(
    'formatSize/formatDate',
    shape.formatSize(1721292800) === '1.7 GB' && shape.formatSize(48213e3) === '48 MB' && shape.formatSize(0) === '' && shape.formatDate(one.published_at) === 'Sep 29, 2026',
    `${shape.formatSize(1721292800)} ${shape.formatDate(one.published_at)}`,
  );
  const a = shape.parseRelease(one);
  const older = shape.parseRelease({ ...one, published_at: '2026-01-01T00:00:00Z' });
  const newer = shape.parseRelease({ ...one, tag_name: 'v20261005.070000', published_at: '2026-10-05T07:00:00Z' });
  check('pickNewer never downgrades', shape.pickNewer(a, null) === a && shape.pickNewer(a, older) === a && shape.pickNewer(older, a) === a && shape.pickNewer(null, a) === a);
  check(
    'non-https asset URLs are refused',
    shape.parseRelease({ ...one, assets: one.assets.map((x) => ({ ...x, browser_download_url: x.browser_download_url.replace('https', 'http') })) }) === null,
  );

  // settleLookup: what the card shows after the browser's answer.
  const S = shape.settleLookup;
  const ready = { state: 'ready', release: a };
  const none = { state: 'none' };
  const unknown = { state: 'unknown' };
  const ok = (latest) => ({ ok: true, latest });
  const failed = { ok: false, latest: null };
  check('settle: a failed answer changes nothing', S(ready, failed) === ready && S(none, failed) === none && S(unknown, failed) === unknown);
  check('settle: ready never downgrades (empty list, older release)', S(ready, ok(null)) === ready && S(ready, ok({ ...one, published_at: '2026-01-01T00:00:00Z' })) === ready);
  check('settle: ready moves to a newer release', S(ready, ok({ ...one, tag_name: newer.tag, published_at: newer.published })).release?.tag === 'v20261005.070000');
  check('settle: none and unknown upgrade to ready', S(none, ok(one)).state === 'ready' && S(unknown, ok(one)).state === 'ready');
  check('settle: unknown + a list without a stable release is none', S(unknown, ok(null)).state === 'none');
  check('settle: none + a list without a stable release stays none', S(none, ok(null)) === none);
  check('settle: a stable release without an ISO keeps unknown', S(unknown, ok({ ...one, assets: [] })) === unknown);
} finally {
  globalThis.fetch = realFetch;
  rmSync(tmp, { recursive: true, force: true });
}

console.log(failures ? `\n${failures} FAILED` : '\nALL RELEASE UNIT CHECKS PASSED');
process.exit(failures ? 1 : 0);
