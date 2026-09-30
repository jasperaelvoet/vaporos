// Flows over the System index (spec-cc-screens §6): the nameplate, a row
// per sub-page with its live summary, and Restart, Power off and Sign out.
// The hold itself is the shell's (shell.spec.mjs: SHELL-scene,
// SHELL-hold-*). Each ID is a row of tools/web/e2e/parity.json (owner C4).
// See legacy.spec.mjs for the flow format.

import assert from 'node:assert/strict';

import { closed, dev } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const until = (page, fn, arg) => page.waitForFunction(fn, arg, { timeout: 5000 });
// live waits for the event stream and its replay window (core/live.js: the
// first 400 ms after it opens are the replay), so a /__dev event is live.
const live = async (page) => {
  await until(page, () => document.getElementById('link').dataset.link === 'live');
  await page.waitForTimeout(500);
};
const sum = (page, id) => text(page, `#sum-${id}`);

async function open(t, path = '/system') {
  await t.page.goto(t.url(path));
  await t.ready();
}

// A failed GET, as the browser logs it.
const FAILED_STATUS = [/status of 500/, /500 GET .*\/api\/v1\/status$/];

export default [
  {
    id: 'SYS-index-rows',
    ui: ['next'],
    allow: FAILED_STATUS,
    async run(t) {
      const { page, server, step } = t;
      await step('the nameplate names this PC, its address, version and uptime', async () => {
        await open(t);
        assert.equal(await text(page, '#plate-name'), 'vapor');
        assert.equal(await text(page, '#plate-addr'), 'vapor.local');
        assert.match(await text(page, '#plate-meta'), /^VaporOS 20260929\.101500 · up\s\d+\s(d|h|min)/);
        assert.equal(await text(page, '#plate-word'), 'ready');
      });
      await step('each row says what its page has to say now', async () => {
        assert.equal(await sum(page, 'updates'), 'Up to date');
        assert.equal(await sum(page, 'power'), 'Powers off after 15 min idle');
        assert.equal(await sum(page, 'storage'), '612 GB free on the system drive');
        assert.equal(await page.getAttribute('#row-updates', 'data-tone'), null);
      });
      await step('a row is one link whose name carries its summary', async () => {
        // "Updates, Up to date" (the comma is for screen readers only).
        const named = (name, summary) => page.getByRole('link', { name: new RegExp(`^${name}\\s*,\\s*${summary}$`) });
        assert.equal(await named('Updates', 'Up to date').getAttribute('href'), '/system/updates');
        for (const [name, summary, href] of [['Settings', 'Name, password and remote access', '/system/settings'], ['Logs', 'Stream server log', '/system/logs'], ['About', 'Hardware and versions', '/system/about']]) {
          assert.equal(await named(name, summary).getAttribute('href'), href);
        }
        const nav = page.getByRole('navigation', { name: 'System pages' });
        assert.equal(await nav.getByRole('link').count(), 6);
      });
      await step('a live download moves the Updates row without a request', async () => {
        await live(page);
        const asked = [];
        page.on('request', (r) => r.url().includes('/api/v1/update') && asked.push(r.url()));
        await dev(server, 'event', { topic: 'update.progress', data: { phase: 'download', percent: 42, bytes: 1, total: 2, version: '20260930.080000' } });
        await until(page, () => document.getElementById('sum-updates').textContent === 'Updating · 42%');
        assert.equal(await page.getAttribute('#row-updates', 'data-tone'), 'hot');
        assert.deepEqual(asked, []);
      });
      await step('an idle countdown shows on the Power row', async () => {
        await dev(server, 'event', { topic: 'power.idle', data: { idle_seconds: 780, shutdown_in: 120 } });
        await until(page, () => document.getElementById('sum-power').textContent === 'Powers off in 2 min');
        assert.equal(await page.getAttribute('#row-power', 'data-tone'), 'hot');
      });
      await step('a downloaded update reads "Version <v> is ready", with its pip', async () => {
        await dev(server, 'preset', { name: 'update-staged' });
        await open(t);
        assert.equal(await sum(page, 'updates'), 'Version 20260929.143000 is ready');
        assert.equal(await page.getAttribute('#row-updates', 'data-tone'), 'hot');
        // A staged update leaves the box ready (MASTER-PLAN §1.3): the row says it, the plate stays at ready.
        assert.equal(await text(page, '#plate-word'), 'ready');
      });
      await step('an almost full system drive says so in words and goes cold', async () => {
        await dev(server, 'preset', { name: 'disk-low' });
        await open(t);
        assert.equal(await sum(page, 'storage'), '38 GB free on the system drive · almost full');
        assert.equal(await page.getAttribute('#row-storage', 'data-tone'), 'cold');
      });
      await step('when GET /status fails, each row says so and still opens its page', async () => {
        await page.route('**/api/v1/status', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"boom"}' }));
        await open(t);
        for (const id of ['updates', 'power', 'storage']) assert.equal(await sum(page, id), "Couldn't load");
        assert.equal(await text(page, '#plate-meta'), "Couldn't load this PC's details.");
        await page.getByRole('link', { name: /^Storage\s*,/ }).click();
        await page.waitForURL((u) => new URL(u).pathname === '/system/storage');
      });
    },
  },
  {
    id: 'SYS-index-power',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('Restart and Power off sit under their hint, which says streams stop', async () => {
        await open(t);
        assert.equal(await page.isEnabled('#sys-reboot'), true);
        assert.equal(await page.isEnabled('#sys-poweroff'), true);
        assert.equal(await text(page, '#sys-hold-hint'), 'Hold to confirm, or tap to be asked first. Streams in progress stop.');
        assert.equal(await page.getAttribute('#sys-reboot', 'aria-describedby'), 'sys-hold-hint');
      });
      await step('a tap on Restart asks first (C-reboot), and Cancel changes nothing', async () => {
        await page.click('#sys-reboot');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart VaporOS?');
        assert.equal(await text(page, '#confirm-body'), 'Streams in progress stop. VaporOS is back in about a minute.');
        await page.click('#confirm-cancel');
        await closed(page, 'confirm');
        assert.equal(await page.locator('#scene[open]').count(), 0);
      });
      await step('a tap on Power off asks with the danger confirm (C-poweroff)', async () => {
        await page.click('#sys-poweroff');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Power off VaporOS?');
        assert.equal(await page.getAttribute('#confirm', 'data-tone'), 'danger');
        await page.keyboard.press('Escape');
        await closed(page, 'confirm');
      });
      const phone = (page.viewportSize()?.width ?? 0) < 1024;
      await step(phone ? 'Sign out sits at the foot of the page on a phone' : 'Sign out lives in the rail on a desktop', async () => {
        assert.equal(await page.isVisible('#sys-signout'), phone);
        assert.equal(await page.isVisible('#signout'), !phone);
        await page.click(phone ? '#sys-signout' : '#signout');
        await page.waitForURL((u) => new URL(u).pathname === '/login');
      });
    },
  },
];
