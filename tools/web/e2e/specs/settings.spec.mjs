// Flows over System › Settings (spec-cc-screens §10): the network name and
// the rename hand-off, the admin password, and remote access over SSH.
// Each ID is a row of tools/web/e2e/parity.json (owner C4). See
// legacy.spec.mjs for the flow format.

import assert from 'node:assert/strict';

import { closed, dev } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const until = (page, fn, arg, timeout = 5000) => page.waitForFunction(fn, arg, { timeout });
const focusedId = (page) => page.evaluate(() => document.activeElement?.id ?? '');
const notice = (page, words) => page.locator('#notices .notice', { hasText: words }).first().waitFor();

async function open(t) {
  await t.page.goto(t.url('/system/settings'));
  await t.ready();
}

// writes counts the page's writes to path.
function writes(page, method, path) {
  const seen = [];
  page.on('request', (r) => r.method() === method && new URL(r.url()).pathname === `/api/v1${path}` && seen.push(r.postDataJSON()));
  return seen;
}

const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFOTT0p7Iivr1j7lNb97ivie7D2deh2qJaSY5wNBf9Uj sam@laptop';

export default [
  {
    id: 'SYS-set-rename',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      const puts = writes(page, 'PUT', '/system/hostname');
      await step('the current name, with Rename off until it changes', async () => {
        await open(t);
        assert.equal(await page.inputValue('#hostname'), 'vapor');
        assert.equal(await page.isDisabled('#rename'), true);
        assert.equal(await text(page, '#rename-note'), "That's the current name.");
        assert.equal(await page.getAttribute('#rename', 'aria-describedby'), 'rename-note');
      });
      await step('"localhost" is refused inline, before any request', async () => {
        await page.fill('#hostname', 'localhost');
        assert.equal(await page.isVisible('#rename-note'), false);
        await page.click('#rename');
        assert.equal(await text(page, '#hostname-error'), '"localhost" is reserved. Pick another name.');
        assert.equal(await page.getAttribute('#hostname', 'aria-invalid'), 'true');
        assert.equal(await focusedId(page), 'hostname');
        assert.deepEqual(puts, []);
      });
      await step('a rename asks first and says the page moves (C-rename)', async () => {
        await page.fill('#hostname', 'Arcade');
        await page.click('#rename');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Rename to arcade?');
        assert.equal(await text(page, '#confirm-body'), 'This page moves to http://arcade.local, where you sign in again. Home-screen shortcuts to the old name stop working.');
        await page.click('#confirm-cancel');
        await closed(page, 'confirm');
        assert.deepEqual(puts, []);
      });
      await step('renamed: the hand-off offers the new name and the IP, each to sign in again', async () => {
        await page.click('#rename');
        await page.click('#confirm-ok');
        await page.locator('#handoff[open]').waitFor();
        assert.deepEqual(puts, [{ hostname: 'arcade' }]);
        assert.equal(await text(page, '#handoff-title'), 'Renamed to arcade');
        assert.equal(await focusedId(page), 'handoff-title');
        assert.equal(await text(page, '#handoff-text'), 'This address stopped working. Open VaporOS at its new address and sign in again.');
        const port = new URL(page.url()).port;
        assert.equal(await page.getAttribute('#handoff-name', 'href'), `http://arcade.local:${port}/login`);
        assert.equal(await text(page, '#handoff-name'), 'Open arcade.local');
        assert.equal(await page.getAttribute('#handoff-ip', 'href'), `http://192.168.1.40:${port}/login`);
        assert.equal(await text(page, '#handoff-ip'), 'Open 192.168.1.40');
      });
      await step('the old address never flashes "Can\'t reach": the stream is closed, Esc stays', async () => {
        await page.keyboard.press('Escape');
        await page.waitForTimeout(3000);
        assert.equal(await page.locator('#handoff[open]').count(), 1);
        assert.equal(await page.getAttribute('#link', 'data-link'), 'live');
        assert.equal(await page.isVisible('#fatal'), false);
        assert.equal(await page.isVisible('#offline'), false);
      });
      await step('the new name answers, with a sign-in', async () => {
        await page.click('#handoff-name');
        await page.waitForURL((u) => new URL(u).hostname === 'arcade.local' && new URL(u).pathname === '/login');
        await t.ready();
      });
    },
  },
  {
    id: 'SYS-set-password',
    ui: ['next'],
    allow: [/status of 403/, /403 POST .*\/api\/v1\/auth\/password$/, /status of 429/, /429 POST .*\/api\/v1\/auth\/password$/],
    async run(t) {
      const { page, step } = t;
      await step('an empty submit shows each error under its field and focuses the first', async () => {
        await open(t);
        assert.equal(await page.getAttribute('input[name="username"]', 'autocomplete'), 'username');
        assert.equal(await page.getAttribute('#pw-current', 'autocomplete'), 'current-password');
        assert.equal(await page.getAttribute('#pw-new', 'autocomplete'), 'new-password');
        await page.click('#pw-save');
        assert.equal(await text(page, '#pw-current-error'), 'Enter the current password.');
        assert.equal(await text(page, '#pw-new-error'), 'Use at least 8 characters.');
        assert.equal(await focusedId(page), 'pw-current');
      });
      await step('a repeat that differs is caught under Repeat', async () => {
        await page.fill('#pw-current', 'vaporvapor');
        await page.fill('#pw-new', 'arcade-night-1');
        await page.fill('#pw-again', 'arcade-night-2');
        await page.click('#pw-save');
        assert.equal(await text(page, '#pw-again-error'), "The passwords don't match.");
        assert.equal(await page.isVisible('#pw-new-error'), false);
      });
      await step('Show password reveals a field and says so', async () => {
        await page.click('[data-reveal="pw-new"]');
        assert.equal(await page.getAttribute('#pw-new', 'type'), 'text');
        assert.equal(await page.getAttribute('[data-reveal="pw-new"]', 'aria-pressed'), 'true');
        await page.click('[data-reveal="pw-new"]');
        assert.equal(await page.getAttribute('#pw-new', 'type'), 'password');
      });
      await step('a wrong current password lands under Current', async () => {
        await page.fill('#pw-current', 'not-it-at-all');
        await page.fill('#pw-again', 'arcade-night-1');
        await page.click('#pw-save');
        await page.locator('#pw-current-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#pw-current-error'), "That's not the current password.");
        assert.equal(await focusedId(page), 'pw-current');
      });
      await step('too many tries count down on the button', async () => {
        await page.route('**/api/v1/auth/password', (r) => r.fulfill({ status: 429, headers: { 'Retry-After': '2' }, contentType: 'application/json', body: '{"error":"too many attempts"}' }));
        await page.click('#pw-save');
        await until(page, () => /^Try again in \d s$/.test(document.getElementById('pw-save').textContent));
        assert.equal(await page.isDisabled('#pw-save'), true);
        assert.equal(await text(page, '#pw-form-error'), 'Too many tries. Wait until the button is ready again.');
        await until(page, () => document.getElementById('pw-save').textContent === 'Change password', null, 6000);
        assert.equal(await page.isDisabled('#pw-save'), false);
        await page.unroute('**/api/v1/auth/password');
      });
      await step('the right one changes it: the form empties and says other devices are signed out', async () => {
        await page.fill('#pw-current', 'vaporvapor');
        await page.click('#pw-save');
        await notice(page, 'Password changed. Other devices are signed out.');
        for (const id of ['pw-current', 'pw-new', 'pw-again']) assert.equal(await page.inputValue(`#${id}`), '');
        assert.equal(await page.isVisible('#pw-current-error'), false);
      });
    },
  },
  {
    id: 'SYS-set-ssh',
    ui: ['next'],
    allow: [/status of 500/, /500 GET .*\/api\/v1\/ssh$/, /status of 400/, /400 PUT .*\/api\/v1\/ssh$/],
    async run(t) {
      const { page, server, step } = t;
      const puts = writes(page, 'PUT', '/ssh');
      await step('off with no keys: no connection hint, Save waits for a change', async () => {
        await open(t);
        assert.equal(await page.isChecked('#ssh-on'), false);
        assert.equal(await page.getAttribute('#ssh-on', 'role'), 'switch');
        assert.equal(await page.inputValue('#ssh-keys'), '');
        assert.equal(await page.isVisible('#ssh-connect'), false);
        assert.equal(await page.isDisabled('#ssh-save'), true);
      });
      await step('turning it on without a key is refused inline', async () => {
        await page.check('#ssh-on');
        await page.click('#ssh-save');
        assert.equal(await text(page, '#ssh-keys-error'), 'Add at least one public key. SSH never accepts passwords.');
        assert.equal(await page.getAttribute('#ssh-keys', 'aria-invalid'), 'true');
        assert.deepEqual(puts, []);
      });
      await step('a line that is no key says which one', async () => {
        await page.fill('#ssh-keys', 'hello there');
        await page.click('#ssh-save');
        assert.match(await text(page, '#ssh-keys-error'), /^Not an SSH public key .* It starts “hello there”\.$/);
      });
      await step("the server's own word lands under the field", async () => {
        await page.route('**/api/v1/ssh', (r) => (r.request().method() === 'PUT' ? r.fulfill({ status: 400, contentType: 'application/json', body: '{"error":"line 1: RSA keys need at least 2048 bits"}' }) : r.fallback()));
        await page.fill('#ssh-keys', KEY);
        await page.click('#ssh-save');
        await page.locator('#ssh-keys-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#ssh-keys-error'), 'Line 1: RSA keys need at least 2048 bits');
        await page.unroute('**/api/v1/ssh');
      });
      await step('a key saves it: SSH is on, and says how to connect', async () => {
        await page.click('#ssh-save');
        await notice(page, 'SSH is on for the keys listed.');
        assert.deepEqual(puts.at(-1), { enabled: true, keys: [KEY] });
        assert.equal(await text(page, '#ssh-command'), 'ssh vapor@vapor.local');
        assert.equal(await page.isVisible('#ssh-connect'), true);
        assert.equal(await page.isDisabled('#ssh-save'), true);
      });
      await step('a failed read is an inline error with Try again', async () => {
        await page.route('**/api/v1/ssh', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"reading sshd config: permission denied"}' }));
        await open(t);
        await page.locator('#ssh-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#ssh-error-text'), "Couldn't load SSH settings. Reading sshd config: permission denied");
        assert.equal(await page.isVisible('#ssh-form'), false);
        await page.unroute('**/api/v1/ssh');
        await page.click('#ssh-retry');
        await page.locator('#ssh-form:not([hidden])').waitFor();
        assert.equal(await page.isChecked('#ssh-on'), true);
      });
      await step('turned off, the hint goes and it says so', async () => {
        await dev(server, 'preset', { name: 'ssh-on' });
        await open(t);
        assert.equal(await page.isVisible('#ssh-connect'), true);
        await page.uncheck('#ssh-on');
        await page.click('#ssh-save');
        await notice(page, 'SSH is off.');
        assert.equal(await page.isVisible('#ssh-connect'), false);
      });
    },
  },
];
