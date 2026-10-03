// Flows over Home (spec-cc-screens §3, MASTER-PLAN §3.5 C1): the state hero
// in each state, its screen shape and eyebrow, the action row (Pair, Stay
// awake, the Power sheet under the hold rule), the one context card with
// "Show n more", the power line and the update status line. Each ID is a
// HOME- row of tools/web/e2e/parity.json (owner C1). See legacy.spec.mjs for
// the flow format.

import assert from 'node:assert/strict';

import { closed, dev } from '../lib/dev.mjs';

const text = async (page, sel) => ((await page.textContent(sel)) ?? '').replace(/\s+/g, ' ').trim();
const focused = (page) => page.evaluate(() => document.activeElement?.id || '');
const phone = (page) => page.viewportSize().width < 1024;
const hero = (page, state) => page.locator(`#hero[data-state="${state}"]`).waitFor();
// Events sent before the stream is open, or in its first 400 ms (core/live.js),
// count as its replay, which Home rightly ignores for progress (B1).
async function live(page) {
  await page.locator('#link[data-link="live"]').waitFor({ state: 'attached' });
  await page.waitForTimeout(500);
}

// Home's own answers, changed on the way: the fake API has no preset for a
// failing /status part.
// A late refresh can still be in flight when the flow ends: never throw.
async function patchStatus(page, patch) {
  await page.route('**/api/v1/status', async (route) => {
    try {
      // Node can't resolve vapor.local (the browser maps it), so ask the loopback.
      const res = await route.fetch({ url: route.request().url().replace('//vapor.local:', '//127.0.0.1:') });
      await route.fulfill({ response: res, json: patch(await res.json()) });
    } catch {
      await route.abort().catch(() => {});
    }
  });
}

// unroute drops every route, including one whose answer is still coming.
const unroute = (page) => page.unrouteAll({ behavior: 'ignoreErrors' });

// A press held on the key, in the middle of it.
async function hold(page, sel, ms) {
  await page.locator(sel).hover(); // waits until a sheet has slid into place
  await page.mouse.down();
  await page.waitForTimeout(ms);
  await page.mouse.up();
}

// A box that is down answers nothing: the browser logs every refused request.
const DOWN = [/Failed to load resource/, /net::ERR_/, /\/api\/v1\/(ping|events|system|auth\/me|status)/, /ERR_EMPTY_RESPONSE|ERR_CONNECTION/];

