// Flows over System › Extensions (docs/CONTRACTS.md "Extensions"): the
// cards and their chips, Install with the VaporOS password, a download the
// event stream alone moves on, Restart now, Needs attention and Try again,
// Remove with its data, settings, actions, a start without extensions and
// the row on System. The words a card should show come from the fixtures
// through ext.js, the page's own pure module, so the flows hold for any
// catalogue the fixtures carry. The IDs have no parity row (BEYOND_PARITY
// in e2e/parity.mjs): the eight-page UI never had extensions. See
// legacy.spec.mjs for the flow format.

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { assertAxe } from '../lib/checks.mjs';
import { closed, dev, request, write } from '../lib/dev.mjs';
import { mergePatch } from '../../../../internal/web/jstest/lib/fixtures.mjs';
import * as X from '../../../../internal/web/static/js/ext.js';

const FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', '..', 'internal', 'web', 'fixtures');
const json = (p) => JSON.parse(readFileSync(join(FIXTURES, p), 'utf8'));

// doc is GET /extensions as the dev server starts preset name.
function doc(name = 'idle') {
  const patch = json(`presets/${name}.json`).patch?.extensions;
  return patch ? mergePatch(json('base/extensions.json'), patch) : json('base/extensions.json');
}
const ctx = (d) => ({ names: X.names(d), enabled: new Set(d.extensions.filter((x) => x.wanted || x.mounted || x.core).map((x) => x.id)) });

const text = async (loc) => ((await loc.textContent()) ?? '').trim();
const texts = async (loc) => (await loc.allTextContents()).map((s) => s.trim());
const until = (page, fn, arg, timeout = 5000) => page.waitForFunction(fn, arg, { timeout });
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const card = (page, name) => page.locator('.ext-card', { has: page.locator('.ext-name', { hasText: new RegExp(`^${esc(name)}$`) }) });
const notice = (page, words) => page.locator('#notices .notice', { hasText: words }).waitFor();

async function open(t, path = '/system/extensions') {
  await t.page.goto(t.url(path));
  await t.ready();
}

// gets counts the page's GET /extensions from now on.
function gets(page) {
  const seen = [];
  page.on('request', (r) => r.method() === 'GET' && new URL(r.url()).pathname === '/api/v1/extensions' && seen.push(r.url()));
  return seen;
}

