// Flows over System › Extensions (docs/CONTRACTS.md "Extensions"): the
// cards and their chips, Install with the VaporOS password, a download the
// event stream alone moves on, Restart now, Needs attention and Try again,
// an extension's own page while it runs, Remove with its data and Install
// again before the restart, settings, the drive an extension needs (asked
// for as it installs), actions, a start without extensions, a first read
// that fails and the row on System. The words a card should show come
// from the fixtures through ext.js, the page's own pure module, so the
// flows hold for any catalogue the fixtures carry; each flow first checks
// (need) that they carry what it needs. The IDs have no parity row
// (BEYOND_PARITY in e2e/parity.mjs): the eight-page UI never had
// extensions. See legacy.spec.mjs for the flow format.

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

// need fails a flow at once, saying what its fixtures lack.
function need(v, what) {
  assert.ok(v, `the fixtures lack what this flow needs: ${what}`);
  return v;
}

const text = async (loc) => ((await loc.textContent()) ?? '').trim();
const texts = async (loc) => (await loc.allTextContents()).map((s) => s.trim());
const until = (page, fn, arg, timeout = 5000) => page.waitForFunction(fn, arg, { timeout });
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const card = (page, name) => page.locator('.ext-card', { has: page.locator('.ext-name', { hasText: new RegExp(`^${esc(name)}$`) }) });
const notice = (page, words) => page.locator('#notices .notice', { hasText: words }).first().waitFor();
// chipOf waits until the card named n shows the chip words (or none: '').
const chipOf = (page, n, words, timeout) =>
  until(page, ([name, w]) => [...document.querySelectorAll('.ext-card')].some((li) => {
    const chip = li.querySelector('.ext-chip');
    return li.querySelector('.ext-name').textContent === name && (w ? !chip.hidden && chip.textContent === w : chip.hidden);
  }), [n, words], timeout);
// live waits until the page takes events as news: once the stream is open
// and past its first 400 ms (core/live.js), which count as its replay.
async function live(page) {
  await page.locator('#link[data-link="live"]').waitFor({ state: 'attached' });
  await page.waitForTimeout(500);
}
const badge = (page, on) => until(page, (b) => {
  const el = document.querySelector('.tab[data-tab="system"] [data-part="badge"]');
  return !!el && el.hidden === !b;
}, on);

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

