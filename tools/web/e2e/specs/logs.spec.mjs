// Flows over System › Logs (spec-cc-screens §11): the log, Refresh and
// Follow (a bounded, passive poll), Copy and Download. Each ID is a row of
// tools/web/e2e/parity.json (owner C4). See legacy.spec.mjs for the flow
// format.

import assert from 'node:assert/strict';

import { dev } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const until = (page, fn, arg, timeout = 5000) => page.waitForFunction(fn, arg, { timeout });

async function open(t, path = '/system/logs') {
  await t.page.goto(t.url(path));
  await t.ready();
}

// logReads counts GET /sunshine/logs and whether each was passive.
function logReads(page) {
  const seen = [];
  page.on('request', (r) => {
    if (r.method() === 'GET' && new URL(r.url()).pathname === '/api/v1/sunshine/logs') seen.push(r.headers()['x-vos-passive'] === '1');
  });
  return seen;
}

const LONG = Array.from({ length: 400 }, (_, i) => `[2026-09-30 09:${String(i % 60).padStart(2, '0')}:00.000]: Info: line ${i + 1}`);
const LEVELS = [
  '[2026-09-30 09:00:00.000]: Info: Sunshine version: v2026.928.143000',
  '[2026-09-30 09:00:01.000]: Warning: No gamepad input device found',
  '[2026-09-30 09:00:02.000]: Error: Couldn\'t open the encoder for 3840x2160 at 240 Hz, which is a long message that wraps on a phone',
];

async function answerLog(page, lines) {
  await page.route('**/api/v1/sunshine/logs', (r) => r.fulfill({ status: 200, contentType: 'text/plain; charset=utf-8', body: `${lines.join('\n')}\n` }));
}

// hide and show pretend the tab went to the background and came back.
const setHidden = (page, hidden) =>
  page.evaluate((h) => {
    Object.defineProperty(document, 'hidden', { value: h, configurable: true });
    Object.defineProperty(document, 'visibilityState', { value: h ? 'hidden' : 'visible', configurable: true });
    document.dispatchEvent(new Event('visibilitychange'));
  }, hidden);