export default [
  {
    id: 'HOME-hero-ready',
    ui: ['next'],
    allow: DOWN,
    async run({ page, url, ready, server, step }) {
      await step('the server paints the hero as Checking… in the neutral state', async () => {
        await page.route('**/api/v1/status', (r) => setTimeout(() => r.continue().catch(() => {}), 1500));
        await page.goto(url('/'), { waitUntil: 'domcontentloaded' });
        assert.equal(await text(page, '#hero-title'), 'Checking…');
        assert.equal(await page.getAttribute('#hero', 'data-state'), '');
        assert.equal(await page.getAttribute('#home', 'aria-busy'), 'true');
        assert.equal(await page.isDisabled('#home-power'), true);
        await ready();
        await unroute(page);
      });
      await step('with nothing to do it reads Ready to stream, at the ready heat', async () => {
        await page.goto(url('/'));
        await ready();
        await hero(page, 'ready');
        assert.equal(await text(page, '#hero-title'), 'Ready to stream');
        assert.equal(await text(page, '#hero-needle'), 'ready');
        assert.equal(await page.locator('#hero-actions > *').count(), 0);
        assert.equal(await page.getAttribute('#home', 'aria-busy'), null);
        assert.equal(await page.isVisible('#cards'), false);
        assert.equal(await page.isVisible('#home-pair'), false);
      });
      await step('the heading is h2 under the page h1, and the field is decoration', async () => {
        assert.equal(await page.evaluate(() => document.getElementById('hero-title').tagName), 'H2');
        assert.equal(await page.getAttribute('#hero .heat-field', 'aria-hidden'), 'true');
        assert.equal(await page.getAttribute('#hero .screen-shape', 'aria-hidden'), 'true');
      });
      await step('the box stops answering: the last known Ready cools at once, and the needle says last seen', async () => {
        await page.locator('#link[data-link="live"]').waitFor();
        await dev(server, 'down', { seconds: -1 });
        await page.locator('#hero[data-stale]').waitFor({ timeout: 15000 });
        assert.equal(await text(page, '#hero-needle'), 'last seen');
        assert.equal(await text(page, '#hero-title'), 'Ready to stream');
      });
      await step('back, it is live and warm again', async () => {
        await dev(server, 'down', { seconds: 0 });
        await page.locator('#link[data-link="live"]').waitFor({ timeout: 40000 });
        await page.waitForFunction(() => !document.getElementById('hero').hasAttribute('data-stale'));
      });
    },
  },
  {
    id: 'HOME-hero-welcome',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await step('with the monitor on the welcome screen, the detail says so', async () => {
        await page.goto(url('/'));
        await ready();
        await hero(page, 'ready');
        assert.equal(await text(page, '#hero-detail'), 'The monitor shows the welcome screen until a game starts.');
      });
      await step('otherwise it says how to start: Moonlight, and this PC by name', async () => {
        await patchStatus(page, (s) => ({ ...s, display: { ...s.display, state: 'gaming' } }));
        await page.reload();
        await ready();
        assert.equal(await text(page, '#hero-detail'), 'Open Moonlight on any device and pick vapor.');
      });
      await unroute(page);
    },
  },
  {
    id: 'HOME-hero-eyebrow',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('the eyebrow names the machine (the app bar has its address) and the graphics card, on one line', async () => {
        await hero(page, 'ready');
        assert.equal(await text(page, '#hero-host'), 'vapor');
        assert.equal(await text(page, '#hero-gpu'), 'AMD Radeon RX 9070 XT');
        const tops = await page.$$eval('#hero-eyebrow .vf-plate:not([hidden])', (els) => els.map((e) => Math.round(e.getBoundingClientRect().top)));
        assert.equal(new Set(tops).size, 1, `plates on ${tops.join(', ')}`);
      });
      await step('without mDNS it falls back to the hostname', async () => {
        await patchStatus(page, (s) => ({ ...s, system: { ...s.system, mdns: '', hostname: 'den' } }));
        await page.reload();
        await ready();
        await page.locator('#hero-host', { hasText: /^den$/ }).waitFor();
      });
      await unroute(page);
    },
  },
  {
    id: 'HOME-hero-mode-chip',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('idle, the mode is a tag: no virtual screen is drawn, as none exists until a game starts', async () => {
        await hero(page, 'ready');
        assert.equal(await page.getAttribute('#hero .screen-shape', 'data-shown'), null);
        assert.equal(await text(page, '#hero-chips'), '1920 × 1080 · 60 Hz');
        assert.equal(await page.locator('#hero-chips li.hero-tag[data-kind="mode"]').count(), 1);
        assert.equal(await page.locator('#hero-chips li.sr-only').count(), 0);
      });
      await step('without a current mode there is no shape and no chip', async () => {
        await patchStatus(page, (s) => ({ ...s, display: { ...s.display, current: '' } }));
        await page.reload();
        await ready();
        await hero(page, 'ready');
        assert.equal(await page.getAttribute('#hero .screen-shape', 'data-shown'), null);
        assert.equal(await page.locator('#hero-chips li').count(), 0);
      });
      await unroute(page);
    },
  },
  {
    id: 'HOME-hero-screen-shape',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, server, step }) {
      const ratio = () => page.evaluate(() => {
        const r = document.querySelector('#hero [data-part="frame"]').getBoundingClientRect();
        return r.width / r.height;
      });
      await page.goto(url('/'));
      await ready();
      await live(page);
      await step("the shape is the client's screen: 16:9 for the TV", async () => {
        await hero(page, 'streaming');
        assert.equal(await page.getAttribute('#hero .screen-shape', 'data-shown'), '');
        assert.equal(await text(page, '#hero [data-part="readout"]'), '3840 × 2160 · 60 Hz');
        await page.waitForTimeout(400);
        assert.ok(Math.abs((await ratio()) - 16 / 9) < 0.05, `ratio ${await ratio()}`);
      });
      await step('a phone takes over and the shape reshapes to its mode', async () => {
        await dev(server, 'event', { topic: 'session.begin', data: { client: 'Pixel 9', mode: '2796x1290@120', hdr: false, since: new Date().toISOString() } });
        await page.locator('#hero [data-part="readout"]', { hasText: '2796 × 1290 · 120 Hz' }).waitFor();
        await page.waitForTimeout(500);
        assert.ok(Math.abs((await ratio()) - 2796 / 1290) < 0.06, `ratio ${await ratio()}`);
        assert.equal(await text(page, '#hero-title'), 'Streaming to Pixel 9');
      });
    },
  },
  {
    id: 'HOME-hero-streaming',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/'));
      await ready();
      await live(page);
      await step('the hero reads Streaming to the device, hot, with the mode and HDR', async () => {
        await hero(page, 'streaming');
        assert.equal(await text(page, '#hero-title'), 'Streaming to Living room TV');
        assert.equal(await text(page, '#hero .hero-word-lead'), 'Streaming');
        assert.equal(await text(page, '#hero .hero-word-rest'), 'to Living room TV');
        assert.equal(await text(page, '#hero-needle'), 'streaming');
        assert.deepEqual(await page.locator('#hero-chips li').allTextContents(), ['3840 × 2160 · 60 Hz', 'HDR']);
        assert.equal(await page.getAttribute('#hero-chips li:nth-child(2)', 'data-tone'), 'hot');
      });
      await step('the word Streaming never breaks, down to 320 px and at 200 % text', async () => {
        const oneLine = () => page.evaluate(() => {
          const el = document.querySelector('#hero .hero-word-lead');
          const r = document.createRange();
          r.selectNodeContents(el);
          const lines = new Set([...r.getClientRects()].map((x) => Math.round(x.top)));
          const vf = el.closest('.vf').getBoundingClientRect();
          const b = el.getBoundingClientRect();
          return { lines: lines.size, inside: b.right <= vf.right + 1 && b.left >= vf.left - 1 };
        });
        const size = page.viewportSize();
        for (const width of [size.width, 320]) {
          await page.setViewportSize({ width, height: size.height });
          await page.waitForTimeout(150);
          assert.deepEqual(await oneLine(), { lines: 1, inside: true }, `at ${width} px`);
        }
        await page.evaluate(() => document.documentElement.style.setProperty('font-size', '200%'));
        await page.waitForTimeout(150);
        assert.deepEqual(await oneLine(), { lines: 1, inside: true }, 'at 200 % text');
        await page.evaluate(() => document.documentElement.style.removeProperty('font-size'));
        await page.setViewportSize(size);
      });
      await step('Details is a way to the stream sheet, not the thing to act on: a ghost key', async () => {
        assert.equal(await page.getAttribute('#hero-actions button', 'class'), 'btn ghost');
      });
      await step('Details opens the stream sheet, and Esc gives focus back', async () => {
        await page.click('#hero-actions button');
        await page.locator('#stream-sheet[open]').waitFor();
        assert.equal(await text(page, '#ss-client'), 'Living room TV');
        await page.keyboard.press('Escape');
        await closed(page, 'stream-sheet');
        assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Details');
      });
      if (phone(page)) {
        await step('the strip docks only once the hero scrolls away', async () => {
          const size = page.viewportSize();
          await page.setViewportSize({ width: size.width, height: 300 });
          assert.equal(await page.isVisible('#strip'), false);
          await page.evaluate(() => scrollTo(0, document.body.scrollHeight));
          await page.locator('#strip:not([hidden])').waitFor();
          await page.evaluate(() => scrollTo(0, 0));
          await page.locator('#strip').waitFor({ state: 'hidden' });
          await page.setViewportSize(size);
        });
      }
      await step('when the stream ends, the hero cools to Ready, said at most once', async () => {
        await page.evaluate(() => {
          window.vosSaid = [];
          const el = document.getElementById('announce-polite');
          new MutationObserver(() => window.vosSaid.push(el.textContent)).observe(el, { childList: true, characterData: true, subtree: true });
        });
        await dev(server, 'script', { name: 'stream-end' });
        await hero(page, 'ready');
        assert.equal(await text(page, '#hero-title'), 'Ready to stream');
        await page.waitForTimeout(2500);
        const said = (await page.evaluate(() => window.vosSaid)).filter(Boolean);
        assert.ok(said.length > 0 && said.filter((x) => x === 'Ready to stream').length <= 1, `announced: ${said.join(' | ')}`);
      });
    },
  },
  {
    id: 'HOME-hero-updating',
    ui: ['next'],
    preset: 'update-staging',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/'));
      await ready();
      await live(page);
      await step('a download in progress heats the hero to Updating, with the bar', async () => {
        await hero(page, 'updating');
        assert.equal(await text(page, '#hero-title'), 'Updating to 20260929.143000');
        assert.equal(await page.isVisible('#hero-progress'), true);
        assert.equal(await text(page, '#hero-phase'), 'Downloading');
        assert.ok(Number(await page.getAttribute('#hero-bar', 'aria-valuenow')) >= 42);
        assert.match(await page.getAttribute('#hero-bar', 'aria-valuetext'), /^Downloading, \d+ percent$/);
        assert.equal(await text(page, '#hero-detail'), 'Keep playing: it switches over when VaporOS restarts.');
        assert.equal(await page.getAttribute('#hero-actions a', 'href'), '/system/updates');
      });
      await step('live ticks move the bar, and nothing is announced', async () => {
        const from = Number(await page.getAttribute('#hero-bar', 'aria-valuenow'));
        await page.waitForFunction((n) => Number(document.getElementById('hero-bar').getAttribute('aria-valuenow')) > n, from, { timeout: 20000 });
        assert.equal(await text(page, '#hero-title'), 'Updating to 20260929.143000');
        await page.waitForTimeout(1200);
        assert.equal(await text(page, '#announce-polite'), '');
      });
    },
  },
  {
    id: 'HOME-hero-nogpu',
    ui: ['next'],
    preset: 'no-gpu',
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('no supported card: the cold fault, its words, and no action', async () => {
        await hero(page, 'fault');
        assert.equal(await text(page, '#hero-title'), 'No supported graphics card');
        assert.equal(await page.getAttribute('#hero-title', 'data-long'), '');
        assert.match(await text(page, '#hero-detail'), /Streaming needs an AMD Radeon GPU\.$/);
        assert.equal(await page.isVisible('#hero .vf-tape'), true);
        assert.equal(await page.locator('#hero-actions > *').count(), 0);
        assert.equal(await page.getAttribute('#hero .screen-shape', 'data-shown'), null);
      });
    },
  },
  {
    id: 'HOME-hero-sun-stopped',
    ui: ['next'],
    preset: 'sunshine-stopped',
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('a stopped stream server is a fault with Restart streaming', async () => {
        await hero(page, 'fault');
        assert.equal(await text(page, '#hero-title'), 'Streaming is stopped');
        assert.equal(await page.getAttribute('#hero', 'data-reason'), 'stopped');
        assert.equal(await text(page, '#hero-actions button'), 'Restart streaming');
      });
      await step('Restart streaming restarts it and the hero comes back to Ready', async () => {
        await page.click('#hero-actions button');
        await page.locator('#notices-polite .notice', { hasText: 'Streaming is restarting.' }).waitFor();
        await hero(page, 'ready');
      });
    },
  },
  {
    id: 'HOME-hero-unreadable',
    ui: ['next'],
    // The flow answers /status and /sunshine with errors on purpose.
    allow: [/status of (500|502|503)/, /(500|502|503) GET .*\/api\/v1\/(status|sunshine)$/],
    async run({ page, url, ready, server, step }) {
      const sunshine = async (status) => {
        await page.unroute('**/api/v1/sunshine');
        await page.route('**/api/v1/sunshine', (r) => r.fulfill({ status, json: { error: 'Sunshine is still being set up; try again in a few seconds' } }).catch(() => {}));
        await page.reload();
        await ready();
      };
      await patchStatus(page, (s) => ({ ...s, sunshine: null }));
      await page.goto(url('/'));
      await step('a /status without its stream part asks /sunshine: 503 is Starting up', async () => {
        await sunshine(503);
        await page.locator('#hero-title', { hasText: 'Starting up…' }).waitFor();
        assert.equal(await page.getAttribute('#hero', 'data-state'), '');
        assert.equal(await text(page, '#hero-needle'), 'starting');
      });
      await step('502 is Streaming isn’t answering, with Restart streaming and the log', async () => {
        await sunshine(502);
        await page.locator('#hero-title', { hasText: "Streaming isn't answering" }).waitFor();
        assert.deepEqual(await page.locator('#hero-actions > *').allTextContents(), ['Restart streaming', 'View log']);
        assert.equal(await page.getAttribute('#hero-actions a', 'href'), '/system/logs');
      });
      await step('anything else is Can’t read the streaming status', async () => {
        await sunshine(500);
        await page.locator('#hero-title', { hasText: "Can't read the streaming status" }).waitFor();
        await hero(page, 'fault');
      });
      await step('a live pairing.state is no answer from Sunshine: the hero keeps its fault', async () => {
        await live(page);
        await dev(server, 'event', { topic: 'pairing.state', data: { pairings: [] } });
        await page.waitForTimeout(300);
        assert.equal(await text(page, '#hero-title'), "Can't read the streaming status");
        await hero(page, 'fault');
      });
      await step('a failed /status: each part says so in its place, with Try again', async () => {
        await unroute(page);
        await page.route('**/api/v1/status', (r) => r.fulfill({ status: 500, json: { error: 'status: internal error' } }).catch(() => {}));
        await page.reload();
        await ready();
        await page.locator('#hero-title', { hasText: "Can't read the streaming status" }).waitFor();
        assert.equal(await text(page, '#home-power-line'), "Couldn't read the power settings. Try again");
        assert.equal(await text(page, '#home-status'), "Couldn't read the update status.");
        await unroute(page);
        await page.click('#hero-actions button');
        await hero(page, 'ready');
      });
    },
  },
  {
    id: 'HOME-quick-pair',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('Pair goes to the pairing steps on Devices', async () => {
        await page.click('#home-pair-key');
        await page.waitForURL((u) => new URL(u).pathname === '/devices' && new URL(u).hash === '#pair');
        await ready();
      });
    },
  },
  {
    id: 'HOME-quick-awake',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('Stay awake 1 h is a toggle: its name stays, pressed says it is on, the line under it until when', async () => {
        await hero(page, 'ready');
        assert.equal(await page.getAttribute('#home-awake', 'aria-pressed'), 'false');
        assert.equal(await text(page, '#home-awake'), 'Stay awake 1 h');
        await page.focus('#home-awake');
        await page.keyboard.press('Enter');
        await page.locator('#home-awake[aria-pressed="true"]').waitFor();
        assert.equal(await text(page, '#home-awake b'), 'Stay awake 1 h');
        assert.match(await text(page, '#home-awake-sub'), /^Awake until .+$/);
        assert.equal(await page.getAttribute('#home-awake', 'aria-describedby'), 'home-awake-sub');
        await page.locator('#home-power-line', { hasText: /^Staying awake until .+\.$/ }).waitFor();
        // Focus stayed on the key while it worked (WCAG 2.4.3).
        assert.equal(await focused(page), 'home-awake');
      });
      await step('pressed again, it stops', async () => {
        await page.click('#home-awake');
        await page.locator('#home-awake[aria-pressed="false"]').waitFor();
        await page.locator('#notices-polite .notice', { hasText: 'Staying awake is off.' }).waitFor();
      });
      await step('with idle power-off off, it is disabled and says why', async () => {
        await patchStatus(page, (s) => ({ ...s, power: { ...s.power, idle_shutdown: false, keep_awake_until: undefined } }));
        await page.reload();
        await ready();
        await page.locator('#home-awake:disabled').waitFor();
        assert.equal(await text(page, '#home-awake-sub'), 'Idle power-off is off');
        assert.equal(await text(page, '#home-power-line'), 'Always on.');
      });
      await unroute(page);
    },
  },
  {
    id: 'HOME-power-sheet',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('one Power control opens a sheet with Restart and Power off', async () => {
        await page.locator('#home-power:enabled').waitFor();
        await page.focus('#home-power');
        await page.keyboard.press('Enter');
        await page.locator('#power-sheet[open]').waitFor();
        assert.equal(await focused(page), 'power-sheet-title');
        assert.equal(await page.isVisible('#home-reboot'), true);
        assert.equal(await page.isVisible('#home-poweroff'), true);
        assert.equal(await text(page, '#home-hold-hint'), 'Hold to confirm, or tap to be asked first.');
        assert.match(await text(page, '#power-sheet-line'), /power-off|powers off/i);
      });
      await step('Esc closes it and focus goes back to Power', async () => {
        await page.keyboard.press('Escape');
        await closed(page, 'power-sheet');
        assert.equal(await focused(page), 'home-power');
      });
    },
  },
  {
    id: 'HOME-quick-restart',
    ui: ['next'],
    allow: DOWN,
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await page.locator('#home-power:enabled').waitFor();
      await page.click('#home-power');
      await page.locator('#power-sheet[open]').waitFor();
      await step('a tap on Restart asks first, one layer at a time, and Cancel keeps things as they are', async () => {
        await page.click('#home-reboot');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart VaporOS?');
        // The sheet steps aside for the question: never two bottom layers.
        await closed(page, 'power-sheet');
        await page.click('#confirm-cancel');
        await closed(page, 'confirm');
        assert.equal(await page.locator('#scene[open]').count(), 0);
        assert.equal(await focused(page), 'home-power');
      });
      await step('letting go early nudges: keep holding', async () => {
        await page.click('#home-power');
        await page.locator('#power-sheet[open]').waitFor();
        // The sheet slides in: press only once it has settled under the pointer.
        await page.waitForFunction(() => document.getElementById('power-sheet').getAnimations({ subtree: true }).every((a) => a.playState !== 'running'));
        await hold(page, '#home-reboot', 500);
        assert.equal(await text(page, '#home-hold-hint'), 'Keep holding Restart until the key fills.');
      });
      await step('held for 1.2 s it restarts without a dialog and the scene takes over', async () => {
        await hold(page, '#home-reboot', 1400);
        await page.locator('#scene[open]').waitFor();
        assert.equal(await text(page, '#scene-title'), 'Restarting');
        assert.equal(await page.locator('#power-sheet[open]').count(), 0);
        await page.waitForEvent('load', { timeout: 30000 });
        await ready();
        await hero(page, 'ready');
      });
    },
  },
  {
    id: 'HOME-quick-poweroff',
    ui: ['next'],
    preset: 'no-wol',
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await page.locator('#home-power:enabled').waitFor();
      await page.click('#home-power');
      await page.locator('#power-sheet[open]').waitFor();
      await step('with Wake-on-LAN off, Power off only asks, and the hint says why', async () => {
        assert.equal(await page.getAttribute('#home-poweroff', 'data-hold-disabled'), '');
        assert.equal(await page.getAttribute('#home-reboot', 'data-hold-disabled'), null);
        assert.match(await text(page, '#home-hold-hint'), /Wake-on-LAN is off: only the power button starts it again\./);
      });
      await step('its confirm is a danger one: Cancel has focus, Esc cancels', async () => {
        await hold(page, '#home-poweroff', 1400);
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Power off VaporOS?');
        assert.equal(await text(page, '#confirm-body'), 'Wake-on-LAN is off, so only its power button starts it again.');
        assert.equal(await focused(page), 'confirm-cancel');
        await page.keyboard.press('Escape');
        await closed(page, 'confirm');
        assert.equal(await page.locator('#scene[open]').count(), 0);
      });
    },
  },
  {
    id: 'HOME-power-line',
    ui: ['next'],
    preset: 'busy-web',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/'));
      await ready();
      await live(page);
      await step('only this page keeps it on: the line says until when the PC stays on', async () => {
        await page.locator('#home-power-line', { hasText: /^On until about .+\. Idle power-off starts 15 min after this page closes\.$/ }).waitFor();
      });
      await step('a live countdown takes over; a new kind of line is announced, a tick is not', async () => {
        await dev(server, 'event', { topic: 'power.idle', data: { idle_seconds: 660, shutdown_in: 240, busy: null } });
        await page.locator('#home-power-line', { hasText: 'Powers off in 4 min if nobody plays.' }).waitFor();
        await page.locator('#announce-polite', { hasText: 'Powers off in 4 min if nobody plays.' }).waitFor();
        await dev(server, 'event', { topic: 'power.idle', data: { idle_seconds: 720, shutdown_in: 180, busy: null } });
        await page.locator('#home-power-line', { hasText: 'Powers off in 3 min if nobody plays.' }).waitFor();
        await page.waitForTimeout(1300);
        assert.equal(await text(page, '#announce-polite'), 'Powers off in 4 min if nobody plays.');
      });
      await step('the box counting down by itself says so too', async () => {
        await dev(server, 'preset', { name: 'idle-countdown' });
        await page.reload();
        await ready();
        await page.locator('#home-power-line', { hasText: /^Powers off in \d+ min if nobody plays\.$/ }).waitFor();
      });
      await step('while someone streams, it stays on and says who', async () => {
        await dev(server, 'preset', { name: 'streaming' });
        await page.reload();
        await ready();
        await page.locator('#home-power-line', { hasText: 'Staying on: Streaming to Living room TV.' }).waitFor();
      });
    },
  },
  {
    id: 'HOME-card-pairing',
    ui: ['next'],
    preset: 'pairing-1',
    // The flow tries a wrong PIN, which is answered 400.
    allow: [/status of 400/, /400 POST .*\/api\/v1\/sunshine\/pair/],
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/'));
      await ready();
      await live(page);
      await step('a waiting device is the hero: its words, a warmer field and Enter PIN in white-hot', async () => {
        await page.locator('#hero[data-attention="pair"]').waitFor();
        assert.equal(await text(page, '#hero-title'), 'Steam Deck wants to pair');
        assert.equal(await text(page, '#hero-needle'), 'pairing');
        assert.equal(await text(page, '#hero-actions button'), 'Enter PIN');
        assert.equal(await page.getAttribute('#hero-actions button', 'class'), 'btn primary');
        assert.equal(await page.isVisible('#home-pair'), false);
        assert.equal(await page.locator('#notices-polite .notice', { hasText: 'wants to pair' }).count(), 0);
      });
      await step('Enter PIN opens the PIN pad for it', async () => {
        await page.click('#hero-actions button');
        await page.locator('#pinpad[open]').waitFor();
        assert.equal(await text(page, '#pin-title'), 'Steam Deck wants to pair');
        await page.click('#pin-close');
        await closed(page, 'pinpad');
      });
      await step('while streaming, the prompt is the card above the hero', async () => {
        await dev(server, 'event', { topic: 'session.begin', data: { client: 'Living room TV', mode: '3840x2160@60', hdr: false, since: new Date().toISOString() } });
        await hero(page, 'streaming');
        await page.locator('#home-pair .ctx-card').waitFor();
        assert.equal(await text(page, '#home-pair .ctx-title'), 'Steam Deck wants to pair');
        assert.equal(await page.evaluate(() => document.querySelector('#home-pair .ctx-title').tagName), 'H2');
        await dev(server, 'event', { topic: 'session.end', data: {} });
        await page.locator('#hero[data-attention="pair"]').waitFor();
      });
      await step('when Moonlight stops waiting, the prompt goes and the hero cools to Ready', async () => {
        await dev(server, 'event', { topic: 'pairing.state', data: { pairings: [] } });
        await page.locator('#hero-title', { hasText: 'Ready to stream' }).waitFor();
        assert.equal(await page.isVisible('#home-pair'), false);
        assert.equal(await page.getAttribute('#hero', 'data-attention'), null);
      });
    },
  },
  {
    id: 'HOME-card-update-progress',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/'));
      await ready();
      await live(page);
      await hero(page, 'streaming');
      await step('an update while streaming is a card; the hero stays on the stream', async () => {
        await dev(server, 'event', { topic: 'update.progress', data: { phase: 'download', percent: 20, version: '20261001.090000' } });
        await page.locator('#cards-list .ctx-card', { hasText: 'Updating to 20261001.090000' }).waitFor();
        assert.equal(await text(page, '#cards-list .ctx-body'), 'Downloading · 20%');
        assert.equal(await page.getAttribute('#hero', 'data-state'), 'streaming');
      });
      await step('focus on its Details survives a live tick', async () => {
        await page.focus('#cards-list a[data-act="updates"]');
        await dev(server, 'event', { topic: 'update.progress', data: { phase: 'download', percent: 35, version: '20261001.090000' } });
        await page.locator('#cards-list .ctx-body', { hasText: 'Downloading · 35%' }).waitFor();
        assert.equal(await page.evaluate(() => document.activeElement?.dataset.act), 'updates');
      });
    },
  },
  {
    id: 'HOME-card-update-ready',
    ui: ['next'],
    preset: 'update-staged',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/'));
      await ready();
      await live(page);
      await step('a staged version while someone streams is the card Version <v> is ready', async () => {
        await dev(server, 'event', { topic: 'session.begin', data: { client: 'Living room TV', mode: '3840x2160@60', hdr: true, since: new Date().toISOString() } });
        await hero(page, 'streaming');
        const card = page.locator('#cards-list .ctx-card', { hasText: 'Version 20260929.143000 is ready' });
        await card.waitFor();
        assert.equal(await text(page, '#cards-list .ctx-body'), 'It starts the next time VaporOS restarts.');
        await card.locator('button', { hasText: 'Restart to update' }).click();
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart to update?');
        await page.click('#confirm-cancel');
        await closed(page, 'confirm');
      });
    },
  },
  {
    id: 'HOME-card-staged-ready',
    ui: ['next'],
    preset: 'update-staged',
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('a staged update leaves VaporOS ready (MASTER-PLAN §1.3): the hero says so, and the card offers it', async () => {
        await hero(page, 'ready');
        assert.equal(await text(page, '#hero-title'), 'Ready to stream');
        assert.equal(await text(page, '#hero-needle'), 'ready');
        assert.equal(await page.getAttribute('html', 'data-state'), 'ready');
        const card = page.locator('#cards-list .ctx-card', { hasText: 'Version 20260929.143000 is ready' });
        await card.waitFor();
        assert.equal(await card.locator('button').first().textContent(), 'Restart to update');
      });
      await step('its Restart to update asks with the update confirm', async () => {
        await page.click('#cards-list .ctx-card button:has-text("Restart to update")');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart to update?');
        assert.match(await text(page, '#confirm-body'), /If version 20260929\.143000 doesn't start, VaporOS goes back by itself\./);
        await page.keyboard.press('Escape');
        await closed(page, 'confirm');
      });
      await step('the System tab carries no restart badge for it', async () => {
        assert.equal(await page.locator('.tab[data-tab="system"] [data-part="badge"]').isVisible(), false);
      });
    },
  },
  {
    id: 'HOME-card-update-available',
    ui: ['next'],
    preset: 'update-available',
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('a newer version is one card with its size and Download update', async () => {
        await page.locator('#cards-list .ctx-card', { hasText: /^Version \d{8}\.\d{6} is available/ }).waitFor();
        assert.match(await text(page, '#cards-list .ctx-body'), /^Up to 1\.4 GB download\. Switches over on the next restart\.$/);
        await page.click('#cards-list button:has-text("Download update")');
        await page.locator('#notices-polite .notice', { hasText: /^Downloading version / }).waitFor();
      });
    },
  },
  {
    id: 'HOME-card-update-failed',
    ui: ['next'],
    preset: 'update-failed-newer',
    async run({ page, url, ready, step }) {
      const card = page.locator('#cards-list .ctx-card', { hasText: "Version 20260929.143000 didn't start" });
      await page.goto(url('/'));
      await ready();
      await step('a newer version that did not start is the first card, the rest one tap away', async () => {
        await card.waitFor();
        assert.equal(await page.locator('#cards-list > li:not([hidden])').count(), 1);
        assert.equal(await text(page, '#cards-more'), 'Show 1 more');
        await page.click('#cards-more');
        assert.equal(await page.locator('#cards-list > li:not([hidden])').count(), 2);
        assert.equal(await page.getAttribute('#cards-more', 'aria-expanded'), 'true');
        await page.click('#cards-more');
      });
      await step('Dismiss hides it, and it stays hidden after a reload', async () => {
        await card.locator('button', { hasText: 'Dismiss' }).click();
        await card.waitFor({ state: 'detached' });
        await page.reload();
        await ready();
        await page.locator('#cards-list .ctx-card').first().waitFor();
        assert.equal(await card.count(), 0);
      });
      await step('it comes back for a different version', async () => {
        await page.evaluate(() => localStorage.setItem('vos-dismissed-failed', '20260101.000000'));
        await page.reload();
        await ready();
        await card.waitFor();
      });
    },
  },
  {
    id: 'HOME-card-lowdisk',
    ui: ['next'],
    preset: 'disk-low',
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('96% used: Almost out of space, with the way to Storage', async () => {
        const card = page.locator('#cards-list .ctx-card', { hasText: 'Almost out of space' });
        await card.waitFor();
        assert.equal(await card.getAttribute('data-tone'), 'danger');
        assert.equal(await text(page, '#cards-list .ctx-body'), '38 GB free on the system drive.');
        await card.locator('a', { hasText: 'Storage' }).click();
        await page.waitForURL((u) => new URL(u).pathname === '/system/storage');
      });
    },
  },
  {
    id: 'HOME-status-running',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('the status line names the running version and leads to Updates', async () => {
        await page.locator('#home-status', { hasText: /^VaporOS 20260929\.101500 · / }).waitFor();
        await page.click('#home-status');
        await page.waitForURL((u) => new URL(u).pathname === '/system/updates');
      });
    },
  },
  {
    id: 'HOME-status-checked',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/'));
      await ready();
      await step('up to date, with the real time of the last check (B2)', async () => {
        await page.locator('#home-status', { hasText: 'VaporOS 20260929.101500 · Up to date · checked 40 min ago' }).waitFor();
      });
      await step('never checked, or automatic updates off, it says so', async () => {
        await patchStatus(page, (s) => ({ ...s, update: { ...s.update, checked: undefined } }));
        await page.reload();
        await ready();
        await page.locator('#home-status', { hasText: 'VaporOS 20260929.101500 · Not checked yet' }).waitFor();
        await unroute(page);
        await patchStatus(page, (s) => ({ ...s, update: { ...s.update, config: { ...s.update.config, auto: 'off' } } }));
        await page.reload();
        await ready();
        await page.locator('#home-status', { hasText: 'VaporOS 20260929.101500 · Automatic updates are off' }).waitFor();
      });
      await unroute(page);
    },
  },
];