const WRONG = [/status of 403/, /403 (POST|PUT) .*\/api\/v1\/extensions\//];
// The restart takes the dev server down for a few seconds.
const DOWN = [/Failed to load resource/, /net::ERR_/, /\/api\/v1\/(ping|events|system|auth\/me|status|extensions)/, /ERR_EMPTY_RESPONSE|ERR_CONNECTION/];

export default [
  {
    id: 'SYS-ext-cards',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      const d = doc();
      need(d.extensions.length, 'GET /extensions lists no extension');
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
        const core = need(d.extensions.find((x) => x.core), 'a core extension');
        const c = card(page, core.name);
        assert.equal(await text(c.locator('.ext-plain')), 'Always on');
        assert.equal(await c.getByRole('button', { name: /^(Install|Remove)\b/ }).count(), 0);
        assert.deepEqual(await texts(c.locator('.ext-line')), X.lines(core, X.context(d)).map((l) => l.text));
      });
      await step('What it can do and what it downloads open from each card, and say nothing the document does not', async () => {
        for (const x of d.extensions) {
          const c = card(page, x.name);
          const can = X.can(x);
          const dl = X.downloads(x);
          const cav = x.caveats || [];
          if (!can.length && !dl.length && !cav.length) {
            assert.equal(await c.locator('.ext-more').isVisible(), false, `${x.id}: nothing to tell, no fold`);
            continue;
          }
          assert.equal(await text(c.locator('.ext-more-summary')), X.moreLabel(can.length, dl.length), x.id);
          await c.locator('.ext-more-summary').click();
          assert.equal(await c.locator('[data-part="can-box"]').isVisible(), can.length > 0, x.id);
          assert.deepEqual(await texts(c.locator('[data-part="can"] .ext-fact')), can, x.id);
          assert.deepEqual(await texts(c.locator('[data-part="downloads"] .ext-fact')), dl.map((l) => l.text), x.id);
          assert.deepEqual(await texts(c.locator('[data-part="caveats"] .ext-fact')), cav, x.id);
        }
        const unchecked = need(d.extensions.find((x) => X.downloads(x).some((l) => l.warn)), 'a download VaporOS cannot check');
        assert.match((await texts(card(page, unchecked.name).locator('[data-part="downloads"] .ext-fact[data-tone="warning"]')))[0], /VaporOS can't check these files\.$/);
      });
      await step('every extension that is not in yet offers Install, named for it', async () => {
        need(d.extensions.some(X.canInstall), 'an extension that is not installed yet');
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
      const x = need(d.extensions.find((y) => X.canInstall(y) && y.needs_password && !X.required(y).length),
        'an extension to install that asks for the password (needs_password) and no drive (no settings[].required)');
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
        assert.equal(await text(page.locator('#ext-dialog-note')), X.installNote(x, d, X.context(d)));
        assert.match(await text(page.locator('#ext-dialog-note')), /added at the next restart/);
        assert.equal(await page.isVisible('#ext-dialog-choose'), false, 'nothing to pick first');
        assert.equal(await page.isVisible('#ext-dialog-pw'), true);
        assert.equal(await text(page.locator('#ext-dialog-pw-hint')), X.passwordHint(x, X.context(d)) || 'The one you sign in with.');
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
        await notice(page, X.installedText(x));
        assert.match(await text(c.locator('.ext-chip')), /^(Installing( · \d+%)?|Restart needed)$/);
        await chipOf(page, x.name, 'Restart needed', 15000);
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
        // The shell asks /status again once, while the download's events keep coming.
        await badge(page, true);
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
      need(waiting.length && d.restart.needed, 'extensions-restart: a card waiting for a restart, and restart.needed');
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
      await step('with the next start left without extensions, the cards and the restart card say the change waits for the restart after it', async () => {
        await live(page);
        await page.click('#ext-skip');
        await page.click('#confirm-ok');
        await until(page, () => /^The next start is without extensions\./.test(document.getElementById('ext-restart-text').textContent));
        const skip = { ...d, skip_once: true };
        for (const x of waiting) assert.deepEqual(await texts(card(page, x.name).locator('.ext-line')), X.lines(x, X.context(skip)).map((l) => l.text));
        assert.match(await text(page.locator('#ext-restart-text')), /Restart again after it to finish adding /);
        assert.equal(await page.isVisible('#ext-restart-auto'), false, 'VaporOS does not restart by itself for it');
        await page.click('#ext-skip-cancel');
        await until(page, (want) => document.getElementById('ext-restart-text').textContent === want, X.restartText(d));
        assert.equal(await page.isVisible('#ext-restart-auto'), !!d.restart.auto);
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
      const { page, server, step } = t;
      const d = doc('extensions-attention');
      const bad = d.extensions.filter((x) => x.state === 'needs-attention');
      need(bad.length, 'extensions-attention: a card that needs attention');
      const site = need(d.extensions.find((y) => y.mounted && (y.wanted || y.core) && y.web && y.web.port),
        'extensions-attention: a running extension with its own web page (web.port)');
      await step('Needs attention is the fault: its cold chip, hazard edge and the reason in words', async () => {
        await open(t);
        for (const x of bad) {
          const c = card(page, x.name);
          assert.equal(await text(c.locator('.ext-chip')), 'Needs attention');
          assert.equal(await c.getAttribute('data-state'), 'fault');
          assert.equal(await text(c.locator('.ext-reason')), X.chip(x).reason);
          assert.equal(await c.getByRole('button', { name: `Try again to install ${x.name}` }).isVisible(), true);
        }
        await assertAxe(page, 'a card that needs attention');
      });
      await step('an installed extension shows its status lines, and Open only while its own page runs (web_running)', async () => {
        const c = card(page, site.name);
        assert.deepEqual(await texts(c.locator('.ext-line')), X.lines(site, X.context(d)).map((l) => l.text));
        const link = c.getByRole('link', { name: new RegExp(`^${esc(X.webLabel(site))}`) });
        assert.equal(await link.count(), X.webLink(site, 'vapor.local') ? 1 : 0, 'as the document says');
        // The event stream says when its page starts and stops.
        await live(page);
        const says = async (patch) => {
          const next = structuredClone(d);
          Object.assign(next.extensions.find((y) => y.id === site.id), patch);
          await dev(server, 'event', { topic: 'extensions.state', data: next });
          const on = !!X.webLink(next.extensions.find((y) => y.id === site.id), 'vapor.local');
          const install = X.canInstall(next.extensions.find((y) => y.id === site.id));
          await until(page, ([n, a, i]) => [...document.querySelectorAll('.ext-card')].some((li) => li.querySelector('.ext-name').textContent === n &&
            !!li.querySelector('.ext-actions a[href]') === a && [...li.querySelectorAll('.ext-actions button')].some((b) => b.textContent.startsWith('Install')) === i), [site.name, on, install]);
          return on;
        };
        assert.equal(await says({ web_running: true }), true);
        assert.equal(await link.getAttribute('href'), `http://vapor.local:${site.web.port}/`);
        assert.equal(await link.getAttribute('target'), '_blank');
        assert.equal(await link.getAttribute('rel'), 'noopener noreferrer');
        assert.equal(await page.locator('iframe').count(), 0);
        assert.equal(await says({ web_running: false }), false, 'its page is down: no link to it');
        await says({ web_running: true });
        // Removed until the restart (Install again on offer), its page is not offered either.
        assert.equal(await says({ web_running: true, wanted: false, state: 'restart-needed' }), false);
        assert.equal(await link.count(), 0);
        await says({});
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
      const ctx = X.context(d);
      const x = need(d.extensions.find((y) => y.mounted && !y.core && X.removal(y, ctx).show && !X.removal(y, ctx).why && !X.required(y).length),
        'extensions-attention: a running extension the user added that nothing needs, and that needs no drive');
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
        await chipOf(page, x.name, 'Restart needed');
        assert.ok((await texts(c.locator('.ext-line'))).includes('It is removed at the next restart.'));
        assert.equal(await page.isVisible('#ext-restart'), true);
        assert.equal(await c.getByRole('button', { name: `Remove ${x.name}` }).count(), 0, 'removed already: no second Remove');
        await badge(page, true);
      });
      await step('before the restart it can be installed again: it stays and is set up again, and nothing waits for a restart', async () => {
        await c.getByRole('button', { name: `Install ${x.name}` }).click();
        await page.locator('#ext-dialog[open]').waitFor();
        assert.equal(await text(page.locator('#ext-dialog-note')), 'It stays, and VaporOS sets it up again.');
        assert.equal(await page.isVisible('#ext-dialog-pw'), !!x.needs_password);
        if (x.needs_password) await page.fill('#ext-dialog-pw', 'vaporvapor');
        const post = request(page, 'POST', `/extensions/${x.id}`);
        await page.click('#ext-dialog-ok');
        await post;
        await closed(page, 'ext-dialog');
        await notice(page, `${x.name} stays, and VaporOS sets it up again.`);
        await chipOf(page, x.name, '');
        assert.equal(await text(c.locator('.ext-plain')), 'Installed');
        assert.equal(await page.isVisible('#ext-restart'), false);
        assert.equal(await c.getByRole('button', { name: `Remove ${x.name}` }).isVisible(), true);
        await badge(page, false);
      });
      await step('the core extension is never removed', async () => {
        const core = need(d.extensions.find((y) => y.core), 'a core extension');
        assert.equal(await card(page, core.name).getByRole('button', { name: /^Remove/ }).count(), 0);
      });
    },
  },
  {
    id: 'SYS-ext-settings',
    ui: ['next'],
    preset: 'extensions-attention',
    allow: WRONG,
    async run(t) {
      const { page, step } = t;
      const d = doc('extensions-attention');
      const x = need(d.extensions.find((y) => y.mounted && (y.settings || []).some((s) => s.type === 'bool' && s.needs_password)),
        'extensions-attention: a running extension with a switch that takes the password (settings[].needs_password)');
      const flag = x.settings.find((s) => s.type === 'bool' && s.needs_password);
      // A setting without the password: a choice, or a drive (one of those GET /storage lists).
      const free = (s) => !s.needs_password && (s.type === 'choice' || s.type === 'disk');
      const y = need(d.extensions.find((z) => (z.wanted || z.mounted) && (z.settings || []).some(free)),
        'extensions-attention: an added extension with a choice or a drive that takes no password');
      const plain = y.settings.find(free);
      const values = plain.type === 'disk' ? X.drives(json('base/storage.json').disks).map((v) => v.path) : plain.choices;
      const was = String(plain.value ?? '');
      need(!plain.required || was, `${y.name}'s ${plain.label}: a drive picked already (a required one never goes back to "")`);
      const pick = need(values.find((v) => v !== was), `another value for ${y.name}'s ${plain.label}`);
      const sel = () => card(page, y.name).getByLabel(plain.label, { exact: true });
      const sw = () => card(page, x.name).getByRole('switch', { name: flag.label, exact: true });
      const saved = flag.restart ? `Saved. ${X.AFTER_RESTART}` : 'Saved.';
      await step('a setting without the password saves at once', async () => {
        await open(t);
        if (plain.type === 'disk') await until(page, (id) => document.getElementById(id).options.length > 1, `ext-${y.id}-${plain.key}`);
        const body = write(page, 'PUT', `/extensions/${y.id}/settings`);
        await sel().selectOption(pick);
        assert.deepEqual(await body, { settings: { [plain.key]: pick } });
        await notice(page, 'Saved.');
        assert.equal(await sel().inputValue(), pick);
      });
      await step('a setting that takes the password asks for it first, and a wrong one is said under the field', async () => {
        assert.equal(await sw().isChecked(), flag.value === true);
        if (flag.restart) assert.match(await text(page.locator(`#${await sw().getAttribute('aria-describedby')}`)), /Takes effect after a restart\.$/);
        await sw().click({ force: true });
        await page.locator('#ext-dialog[open]').waitFor();
        assert.equal(await text(page.locator('#ext-dialog-title')), `Change ${flag.label}?`);
        assert.equal(await page.evaluate(() => document.activeElement.id), 'ext-dialog-pw');
        await page.fill('#ext-dialog-pw', 'not-the-password');
        await page.click('#ext-dialog-ok');
        await until(page, () => document.getElementById('ext-dialog-pw-error').textContent === "That's not the VaporOS password.");
        assert.equal(await page.locator('#ext-dialog[open]').count(), 1, 'the dialog stays open');
      });
      await step('the right password saves it', async () => {
        const body = write(page, 'PUT', `/extensions/${x.id}/settings`);
        await page.fill('#ext-dialog-pw', 'vaporvapor');
        await page.click('#ext-dialog-ok');
        assert.deepEqual(await body, { settings: { [flag.key]: flag.value !== true }, password: 'vaporvapor' });
        await closed(page, 'ext-dialog');
        await notice(page, saved);
        assert.equal(await sw().isChecked(), flag.value !== true);
      });
      await step('Cancel puts the switch back', async () => {
        const before = await sw().isChecked();
        await sw().click({ force: true });
        await page.locator('#ext-dialog[open]').waitFor();
        await page.click('#ext-dialog-cancel');
        await closed(page, 'ext-dialog');
        assert.equal(await sw().isChecked(), before);
      });
      await step('a change the box guards after all asks for the password instead of failing', async () => {
        // The first step chose another value: this one changes it back.
        await page.route(`**/api/v1/extensions/${y.id}/settings`, (r) => r.fulfill({ status: 403, json: { error: 'Changing this setting needs the admin password' } }), { times: 1 });
        await sel().selectOption(was);
        await page.locator('#ext-dialog[open]').waitFor();
        assert.equal(await text(page.locator('#ext-dialog-title')), `Change ${plain.label}?`);
        assert.equal(await page.evaluate(() => document.activeElement.id), 'ext-dialog-pw');
        const body = write(page, 'PUT', `/extensions/${y.id}/settings`);
        await page.fill('#ext-dialog-pw', 'vaporvapor');
        await page.click('#ext-dialog-ok');
        assert.deepEqual(await body, { settings: { [plain.key]: was }, password: 'vaporvapor' });
        await closed(page, 'ext-dialog');
        await notice(page, 'Saved.');
        assert.equal(await sel().inputValue(), was);
      });
    },
  },
  {
    id: 'SYS-ext-disk',
    ui: ['next'],
    allow: WRONG,
    async run(t) {
      const { page, step } = t;
      const d = doc();
      const x = need(d.extensions.find((y) => X.canInstall(y) && !y.needs_password && X.required(y).length === 1),
        'an extension to install without the password that needs a drive (one disk setting with "required": true, star-citizen)');
      const [s] = X.required(x);
      need(!s.value, `${x.name}'s ${s.label}: no drive picked yet (value "")`);
      const drives = X.drives(json('base/storage.json').disks);
      const sys = need(drives.find((v) => v.system && v.path === '/var' && / free$/.test(v.text)), 'GET /storage: the system drive (vos_data) with its free space');
      const game = need(drives.find((v) => !v.system), 'GET /storage: an adopted game drive that is mounted');
      const c = card(page, x.name);
      const pick = page.locator('#ext-dialog-choose').getByLabel(s.label, { exact: true });
      const sel = c.getByLabel(s.label, { exact: true });
      const options = (loc) => loc.locator('option').evaluateAll((os) => os.map((o) => [o.value, o.textContent, o.disabled]));
      await step(`Install asks for the drive first: each game drive and the system drive, and Install waits for one`, async () => {
        await open(t);
        assert.equal(await c.locator('.ext-settings').isVisible(), false, 'not added: no settings');
        await c.getByRole('button', { name: `Install ${x.name}` }).click();
        await page.locator('#ext-dialog[open]').waitFor();
        assert.equal(await page.isVisible('#ext-dialog-pw'), false);
        assert.deepEqual(await options(pick), [['', 'Choose a drive', true], ...drives.map((v) => [v.path, v.text, false])]);
        assert.equal(await pick.inputValue(), '');
        assert.equal(await page.evaluate(() => document.activeElement.id), await pick.getAttribute('id'), 'the drive comes first');
        assert.equal(await text(page.locator(`#${await pick.getAttribute('aria-describedby')}`)), X.settingHint(s));
        assert.equal(await page.isDisabled('#ext-dialog-ok'), true, 'no drive yet: Install waits');
        await assertAxe(page, 'the install dialog asking for a drive');
      });
      await step('the drive goes with the install; a password the box wants after all is asked for', async () => {
        await pick.selectOption(game.path);
        assert.equal(await page.isDisabled('#ext-dialog-ok'), false);
        // The box may want the password where the card did not say so.
        await page.route(`**/api/v1/extensions/${x.id}`, (r) => r.fulfill({ status: 403, json: { error: `Adding ${x.name} needs the admin password` } }), { times: 1 });
        const first = write(page, 'POST', `/extensions/${x.id}`);
        await page.click('#ext-dialog-ok');
        assert.deepEqual(await first, { options: { [s.key]: game.path } });
        await page.locator('#ext-dialog-pw').waitFor();
        assert.equal(await text(page.locator('#ext-dialog-pw-error')), 'Enter the VaporOS password.');
        assert.equal(await page.evaluate(() => document.activeElement.id), 'ext-dialog-pw');
        const body = write(page, 'POST', `/extensions/${x.id}`);
        await page.fill('#ext-dialog-pw', 'vaporvapor');
        await page.click('#ext-dialog-ok');
        assert.deepEqual(await body, { options: { [s.key]: game.path }, password: 'vaporvapor' });
        await closed(page, 'ext-dialog');
        await notice(page, X.installedText(x));
        await sel.waitFor();
      });
      await step('its card holds that drive, and no longer offers none', async () => {
        await until(page, ([id, v]) => document.getElementById(id).value === v, [`ext-${x.id}-${s.key}`, game.path]);
        assert.deepEqual(await options(sel), drives.map((v) => [v.path, v.text, false]));
        // The closed dialog slides away first.
        await page.locator('#ext-dialog').waitFor({ state: 'hidden' });
        await assertAxe(page, 'a drive setting');
      });
      await step("a drive saves as its folder: the system drive's (/var), then the game drive's", async () => {
        for (const v of [sys, game]) {
          const put = request(page, 'PUT', `/extensions/${x.id}/settings`);
          await sel.selectOption(v.path);
          const r = await put;
          assert.deepEqual(r.postDataJSON(), { settings: { [s.key]: v.path } });
          assert.equal((await r.response()).status(), 200);
          assert.equal(await sel.inputValue(), v.path);
        }
        await notice(page, 'Saved.');
      });
    },
  },
  {
    id: 'SYS-ext-late',
    ui: ['next'],
    allow: [/status of 500/, /500 GET .*\/api\/v1\/extensions$/],
    async run(t) {
      const { page, server, step } = t;
      const d = doc();
      need(d.extensions.length, 'GET /extensions lists no extension');
      await step('a first read that fails says so, in place of the list', async () => {
        await page.route('**/api/v1/extensions', (r) => (r.request().method() === 'GET' ? r.fulfill({ status: 500, json: { error: 'the extension store could not be read' } }) : r.fallback()), { times: 1 });
        await open(t);
        await page.locator('#ext-error').waitFor();
        assert.match(await text(page.locator('#ext-error-text')), /^Couldn't load the extensions\./);
        assert.equal(await page.isVisible('#ext-list'), false);
      });
      await step('the next extensions.state brings the cards back, and the error goes', async () => {
        await dev(server, 'event', { topic: 'extensions.state', data: d });
        await page.locator('#ext-list > .ext-card').first().waitFor();
        assert.equal(await page.isVisible('#ext-list'), true);
        assert.equal(await page.isVisible('#ext-error'), false);
        assert.equal(await page.locator('#ext-list > .ext-card').count(), d.extensions.length);
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
      const x = need(d.extensions.find((y) => y.mounted && (y.actions || []).some((a) => a.confirm)), 'extensions-attention: a running extension with an action that asks first');
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
      need(doc().skip_once === false, 'GET /extensions: skip_once false to begin with');
      const cancel = () => page.getByRole('button', { name: 'Cancel the start without extensions' });
      await step('the next start can leave every extension out, after a question', async () => {
        await open(t);
        assert.equal(await page.isVisible('#ext-skip-on'), false);
        await page.click('#ext-skip');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page.locator('#confirm-title')), 'Start once without extensions?');
        const post = request(page, 'POST', '/extensions/skip-once');
        await page.click('#confirm-ok');
        await post;
        await notice(page, 'The next start leaves extensions out.');
        await page.locator('#ext-skip-on').waitFor();
        assert.equal(await text(page.locator('#ext-skip-on')), 'Next start leaves extensions out.');
        assert.equal(await page.isVisible('#ext-skip'), false);
        assert.equal(await page.evaluate(() => document.activeElement.id), 'ext-skip-cancel');
      });
      await step('the page says so again when it loads (skip_once)', async () => {
        await open(t);
        await page.locator('#ext-list > .ext-card').first().waitFor();
        await page.locator('#ext-skip-on').waitFor({ timeout: 3000 }).catch(() => assert.fail('GET /extensions should say skip_once: true after POST /extensions/skip-once'));
        assert.equal(await cancel().isVisible(), true);
      });
      await step('Cancel calls it off', async () => {
        const del = request(page, 'DELETE', '/extensions/skip-once');
        await cancel().click();
        await del;
        await notice(page, 'The next start adds extensions again.');
        assert.equal(await page.isVisible('#ext-skip-on'), false);
        assert.equal(await page.isVisible('#ext-skip'), true);
        assert.equal(await page.evaluate(() => document.activeElement.id), 'ext-skip');
        await open(t);
        await page.locator('#ext-list > .ext-card').first().waitFor();
        assert.equal(await page.isVisible('#ext-skip-on'), false, 'GET /extensions says skip_once: false after DELETE /extensions/skip-once');
      });
    },
  },
  {
    id: 'SYS-ext-row',
    ui: ['next'],
    preset: 'extensions-installing',
    async run(t) {
      const { page, server, step } = t;
      const d = doc('extensions-installing');
      const busy = need(d.extensions.find((x) => x.state === 'installing'), 'extensions-installing: a card that is installing');
      need(d.extensions.filter((x) => x.state === 'installing').length === 1, 'extensions-installing: only one card installing');
      const head = X.rowLine({ extensions: d.extensions.filter((x) => x.state !== 'installing') })[0];
      need(X.rowLine(doc('extensions-attention'))[1] === 'cold', 'extensions-attention: a card that needs attention');
      await step('the Extensions row on System says what is installed and what is under way', async () => {
        await open(t, '/system');
        await until(page, () => /^\d+ installed|^None installed/.test(document.getElementById('sum-extensions').textContent));
        assert.match(await text(page.locator('#sum-extensions')), new RegExp(`^${esc(head)} · (installing ${esc(busy.name)}( · \\d+%)?|restart needed)$`));
        assert.equal(await page.getAttribute('#row-extensions', 'href'), '/system/extensions');
        assert.equal(await page.getAttribute('#row-extensions', 'data-tone'), 'hot');
      });
      await step('the event stream moves it on, without asking again', async () => {
        const asked = gets(page);
        await until(page, (w) => document.getElementById('sum-extensions').textContent === w, `${head} · restart needed`, 15000);
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
