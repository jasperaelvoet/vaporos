// Flows over sign-in and first-run setup (spec-cc-screens §13, §14,
// MASTER-PLAN §3.5 C5): the fields, the errors with the 429 countdown, the
// help, ?next=, the lead lines, the setup code and the new password. Each
// ID is a row of tools/web/e2e/parity.json (owner C5); the installer's are
// in install.spec.mjs. See legacy.spec.mjs for the flow format.

import assert from 'node:assert/strict';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const focusedId = (page) => page.evaluate(() => document.activeElement?.id ?? '');

// A wrong password or code is answered 401 or 403, which the browser logs.
const REFUSED = [/status of 40[13]/, /40[13] (POST|GET) .*\/api\/v1\/(auth\/(login|setup|me)|install\/status)/];
const LEAD = 'Enter the admin password you chose when you set up VaporOS.';

// fake answers the next call to path with status, as a 429 or 503 would.
async function fake(page, path, status, headers = {}) {
  await page.route(`**/api/v1${path}`, (r) => r.fulfill({ status, headers: { 'Content-Type': 'application/json', ...headers }, body: JSON.stringify({ error: 'refused' }) }), { times: 1 });
}

export default [
  {
    id: 'ENTRY-login-fields',
    ui: ['next'],
    preset: 'signed-out',
    async run({ page, url, ready, step }) {
      await page.goto(url('/login'));
      await ready();
      await step('the served page renders Sign in disabled until the script has asked /auth/me', async () => {
        const html = await page.evaluate(() => fetch('/login').then((r) => r.text()));
        assert.match(html, /<button class="btn primary wide" id="login-submit" type="submit" disabled>Sign in<\/button>/);
        assert.match(html, /<svg class="handshake" viewBox="0 0 5 5"/);
      });
      await step('the hidden username is admin and the password field has focus', async () => {
        assert.equal(await page.inputValue('input[name="username"]'), 'admin');
        assert.equal(await page.getAttribute('input[name="username"]', 'autocomplete'), 'username');
        assert.equal(await page.getAttribute('#login-password', 'autocomplete'), 'current-password');
        assert.equal(await focusedId(page), 'login-password');
        assert.equal(await page.isEnabled('#login-submit'), true);
      });
      await step('the viewfinder names this PC with its handshake mark', async () => {
        assert.equal(await text(page, '#login-vf .evf-addr'), 'http://vapor.local');
        assert.equal(await page.locator('#login-vf svg.handshake').count(), 1);
        assert.equal(await page.getAttribute('#login-vf', 'data-heat'), 'standby');
      });
      await step('the eye shows the password and hides it again', async () => {
        await page.fill('#login-password', 'secret words');
        const eye = page.getByRole('button', { name: 'Show password' });
        await eye.click();
        assert.equal(await page.getAttribute('#login-password', 'type'), 'text');
        assert.equal(await eye.getAttribute('aria-pressed'), 'true');
        await eye.click();
        assert.equal(await page.getAttribute('#login-password', 'type'), 'password');
        assert.equal(await eye.getAttribute('aria-pressed'), 'false');
      });
    },
  },
  {
    id: 'ENTRY-login-error',
    ui: ['next'],
    preset: 'signed-out',
    allow: [...REFUSED, /status of (429|503)/, /(429|503) POST .*\/api\/v1\/auth\/login/],
    async run({ page, url, ready, step }) {
      await page.goto(url('/login'));
      await ready();
      await step('an empty password is caught before it is sent', async () => {
        await page.click('#login-submit');
        assert.equal(await text(page, '#login-error'), 'Enter the admin password.');
        assert.equal(await page.getAttribute('#login-password', 'aria-invalid'), 'true');
      });
      await step('a wrong password says so, marks the field, selects it and turns the field cold', async () => {
        await page.fill('#login-password', 'not-the-password');
        await page.press('#login-password', 'Enter');
        await page.locator('#login-error', { hasText: "That password isn't right." }).waitFor();
        assert.equal(await page.getAttribute('#login-error', 'role'), 'alert');
        assert.equal(await page.getAttribute('#login-password', 'aria-invalid'), 'true');
        assert.equal(await page.getAttribute('#login-vf', 'data-heat'), 'cold');
        assert.deepEqual(await page.$eval('#login-password', (i) => [i.selectionStart, i.selectionEnd, document.activeElement === i]), [0, 16, true]);
      });
      await step('typing again clears the mark and warms the field back up', async () => {
        await page.keyboard.type('x');
        assert.equal(await page.getAttribute('#login-password', 'aria-invalid'), null);
        assert.equal(await page.getAttribute('#login-vf', 'data-heat'), 'standby');
      });
      await step('too many tries: the error says it once and the button counts down', async () => {
        await fake(page, '/auth/login', 429, { 'Retry-After': '2' });
        await page.click('#login-submit');
        await page.locator('#login-error', { hasText: 'Too many wrong passwords. Try again in 2 s.' }).waitFor();
        assert.match(await text(page, '#login-submit'), /^Try again in [12] s$/);
        assert.equal(await page.isDisabled('#login-submit'), true);
        await page.locator('#login-submit:not([disabled])', { hasText: 'Sign in' }).waitFor({ timeout: 4000 });
        assert.equal(await text(page, '#login-error'), 'Too many wrong passwords. Try again in 2 s.');
      });
      await step('a busy box says so in plain words', async () => {
        await fake(page, '/auth/login', 503);
        await page.click('#login-submit');
        await page.locator('#login-error', { hasText: 'VaporOS is busy checking other sign-ins. Try again in a moment.' }).waitFor();
      });
    },
  },
  {
    id: 'ENTRY-login-forgot',
    ui: ['next'],
    preset: 'signed-out',
    async run({ page, url, ready, step }) {
      await page.goto(url('/login'));
      await ready();
      await step('"Forgot the password?" opens the repair hint', async () => {
        assert.equal(await page.$eval('#login-forgot', (d) => d.open), false);
        await page.click('#login-forgot summary');
        assert.equal(await page.$eval('#login-forgot', (d) => d.open), true);
        assert.equal(await text(page, '#login-forgot p'), 'Reinstall from the VaporOS USB stick and choose Repair. It keeps your games and settings and lets you set a new password.');
      });
    },
  },
  {
    id: 'ENTRY-login-next',
    ui: ['next'],
    preset: 'signed-out',
    async run({ page, context, url, ready, step }) {
      await step('signing in goes back to the page in ?next=', async () => {
        await page.goto(url('/login?next=%2Fsystem%2Fpower%3Fx%3D1'));
        await ready();
        await page.fill('#login-password', 'vaporvapor');
        await page.press('#login-password', 'Enter');
        await page.waitForURL((u) => new URL(u).pathname === '/system/power' && new URL(u).search === '?x=1');
        await ready();
      });
      await step('a signed-in visit to sign-in goes straight on', async () => {
        await page.goto(url('/login?next=%2Fscreen'));
        await page.waitForURL((u) => new URL(u).pathname === '/screen');
      });
      await step('?next= never leaves this PC', async () => {
        for (const bad of ['//evil.example/x', '/\\evil.example', 'https://evil.example/', '/login']) {
          // Signed out between tries, with no page left that could ask.
          await page.goto('about:blank');
          await context.clearCookies();
          await page.goto(url(`/login?next=${encodeURIComponent(bad)}`));
          await ready();
          await page.fill('#login-password', 'vaporvapor');
          await page.press('#login-password', 'Enter');
          await page.waitForURL((u) => new URL(u).pathname === '/' && new URL(u).host === new URL(url('/')).host);
          await ready();
        }
      });
    },
  },
  {
    id: 'ENTRY-login-cold',
    ui: ['next'],
    preset: 'signed-out',
    async run({ page, url, ready, step }) {
      await step('a signed-out /pair (the TV\'s link) asks to sign in with the default lead', async () => {
        await page.goto(url('/pair'));
        await page.waitForURL(/\/login\?next=%2Fdevices%23pair$/);
        await ready();
        assert.equal(await text(page, '#login-lead'), LEAD);
      });
      await step('the lead follows why the visitor is here', async () => {
        await page.goto(url('/login?ended=1'));
        await ready();
        assert.equal(await text(page, '#login-lead'), 'You were signed out. Sign in again.');
        await page.goto(url('/login?installed=1'));
        await ready();
        assert.equal(await text(page, '#login-lead'), 'VaporOS is installed. Sign in with the password you just chose.');
      });
      await step('signing in then lands on Devices with the pair step', async () => {
        await page.goto(url('/login?next=%2Fdevices%23pair'));
        await ready();
        await page.fill('#login-password', 'vaporvapor');
        await page.press('#login-password', 'Enter');
        await page.waitForURL((u) => new URL(u).pathname === '/devices' && new URL(u).hash === '#pair');
      });
    },
  },
  {
    id: 'ENTRY-setup-code',
    ui: ['next'],
    preset: 'first-run',
    allow: REFUSED,
    async run({ page, url, ready, step }) {
      await step('a code from the QR link is tidied, kept for the tab and taken out of the address', async () => {
        await page.goto(url('/setup?code=abcd%20efgh'));
        await ready();
        assert.equal(await page.inputValue('#setup-code'), 'ABCD-EFGH');
        assert.equal(new URL(page.url()).search, '');
        assert.equal(await page.evaluate(() => sessionStorage.getItem('vos-setup-code')), 'ABCD-EFGH');
      });
      await step('the code sits in the panel the PC\'s screen shows it in, with its hint', async () => {
        assert.equal(await text(page, '#setup-code-hint'), "It's on the screen connected to the PC. Scanning the QR code there fills it in.");
        assert.equal(await page.getAttribute('#setup-code', 'autocomplete'), 'off');
        assert.equal(await page.getAttribute('#setup-code', 'autocapitalize'), 'characters');
      });
      await step('a typed code is tidied when the field loses focus', async () => {
        await page.fill('#setup-code', 'wxyz2345');
        await page.press('#setup-code', 'Tab');
        assert.equal(await page.inputValue('#setup-code'), 'WXYZ-2345');
      });
      await step('a wrong code is refused under the code field', async () => {
        await page.fill('#setup-password', 'correct horse');
        await page.fill('#setup-password2', 'correct horse');
        await page.click('#setup-submit');
        await page.locator('#setup-code-error', { hasText: "That setup code isn't right. Use the code on the screen connected to the PC." }).waitFor();
        assert.equal(await page.getAttribute('#setup-code', 'aria-invalid'), 'true');
        assert.equal(await focusedId(page), 'setup-code');
        assert.equal(await page.getAttribute('#setup-vf', 'data-heat'), 'cold');
      });
      await step('an empty code is caught before anything is sent', async () => {
        await page.fill('#setup-code', '');
        await page.click('#setup-submit');
        assert.equal(await text(page, '#setup-code-error'), 'Enter the setup code from the screen.');
      });
    },
  },
  {
    id: 'ENTRY-setup-password',
    ui: ['next'],
    preset: 'first-run',
    async run({ page, url, ready, step }) {
      await page.goto(url('/setup?code=ABCD-EFGH'));
      await ready();
      await step('the page is "Welcome to VaporOS", titled Set up VaporOS, with the handshake mark', async () => {
        assert.equal(await page.title(), 'Set up VaporOS');
        assert.equal(await text(page, 'h1'), 'Welcome to VaporOS');
        assert.equal(await page.locator('#setup-vf svg.handshake').count(), 1);
      });
      await step('a short password and a mismatch are shown under their fields', async () => {
        await page.fill('#setup-password', 'short');
        await page.click('#setup-submit');
        assert.equal(await text(page, '#setup-password-error'), 'Use at least 8 characters.');
        assert.equal(await focusedId(page), 'setup-password');
        await page.fill('#setup-password', 'correct horse');
        await page.fill('#setup-password2', 'correct hors');
        await page.click('#setup-submit');
        assert.equal(await text(page, '#setup-password-error'), '');
        assert.equal(await text(page, '#setup-password2-error'), "The passwords don't match.");
        assert.equal(await focusedId(page), 'setup-password2');
      });
      await step('both fields have their own show toggle', async () => {
        await page.getByRole('button', { name: 'Show repeated password' }).click();
        assert.equal(await page.getAttribute('#setup-password2', 'type'), 'text');
        assert.equal(await page.getAttribute('#setup-password', 'type'), 'password');
      });
      await step('Save and continue sets the password and goes Home, signed in', async () => {
        await page.fill('#setup-password2', 'correct horse');
        await page.click('#setup-submit');
        await page.waitForURL((u) => new URL(u).pathname === '/' && new URL(u).search === '?welcome=1');
        await ready();
        assert.equal(await page.evaluate(() => sessionStorage.getItem('vos-setup-code')), null);
        const me = await page.evaluate(() => fetch('/api/v1/auth/me').then((r) => r.json()));
        assert.equal(me.authenticated, true);
        assert.equal(me.needs_setup, false);
      });
    },
  },
];
