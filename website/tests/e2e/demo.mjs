#!/usr/bin/env node
// The live demo's gate on a build with the demo on (pages.yml, after
// tests/e2e/ci.mjs; G-15/G-16 for /demo/, G-20 for the site's side), headless:
// /demo/ at 1440 starts the frame on the export's hash, ?open= deep-links;
// the scenarios (stream and stream-end, pair with the Moonlight PIN typed
// into the frame, update through the restart, power-off and wake, reset) are
// announced and keep the focus on their button; at 390 the frame fills the
// screen and the scenarios are a bottom sheet; the home page's #demo loads
// on approach (a card on phones, no frame); a demo that never answers shows
// the way out after 8 s; the nav, footer, 404 and sitemap link it; and
// throughout, 0 requests to /api/v1/, 0 console errors or CSP violations
// (page and frame), no sideways scroll. Then shoot --strict and axe on /demo/.
//
//   node tests/e2e/demo.mjs [outDir=out] [--report=DIR] [--port=4345]
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';
import { headlessShell } from '../../scripts/chrome.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const argv = process.argv.slice(2);
const opt = Object.fromEntries(argv.filter((a) => a.startsWith('--')).map((a) => [a.slice(2).split('=')[0], a.includes('=') ? a.slice(a.indexOf('=') + 1) : true]));
const OUT = resolve(ROOT, argv.find((a) => !a.startsWith('--')) ?? 'out');
const REPORT = resolve(String(opt.report ?? join(tmpdir(), 'vaporos-demo-e2e')));
const PORT = Number(opt.port ?? 4345);
const ORIGIN = `http://127.0.0.1:${PORT}`;
const SITE = `${ORIGIN}/vaporos/`;
mkdirSync(join(REPORT, 'shots'), { recursive: true });

const manifestFile = join(OUT, 'demo', 'ui', 'demo-manifest.json');
if (!existsSync(join(OUT, 'demo', 'index.html')) || !existsSync(manifestFile)) {
  console.error(`demo e2e: ${OUT} has no /demo/ page or no demo in demo/ui (build with the demo on and its export in public/demo/ui)`);
  process.exit(2);
}
const HASH = JSON.parse(readFileSync(manifestFile, 'utf8')).hash;

