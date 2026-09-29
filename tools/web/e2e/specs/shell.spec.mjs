// Flows over the next control center's shell (spec-cc-screens §2, ARCH §6):
// the tabs, the old URLs, sign-in redirects, notices and Recent, confirm and
// hold, the reconnection scene, the connection indicator and the Asleep
// scene, the strip and its sheet, the restart row and the PIN pad. Each ID
// is a row of tools/web/e2e/parity.json (owner C0b). See legacy.spec.mjs for
// the flow format.

import assert from 'node:assert/strict';

import { closed, dev } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const focused = (page) => page.evaluate(() => document.activeElement?.id || document.activeElement?.className || '');

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
        await page.goto(url('/advanced?x=1#logs'));
        await page.waitForURL((u) => new URL(u).pathname === '/system/settings' && new URL(u).search === '?x=1');
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
        const box = await page.locator('#sys-reboot').boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.waitForTimeout(500);
        await page.mouse.up();
        assert.match(await text(page, '#sys-hold-hint'), /^Keep holding Restart until the key fills\.$/);
        assert.equal(await page.locator('#confirm[open]').count(), 0);
      });
      await step('holding Restart for 1.2 s restarts without a dialog and shows the scene', async () => {
        const box = await page.locator('#sys-reboot').boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.waitForTimeout(1400);
        await page.mouse.up();
        await page.locator('#scene[open]').waitFor();
        assert.equal(await text(page, '#scene-title'), 'Restarting');
        assert.equal(await page.locator('#confirm[open]').count(), 0);
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
        assert.equal(await text(page, '#strip-mode'), '3840×2160 · 60 Hz · HDR');
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
        const box = await page.locator('#sys-reboot').boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.waitForTimeout(1400);
        await page.mouse.up();
        await page.locator('#confirm[open]').waitFor();
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
      });
    },
  },
  {
    id: 'SHELL-restart-row',
    ui: ['next'],
    preset: 'reboot-needed',
    async run({ page, url, ready, step }) {
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
];
