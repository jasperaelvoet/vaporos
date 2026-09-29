// Flows over today's UI (the legacy set). They prove the harness end to end
// until the new UI's specs arrive, and go with the legacy set at L1.
//
// A spec file exports an array of flows:
//   { id, ui: ['legacy'|'next', …] (default both), preset (default 'idle'),
//     allow: [RegExp] console lines the flow is designed to cause,
//     async run(t) }
// where t = { page, context, origin, url(path), ui, server, ready(), step(name, fn) }.
// A flow fails on a thrown error, a console error or warning, a CSP
// violation or a failed request that allow does not cover.

import assert from 'node:assert/strict';

export default [
  {
    id: 'LEGACY-login',
    ui: ['legacy'],
    preset: 'signed-out',
    // The wrong password is answered 401, which Chrome logs.
    allow: [/status of 401/, /401 POST .*\/api\/v1\/auth\/login/],
    async run({ page, url, ready, step }) {
      await step('a signed-out visit goes to sign-in and keeps the page', async () => {
        await page.goto(url('/updates'));
        await page.waitForURL(/\/login\?next=%2Fupdates$/);
        await ready();
      });
      await step('a wrong password shows the inline error', async () => {
        await page.fill('#login-password', 'wrong');
        await page.press('#login-password', 'Enter');
        await page.locator('#login-error').waitFor({ state: 'visible' });
        assert.equal(await page.textContent('#login-error'), "That password isn't right.");
        assert.equal(await page.getAttribute('#login-password', 'aria-invalid'), 'true');
      });
      await step('the right password lands on the page the visitor wanted', async () => {
        await page.fill('#login-password', 'vaporvapor');
        await page.press('#login-password', 'Enter');
        await page.waitForURL(/\/updates$/);
        await ready();
      });
    },
  },
  {
    id: 'LEGACY-nav',
    ui: ['legacy'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      for (const [label, path] of [['Pair', '/pair'], ['Updates', '/updates'], ['Power', '/power']]) {
        await step(`the menu opens ${label}`, async () => {
          if (await page.isVisible('#menu-btn')) await page.click('#menu-btn');
          await page.click(`#nav a[href="${path}"]`);
          await page.waitForURL(new RegExp(`${path}$`));
          await ready();
          assert.equal(await page.getAttribute(`#nav a[href="${path}"]`, 'aria-current'), 'page');
        });
      }
    },
  },
  {
    id: 'LEGACY-installer-code',
    ui: ['legacy'],
    preset: 'installer-code',
    // Without the code the wizard's first probe is refused with 403.
    allow: [/status of 403/, /403 GET .*\/api\/v1\/install\/status/],
    async run({ page, url, ready, step }) {
      await step('without the code the wizard asks for it', async () => {
        await page.goto(url('/setup'));
        await ready();
        await page.locator('#code-form').waitFor({ state: 'visible' });
      });
      await step('the code from the screen opens the drive step', async () => {
        await page.fill('#code-form #setup-code', 'ABCD-EFGH');
        await page.click('#code-form button[type="submit"]');
        await page.locator('.step[data-step="disk"]').waitFor({ state: 'visible' });
      });
    },
  },
];
