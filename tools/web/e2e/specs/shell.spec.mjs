// Flows over the next control center's shell (spec-cc-screens §2, ARCH §6):
// the tabs, the old URLs, sign-in redirects, notices and Recent, confirm and
// hold, the reconnection scene, the connection indicator and the Asleep
// scene, the strip and its sheet, the restart row and the PIN pad. Each ID
// is a row of tools/web/e2e/parity.json (owner C0b). See legacy.spec.mjs for
// the flow format.

import assert from 'node:assert/strict';

import { assertAxe } from '../lib/checks.mjs';
import { closed, dev, request } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const focused = (page) => page.evaluate(() => document.activeElement?.id || document.activeElement?.className || '');

// press holds the pointer on the middle of a key for ms, scrolled into view
// first so the tab bar is never what gets pressed.
async function press(page, sel, ms) {
  await page.locator(sel).evaluate((el) => el.scrollIntoView({ block: 'center' }));
  const box = await page.locator(sel).boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(ms);
  await page.mouse.up();
}

// patchJSON answers GET path with the real answer changed by fn.
async function patchJSON(page, path, fn) {
  await page.route(`**/api/v1${path}`, async (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    try {
      const res = await route.fetch({ url: route.request().url().replace('//vapor.local:', '//127.0.0.1:') });
      await route.fulfill({ status: res.status(), contentType: 'application/json', body: JSON.stringify(fn(await res.json())) });
    } catch {
      // the page went on while this answer was in flight
    }
  });
}

// A box that is down answers nothing: the browser logs every refused
// request, which these flows cause on purpose.
const DOWN = [/Failed to load resource/, /net::ERR_/, /\/api\/v1\/(ping|events|system|auth\/me|status)/, /ERR_EMPTY_RESPONSE|ERR_CONNECTION/];