let failures = 0;
const check = (name, cond, detail = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${!cond && detail ? `  (${detail})` : ''}`);
  if (!cond) failures++;
};

// ---------------------------------------------------------------- watchers
const api = [];
const errors = [];
// Reports a policy violation in any frame as a console error the watcher sees.
const INIT = `addEventListener('securitypolicyviolation', (e) => console.error('CSP violation: ' + e.violatedDirective + ' ' + e.blockedURI));`;

async function context(browser, { width, height }) {
  const phone = width < 600;
  const ctx = await browser.newContext({ viewport: { width, height }, isMobile: phone, hasTouch: phone, colorScheme: 'dark' });
  await ctx.addInitScript(INIT);
  // The download card's release lookup never reaches GitHub from a test.
  await ctx.route('https://api.github.com/**', (r) => r.fulfill({ status: 404, contentType: 'application/json', body: '{"message":"Not Found"}' }));
  ctx.on('request', (r) => {
    if (new URL(r.url()).pathname.includes('/api/v1/')) api.push(`${r.method()} ${r.url()}`);
  });
  return ctx;
}

function watch(page, where) {
  page.on('console', (m) => {
    // The stubbed GitHub answer above is a 404 the browser logs; it isn't the site's.
    if (m.type() === 'error' && !(m.location()?.url ?? '').startsWith('https://api.github.com/')) errors.push(`${where}: ${m.text()}`);
  });
  page.on('pageerror', (e) => errors.push(`${where}: pageerror ${e.message}`));
}

const frameOf = async (page) => {
  const el = await page.waitForSelector('.demo-frame', { timeout: 10000 });
  return el.contentFrame();
};
const started = (page, timeout = 8000) => page.waitForSelector('.demo-stage[data-started]', { timeout });
const said = (page, re, timeout = 10000) =>
  page.waitForFunction((src) => new RegExp(src).test(document.querySelector('.demo-stage [aria-live]')?.textContent ?? ''), re.source, { timeout });
const overflow = (page) => page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
const focused = (page) => page.evaluate(() => document.activeElement?.textContent?.trim() ?? '');

const step = (name, fn) => fn().catch((e) => check(name, false, String(e?.message ?? e).split('\n')[0]));

// ---------------------------------------------------------------- run
const server = spawn(process.execPath, ['scripts/serve.mjs', OUT, String(PORT), '--quiet'], { cwd: ROOT, stdio: ['ignore', 'pipe', 'inherit'] });
await new Promise((ok, fail) => {
  server.stdout.on('data', (d) => (String(d).startsWith('serve:') ? ok() : null));
  server.on('exit', (code) => fail(new Error(`serve exited (${code})`)));
});
process.on('exit', () => server.kill());
const browser = await chromium.launch({ executablePath: headlessShell(), headless: true });

try {
  // ------------------------------------------------------------ /demo/ wide
  {
    const ctx = await context(browser, { width: 1440, height: 900 });
    const page = await ctx.newPage();
    watch(page, '/demo/@1440');
    await page.goto(`${SITE}demo/`, { waitUntil: 'load' });

    await step('page: the frame starts', async () => {
      const t0 = Date.now();
      await started(page);
      const [src, sandbox] = [await page.getAttribute('.demo-frame', 'src'), await page.getAttribute('.demo-frame', 'sandbox')];
      check(`page: the frame starts in ${Date.now() - t0} ms, on the export's hash`, src === `/vaporos/demo/ui/?v=${HASH}`, src);
      check('page: the frame is sandboxed without top navigation', !!sandbox && !/allow-top-navigation/.test(sandbox));
      check('page: the phone and the panel side by side', (await page.isVisible('.demo-side')) && !(await page.isVisible('.demo-open-sheet')));
      check('page: no sideways scrolling', (await overflow(page)) <= 0);
      await page.screenshot({ path: join(REPORT, 'shots', 'demo-1440.png') });
    });

    const fr = await frameOf(page);
    await step('scenario stream', async () => {
      const btn = page.getByRole('button', { name: 'Start a stream' });
      await btn.click();
      await fr.waitForFunction(() => document.documentElement.dataset.state === 'streaming', null, { timeout: 8000 });
      await said(page, /Streaming to Living room TV/);
      check('scenario stream: the frame streams to Living room TV, and says so', true);
      check('scenario stream: the button turns into End the stream, and keeps the focus', (await focused(page)) === 'End the stream', await focused(page));
      await page.screenshot({ path: join(REPORT, 'shots', 'demo-streaming.png') });
      await page.getByRole('button', { name: 'End the stream' }).click();
      await fr.waitForFunction(() => document.documentElement.dataset.state === 'ready', null, { timeout: 8000 });
      await said(page, /The stream ended/);
      check('scenario stream-end: back to ready', true);
    });

    await step('scenario pair', async () => {
      await page.getByRole('button', { name: 'A device wants to pair' }).click();
      await page.waitForSelector('.demo-ml[data-ml="pin"]', { timeout: 8000 });
      const pin = (await page.textContent('.demo-ml-pin [aria-hidden]'))?.trim() ?? '';
      check('scenario pair: Moonlight shows a 4-digit PIN', /^\d{4}$/.test(pin), pin);
      await said(page, /Steam Deck wants to pair/);
      await page.screenshot({ path: join(REPORT, 'shots', 'demo-pair.png') });
      const enter = fr.getByRole('button', { name: /Enter PIN/ }).first();
      await enter.waitFor({ state: 'visible', timeout: 8000 });
      if (!(await fr.locator('#pinpad[open]').count())) await enter.click();
      await fr.locator('#pinpad[open]').waitFor({ timeout: 5000 });
      await fr.locator('#pin').fill(pin);
      await page.waitForSelector('.demo-ml[data-ml="paired"]', { timeout: 10000 });
      await said(page, /Steam Deck is paired/);
      check('scenario pair: the PIN from Moonlight pairs the Steam Deck', true);
      await page.screenshot({ path: join(REPORT, 'shots', 'demo-paired.png') });
    });

    await step('scenario update', async () => {
      await page.getByRole('button', { name: 'An update arrives' }).click();
      await fr.getByText('Version 20261001.090000 is available').first().waitFor({ timeout: 8000 });
      check('scenario update: Home offers 20261001.090000', true);
      await fr.goto(`${ORIGIN}/vaporos/demo/ui/system/updates`);
      await fr.locator('#upd-stage:enabled').waitFor({ timeout: 8000 });
      await fr.locator('#upd-stage').evaluate((el) => el.scrollIntoView({ block: 'center' }));
      await fr.click('#upd-stage');
      await said(page, /installing an update/);
      await fr.waitForFunction(() => /is ready/.test(document.getElementById('upd-title')?.textContent ?? ''), null, { timeout: 60000 });
      check('scenario update: it downloads and stages, and the panel said so', true);
      await fr.locator('#upd-restart:visible, #upd-reboot:visible').first().click();
      await fr.locator('#confirm-ok').click({ timeout: 5000 });
      await said(page, /VaporOS is off/, 15000);
      await said(page, /VaporOS is back on/, 30000);
      await fr.waitForFunction(() => /20261001\.090000/.test(document.body.innerText), null, { timeout: 20000 });
      check('scenario update: it restarts onto 20261001.090000', true);
    });

    await step('scenario power-off and wake', async () => {
      const btn = page.getByRole('button', { name: 'Power it off' });
      await btn.click();
      await said(page, /VaporOS is off/);
      check('scenario power-off: the button turns into Wake it, and keeps the focus', (await focused(page)) === 'Wake it', await focused(page));
      await page.getByRole('button', { name: 'Wake it' }).click();
      await said(page, /VaporOS is back on/, 15000);
      check('scenario power-off and wake: off, then back on', true);
    });

    await step('scenario reset', async () => {
      await page.getByRole('button', { name: 'Start over' }).click();
      await page.waitForTimeout(500);
      await fr.waitForFunction(() => document.documentElement.dataset.state === 'ready' && !!document.documentElement.dataset.boot, null, { timeout: 10000 });
      check('scenario reset: the frame starts over, ready to stream', (await focused(page)) === 'Start over', await focused(page));
    });
    await ctx.close();
  }

  // ------------------------------------------------------------ deep link
  await step('deep link', async () => {
    const ctx = await context(browser, { width: 1440, height: 900 });
    const page = await ctx.newPage();
    watch(page, '/demo/?open=devices');
    await page.goto(`${SITE}demo/?open=devices`, { waitUntil: 'load' });
    await started(page);
    const fr = await frameOf(page);
    check('deep link: ?open=devices opens Devices', new URL(fr.url()).pathname.replace(/\/$/, '') === '/vaporos/demo/ui/devices', fr.url());
    await page.goto(`${SITE}demo/?open=../../download`, { waitUntil: 'load' });
    await started(page);
    check('deep link: anything else opens Home', (await page.getAttribute('.demo-frame', 'src')) === `/vaporos/demo/ui/?v=${HASH}`);
    await ctx.close();
  });

  // ------------------------------------------------------------ /demo/ on a phone
  await step('phone', async () => {
    const ctx = await context(browser, { width: 390, height: 844 });
    const page = await ctx.newPage();
    watch(page, '/demo/@390');
    await page.goto(`${SITE}demo/`, { waitUntil: 'load' });
    await started(page);
    const box = await page.locator('.demo-frame').boundingBox();
    check('phone: the frame fills the width, unscaled', !!box && Math.round(box.width) === 390, JSON.stringify(box));
    check('phone: the panel is not beside it', !(await page.isVisible('.demo-side')));
    check('phone: no sideways scrolling', (await overflow(page)) <= 0);
    const open = page.getByRole('button', { name: 'Scenarios' });
    await open.click();
    await page.waitForSelector('.demo-sheet[open]');
    await page.screenshot({ path: join(REPORT, 'shots', 'demo-390-sheet.png') });
    await page.locator('.demo-sheet').getByRole('button', { name: 'A device wants to pair' }).click();
    await page.waitForSelector('.demo-sheet:not([open])', { state: 'attached' });
    check('phone: a scenario closes the sheet and gives the focus back to Scenarios', (await focused(page)) === 'Scenarios', await focused(page));
    await page.waitForSelector('.demo-pinbar', { timeout: 8000 });
    check('phone: the PIN shows under the bar', /1234/.test((await page.textContent('.demo-pinbar')) ?? ''));
    await page.screenshot({ path: join(REPORT, 'shots', 'demo-390-pin.png') });
    await page.getByRole('button', { name: 'Scenarios' }).click();
    await page.keyboard.press('Escape');
    await page.waitForSelector('.demo-sheet:not([open])', { state: 'attached' });
    check('phone: Escape closes the sheet', (await focused(page)) === 'Scenarios', await focused(page));
    await ctx.close();
  });

  // ------------------------------------------------------------ home
  await step('home', async () => {
    const ctx = await context(browser, { width: 1440, height: 900 });
    const page = await ctx.newPage();
    watch(page, '/@1440');
    await page.goto(SITE, { waitUntil: 'load' });
    check('home: the hero offers the live demo', (await page.getAttribute('a:has-text("Try the live demo")', 'href'))?.endsWith('#demo'));
    await page.waitForTimeout(1500);
    check('home: no frame before the demo is near', (await page.locator('.demo-frame').count()) === 0);
    await page.locator('#demo').scrollIntoViewIfNeeded();
    await started(page, 12000);
    check('home: #demo loads the frame on approach and it starts', true);
    await page.locator('.demo-home').screenshot({ path: join(REPORT, 'shots', 'home-demo-1440.png') });
    await ctx.close();

    const pctx = await context(browser, { width: 390, height: 844 });
    const phone = await pctx.newPage();
    watch(phone, '/@390');
    await phone.goto(SITE, { waitUntil: 'load' });
    await phone.locator('#demo').scrollIntoViewIfNeeded();
    await phone.waitForTimeout(2500);
    check('home: phones get the card, and no frame', (await phone.isVisible('.demo-card')) && (await phone.locator('.demo-frame').count()) === 0);
    check('home: the card opens /demo/', (await phone.getAttribute('.demo-card a', 'href')) === '/vaporos/demo/');
    await phone.locator('.demo-card').screenshot({ path: join(REPORT, 'shots', 'home-demo-390.png') });
    await pctx.close();
  });

  // ------------------------------------------------------------ fallback
  await step('fallback', async () => {
    const ctx = await context(browser, { width: 1440, height: 900 });
    await ctx.route('**/vaporos/demo/ui/**', (r) => r.abort());
    const page = await ctx.newPage();
    await page.goto(`${SITE}demo/`, { waitUntil: 'load' });
    const t0 = Date.now();
    await page.getByText("The demo didn't start.").waitFor({ timeout: 10000 });
    const ms = Date.now() - t0;
    check(`fallback: a demo that never answers offers its own tab after ${ms} ms`, ms >= 7000 && (await page.getAttribute('.demo-skel a', 'href')) === `/vaporos/demo/ui/?v=${HASH}`);
    await ctx.close();
  });

  // ------------------------------------------------------------ links
  await step('links', async () => {
    const ctx = await context(browser, { width: 1440, height: 900 });
    const page = await ctx.newPage();
    await page.goto(`${SITE}faq/`, { waitUntil: 'load' });
    check('links: the nav and the menu name the live demo', (await page.locator('header nav a[href="/vaporos/demo/"]').count()) === 2);
    check('links: the footer names the live demo', (await page.locator('footer a[href="/vaporos/demo/"]').count()) === 1);
    await page.goto(`${SITE}nope/`, { waitUntil: 'load' });
    check('links: the 404 page offers the live demo', (await page.locator('main a[href="/vaporos/demo/"]').count()) >= 1);
    const sitemap = readFileSync(join(OUT, 'sitemap.xml'), 'utf8');
    check('links: the sitemap lists /demo/ and not its control center', sitemap.includes('/vaporos/demo/</loc>') && !sitemap.includes('/demo/ui'));
    await ctx.close();
  });
} finally {
  await browser.close();
}

