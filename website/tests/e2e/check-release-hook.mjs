#!/usr/bin/env node
// The download card's browser half (use-latest-release.ts), headless, with
// the GitHub API stubbed: upgrades, skips drafts and prereleases, never
// downgrades, settles an `unknown` build, caches one answer for ten minutes.
// Needs builds of the same site, each served (scripts/serve.mjs):
//   none     VAPOROS_RELEASE_FIXTURE=/dev/null
//   fixture  VAPOROS_RELEASE_FIXTURE=tests/fixtures/release-latest.json
//   unknown  (optional) a fixture holding {"message":"API rate limit exceeded"}
// The card keeps data-download-card, data-state (ready|none|unknown),
// data-origin (build|live) and an <a href$=".iso"> for the ISO.
//
//   node tests/e2e/check-release-hook.mjs <noneUrl> <fixtureUrl> <fixture.json> [unknownUrl]
import { readFileSync } from 'node:fs';
import { chromium } from 'playwright-core';
import { headlessShell } from '../../scripts/chrome.mjs';

const [NONE_ARG, READY_ARG, FIXTURE, UNKNOWN_ARG] = process.argv.slice(2);
if (!NONE_ARG || !READY_ARG || !FIXTURE) {
  console.error('usage: node tests/e2e/check-release-hook.mjs <noneUrl> <fixtureUrl> <fixture.json> [unknownUrl]');
  process.exit(2);
}
const fx = JSON.parse(readFileSync(FIXTURE, 'utf8'));
const FX_ISO = fx.assets.find((a) => a.name.endsWith('.iso')).name;
const rel = (tag, published, extra = {}) => {
  const v = tag.replace(/^v/, '');
  return {
    ...fx,
    tag_name: tag,
    published_at: published,
    html_url: `https://github.com/jasperaelvoet/vaporos/releases/tag/${tag}`,
    assets: fx.assets.map((a) =>
      a.name.endsWith('.iso')
        ? { ...a, name: `vaporos-${v}.iso`, browser_download_url: `https://github.com/jasperaelvoet/vaporos/releases/download/${tag}/vaporos-${v}.iso` }
        : { ...a, browser_download_url: a.browser_download_url.replace(fx.tag_name, tag) },
    ),
    ...extra,
  };
};
const slash = (u) => (u.endsWith('/') ? u : `${u}/`);
const NONE = slash(NONE_ARG);
const READY = slash(READY_ARG);
const UNKNOWN = UNKNOWN_ARG ? slash(UNKNOWN_ARG) : null;
const browser = await chromium.launch({ executablePath: headlessShell(), headless: true });
let failures = 0;
const check = (name, cond, detail = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${detail ? `  (${detail})` : ''}`);
  if (!cond) failures++;
};

async function run(base, path, reply) {
  const ctx = await browser.newContext();
  let calls = 0;
  await ctx.route('https://api.github.com/**', (r) => {
    calls++;
    return reply(r);
  });
  const p = await ctx.newPage();
  await p.goto(base + path, { waitUntil: 'load' });
  await p.waitForTimeout(800);
  const card = p.locator('[data-download-card]').first();
  const state = {
    state: await card.getAttribute('data-state'),
    origin: await card.getAttribute('data-origin'),
    text: await card.innerText(),
    iso: await card
      .locator('a[href$=".iso"]')
      .first()
      .getAttribute('href', { timeout: 500 })
      .catch(() => null),
    cache: await p.evaluate(() => sessionStorage.getItem('vaporos-latest-release')),
  };
  return { ctx, p, state, calls: () => calls };
}
const json =
  (body, status = 200) =>
  (r) =>
    r.fulfill({ status, headers: { 'access-control-allow-origin': '*', 'content-type': 'application/json' }, body: JSON.stringify(body) });

try {
  // A. none build + list [draft, prerelease, stable] → upgrades to the stable one only
  {
    const list = [
      rel('v20261003.090000', '2026-10-03T09:00:00Z', { draft: true }),
      rel('v20261002.120000', '2026-10-02T12:00:00Z', { prerelease: true }),
      fx,
    ];
    const { ctx, state } = await run(NONE, '', json(list));
    check('A upgrades none → ready from the list', state.state === 'ready' && state.origin === 'live', `${state.state}/${state.origin}`);
    check('A skips draft and prerelease', state.iso?.endsWith(FX_ISO), state.iso);
    check('A compact card has verify + requirements links', /Verify this download/.test(state.text) && /Requirements/.test(state.text));
    await ctx.close();
  }
  // B. none build + 403 (rate limit) → stays none, failure cached
  {
    const { ctx, state } = await run(NONE, 'download/', json({ message: 'API rate limit exceeded' }, 403));
    check('B 403 keeps the build state', state.state === 'none' && state.origin === 'build', `${state.state}/${state.origin}`);
    check('B failure is cached', !!state.cache && JSON.parse(state.cache).ok === false, state.cache?.slice(0, 40));
    check('B none card has no ISO link', state.iso === null);
    await ctx.close();
  }
  // C. ready build + [] → never downgrades
  {
    const { ctx, state } = await run(READY, 'download/', json([]));
    check('C empty list keeps ready', state.state === 'ready' && state.origin === 'build' && state.iso?.endsWith(FX_ISO), state.iso);
    await ctx.close();
  }
  // D. ready build + older stable release → keeps the newer build release
  {
    const { ctx, state } = await run(READY, 'download/', json([rel('v20260901.080000', '2026-09-01T08:00:00Z')]));
    check('D older live release does not replace a newer build release', state.origin === 'build' && state.iso?.endsWith(FX_ISO), state.iso);
    await ctx.close();
  }
  // E. ready build + newer stable release → upgrades
  {
    const { ctx, state } = await run(READY, 'download/', json([rel('v20261005.070000', '2026-10-05T07:00:00Z')]));
    check(
      'E newer live release upgrades the card',
      state.origin === 'live' && state.iso?.endsWith('vaporos-20261005.070000.iso') && /Oct 5, 2026/.test(state.text),
      state.iso,
    );
    await ctx.close();
  }
  // F. network error → stays; one request per 10 min across page loads and client navigation
  {
    const { ctx, p, state, calls } = await run(NONE, '', (r) => r.abort('failed'));
    check('F network error keeps the build state', state.state === 'none');
    await p.goto(`${NONE}download/`, { waitUntil: 'load' });
    await p.waitForTimeout(500);
    await p.getByRole('link', { name: 'FAQ' }).first().click();
    await p.waitForURL('**/faq/');
    await p.waitForTimeout(300);
    await p.getByRole('link', { name: 'Download' }).first().click();
    await p.waitForURL('**/download/');
    await p.waitForTimeout(500);
    check('F one API request across 2 page loads + client navigation', calls() === 1, `calls=${calls()}`);
    await ctx.close();
  }
  // G. list with the stable release second (after a prerelease) → the full card
  {
    const list = [rel('v20261001.000000', '2026-10-01T00:00:00Z', { prerelease: true }), fx];
    const { ctx, state } = await run(NONE, 'download/', json(list));
    check('G full card shows all four asset links', ['SHA256SUMS', 'manifest.json', 'manifest.json.sig', 'Release notes'].every((t) => state.text.includes(t)));
    await ctx.close();
  }
  // H. unknown build: never claims there is no release, settles on a good answer
  if (UNKNOWN) {
    {
      const { ctx, p, state } = await run(UNKNOWN, 'download/', json({ message: 'API rate limit exceeded' }, 403));
      const latest = await p.locator('[data-download-card] a[href$="/releases/latest"]').count();
      check('H unknown + 403 stays unknown and links to the latest release', state.state === 'unknown' && state.iso === null && latest > 0, `${state.state} links=${latest}`);
      check('H unknown card never says there is no release', !/hasn't published a release/i.test(state.text));
      await ctx.close();
    }
    {
      const { ctx, state } = await run(UNKNOWN, 'download/', json([fx]));
      check('H unknown + a release → ready', state.state === 'ready' && state.origin === 'live' && state.iso?.endsWith(FX_ISO), state.iso);
      await ctx.close();
    }
    {
      const { ctx, state } = await run(UNKNOWN, 'download/', json([]));
      check('H unknown + [] → none', state.state === 'none' && state.origin === 'live', `${state.state}/${state.origin}`);
      await ctx.close();
    }
  } else {
    console.log('SKIP  H (no unknown build given)');
  }
} finally {
  await browser.close();
}
console.log(failures ? `\n${failures} FAILED` : '\nALL HOOK CHECKS PASSED');
process.exit(failures ? 1 : 0);