export default [
  {
    id: 'SYS-log-view',
    ui: ['next'],
    allow: [/status of 502/, /502 GET .*\/api\/v1\/sunshine\/logs$/],
    async run(t) {
      const { page, server, step } = t;
      await step('the old /advanced#logs link lands on Logs', async () => {
        await page.goto(t.url('/advanced#logs'));
        await page.waitForURL((u) => new URL(u).pathname === '/system/logs');
        await t.ready();
      });
      await step('the log reads as a focusable, named block with when it was read', async () => {
        assert.match(await text(page, '#log-text'), /^\[2026-09-29 14:00:02\.118\]: Info: Sunshine version: v2026\.928\.143000/);
        assert.equal(await page.getAttribute('#log-text', 'tabindex'), '0');
        assert.equal(await page.getAttribute('#log-text', 'aria-label'), 'Stream server log');
        assert.match(await text(page, '#log-meta'), /^Updated \S/);
        assert.equal(await page.getAttribute('#log-download', 'target'), null);
      });
      await step('a long log opens at its end, the newest lines', async () => {
        await answerLog(page, LONG);
        await open(t);
        await until(page, () => {
          const pre = document.getElementById('log-text');
          return pre.scrollHeight > pre.clientHeight && pre.scrollHeight - pre.scrollTop - pre.clientHeight < 2;
        });
        assert.equal(await page.locator('#log-text .log-line').count(), 400);
      });
      await step('errors and warnings keep their words and gain a mark', async () => {
        await answerLog(page, LEVELS);
        await open(t);
        const lines = page.locator('#log-text .log-line');
        assert.equal(await lines.nth(0).getAttribute('data-level'), null);
        assert.equal(await lines.nth(1).getAttribute('data-level'), 'warn');
        assert.equal(await lines.nth(2).getAttribute('data-level'), 'error');
        assert.match(await lines.nth(2).textContent(), /Error: Couldn't open the encoder/);
        await page.unroute('**/api/v1/sunshine/logs');
      });
      await step('an empty log says so, and there is nothing to copy', async () => {
        await dev(server, 'preset', { name: 'logs-empty' });
        await open(t);
        assert.equal(await text(page, '#log-text'), 'The log is empty.');
        assert.equal(await page.isDisabled('#log-copy'), true);
      });
      await step('an unreadable log is an inline error with Try again', async () => {
        await dev(server, 'preset', { name: 'logs-error' });
        await open(t);
        await page.locator('#log-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#log-error-text'), "Couldn't load the log: No Sunshine log available: journalctl: exit status 1");
        await dev(server, 'preset', { name: 'idle' });
        await page.click('#log-retry');
        await page.locator('#log-error').waitFor({ state: 'hidden' });
        assert.match(await text(page, '#log-text'), /Sunshine version/);
      });
    },
  },
  {
    id: 'SYS-log-refresh',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await page.clock.install();
      const reads = logReads(page);
      await step('Refresh reads the log again', async () => {
        await open(t);
        await until(page, () => !document.getElementById('log-refresh').hasAttribute('aria-busy'));
        const before = reads.length;
        await page.click('#log-refresh');
        await until(page, () => !document.getElementById('log-refresh').hasAttribute('aria-busy'));
        assert.equal(reads.length, before + 1);
        assert.equal(reads.at(-1), false);
      });
      await step('Follow re-reads every 5 s, passively', async () => {
        assert.equal(await page.getAttribute('#log-follow', 'role'), 'switch');
        await page.check('#log-follow');
        await page.waitForTimeout(300); // turning it on reads once at once
        const n = reads.length;
        for (let i = 0; i < 2; i++) {
          await page.clock.runFor(5000);
          await page.waitForTimeout(300);
        }
        assert.ok(reads.length >= n + 2, `${reads.length - n} reads in 10 s`);
        assert.ok(reads.slice(n).every(Boolean), 'every Follow read is passive');
      });
      await step('in the background it waits, and goes on when back in view', async () => {
        await setHidden(page, true);
        const n = reads.length;
        await page.clock.runFor(30000);
        await page.waitForTimeout(300);
        assert.equal(reads.length, n);
        await setHidden(page, false);
        await page.waitForTimeout(300);
        assert.ok(reads.length > n);
      });
      await step('after 10 minutes it pauses, with Resume', async () => {
        await page.clock.runFor(10 * 60 * 1000);
        await page.waitForTimeout(300);
        await page.clock.runFor(5000);
        await page.locator('#log-resume:not([hidden])').waitFor();
        assert.equal(await text(page, '#log-follow-text'), 'Paused after 10 minutes.');
        assert.equal(await page.isChecked('#log-follow'), false);
        const n = reads.length;
        await page.clock.runFor(20000);
        await page.waitForTimeout(300);
        assert.equal(reads.length, n);
        await page.click('#log-resume');
        assert.equal(await page.isChecked('#log-follow'), true);
        assert.equal(await page.isVisible('#log-resume'), false);
      });
    },
  },
  {
    id: 'SYS-log-download',
    ui: ['next'],
    async run(t) {
      const { page, context, step } = t;
      await step('Download saves the log in this tab, with the session', async () => {
        await open(t);
        assert.equal(await page.getAttribute('#log-download', 'download'), 'sunshine.log');
        const pages = context.pages().length;
        const [download] = await Promise.all([page.waitForEvent('download'), page.click('#log-download')]);
        assert.equal(download.suggestedFilename(), 'sunshine.log');
        const body = await new Promise((resolve, reject) => {
          download.createReadStream().then((s) => {
            let out = '';
            s.on('data', (c) => (out += c));
            s.on('end', () => resolve(out));
            s.on('error', reject);
          }, reject);
        });
        assert.match(body, /Info: Sunshine version: v2026\.928\.143000/);
        assert.equal(context.pages().length, pages);
      });
      await step('Copy takes the whole log', async () => {
        await page.evaluate(() => {
          window.__copied = [];
          const real = document.execCommand.bind(document);
          document.execCommand = (cmd, ...a) => {
            if (cmd === 'copy') window.__copied.push(document.querySelector('.copy-scratch')?.value ?? '');
            return cmd === 'copy' ? true : real(cmd, ...a);
          };
        });
        await page.click('#log-copy');
        await page.locator('#notices .notice', { hasText: 'Copied.' }).waitFor();
        const copied = await page.evaluate(() => window.__copied[0]);
        assert.match(copied, /^\[2026-09-29 14:00:02\.118\]: Info: Sunshine version/);
        assert.equal(copied.split('\n').filter(Boolean).length, await page.locator('#log-text .log-line').count());
      });
    },
  },
];