export default [
  {
    id: 'SHELL-tabs',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      for (const [name, path] of [['Devices', '/devices'], ['Screen', '/screen'], ['System', '/system'], ['Home', '/']]) {
        await step(`the ${name} tab opens ${path} and marks itself`, async () => {
          await page.click(`#nav a[href="${path}"]`);
          await page.waitForURL((u) => new URL(u).pathname === path);
          await ready();
          assert.equal(await page.getAttribute(`#nav a[href="${path}"]`, 'aria-current'), 'page');
          assert.equal(await page.locator('#nav a[aria-current]').count(), 1);
        });
      }
      await step('a sub-page marks its tab with true and links back to System', async () => {
        await page.goto(url('/system/power'));
        await ready();
        assert.equal(await page.getAttribute('#nav a[href="/system"]', 'aria-current'), 'true');
        await page.click('a.back');
        await page.waitForURL((u) => new URL(u).pathname === '/system');
      });
      await step('Sign out lives on the System index on phones and in the rail from 64rem, never both', async () => {
        await ready();
        const wide = page.viewportSize().width >= 1024;
        assert.equal(await page.isVisible('#sys-signout'), !wide);
        assert.equal(await page.isVisible('#signout'), wide);
      });
      await step('it signs out', async () => {
        await page.click(page.viewportSize().width >= 1024 ? '#signout' : '#sys-signout');
        await page.waitForURL((u) => new URL(u).pathname === '/login');
      });
    },
  },
  {
    id: 'SHELL-legacy-tv-urls',
    ui: ['next'],
    preset: 'pairing-1',
    // A wrong PIN is answered 400, which the browser logs.
    allow: [/status of 400/, /400 POST .*\/api\/v1\/sunshine\/pair/],
    async run({ page, url, ready, step }) {
      await step('the TV prints /pair: it lands on Devices and opens the PIN pad', async () => {
        await page.goto(url('/pair'));
        await page.waitForURL((u) => new URL(u).pathname === '/devices' && new URL(u).hash === '#pair');
        await ready();
        await page.locator('#pinpad[open]').waitFor();
        assert.equal(await text(page, '#pin-title'), 'Steam Deck wants to pair');
      });
      await step('a wrong PIN says so, clears and turns the field cold', async () => {
        await page.keyboard.type('9999');
        await page.locator('#pin-error:not(:empty)').waitFor();
        assert.equal(await text(page, '#pin-error'), "That PIN didn't work. Check Moonlight and try again.");
        assert.equal(await page.getAttribute('#pinpad', 'data-phase'), 'error');
        assert.equal(await page.inputValue('#pin'), '');
      });
      await step('the right PIN pairs and the takeover closes', async () => {
        await page.keyboard.type('1234');
        await page.locator('#pinpad[data-phase="success"]').waitFor();
        await closed(page, 'pinpad');
      });
    },
  },
  {
    id: 'SHELL-pairing-prompt',
    ui: ['next'],
    preset: 'pairing-1',
    async run({ page, url, ready, server, step }) {
      await step('a waiting device raises one sticky notice with Enter PIN', async () => {
        await page.goto(url('/system'));
        await ready();
        const notice = page.locator('#notices-polite .notice', { hasText: 'Steam Deck wants to pair' });
        await notice.waitFor();
        assert.equal(await page.locator('#nav a[href="/devices"] [data-part="badge"]').isVisible(), true);
        await notice.getByRole('button', { name: 'Enter PIN' }).click();
        await page.locator('#pinpad[open]').waitFor();
      });
      await step('Not now closes it and the prompt stays one tap away', async () => {
        await page.click('#pin-close');
        await closed(page, 'pinpad');
        await page.locator('#notices-polite .notice', { hasText: 'Steam Deck wants to pair' }).waitFor();
      });
      await step('when Moonlight stops waiting, the prompt goes away', async () => {
        await dev(server, 'event', { topic: 'pairing.state', data: { pairings: [] } });
        await page.locator('#notices-polite .notice', { hasText: 'wants to pair' }).waitFor({ state: 'detached' });
        assert.equal(await page.locator('#nav a[href="/devices"] [data-part="badge"]').isVisible(), false);
      });
    },
  },
  {
    id: 'SHELL-legacy-advanced-logs',
    ui: ['next'],
    async run({ page, url, step }) {
      await step('/advanced goes to System › Settings with the query kept', async () => {
        await page.goto(url('/advanced?x=1'));
        await page.waitForURL((u) => new URL(u).pathname === '/system/settings' && new URL(u).search === '?x=1');
      });
      await step('/advanced#logs goes on to System › Logs', async () => {
        await page.goto(url('/advanced?x=1#logs'));
        await page.waitForURL((u) => new URL(u).pathname === '/system/logs' && new URL(u).search === '?x=1');
      });
      for (const [from, to] of [['/streaming', '/screen'], ['/display', '/screen'], ['/storage', '/system/storage'], ['/updates', '/system/updates'], ['/power', '/system/power']]) {
        await step(`${from} goes to ${to}`, async () => {
          await page.goto(url(from));
          await page.waitForURL((u) => new URL(u).pathname === to);
        });
      }
    },
  },
  {
    id: 'SHELL-redirects',
    ui: ['next'],
    preset: 'signed-out',
    async run({ page, url, ready, step }) {
      await step('a signed-out visit goes to sign-in and remembers the page', async () => {
        await page.goto(url('/system/updates'));
        await page.waitForURL(/\/login\?next=%2Fsystem%2Fupdates$/);
        await ready();
      });
      await step('signing in lands on the page the visitor wanted', async () => {
        await page.fill('#login-password', 'vaporvapor');
        await page.press('#login-password', 'Enter');
        await page.waitForURL((u) => new URL(u).pathname === '/system/updates');
        await ready();
      });
    },
  },
  {
    id: 'SHELL-fatal',
    ui: ['next'],
    allow: [/Failed to load resource/, /net::ERR_FAILED/, /\/api\/v1\/auth\/me/],
    async run({ page, url, step }) {
      await step("when /auth/me cannot be reached the page says so and focuses it", async () => {
        await page.route('**/api/v1/auth/me', (r) => r.abort());
        await page.goto(url('/screen'));
        await page.locator('#fatal:not([hidden])').waitFor();
        assert.equal(await page.getAttribute('html', 'data-boot'), 'failed');
        assert.equal(await focused(page), 'fatal-title');
      });
    },
  },
  {
    id: 'SHELL-confirm',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/system'));
      await ready();
      await step('Enter on Restart asks first, with the action focused', async () => {
        await page.focus('#sys-reboot');
        await page.keyboard.press('Enter');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart VaporOS?');
        assert.equal(await focused(page), 'confirm-ok');
      });
      await step('Esc cancels and gives focus back to the key', async () => {
        await page.keyboard.press('Escape');
        await closed(page, 'confirm');
        assert.equal(await focused(page), 'sys-reboot');
      });
      await step('Power off is a danger confirm: Cancel takes focus, Enter cancels', async () => {
        await page.focus('#sys-poweroff');
        await page.keyboard.press('Enter');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await page.getAttribute('#confirm', 'role'), 'alertdialog');
        assert.equal(await focused(page), 'confirm-cancel');
        await page.keyboard.press('Enter');
        await closed(page, 'confirm');
      });
    },
  },
  {
    id: 'SHELL-scene',
    ui: ['next'],
    allow: DOWN,
    async run({ page, url, ready, step }) {
      await page.goto(url('/system'));
      await ready();
      await step('a short press nudges instead of firing', async () => {
        await press(page, '#sys-reboot', 500);
        assert.match(await text(page, '#sys-hold-hint'), /^Keep holding Restart until the key fills\.$/);
        assert.equal(await page.locator('#confirm[open]').count(), 0);
      });
      await step('holding Restart for 1.2 s restarts without a dialog and shows the scene', async () => {
        await press(page, '#sys-reboot', 1400);
        await page.locator('#scene[open]').waitFor();
        assert.equal(await text(page, '#scene-title'), 'Restarting');
        assert.equal(await page.locator('#confirm[open]').count(), 0);
        await assertAxe(page, 'the reconnection scene open');
      });
      await step('the scene waits for VaporOS and reloads when it is back', async () => {
        await page.locator('#scene[data-phase="down"]').waitFor({ timeout: 10000 });
        await page.waitForEvent('load', { timeout: 30000 });
        await ready();
        assert.equal(await page.locator('#scene[open]').count(), 0);
      });
    },
  },
  {
    id: 'SHELL-power-action',
    ui: ['next'],
    allow: DOWN,
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/system'));
      await ready();
      await step('Power off asks, then shows the off phase with the Wake card', async () => {
        await page.click('#sys-poweroff');
        await page.locator('#confirm[open]').waitFor();
        await page.click('#confirm-ok');
        await page.locator('#scene[data-phase="off"]').waitFor();
        assert.equal(await text(page, '#scene-title'), 'Off');
        await page.locator('#scene-wake:not([hidden])').waitFor();
      });
      await step('woken, the page comes back by itself', async () => {
        await page.waitForTimeout(2500);
        await dev(server, 'down', { seconds: 0 });
        await page.waitForEvent('load', { timeout: 20000 });
        await ready();
      });
    },
  },
  {
    id: 'SHELL-notices',
    ui: ['next'],
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/screen'));
      await ready();
      await step('a system.message error shows in the alert region and stays', async () => {
        await dev(server, 'event', { topic: 'system.message', data: { level: 'error', text: 'Streaming stopped by itself.' } });
        const n = page.locator('#notices-alert .notice[data-kind="error"]', { hasText: 'Streaming stopped by itself.' });
        await n.waitFor();
        await page.waitForTimeout(4500);
        assert.equal(await n.isVisible(), true);
      });
      await step('an info notice times out', async () => {
        await dev(server, 'event', { topic: 'system.message', data: { level: 'info', text: 'Just so you know.' } });
        const n = page.locator('#notices-polite .notice', { hasText: 'Just so you know.' });
        await n.waitFor();
        await n.waitFor({ state: 'detached', timeout: 7000 });
      });
      await step('Recent, from the connection indicator, lists both after a navigation', async () => {
        await page.click('#nav a[href="/devices"]');
        await ready();
        await page.click('#link');
        await page.locator('#recent[open]').waitFor();
        assert.equal(await page.locator('#recent-list .recent-item').count(), 2);
        await assertAxe(page, 'Recent open with notices in it');
        await page.keyboard.press('Escape');
        await closed(page, 'recent');
        assert.equal(await focused(page), 'link');
      });
    },
  },
  {
    id: 'SHELL-recent-empty',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('Recent says so when nothing happened yet', async () => {
        await page.click('#link');
        await page.locator('#recent[open]').waitFor();
        assert.equal(await page.isVisible('#recent-empty'), true);
        assert.equal(await text(page, '#recent-empty'), 'No notices yet');
      });
    },
  },
  {
    id: 'SHELL-link',
    ui: ['next'],
    allow: DOWN,
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('live: the indicator is a quiet dot with its name', async () => {
        await page.locator('#link[data-link="live"]').waitFor();
        assert.equal(await text(page, '#link-name'), 'Live updates on. Show recent notices.');
      });
      await step('a restart takes it offline, and it comes back live', async () => {
        await dev(server, 'down', { seconds: 4 });
        await page.locator('#link[data-link="offline"], #link[data-link="connecting"]').first().waitFor({ timeout: 10000 });
        await page.locator('#link[data-link="live"]').waitFor({ timeout: 30000 });
      });
    },
  },
  {
    id: 'SHELL-offline-long',
    ui: ['next'],
    allow: DOWN,
    async run({ page, url, ready, server, step }) {
      await page.clock.install();
      await page.goto(url('/screen'));
      await ready();
      await page.locator('#link[data-link="live"]').waitFor();
      // The shell keeps the Wake-on-LAN adapters once VaporOS answers GET /power.
      await page.waitForFunction(() => JSON.parse(localStorage.getItem('vos-last') || '{}').wol);
      await step('powered off behind the page: after 10 s the page says it shows the last known state', async () => {
        await dev(server, 'down', { seconds: -1 });
        await page.locator('#link[data-link="offline"]').waitFor({ timeout: 15000 });
        await page.clock.fastForward(11000);
        await page.locator('#offline:not([hidden])').waitFor();
        assert.equal(await text(page, '#offline-text'), 'Showing the last known state.');
      });
      await step('after a minute: VaporOS might be asleep, with the Asleep scene and the Wake card', async () => {
        await page.clock.fastForward(50000);
        await page.locator('#scene[open][data-state="asleep"]').waitFor();
        assert.equal(await text(page, '#scene-title'), 'Asleep');
        // Its clock counts the outage, not how long the scene has been open.
        assert.match(await text(page, '#scene-elapsed'), /^0?1:0\d$/);
        await page.locator('#scene-wake:not([hidden])').waitFor();
        assert.equal(await text(page, '#scene-wake-mac'), '02:5e:a1:3c:4d:7f');
        assert.equal(await text(page, '#scene-wake-bcast'), '192.168.1.255');
        assert.match(await text(page, '#offline-text'), /might be asleep/);
      });
      await step('the viewer can close it and look at the last known state', async () => {
        await page.click('#scene-close');
        await closed(page, 'scene');
      });
      await step('woken, the page reconnects by itself', async () => {
        await dev(server, 'down', { seconds: 0 });
        await page.clock.fastForward(35000);
        await page.locator('#link[data-link="live"]').waitFor({ timeout: 20000 });
        await page.waitForFunction(() => document.getElementById('offline').hidden);
      });
    },
  },
  {
    id: 'SHELL-strip-sheet',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('the strip names the device, the mode and how long', async () => {
        await page.locator('#strip:not([hidden])').waitFor();
        assert.equal(await text(page, '#strip-client'), 'Living room TV');
        assert.equal(await text(page, '#strip-mode'), '3840 × 2160 · 60 Hz · HDR');
        assert.match(await text(page, '#strip-since'), /^\d+ min$/);
        assert.equal(await text(page, '#strip-name'), 'Now streaming: Living room TV, 3840 by 2160 at 60 hertz, HDR. Show details.');
      });
      await step('its sheet has the rows, and the heading takes focus', async () => {
        await page.click('#strip-open');
        await page.locator('#stream-sheet[open]').waitFor();
        assert.equal(await text(page, '#ss-mode'), '3840 × 2160 · 60 Hz');
        assert.equal(await text(page, '#ss-hdr'), 'On');
        assert.equal(await text(page, '#ss-app'), 'Steam');
        assert.equal(await focused(page), 'stream-sheet-title');
      });
      await step('End stream asks first (Cancel focused), then ends it and the strip goes', async () => {
        await page.click('#ss-end');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'End the stream?');
        assert.equal(await focused(page), 'confirm-cancel');
        await page.click('#confirm-ok');
        await page.locator('#strip[hidden]').waitFor({ state: 'attached', timeout: 8000 });
      });
    },
  },
  {
    id: 'SHELL-strip-resume-hint',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, step }) {
      await page.goto(url('/screen'));
      await ready();
      await step('the stream sheet says how to switch devices', async () => {
        await page.click('#strip-open');
        assert.equal(await text(page, '#ss-hint'), 'Switching devices or changing the resolution? Quit the game in Moonlight, then start it again.');
      });
    },
  },
  {
    id: 'SHELL-hold-streaming',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, step }) {
      await page.goto(url('/system'));
      await ready();
      await step('while someone streams, holding never fires and the hint names who', async () => {
        await page.locator('#sys-reboot[data-hold-disabled]').waitFor({ state: 'attached' });
        assert.match(await text(page, '#sys-hold-hint'), /Ends the stream to Living room TV\./);
        await press(page, '#sys-reboot', 1400);
        await page.locator('#confirm[open]').waitFor();
        // The question names who is streaming, as the hint does.
        assert.equal(await text(page, '#confirm-body'), 'The stream to Living room TV stops. VaporOS is back in about a minute.');
        await assertAxe(page, 'the confirm dialog open');
        await page.keyboard.press('Escape');
        assert.equal(await page.locator('#scene[open]').count(), 0);
      });
    },
  },
  {
    id: 'SHELL-hold-nowol',
    ui: ['next'],
    preset: 'no-wol',
    async run({ page, url, ready, step }) {
      await page.goto(url('/system'));
      await ready();
      await step('with Wake-on-LAN off, Power off only asks, in its own words', async () => {
        await page.locator('#sys-poweroff[data-hold-disabled]').waitFor({ state: 'attached' });
        assert.match(await text(page, '#sys-hold-hint'), /Wake-on-LAN is off: only the power button starts it again\./);
        await page.click('#sys-poweroff');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-body'), 'Wake-on-LAN is off, so only its power button starts it again.');
      });
    },
  },
  {
    id: 'SHELL-hold-staged',
    ui: ['next'],
    preset: 'update-staged',
    async run({ page, url, ready, step }) {
      await page.goto(url('/system'));
      await ready();
      await step('with an update staged, the hint and the confirm say it installs', async () => {
        await page.locator('#sys-hold-hint', { hasText: 'Restarting also installs version 20260929.143000.' }).waitFor();
        await page.focus('#sys-reboot');
        await page.keyboard.press('Enter');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart and update?');
        await page.keyboard.press('Escape');
        await closed(page, 'confirm');
      });
      for (const path of ['/system', '/devices', '/screen']) {
        await step(`a staged update alone is ready, not a restart to finish: no restart row on ${path}, no System badge`, async () => {
          await page.goto(url(path));
          await ready();
          await page.waitForFunction(() => document.documentElement.dataset.state === 'ready');
          assert.equal(await page.isVisible('#restart-row'), false);
          assert.equal(await page.locator('#nav a[href="/system"] [data-part="badge"]').isVisible(), false);
        });
      }
    },
  },
  {
    id: 'SHELL-restart-row',
    ui: ['next'],
    preset: 'reboot-needed',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/screen'));
      await ready();
      await step('display changes show the restart row, and System carries a badge', async () => {
        await page.locator('#restart-row:not([hidden])').waitFor();
        assert.equal(await text(page, '#restart-text'), 'Restart to apply screen changes.');
        assert.equal(await page.locator('#nav a[href="/system"] [data-part="badge"]').isVisible(), true);
      });
      await step('its Restart asks first', async () => {
        await page.click('#restart-go');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart VaporOS?');
        await page.keyboard.press('Escape');
        await closed(page, 'confirm');
      });
      await step('a newer version waiting with nothing staged (a forward rollback) restarts plainly, never Restart to update', async () => {
        await dev(server, 'preset', { name: 'rollback-forward' });
        await page.goto(url('/devices'));
        await ready();
        await page.locator('#restart-row:not([hidden])').waitFor();
        assert.equal(await text(page, '#restart-text'), 'Version 20260929.143000 starts on the next restart.');
        assert.equal(await text(page, '#restart-go'), 'Restart');
        await page.click('#restart-go');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart VaporOS?');
      });
    },
  },
  {
    id: 'SHELL-boot',
    ui: ['next'],
    async run({ page, url, step }) {
      await step('before any API answer the frame and the skeletons are there, without a spinner', async () => {
        let release;
        const held = new Promise((r) => {
          release = r;
        });
        await page.route('**/api/v1/**', async (r) => {
          await held;
          await r.continue().catch(() => {});
        });
        await page.goto(url('/devices'), { waitUntil: 'domcontentloaded' });
        await page.locator('#nav').waitFor();
        assert.equal(await page.getAttribute('main', 'aria-busy'), 'true');
        assert.equal(await page.locator('.skel').count() > 0, true);
        assert.equal(await page.locator('form button[type="submit"]:not([disabled])').count(), 0);
        release();
        await page.unroute('**/api/v1/**');
      });
    },
  },
  {
    id: 'SHELL-landscape',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.setViewportSize({ width: 844, height: 390 });
      await page.goto(url('/devices'));
      await ready();
      await step('a phone on its side keeps the tabs, as icons with their names for screen readers', async () => {
        assert.equal(await page.locator('.tab-label').first().evaluate((el) => el.getBoundingClientRect().width <= 1), true);
        assert.equal(await page.getByRole('link', { name: 'Devices' }).count(), 1);
      });
    },
  },
  {
    id: 'SHELL-noscript',
    ui: ['next'],
    async run({ page, url, step }) {
      await step('without JavaScript the page says it needs it', async () => {
        const ctx = await page.context().browser().newContext({ javaScriptEnabled: false });
        try {
          const p = await ctx.newPage();
          await p.goto(url('/devices'));
          const note = p.locator('.noscript');
          assert.equal(await note.isVisible(), true);
          assert.equal((await note.textContent()).trim(), 'VaporOS needs JavaScript to show this page.');
        } finally {
          await ctx.close();
        }
      });
    },
  },
  {
    id: 'SHELL-api-client',
    ui: ['next'],
    // Each answer this flow provokes is logged by the browser: the stale
    // token's 403, the forced 429 and the ended session's 401.
    allow: [/status of (401|403|429)/, /(401|403|429) (GET|POST) .*\/api\/v1\//],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      const awake = page.locator('#home-awake:enabled');
      await awake.waitFor();
      await step('a sign-in in another tab rotates the CSRF token: the next write fetches it and tries once more', async () => {
        await page.evaluate(() =>
          fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: 'vaporvapor' }) }),
        );
        const answers = [];
        const count = (r) => r.url().endsWith('/api/v1/power/keep-awake') && answers.push(r.status());
        page.on('response', count);
        await awake.click();
        await page.locator('#home-awake[aria-pressed="true"]').waitFor();
        page.off('response', count);
        assert.deepEqual(answers, [403, 200]);
      });
      await step('a 429 says how long to wait, from Retry-After', async () => {
        await page.route('**/api/v1/power/keep-awake', (r) => r.fulfill({ status: 429, headers: { 'Retry-After': '30' }, contentType: 'application/json', body: '{"error":"too many requests"}' }));
        await awake.click();
        await page.locator('#notices-alert .notice', { hasText: 'Too many tries. Try again in 30 s.' }).waitFor();
        await page.unroute('**/api/v1/power/keep-awake');
      });
      await step('a session that ended elsewhere goes to sign-in, which says so (ended=1)', async () => {
        await page.evaluate(async () => {
          const me = await (await fetch('/api/v1/auth/me')).json();
          await fetch('/api/v1/auth/logout', { method: 'POST', headers: { 'X-VOS-CSRF': me.csrf } });
        });
        await awake.click();
        await page.waitForURL((u) => new URL(u).pathname === '/login' && new URL(u).searchParams.get('ended') === '1' && new URL(u).searchParams.get('next') === '/');
      });
    },
  },
  {
    id: 'SHELL-sse-backoff',
    ui: ['next'],
    // The stream is refused on purpose, and the box is down for a second.
    allow: [/Failed to load resource/, /net::ERR_/, /status of 503/, /\/api\/v1\/(events|ping|status|auth\/me|system|sunshine)/, /ERR_EMPTY_RESPONSE|ERR_CONNECTION/],
    async run({ page, url, ready, server, step }) {
      const seen = [];
      page.on('request', (r) => {
        const u = new URL(r.url());
        if (/\/api\/v1\/(events|auth\/me)$/.test(u.pathname)) seen.push({ at: Date.now(), path: u.pathname, passive: u.searchParams.get('passive') === '1' });
      });
      await page.goto(url('/devices'));
      await ready();
      await page.locator('#link[data-link="live"]').waitFor();
      await step('refused, the stream goes offline and retries with a session re-check, backing off from 1 s', async () => {
        await page.route('**/api/v1/events*', (r) => r.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"starting"}' }));
        const from = Date.now();
        await dev(server, 'down', { seconds: 1 });
        await page.locator('#link[data-link="offline"]').waitFor({ timeout: 20000 });
        await page.waitForTimeout(9000);
        const me = seen.filter((x) => x.at > from && x.path.endsWith('/auth/me')).map((x) => x.at);
        assert.ok(me.length >= 2, `${me.length} session re-checks`);
        const gaps = me.slice(1).map((t, i) => t - me[i]);
        for (let i = 1; i < gaps.length; i++) assert.ok(gaps[i] > gaps[i - 1] * 1.4, `the waits grow: ${gaps.join(', ')} ms`);
        assert.ok(seen.some((x) => x.at > from && x.path.endsWith('/events') && x.passive), 'a reopened stream is passive');
      });
      await step('once the stream answers again, the page is live', async () => {
        await page.unroute('**/api/v1/events*');
        await page.locator('#link[data-link="live"]').waitFor({ timeout: 40000 });
      });
    },
  },
  {
    id: 'SHELL-feedback',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/system/settings'));
      await ready();
      await step('a bad value is an inline error under its field, which takes focus: no native bubble', async () => {
        await page.fill('#pw-current', 'vaporvapor');
        await page.fill('#pw-new', 'short');
        await page.fill('#pw-again', 'short');
        await page.click('#pw-save');
        await page.locator('#pw-new-error:not([hidden])').waitFor();
        assert.equal(await page.getAttribute('#pw-new', 'aria-invalid'), 'true');
        assert.equal(await focused(page), 'pw-new');
        assert.equal(await page.evaluate(() => document.getElementById('pw-new').validationMessage), '');
      });
      await step('a busy key keeps focus while it works, and the notice is spoken once', async () => {
        await page.evaluate(() => {
          window.vosSaid = [];
          const el = document.getElementById('announce-polite');
          new MutationObserver(() => el.textContent && window.vosSaid.push(el.textContent)).observe(el, { childList: true, characterData: true, subtree: true });
        });
        await page.fill('#pw-new', 'vaporvapor2');
        await page.fill('#pw-again', 'vaporvapor2');
        await page.focus('#pw-save');
        await page.keyboard.press('Enter');
        await page.locator('#notices-polite .notice', { hasText: 'Password changed.' }).waitFor();
        assert.notEqual(await page.evaluate(() => document.activeElement?.tagName), 'BODY');
        await page.waitForTimeout(1200);
        const said = await page.evaluate(() => window.vosSaid);
        assert.equal(said.filter((x) => x.startsWith('Password changed.')).length, 1, said.join(' | '));
        // The stack is not a live region: the Dismiss button's name is never read with it.
        assert.equal(await page.getAttribute('#notices-polite', 'role'), null);
      });
      await step('with a notice up, the page passes axe', async () => {
        await assertAxe(page, 'a notice showing');
      });
    },
  },
  {
    id: 'SHELL-dom-safe',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      const evil = '<img src=x onerror=alert(1)>';
      await patchJSON(page, '/status', (s) => ({ ...s, system: { ...s.system, hostname: evil, mdns: '', gpu: { ...s.system.gpu, name: evil } } }));
      let dialogs = 0;
      page.on('dialog', (d) => {
        dialogs++;
        d.dismiss().catch(() => {});
      });
      await page.goto(url('/'));
      await ready();
      await step('names from the API are text, never markup', async () => {
        await page.locator('#hero-gpu', { hasText: evil }).waitFor();
        assert.equal(await page.locator('#hero-gpu').textContent(), evil);
        assert.equal(await page.locator('main img, #app img').count(), 0);
        assert.equal(dialogs, 0);
      });
      await page.unrouteAll({ behavior: 'ignoreErrors' });
    },
  },
  {
    id: 'SHELL-forced-colors',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.emulateMedia({ forcedColors: 'active' });
      await page.goto(url('/devices'));
      await ready();
      await step('the current tab keeps a Highlight outline', async () => {
        const o = await page.locator('#nav a[aria-current]').evaluate((el) => getComputedStyle(el).outlineStyle + ' ' + getComputedStyle(el).outlineWidth);
        assert.match(o, /^solid [2-9]/);
      });
      await step('buttons, fields and sheets keep an edge, and a focused field a ring', async () => {
        for (const sel of ['#dev-addrs .copy', '#dev-refresh']) {
          const b = await page.locator(sel).first().evaluate((el) => getComputedStyle(el).borderTopStyle);
          assert.equal(b, 'solid', sel);
        }
        await page.goto(url('/system/settings'));
        await ready();
        await page.focus('#pw-current');
        const ring = await page.locator('#pw-current').evaluate((el) => [getComputedStyle(el).borderTopStyle, getComputedStyle(el).outlineStyle]);
        assert.deepEqual(ring, ['solid', 'solid']);
      });
      await step('the hold fill shows in Highlight while a key is held', async () => {
        await page.goto(url('/system'));
        await ready();
        await page.locator('#sys-reboot').evaluate((el) => el.scrollIntoView({ block: 'center' }));
        const box = await page.locator('#sys-reboot').boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.waitForTimeout(400);
        const fill = await page.locator('#sys-reboot .hold-fill').evaluate((el) => getComputedStyle(el).forcedColorAdjust);
        await page.mouse.up();
        assert.equal(fill, 'none');
      });
      await step('axe passes in forced colours (its contrast rule measures author colours, so it is off)', async () => {
        await assertAxe(page, 'forced colours');
      });
    },
  },
  {
    id: 'SHELL-safe-area',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('the app bar, the tab bar and the page keep clear of the notch and the home indicator', async () => {
        const found = await page.evaluate(() => {
          const want = {
            '.appbar': ['padding', 'safe-area-inset-top'],
            '.tabs': ['padding', 'safe-area-inset-bottom'],
            '.main': ['padding', 'safe-area-inset-left'],
          };
          const hits = {};
          const walk = (rules) => {
            for (const r of rules) {
              if (r.cssRules) walk(r.cssRules);
              if (!r.selectorText || !r.style) continue;
              for (const [sel, [prop, inset]] of Object.entries(want)) {
                if (r.selectorText.split(',').map((x) => x.trim()).includes(sel) && r.style.cssText.includes(prop) && r.style.cssText.includes(`env(${inset})`)) hits[sel] = true;
              }
            }
          };
          for (const sh of document.styleSheets) walk(sh.cssRules);
          return hits;
        });
        assert.deepEqual(found, { '.appbar': true, '.tabs': true, '.main': true });
        assert.equal(await page.getAttribute('meta[name="viewport"]', 'content'), 'width=device-width, initial-scale=1, viewport-fit=cover');
      });
    },
  },
  {
    id: 'SHELL-draft-restore',
    ui: ['next'],
    preset: 'ssh-on',
    async run({ page, url, ready, step }) {
      const key = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC0draft draft@laptop';
      await page.goto(url('/system/settings'));
      await ready();
      await step('unsaved text in a form survives going to another tab and back', async () => {
        await page.locator('#ssh-keys:enabled').waitFor();
        await page.fill('#ssh-keys', key);
        // (The tab bar steps aside for the keyboard while a field has focus.)
        await page.evaluate(() => document.activeElement?.blur());
        await page.click('#nav a[href="/devices"]');
        await ready();
        // A fresh load, not the back/forward cache, which would keep the text anyway.
        await page.goto(url('/system/settings'));
        await ready();
        await page.locator('#ssh-keys:enabled').waitFor();
        assert.equal(await page.inputValue('#ssh-keys'), key);
        assert.equal(await page.isEnabled('#ssh-save'), true);
      });
      await step('a password is never kept', async () => {
        await page.fill('#pw-current', 'secret-typed');
        await page.evaluate(() => document.activeElement?.blur());
        await page.click('#nav a[href="/devices"]');
        await ready();
        await page.goto(url('/system/settings'));
        await ready();
        assert.equal(await page.inputValue('#pw-current'), '');
        assert.ok(!(await page.evaluate(() => JSON.stringify(sessionStorage))).includes('secret-typed'));
      });
    },
  },
  {
    id: 'SHELL-handshake',
    ui: ['next'],
    preset: 'signed-out',
    async run({ page, url, ready, server, step }) {
      let mark = '';
      await step('sign-in shows the mark the TV draws beside its address', async () => {
        await page.goto(url('/login'));
        await ready();
        mark = await page.locator('svg.handshake').first().evaluate((el) => el.outerHTML);
        assert.match(mark, /viewBox="0 0 5 5"/);
      });
      await step('first-run setup shows the same mark', async () => {
        await dev(server, 'preset', { name: 'first-run' });
        await page.goto(url('/setup'));
        await ready();
        assert.equal(await page.locator('svg.handshake').first().evaluate((el) => el.outerHTML), mark);
      });
    },
  },
];
