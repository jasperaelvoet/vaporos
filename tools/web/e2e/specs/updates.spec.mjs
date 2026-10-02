// Flows over System › Updates (spec-cc-screens §7): the status in its
// states, progress, banners, the actions, the two copies with the Go back
// rules, the settings and the versions that didn't start. Each ID is a row
// of tools/web/e2e/parity.json (owner C4). See legacy.spec.mjs for the
// flow format.

import assert from 'node:assert/strict';

import { closed, dev, write } from '../lib/dev.mjs';

const PATH = '/system/updates';
// A restart takes the box down: /ping and the stream fail until it is back.
const DOWN = [/Failed to load resource/, /net::ERR_/, /\/api\/v1\/(ping|events|system|auth\/me|status|update)/, /ERR_EMPTY_RESPONSE|ERR_CONNECTION/];
const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const until = (page, fn, arg, timeout = 5000) => page.waitForFunction(fn, arg, { timeout });
const title = (page, want, timeout = 5000) => until(page, (w) => document.getElementById('upd-title').textContent === w, want, timeout);
const notice = (page, words) => page.locator('#notices .notice', { hasText: words }).first().waitFor();
const live = (page) => until(page, () => document.getElementById('link').dataset.link === 'live');
const banners = (page) => page.$$eval('#upd-banners .upd-banner', (els) => els.map((e) => [e.dataset.tone, e.textContent.trim()]));

async function open(t, preset) {
  if (preset) await dev(t.server, 'preset', { name: preset });
  await t.page.goto(t.url(PATH));
  await t.ready();
}

async function press(page, sel) {
  await page.locator(sel).evaluate((el) => el.scrollIntoView({ block: 'center' }));
  await page.click(sel);
}