const WRONG = [/status of 403/, /403 POST .*\/api\/v1\/extensions\//];
// The restart takes the dev server down for a few seconds.
const DOWN = [/Failed to load resource/, /net::ERR_/, /\/api\/v1\/(ping|events|system|auth\/me|status|extensions)/, /ERR_EMPTY_RESPONSE|ERR_CONNECTION/];

export default [
  {
    id: 'SYS-ext-cards',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      const d = doc();
      await step('the intro says what an extension is, and nothing waits for a restart', async () => {
        await open(t);
        assert.match(await text(page.locator('.ext-intro')), /^Extensions add software that isn't part of VaporOS\. Each one is a sealed, read-only package/);
        assert.equal(await page.isVisible('#ext-restart'), false);
        assert.equal(await page.isVisible('#restart-row'), false);
      });
      await step('a card per extension: name, upstream, licence and size, and its state in plain words', async () => {
        assert.equal(await page.locator('#ext-list > .ext-card').count(), d.extensions.length);
        for (const x of d.extensions) {
          const c = card(page, x.name);
          assert.equal(await text(c.locator('.ext-from')), X.from(x), x.id);
          assert.equal(await text(c.locator('.ext-summary')), x.summary, x.id);
          assert.equal(await c.locator('.ext-chip').isVisible(), false, `${x.id}: no chip for a fact`);
          assert.equal(await text(c.locator('.ext-plain')), X.plain(x), x.id);
        }
      });
      await step('a core extension is always on: no Install, no Remove', async () => {
        const core = d.extensions.find((x) => x.core);
        const c = card(page, core.name);
        assert.equal(await text(c.locator('.ext-plain')), 'Always on');
        assert.equal(await c.getByRole('button', { name: /^(Install|Remove)\b/ }).count(), 0);
        assert.deepEqual(await texts(c.locator('.ext-line')), X.lines(core, ctx(d)).map((l) => l.text));
      });
      await step('What it can do and what it downloads open from each card', async () => {
        for (const x of d.extensions) {
          const c = card(page, x.name);
          await c.locator('.ext-more-summary').click();
          assert.deepEqual(await texts(c.locator('[data-part="can"] .ext-fact')), X.can(x), x.id);
          assert.deepEqual(await texts(c.locator('[data-part="downloads"] .ext-fact')), X.downloads(x).map((l) => l.text), x.id);
          assert.deepEqual(await texts(c.locator('[data-part="caveats"] .ext-fact')), x.caveats || [], x.id);
        }
        const unchecked = d.extensions.find((x) => X.downloads(x).some((l) => l.warn));
        assert.match((await texts(card(page, unchecked.name).locator('[data-part="downloads"] .ext-fact[data-tone="warning"]')))[0], /VaporOS can't check these files\.$/);
      });
      await step('every extension that is not in yet offers Install, named for it', async () => {
        for (const x of d.extensions.filter(X.canInstall)) {
          assert.equal(await card(page, x.name).getByRole('button', { name: `Install ${x.name}` }).isVisible(), true, x.id);
        }
      });
    },
  },
  {
    id: 'SYS-ext-install',
    ui: ['next'],
    allow: WRONG,
    async run(t) {
      const { page, step } = t;
      const d = doc();
      const x = d.extensions.find((y) => X.canInstall(y) && y.needs_password);
      const c = card(page, x.name);
      let asked;
      await step('Install asks first: what it can do, when it comes, and the VaporOS password', async () => {
        await open(t);
        asked = gets(page);
        await c.getByRole('button', { name: `Install ${x.name}` }).click();
        await page.locator('#ext-dialog[open]').waitFor();
        assert.equal(await text(page.locator('#ext-dialog-title')), `Install ${x.name}?`);
        assert.equal(await text(page.locator('#ext-dialog-body')), (x.copy && x.copy.install) || x.summary);
        assert.deepEqual(await texts(page.locator('#ext-dialog-can .ext-fact')), X.can(x));
        assert.match(await text(page.locator('#ext-dialog-note')), /added at the next restart/);
        assert.equal(await page.isVisible('#ext-dialog-pw'), true);
        assert.equal(await page.isVisible('#ext-dialog-purge-row'), false);
        assert.equal(await page.evaluate(() => document.activeElement.id), 'ext-dialog-pw');
        assert.equal(await text(page.locator('#ext-dialog-ok')), 'Install');
        await assertAxe(page, 'the install dialog open');
      });
      await step('no password, then a wrong one, are said under the field', async () => {
        await page.click('#ext-dialog-ok');
        assert.equal(await text(page.locator('#ext-dialog-pw-error')), 'Enter the VaporOS password.');
        await page.fill('#ext-dialog-pw', 'not-the-password');
        await page.press('#ext-dialog-pw', 'Enter');
        await until(page, () => document.getElementById('ext-dialog-pw-error').textContent === "That's not the VaporOS password.");
        assert.equal(await page.getAttribute('#ext-dialog-pw', 'aria-invalid'), 'true');
        assert.equal(await page.locator('#ext-dialog[open]').count(), 1);
      });
      await step('the right password installs: the card downloads, then waits for a restart, all by events', async () => {
        const body = write(page, 'POST', `/extensions/${x.id}`);
        await page.fill('#ext-dialog-pw', 'vaporvapor');
        await page.click('#ext-dialog-ok');
        assert.deepEqual(await body, { password: 'vaporvapor' });
        await closed(page, 'ext-dialog');
        await notice(page, `${x.name} is downloading. It's added at the next restart.`);
        assert.match(await text(c.locator('.ext-chip')), /^(Installing( · \d+%)?|Restart needed)$/);
        await until(page, (n) => [...document.querySelectorAll('.ext-card')].some((li) => li.querySelector('.ext-name').textContent === n && li.querySelector('.ext-chip').textContent === 'Restart needed'), x.name, 15000);
        assert.equal(await c.getAttribute('data-state'), 'restart-needed');
        assert.deepEqual(await texts(c.locator('.ext-line')), ['It is added at the next restart.']);
        assert.deepEqual(asked, [], 'no GET /extensions after the first: the event stream carried it');
      });
      await step('the restart card names it, and says VaporOS restarts by itself', async () => {
        await page.locator('#ext-restart').waitFor();
        assert.equal(await text(page.locator('#ext-restart-text')), `${x.name} is added at the next restart.`);
        // A restart that is newly needed may happen by itself (restart.auto).
        assert.equal(await page.isVisible('#ext-restart-auto'), true);
        assert.equal(await c.getByRole('button', { name: `Remove ${x.name}` }).isVisible(), true, 'it can still be called off before the restart');
        assert.equal(await c.getByRole('button', { name: `Install ${x.name}` }).count(), 0);
      });
    },
  },
  {
    id: 'SYS-ext-restart',
    ui: ['next'],
    preset: 'extensions-restart',
    allow: DOWN,
    async run(t) {
      const { page, step } = t;
      const d = doc('extensions-restart');
      const waiting = d.extensions.filter((x) => x.state === 'restart-needed');
      await step('Restart needed on each card, one restart card for all, and no second restart row', async () => {
        await open(t);
        for (const x of waiting) {
          assert.equal(await text(card(page, x.name).locator('.ext-chip')), 'Restart needed');
          assert.equal(await card(page, x.name).locator('.ext-chip').getAttribute('data-state'), 'restart-needed');
        }
        assert.equal(await text(page.locator('#ext-restart-text')), X.restartText(d));
        assert.equal(await page.isVisible('#ext-restart-auto'), !!d.restart.auto);
        assert.equal(await page.isVisible('#restart-row'), false, 'the page says it itself');
        assert.equal(await page.locator('.tab[data-tab="system"] [data-part="badge"]').isVisible(), true);
      });
      await step('Restart now asks first (C-reboot); Cancel changes nothing', async () => {
        await page.click('#ext-restart-go');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page.locator('#confirm-title')), 'Restart VaporOS?');
        await page.click('#confirm-cancel');
        await closed(page, 'confirm');
        assert.equal(await page.locator('#scene[open]').count(), 0);
      });
      await step('Restart restarts, and the page comes back with them installed', async () => {
        const post = request(page, 'POST', '/system/reboot');
        await page.click('#ext-restart-go');
        await page.click('#confirm-ok');
        await post;
        await page.locator('#scene[open]').waitFor();
        await page.waitForEvent('load', { timeout: 30000 });
        await t.ready();
        for (const x of waiting) {
          const c = card(page, x.name);
          assert.equal(await c.locator('.ext-chip').isVisible(), false);
          assert.equal(await text(c.locator('.ext-plain')), 'Installed');
        }
        assert.equal(await page.isVisible('#ext-restart'), false);
      });
    },
  },
  {
    id: 'SYS-ext-attention',
    ui: ['next'],
    preset: 'extensions-attention',
    async run(t) {
      const { page, step } = t;
      const d = doc('extensions-attention');
      const bad = d.extensions.filter((x) => x.state === 'needs-attention');
      await step('Needs attention is the fault: its cold chip, hazard edge and the reason in words', async () => {
        await open(t);
        assert.ok(bad.length > 0);
        for (const x of bad) {
          const c = card(page, x.name);
          assert.equal(await text(c.locator('.ext-chip')), 'Needs attention');
          assert.equal(await c.getAttribute('data-state'), 'fault');
          assert.equal(await text(c.locator('.ext-reason')), X.chip(x).reason);
          assert.equal(await c.getByRole('button', { name: `Try again to install ${x.name}` }).isVisible(), true);
        }
        await assertAxe(page, 'a card that needs attention');
      });
      await step('an installed extension shows its status lines and opens its own page in a new tab', async () => {
        for (const x of d.extensions.filter((y) => y.mounted && y.web)) {
          const c = card(page, x.name);
          assert.deepEqual(await texts(c.locator('.ext-line')), X.lines(x, ctx(d)).map((l) => l.text));
          const link = c.getByRole('link', { name: new RegExp(`^${esc(X.webLabel(x))}`) });
          assert.equal(await link.getAttribute('href'), `http://vapor.local:${x.web.port}/`);
          assert.equal(await link.getAttribute('target'), '_blank');
          assert.equal(await link.getAttribute('rel'), 'noopener noreferrer');
          assert.equal(await page.locator('iframe').count(), 0);
        }
      });
      await step('Try again asks the box once more, and the card downloads again', async () => {
        const x = bad[0];
        const post = request(page, 'POST', `/extensions/${x.id}/retry`);
        await card(page, x.name).getByRole('button', { name: `Try again to install ${x.name}` }).click();
        await post;
        await until(page, (n) => [...document.querySelectorAll('.ext-card')].some((li) => li.querySelector('.ext-name').textContent === n && /^(Installing|Restart needed)/.test(li.querySelector('.ext-chip').textContent)), x.name);
        assert.equal(await card(page, x.name).locator('.ext-reason').isVisible(), false);
      });
    },
  },
  {
    id: 'SYS-ext-remove',
    ui: ['next'],
    preset: 'extensions-attention',
    async run(t) {
      const { page, step } = t;
      const d = doc('extensions-attention');
      const x = d.extensions.find((y) => y.mounted && !y.core && X.removal(y, ctx(d)).show && !X.removal(y, ctx(d)).why);
      const c = card(page, x.name);
      await step('Remove asks with the danger confirm, and offers to delete its data', async () => {
        await open(t);
        await c.getByRole('button', { name: `Remove ${x.name}` }).click();
        await page.locator('#ext-dialog[open]').waitFor();
        assert.equal(await text(page.locator('#ext-dialog-title')), `Remove ${x.name}?`);
        assert.equal(await page.getAttribute('#ext-dialog', 'data-tone'), 'danger');
        assert.equal(await page.getAttribute('#ext-dialog', 'role'), 'alertdialog');
        assert.equal(await page.evaluate(() => document.activeElement.id), 'ext-dialog-cancel');
        assert.equal(await page.isVisible('#ext-dialog-pw'), false);
        assert.equal(await page.isChecked('#ext-dialog-purge'), false);
        assert.equal(await text(page.locator('#ext-dialog-purge-row')), 'Also delete its settings and downloads');
        await assertAxe(page, 'the remove dialog open');
      });
      await step('Cancel sends nothing', async () => {
        let sent = 0;
        page.on('request', (r) => r.method() === 'DELETE' && sent++);
        await page.click('#ext-dialog-cancel');
        await closed(page, 'ext-dialog');
        assert.equal(sent, 0);
      });
      await step('with its data: purge=1, and it goes at the next restart', async () => {
        await c.getByRole('button', { name: `Remove ${x.name}` }).click();
        await page.check('#ext-dialog-purge');
        const del = request(page, 'DELETE', `/extensions/${x.id}`);
        await page.click('#ext-dialog-ok');
        assert.equal(new URL((await del).url()).searchParams.get('purge'), '1');
        await notice(page, `${x.name} is removed at the next restart.`);
        await until(page, (n) => [...document.querySelectorAll('.ext-card')].some((li) => li.querySelector('.ext-name').textContent === n && li.querySelector('.ext-chip').textContent === 'Restart needed'), x.name);
        assert.ok((await texts(c.locator('.ext-line'))).includes('It is removed at the next restart.'));
        assert.equal(await page.isVisible('#ext-restart'), true);
      });
      await step('the core extension is never removed', async () => {
        const core = d.extensions.find((y) => y.core);
        assert.equal(await card(page, core.name).getByRole('button', { name: /^Remove/ }).count(), 0);
      });
    },
  },
  {
    id: 'SYS-ext-settings',
    ui: ['next'],
    preset: 'extensions-attention',
    async run(t) {
      const { page, step } = t;
      const d = doc('extensions-attention');
      const x = d.extensions.find((y) => y.mounted && (y.settings || []).some((s) => s.type === 'choice') && (y.settings || []).some((s) => s.type === 'bool' && s.restart));
      const choice = x.settings.find((s) => s.type === 'choice');
      const flag = x.settings.find((s) => s.type === 'bool' && s.restart);
      await step('a choice saves at once, without a password', async () => {
        await open(t);
        const pick = choice.choices.find((c) => c !== choice.value);
        const body = write(page, 'PUT', `/extensions/${x.id}/settings`);
        await page.getByLabel(choice.label, { exact: true }).selectOption(pick);
        assert.deepEqual(await body, { settings: { [choice.key]: pick } });
        await notice(page, 'Saved.');
        assert.equal(await page.getByLabel(choice.label, { exact: true }).inputValue(), pick);
      });
      await step('a setting that needs a restart says so, and asks for the password first', async () => {
        const sw = page.getByRole('switch', { name: flag.label });
        assert.equal(await sw.isChecked(), flag.value === true);
        assert.match(await text(page.locator(`#${await sw.getAttribute('aria-describedby')}`)), /Takes effect after a restart\.$/);
        await sw.click({ force: true });
        await page.locator('#ext-dialog[open]').waitFor();
        assert.equal(await text(page.locator('#ext-dialog-title')), `Change ${flag.label}?`);
        const body = write(page, 'PUT', `/extensions/${x.id}/settings`);
        await page.fill('#ext-dialog-pw', 'vaporvapor');
        await page.click('#ext-dialog-ok');
        assert.deepEqual(await body, { settings: { [flag.key]: flag.value !== true }, password: 'vaporvapor' });
        await notice(page, 'Saved. Takes effect after a restart.');
        assert.equal(await sw.isChecked(), flag.value !== true);
      });
      await step('Cancel puts the switch back', async () => {
        const sw = page.getByRole('switch', { name: flag.label });
        const before = await sw.isChecked();
        await sw.click({ force: true });
        await page.locator('#ext-dialog[open]').waitFor();
        await page.click('#ext-dialog-cancel');
        await closed(page, 'ext-dialog');
        assert.equal(await sw.isChecked(), before);
      });
    },
  },
  {
    id: 'SYS-ext-action',
    ui: ['next'],
    preset: 'extensions-attention',
    async run(t) {
      const { page, step } = t;
      const d = doc('extensions-attention');
      const x = d.extensions.find((y) => y.mounted && (y.actions || []).some((a) => a.confirm));
      const a = x.actions.find((y) => y.confirm);
      await step("an action asks with its own words, then runs", async () => {
        await open(t);
        await card(page, x.name).getByRole('button', { name: `${a.label} (${x.name})` }).click();
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page.locator('#confirm-title')), a.confirm.title);
        assert.equal(await text(page.locator('#confirm-body')), a.confirm.body);
        assert.equal(await text(page.locator('#confirm-ok')), a.confirm.button);
        const post = request(page, 'POST', `/extensions/${x.id}/actions/${a.name}`);
        await page.click('#confirm-ok');
        await post;
        await notice(page, `${a.label}: done.`);
      });
      await step('an extension that is not installed offers none of its actions', async () => {
        for (const y of d.extensions.filter((z) => !z.mounted)) {
          for (const b of y.actions || []) assert.equal(await card(page, y.name).getByRole('button', { name: `${b.label} (${y.name})` }).count(), 0);
        }
      });
    },
  },
  {
    id: 'SYS-ext-skip',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the next start can leave every extension out, after a question', async () => {
        await open(t);
        await page.click('#ext-skip');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page.locator('#confirm-title')), 'Start once without extensions?');
        const post = request(page, 'POST', '/extensions/skip-once');
        await page.click('#confirm-ok');
        await post;
        await notice(page, 'The next start leaves extensions out.');
      });
    },
  },
  {
    id: 'SYS-ext-row',
    ui: ['next'],
    preset: 'extensions-installing',
    async run(t) {
      const { page, server, step } = t;
      await step('the Extensions row on System says what is installed and what is under way', async () => {
        await open(t, '/system');
        await until(page, () => /^\d+ installed|^None installed/.test(document.getElementById('sum-extensions').textContent));
        const busy = doc('extensions-installing').extensions.find((x) => x.state === 'installing');
        assert.match(await text(page.locator('#sum-extensions')), new RegExp(`^1 installed · (installing ${esc(busy.name)}( · \\d+%)?|restart needed)$`));
        assert.equal(await page.getAttribute('#row-extensions', 'href'), '/system/extensions');
        assert.equal(await page.getAttribute('#row-extensions', 'data-tone'), 'hot');
      });
      await step('the event stream moves it on, without asking again', async () => {
        const asked = gets(page);
        await until(page, () => document.getElementById('sum-extensions').textContent === '1 installed · restart needed', null, 15000);
        assert.deepEqual(asked, []);
      });
      await step('a fault shows on the row in words and cold', async () => {
        await dev(server, 'preset', { name: 'extensions-attention' });
        await open(t, '/system');
        await until(page, () => document.getElementById('sum-extensions').textContent !== '' && !document.querySelector('#sum-extensions .skel'));
        assert.deepEqual([await text(page.locator('#sum-extensions')), await page.getAttribute('#row-extensions', 'data-tone')], X.rowLine(doc('extensions-attention')));
        await page.getByRole('link', { name: /^Extensions\s*,/ }).click();
        await page.waitForURL((u) => new URL(u).pathname === '/system/extensions');
      });
    },
  },
];