check(`everywhere: 0 requests to /api/v1/`, api.length === 0, api.slice(0, 5).join(', '));
check(`everywhere: 0 console errors or CSP violations`, errors.length === 0, errors.slice(0, 5).join(' | '));

// ---------------------------------------------------------------- shoot and axe on /demo/
const run = (name, args) => {
  console.log(`\n▶ ${name}`);
  const r = spawnSync(process.execPath, args, { cwd: ROOT, stdio: 'inherit' });
  check(name, r.status === 0);
};
run('shoot /demo/ 1440/768/390', ['scripts/shoot.mjs', SITE, join(REPORT, 'shoot'), '--pages=/demo/', '--widths=1440,768,390', '--strict']);
run('shoot /demo/ 390 reduced motion', ['scripts/shoot.mjs', SITE, join(REPORT, 'shoot-rm'), '--pages=/demo/', '--widths=390', '--reduced-motion', '--strict']);
run('axe /demo/', ['tests/e2e/axe.mjs', SITE, '--pages=/demo/', `--out=${join(REPORT, 'axe')}`]);
server.kill();

console.log(failures ? `\n${failures} DEMO CHECK(S) FAILED (reports in ${REPORT})` : `\nDEMO E2E PASSED (reports in ${REPORT})`);
process.exit(failures ? 1 : 0);