export default [
  {
    id: 'SYS-upd-status',
    ui: ['next'],
    async run(t) {
      const { page, server, step } = t;
      await step('up to date: the booted version and when it last checked', async () => {
        await open(t);
        assert.equal(await text(page, '#upd-title'), 'VaporOS is up to date');
        assert.equal(await text(page, '#upd-detail'), 'Version 20260929.101500. Checked 40 min ago.');
        assert.equal(await page.getAttribute('#upd-status', 'data-state'), 'ready');
        assert.equal(await page.isVisible('#upd-check'), true);
        for (const id of ['#upd-stage', '#upd-restart', '#upd-reboot', '#upd-cancel', '#upd-progress', '#upd-checking']) assert.equal(await page.isVisible(id), false, id);
      });
      await step('a replayed check while nothing runs is not an update (B1)', async () => {
        await open(t, 'update-stale-check');
        assert.equal(await text(page, '#upd-title'), 'VaporOS is up to date');
        assert.equal(await page.isVisible('#upd-progress'), false);
        assert.equal(await page.isVisible('#upd-checking'), false);
      });
      await step('available: its size and Download update', async () => {
        await open(t, 'update-available');
        assert.equal(await text(page, '#upd-title'), 'Version 20260929.143000 is available');
        assert.equal(await text(page, '#upd-detail'), 'Up to 1.4 GB download. Checked 40 min ago.');
        assert.equal(await page.isVisible('#upd-stage'), true);
      });
      await step('staged: "Version <v> is ready" with Restart to update, and the restart row stays quiet', async () => {
        await open(t, 'update-staged');
        assert.equal(await text(page, '#upd-title'), 'Version 20260929.143000 is ready');
        assert.equal(await text(page, '#upd-detail'), "Restart to switch over. If it doesn't start, VaporOS goes back by itself.");
        assert.equal(await text(page, '#upd-meta'), 'Prepared 10 min ago');
        assert.equal(await page.isVisible('#upd-restart'), true);
        await until(page, () => document.getElementById('restart-row').hasAttribute('data-covered'));
        assert.equal(await page.isVisible('#restart-row'), false);
      });
      await step('a rollback waiting: what starts next, and Restart now', async () => {
        await open(t, 'rollback-pending');
        assert.equal(await text(page, '#upd-title'), 'Version 20260927.190000 starts on the next restart');
        assert.equal(await text(page, '#upd-detail'), 'You chose to go back from 20260929.101500.');
        assert.equal(await page.getAttribute('#upd-status', 'data-state'), 'restart-needed');
        assert.equal(await page.isVisible('#upd-reboot'), true);
      });
      await step('never checked: an update.state without checked replaces the one with it', async () => {
        await open(t, 'idle');
        await live(page);
        await dev(server, 'event', { topic: 'update.state', data: { booted: '20260929.101500', staged: null, failed: [], available: null, last_error: '' } });
        await title(page, 'VaporOS 20260929.101500');
        assert.equal(await text(page, '#upd-detail'), 'Not checked yet.');
      });
    },
  },
  {
    id: 'SYS-upd-channel-badge',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the stable channel is a quiet badge', async () => {
        await open(t);
        assert.equal(await text(page, '#upd-channel'), 'Channel main');
        assert.equal(await page.getAttribute('#upd-channel', 'data-tone'), '');
      });
      await step('a test channel is named and marked', async () => {
        await page.evaluate(async () => {
          const me = await (await fetch('/api/v1/auth/me')).json();
          await fetch('/api/v1/update/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-VOS-CSRF': me.csrf }, body: JSON.stringify({ channel: 'beta' }) });
        });
        await page.reload();
        await t.ready();
        assert.equal(await text(page, '#upd-channel'), 'Test channel beta');
        assert.equal(await page.getAttribute('#upd-channel', 'data-tone'), 'warn');
      });
    },
  },
  {
    id: 'SYS-upd-progress',
    ui: ['next'],
    preset: 'update-staging',
    async run(t) {
      const { page, step } = t;
      await step('a reload mid-stage shows it working, from GET busy and progress (B1)', async () => {
        await open(t);
        assert.equal(await text(page, '#upd-title'), 'Installing version 20260929.143000');
        assert.equal(await page.getAttribute('#upd-status', 'data-state'), 'updating');
        assert.equal(await page.isVisible('#upd-progress'), true);
        assert.equal(await page.isVisible('#upd-stage'), false);
        assert.equal(await page.isDisabled('#upd-check'), true);
        assert.equal(await page.isVisible('#upd-cancel'), true);
        assert.equal(await text(page, '#upd-phase'), 'Downloading');
        assert.match(await text(page, '#upd-bytes'), /^[\d.]+ [kMG]?B of 1\.4 GB$/);
      });
      await step('the percent and the bar follow the live events; the other copy heats as it is written', async () => {
        const first = Number(await page.getAttribute('#upd-bar', 'aria-valuenow'));
        await until(page, (n) => Number(document.getElementById('upd-bar').getAttribute('aria-valuenow')) > n, first);
        assert.match(await page.getAttribute('#upd-bar', 'aria-valuetext'), /^(Downloading|Verifying|Finishing), \d+ percent$/);
        assert.match(await text(page, '#upd-pct'), /^\d+%$/);
        assert.equal(await page.getAttribute('#upd-other', 'data-heat'), 'fill');
        assert.equal(await text(page, '#upd-other-label'), 'Downloading');
        const heat = await page.$eval('#upd-other', (el) => Number(el.style.getPropertyValue('--heat')));
        assert.ok(heat > 0.4 && heat <= 1, `--heat ${heat}`);
      });
      await step('done: the status turns to "Version <v> is ready"', async () => {
        await title(page, 'Version 20260929.143000 is ready', 20000);
        assert.equal(await page.isVisible('#upd-progress'), false);
        assert.equal(await text(page, '#upd-other-label'), 'Next restart');
        assert.equal(await page.getAttribute('#upd-other', 'data-heat'), 'warm');
      });
    },
  },
  {
    id: 'SYS-upd-banners',
    ui: ['next'],
    preset: 'update-check-failed',
    async run(t) {
      const { page, step } = t;
      await step('a failed check: a warning in plain words, from last_error', async () => {
        await open(t);
        assert.deepEqual(await banners(page), [['warn', "Couldn't check for updates: the update server couldn't be reached. Check the PC's internet connection."]]);
      });
      await step('a stage that failed: the danger banner survives a reload', async () => {
        await open(t, 'update-error');
        assert.deepEqual(await banners(page), [['danger', "The last update didn't install: root.erofs: sha256 mismatch (the download was damaged)."]]);
      });
      await step('a version that did not start: in words, not the server text', async () => {
        await open(t, 'update-failed-newer');
        assert.deepEqual(await banners(page), [['danger', "Version 20260929.143000 didn't start, so VaporOS kept 20260929.101500."]]);
      });
    },
  },
  {
    id: 'SYS-upd-download',
    ui: ['next'],
    preset: 'update-available',
    async run(t) {
      const { page, step } = t;
      await step('Download update stages the available version and the status starts at once', async () => {
        await open(t);
        await live(page);
        const body = write(page, 'POST', '/update/stage');
        await press(page, '#upd-stage');
        assert.deepEqual(await body, { version: '20260929.143000' });
        await title(page, 'Installing version 20260929.143000');
        assert.equal(await page.evaluate(() => document.activeElement.id), 'upd-title');
        await until(page, () => ['Downloading', 'Verifying'].includes(document.getElementById('upd-phase').textContent));
      });
      await step('Stop asks first, with Cancel focused, and stopping says what it cost', async () => {
        await until(page, () => document.getElementById('upd-phase').textContent === 'Downloading' && Number(document.getElementById('upd-bar').getAttribute('aria-valuenow')) > 2);
        await press(page, '#upd-cancel');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Stop the download?');
        assert.equal(await text(page, '#confirm-body'), 'The older version kept for going back is already being replaced, so there is none until the next update.');
        assert.equal(await page.evaluate(() => document.activeElement.id), 'confirm-cancel');
        const sent = write(page, 'POST', '/update/cancel');
        await page.click('#confirm-ok');
        await sent;
        await notice(page, "Download stopped. There's no older version to go back to until the next update.");
        await title(page, 'Version 20260929.143000 is available');
        await until(page, () => document.getElementById('upd-other-version').textContent === 'Unknown');
        assert.equal(await page.isDisabled('#upd-back'), true);
      });
    },
  },
  {
    id: 'SYS-upd-activate',
    ui: ['next'],
    preset: 'update-staged',
    async run(t) {
      const { page, step } = t;
      await step('Restart to update asks with the version, then restarts into it', async () => {
        await open(t);
        await press(page, '#upd-restart');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart to update?');
        assert.equal(await text(page, '#confirm-body'), "Streams in progress stop. If version 20260929.143000 doesn't start, VaporOS goes back by itself.");
        const sent = write(page, 'POST', '/update/activate');
        await page.click('#confirm-ok');
        await sent;
        await page.locator('#scene[open]').waitFor();
        assert.equal(await text(page, '#scene-title'), 'Updating');
      });
      await step('back on the new version: up to date, and the old one is Previous', async () => {
        await page.waitForEvent('load', { timeout: 40000 });
        await t.ready();
        await title(page, 'VaporOS is up to date');
        assert.equal(await text(page, '#upd-run-version'), '20260929.143000');
        assert.equal(await text(page, '#upd-other-version'), '20260929.101500');
        assert.equal(await text(page, '#upd-other-label'), 'Previous');
      });
    },
    allow: DOWN,
  },
  {
    id: 'SYS-upd-check',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('Check now says so while it asks, then that VaporOS is up to date', async () => {
        await open(t);
        await dev(t.server, 'latency', { enabled: true });
        await press(page, '#upd-check');
        await until(page, () => document.getElementById('upd-check-label').textContent === 'Checking…');
        assert.equal(await page.isVisible('#upd-checking'), true);
        await notice(page, 'VaporOS is up to date.');
        await until(page, () => document.getElementById('upd-detail').textContent === 'Version 20260929.101500. Checked just now.');
        assert.equal(await text(page, '#upd-check-label'), 'Check now');
        await dev(t.server, 'latency', { enabled: false });
      });
      await step('a check that cannot reach the server says so', async () => {
        await open(t, 'update-check-failed');
        await press(page, '#upd-check');
        await page.locator('#notices .notice[data-kind="error"]', { hasText: "Couldn't reach the update server. Check the PC's internet connection." }).waitFor();
      });
    },
    allow: [/status of 502/, /502 POST .*\/api\/v1\/update\/check$/],
  },
  {
    id: 'SYS-upd-versions',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the running copy and the previous one, with Go back', async () => {
        await open(t);
        assert.equal(await text(page, '#upd-run-label'), 'Running');
        assert.equal(await text(page, '#upd-run-version'), '20260929.101500');
        assert.equal(await text(page, '#upd-run-sub'), 'Running now');
        assert.equal(await text(page, '#upd-other-label'), 'Previous');
        assert.equal(await text(page, '#upd-other-version'), '20260927.190000');
        assert.equal(await text(page, '#upd-other-sub'), 'Can go back');
        assert.equal(await page.getAttribute('#upd-other', 'data-heat'), 'cool');
        assert.equal(await page.getAttribute('#upd-run', 'data-heat'), 'hot');
        assert.equal(await text(page, '#upd-back'), 'Go back to 20260927.190000');
        assert.equal(await page.isDisabled('#upd-back'), false);
      });
      await step('staged: the other copy is the next restart, never "Previous" (B3), and Go back says why not', async () => {
        await open(t, 'update-staged');
        assert.equal(await text(page, '#upd-other-label'), 'Next restart');
        assert.equal(await text(page, '#upd-other-version'), '20260929.143000');
        assert.equal(await text(page, '#upd-run-sub'), 'Until the next restart');
        assert.equal(await page.isDisabled('#upd-back'), true);
        assert.equal(await page.getAttribute('#upd-back', 'aria-describedby'), 'upd-back-why');
        assert.equal(await text(page, '#upd-back-why'), 'An update is waiting. Restart first.');
      });
      await step('while an update is written, Go back waits', async () => {
        await open(t, 'update-staging');
        assert.equal(await text(page, '#upd-back-why'), 'Wait for the update to finish.');
      });
    },
  },
  {
    id: 'SYS-upd-rollback',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('Go back asks, then offers to restart now', async () => {
        await open(t);
        await press(page, '#upd-back');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Go back to version 20260927.190000?');
        assert.equal(await text(page, '#confirm-body'), 'VaporOS starts version 20260927.190000 from the next restart on. Your games and settings stay.');
        const sent = write(page, 'POST', '/update/rollback');
        await page.click('#confirm-ok');
        await sent;
        await until(page, () => document.getElementById('confirm-title').textContent === 'Restart now?');
        assert.equal(await text(page, '#confirm-cancel'), 'Later');
      });
      await step('Later keeps the waiting rollback on the page (B3), after a reload too', async () => {
        await page.click('#confirm-cancel');
        await closed(page, 'confirm');
        await title(page, 'Version 20260927.190000 starts on the next restart');
        assert.equal(await text(page, '#upd-other-label'), 'Next restart');
        assert.equal(await text(page, '#upd-back-why'), 'Version 20260927.190000 already starts on the next restart.');
        await page.reload();
        await t.ready();
        assert.equal(await text(page, '#upd-title'), 'Version 20260927.190000 starts on the next restart');
        assert.deepEqual(await banners(page), []);
      });
    },
  },
  {
    id: 'SYS-upd-auto',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the switch applies at once', async () => {
        await open(t);
        assert.equal(await page.isChecked('#upd-auto'), true);
        const body = write(page, 'PUT', '/update/settings');
        await press(page, '#upd-auto');
        assert.deepEqual(await body, { auto: 'off' });
        await page.reload();
        await t.ready();
        await until(page, () => !document.getElementById('upd-auto').disabled);
        assert.equal(await page.isChecked('#upd-auto'), false);
      });
      await step('a refused change flips back and says why', async () => {
        await page.route('**/api/v1/update/settings', (route) => route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"config: disk full"}' }));
        await press(page, '#upd-auto');
        await page.locator('#notices .notice[data-kind="error"]', { hasText: 'Config: disk full' }).waitFor();
        assert.equal(await page.isChecked('#upd-auto'), false);
      });
    },
    allow: [/status of 500/, /500 PUT .*\/api\/v1\/update\/settings$/],
  },
  {
    id: 'SYS-upd-channel',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('Other… asks for a name, checked like the server (D2)', async () => {
        await open(t);
        assert.equal(await page.inputValue('#upd-channel-pick'), 'main');
        assert.equal(await page.isDisabled('#upd-channel-save'), true);
        await page.selectOption('#upd-channel-pick', 'other');
        await page.locator('#upd-channel-name').waitFor();
        assert.equal(await page.evaluate(() => document.activeElement.id), 'upd-channel-name');
        await page.fill('#upd-channel-name', '-beta');
        await press(page, '#upd-channel-save');
        assert.equal(await text(page, '#upd-channel-name-error'), 'Use letters, digits, dots, dashes or underscores (at most 128), not starting with a dot or dash.');
        assert.equal(await page.getAttribute('#upd-channel-name', 'aria-invalid'), 'true');
        assert.equal(await page.evaluate(() => document.activeElement.id), 'upd-channel-name');
      });
      await step('a good name saves, says which channel it follows now, and becomes an option', async () => {
        await page.fill('#upd-channel-name', 'beta');
        const body = write(page, 'PUT', '/update/settings');
        await press(page, '#upd-channel-save');
        assert.deepEqual(await body, { channel: 'beta' });
        await notice(page, 'Saved. VaporOS now follows the beta test channel.');
        await until(page, () => document.getElementById('upd-channel').textContent === 'Test channel beta');
        assert.equal(await page.inputValue('#upd-channel-pick'), 'beta');
        assert.equal(await text(page, '#upd-channel-pick option[value="beta"]'), 'beta (test builds)');
      });
      await step('back to main', async () => {
        await page.selectOption('#upd-channel-pick', 'main');
        await press(page, '#upd-channel-save');
        await notice(page, 'Saved.');
        await until(page, () => document.getElementById('upd-channel').textContent === 'Channel main');
      });
    },
  },
  {
    id: 'SYS-upd-failed',
    ui: ['next'],
    preset: 'update-failed-newer',
    async run(t) {
      const { page, step } = t;
      await step('the versions that didn\'t start, newest first, and the cold copy that can\'t be gone back to', async () => {
        await open(t);
        assert.equal(await page.isVisible('#upd-failed'), true);
        assert.equal(await text(page, '#upd-failed-h'), "Versions that didn't start");
        assert.deepEqual(await page.$$eval('#upd-failed-list li', (els) => els.map((e) => e.textContent)), ['20260929.143000', '20260921.083000']);
        assert.equal(await page.getAttribute('#upd-other', 'data-heat'), 'cold');
        assert.equal(await text(page, '#upd-other-sub'), "Didn't start before");
        assert.equal(await text(page, '#upd-back-why'), "Version 20260929.143000 couldn't start, so you can't go back to it.");
        assert.equal(await page.isDisabled('#upd-back'), true);
      });
    },
  },
  {
    id: 'SYS-upd-held',
    ui: ['next'],
    preset: 'update-held',
    async run(t) {
      const { page, server, step } = t;
      const held = 'You went back from version 20260929.143000, so automatic updates skip it and anything older. Newer versions still install.';
      await step('the held hint', async () => {
        await open(t);
        assert.deepEqual(await banners(page), [['hint', held]]);
      });
      await step('an update.state without held clears it: replace, don\'t merge', async () => {
        await live(page);
        await dev(server, 'event', { topic: 'update.state', data: { booted: '20260929.101500', staged: null, failed: ['20260921.083000'], available: null, checked: '2026-09-29T10:00:00Z', last_error: '' } });
        await until(page, () => document.querySelectorAll('#upd-banners .upd-banner').length === 0);
      });
    },
  },
  {
    id: 'SYS-upd-stopped-live',
    ui: ['next'],
    preset: 'update-trial',
    async run(t) {
      const { page, step } = t;
      const trial = "Couldn't install the update. The running version is still being checked after the last update. Try again once it has fully started.";
      await step('Download on a version still on trial: the refusal shows, live', async () => {
        await open(t);
        await live(page);
        await press(page, '#upd-stage');
        await until(page, () => document.querySelectorAll('#upd-banners .upd-banner[data-tone="danger"]').length === 1);
        assert.deepEqual(await banners(page), [['danger', trial]]);
        await title(page, 'Version 20260929.143000 is available');
      });
      await step('after a reload it is gone: a replayed error is ignored', async () => {
        await page.reload();
        await t.ready();
        await page.waitForTimeout(600);
        assert.deepEqual(await banners(page), []);
      });
    },
  },
];
