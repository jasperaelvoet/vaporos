// Flows over Devices (spec-cc-screens §4, MASTER-PLAN §3.5 C2): the three
// steps with the store links and the addresses, the pair card and the PIN
// pad driven by pairing.state (one device, two, and the same phone coming
// back from Moonlight), the paired list with each device's last mode,
// Refresh and Unpair, and Playing now with End stream. Each ID is a row of
// tools/web/e2e/parity.json (owner C2). See legacy.spec.mjs for the format.

import assert from 'node:assert/strict';

import { closed, dev } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const focused = (page) => page.evaluate(() => document.activeElement?.id || document.activeElement?.dataset?.part || document.activeElement?.tagName || '');
const names = (page) => page.$$eval('#dev-paired [data-part="name"]', (els) => els.map((e) => e.textContent));

const DECK = { id: '2e91a88f8967a1bd56be84a93e1fe055', name: 'Steam Deck', address: '192.168.1.31' };
const PIXEL = { id: 'd628a0c01c288581e8ba02a8230917fe', name: 'Pixel 9', address: '192.168.1.44' };

// pairBodies records what the page POSTs to /sunshine/pair.
function pairBodies(page) {
  const bodies = [];
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().endsWith('/api/v1/sunshine/pair')) bodies.push(r.postDataJSON());
  });
  return bodies;
}

// keypad types digits with the on-screen keys (phones) or the keyboard.
async function typePIN(page, digits) {
  const pad = await page.locator('#keypad').isVisible();
  for (const d of digits) {
    if (pad) await page.click(`#keypad [data-key="${d}"]`);
    else await page.keyboard.type(d);
  }
}

// The base fixture's paired devices (internal/web/fixtures/base/sunshine-clients.json).
const CLIENTS = [
  { uuid: '3F1C9A52-7B0E-4D6A-9C21-5E8B7A0D4F13', name: "Sam's iPhone", enabled: true },
  { uuid: 'A07D2E91-44C3-4B8F-8E6D-1B2C3D4E5F60', name: 'Living room TV', enabled: true },
  { uuid: 'C52B8F10-9A3D-4E7C-B1F2-6D7E8F9A0B1C', name: 'MacBook Pro', enabled: true },
];

const unpairIn = (page, name) => page.locator('#dev-paired li', { hasText: name }).locator('[data-part="unpair"]');

export default [
  {
    id: 'DEV-store-links',
    ui: ['next'],
    preset: 'empty',
    async run({ page, url, ready, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('with nothing paired, all three steps show and the list says how to start', async () => {
        assert.equal(await page.getAttribute('#pair', 'data-pair'), 'steps');
        assert.equal(await page.$eval('#dev-step-get', (d) => d.open), true);
        assert.equal(await page.$eval('#dev-step-add', (d) => d.open), true);
        assert.equal(await text(page, '#dev-paired .region-empty'), 'No devices yet. Pair one above to start playing.');
        assert.equal(await text(page, '#dev-paired-title'), 'Paired devices · 0 devices');
      });
      await step('Get Moonlight has the three store links, each saying it opens a new tab', async () => {
        const links = page.locator('#dev-step-get a');
        assert.deepEqual(await links.evaluateAll((as) => as.map((a) => [a.href, a.target, a.rel])), [
          ['https://apps.apple.com/app/moonlight-game-streaming/id1000551566', '_blank', 'noopener noreferrer'],
          ['https://play.google.com/store/apps/details?id=com.limelight', '_blank', 'noopener noreferrer'],
          ['https://moonlight-stream.org/', '_blank', 'noopener noreferrer'],
        ]);
        for (const [who, where] of [['iPhone, iPad, Apple TV', 'App Store'], ['Android, Google TV', 'Google Play'], ['Windows, Mac, Linux', 'moonlight-stream.org']]) {
          const a = page.getByRole('link', { name: new RegExp(`${who}.*${where}.*\\(opens in a new tab\\)`) });
          assert.equal(await a.count(), 1, `${who} · ${where}`);
        }
      });
    },
  },
  {
    id: 'DEV-addresses',
    ui: ['next'],
    async run({ page, url, ready, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('with devices paired, steps 1 and 2 are folded and step 3 listens', async () => {
        assert.equal(await page.$eval('#dev-step-get', (d) => d.open), false);
        assert.equal(await page.$eval('#dev-step-add', (d) => d.open), false);
        await page.locator('#dev-listen[data-link="live"]').waitFor();
        assert.equal(await text(page, '#dev-listen-text'), 'Listening for Moonlight…');
      });
      await step('Add this PC names the PC and lists IPv4, then the .local name, never a link-local address', async () => {
        await page.click('#dev-step-add summary');
        assert.equal(await text(page, '#dev-host'), 'vapor');
        assert.deepEqual(await page.$$eval('#dev-addrs .dev-addr-value', (els) => els.map((e) => e.textContent)), ['192.168.1.40', 'vapor.local']);
      });
      await step('Copy works over plain http: it copies the address and says so', async () => {
        await page.evaluate(() => {
          window.copied = [];
          document.execCommand = (cmd) => {
            const ta = document.querySelector('.copy-scratch');
            if (cmd === 'copy' && ta) window.copied.push(ta.value.slice(ta.selectionStart, ta.selectionEnd));
            return cmd === 'copy';
          };
        });
        const btn = page.locator('#dev-addrs li', { hasText: '192.168.1.40' }).getByRole('button', { name: 'Copy 192.168.1.40' });
        await btn.click();
        assert.deepEqual(await page.evaluate(() => window.copied), ['192.168.1.40']);
        await page.locator('#dev-addrs [data-copied] [data-part="copy-label"]', { hasText: 'Copied' }).waitFor();
        assert.equal(await page.evaluate(() => document.activeElement?.textContent?.includes('192.168.1.40')), true);
      });
    },
  },
  {
    id: 'DEV-pin-input',
    ui: ['next'],
    preset: 'pairing-1',
    async run({ page, url, ready, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('Enter PIN opens the pad with one numeric, one-time-code field of four digits', async () => {
        await page.click('#dev-enter-pin');
        await page.locator('#pinpad[open]').waitFor();
        const pin = page.locator('#pin');
        assert.equal(await pin.getAttribute('autocomplete'), 'one-time-code');
        assert.equal(await pin.getAttribute('pattern'), '[0-9]{4}');
        assert.equal(await pin.getAttribute('maxlength'), '4');
        assert.equal(await page.getByLabel('PIN shown in Moonlight').count(), 1);
        assert.equal(await text(page, '#pin-hint'), '4 digits. Pairs as soon as you enter the last one.');
      });
      await step('on a phone the keypad types and the system keyboard stays down', async () => {
        if (!(await page.locator('#keypad').isVisible())) return; // a fine pointer has no keypad
        assert.equal(await page.getAttribute('#pin', 'inputmode'), 'none');
        await typePIN(page, '12');
        assert.equal(await page.inputValue('#pin'), '12');
        await page.getByRole('button', { name: 'Delete the last digit' }).click();
        assert.equal(await page.inputValue('#pin'), '1');
        await page.click('#keypad [data-key="clear"]');
        assert.equal(await page.inputValue('#pin'), '');
      });
      await step('anything but digits is dropped, so "12a3" is 123', async () => {
        await page.locator('#pin').fill('12a3');
        assert.equal(await page.inputValue('#pin'), '123');
        assert.equal(await page.getAttribute('#pinpad', 'data-digits'), '3');
      });
      await step('Not now closes it and gives focus back to Enter PIN', async () => {
        await page.click('#pin-close');
        await closed(page, 'pinpad');
        assert.equal(await focused(page), 'dev-enter-pin');
        assert.equal(await page.getAttribute('#pair', 'data-pair'), 'waiting');
      });
    },
  },
  {
    id: 'DEV-pin-name-optional',
    ui: ['next'],
    preset: 'pairing-1',
    async run({ page, url, ready, step }) {
      const bodies = pairBodies(page);
      await page.goto(url('/devices'));
      await ready();
      await page.click('#dev-enter-pin');
      await page.locator('#pinpad[open]').waitFor();
      await step("the name is optional and starts as the device's own", async () => {
        await page.click('#pinpad .pin-more summary');
        assert.equal(await page.inputValue('#pin-device-name'), 'Steam Deck');
        assert.equal(await page.getAttribute('#pin-device-name', 'required'), null);
        await page.fill('#pin-device-name', '');
      });
      await step('an emptied name still pairs, as the device calls itself', async () => {
        await page.focus('#pin');
        await typePIN(page, '1234');
        await page.locator('#pinpad[data-phase="success"]').waitFor({ state: 'attached' });
        assert.equal(bodies.length, 1);
        assert.equal(bodies[0].pin, '1234');
        assert.equal(bodies[0].name, 'Steam Deck');
        assert.ok([undefined, DECK.id].includes(bodies[0].pairing_id), `pairing_id ${bodies[0].pairing_id}`);
        await closed(page, 'pinpad');
        await page.waitForFunction(() => document.activeElement.id === 'pair');
      });
      await step('the card goes back to the steps and the new device arrives in the list', async () => {
        await page.locator('#pair[data-pair="steps"]').waitFor();
        await page.locator('#dev-paired li[data-fresh]', { hasText: 'Steam Deck' }).waitFor();
        assert.equal(await text(page, '#dev-paired-title'), 'Paired devices · 4 devices');
        assert.equal(await text(page, '#dev-pair-done'), 'Steam Deck is paired. Pick Steam in Moonlight to play.');
        await page.locator('.notice', { hasText: 'Steam Deck is paired.' }).waitFor();
      });
    },
  },
  {
    id: 'DEV-pin-takeover',
    ui: ['next'],
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      await page.locator('#link[data-link="live"]').waitFor();
      await step('when Moonlight starts waiting, the pad opens by itself', async () => {
        assert.equal(await page.getAttribute('#pair', 'data-pair'), 'steps');
        await dev(server, 'event', { topic: 'pairing.state', data: { pairings: [DECK] } });
        await page.locator('#pinpad[open]').waitFor({ timeout: 3000 });
        assert.equal(await text(page, '#pin-title'), 'Steam Deck wants to pair');
      });
      await step('Not now leaves the prompt on the card, hot, with Enter PIN and no extra notice', async () => {
        await page.click('#pin-close');
        await closed(page, 'pinpad');
        assert.equal(await page.getAttribute('#pair', 'data-pair'), 'waiting');
        assert.equal(await page.getAttribute('#pair', 'data-attention'), 'pair');
        assert.equal(await text(page, '#dev-pair-title'), 'Steam Deck wants to pair');
        assert.equal(await text(page, '#dev-pair-lead'), 'Enter the 4-digit PIN Moonlight shows. From 192.168.1.31.');
        assert.equal(await page.locator('.notice', { hasText: 'wants to pair' }).count(), 0);
        assert.equal(await page.locator('#nav a[href="/devices"] [data-part="badge"]').isVisible(), true);
      });
      await step('Enter PIN opens it again and Esc closes it', async () => {
        await page.click('#dev-enter-pin');
        await page.locator('#pinpad[open]').waitFor();
        await page.keyboard.press('Escape');
        await closed(page, 'pinpad');
        assert.equal(await focused(page), 'dev-enter-pin');
      });
      await step('when Moonlight stops waiting, the card is the steps again', async () => {
        await dev(server, 'event', { topic: 'pairing.state', data: { pairings: [] } });
        await page.locator('#pair[data-pair="steps"]').waitFor();
        assert.equal(await page.getAttribute('#pair', 'data-attention'), null);
        assert.equal(await text(page, '#dev-pair-title'), 'Pair a device');
      });
    },
  },
  {
    id: 'DEV-unpair',
    ui: ['next'],
    // Unpairing a device that is already gone answers 404 on purpose.
    allow: [/status of 404/, /404 DELETE .*\/api\/v1\/sunshine\/clients\//],
    async run({ page, url, ready, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('the list is newest first, each row with its last mode', async () => {
        assert.deepEqual(await names(page), ["Sam's iPhone", 'Living room TV', 'MacBook Pro']);
        const sub = await page.locator('#dev-paired li', { hasText: 'MacBook Pro' }).locator('[data-part="sub"]').textContent();
        assert.match(sub.replace(/ /g, ' '), /^Last used 3 d ago · 2560 × 1600 · 120 Hz$/);
      });
      await step('Unpair asks first, with Cancel focused; Esc keeps the device', async () => {
        await unpairIn(page, 'Living room TV').click();
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Unpair Living room TV?');
        assert.equal(await text(page, '#confirm-body'), "It can't stream from this PC until you pair it again.");
        assert.equal(await page.getAttribute('#confirm', 'role'), 'alertdialog');
        assert.equal(await focused(page), 'confirm-cancel');
        await page.keyboard.press('Escape');
        await closed(page, 'confirm');
        assert.equal(await page.evaluate(() => document.activeElement.closest('li')?.textContent.includes('Living room TV')), true);
        assert.equal((await names(page)).length, 3);
      });
      await step('confirmed, the device goes, it says so and focus moves to the next row', async () => {
        await unpairIn(page, 'Living room TV').click();
        await page.locator('#confirm[open]').waitFor();
        await page.click('#confirm-ok');
        await page.locator('.notice', { hasText: 'Living room TV is unpaired.' }).waitFor();
        assert.deepEqual(await names(page), ["Sam's iPhone", 'MacBook Pro']);
        assert.equal(await text(page, '#dev-paired-title'), 'Paired devices · 2 devices');
        assert.equal(await page.evaluate(() => document.activeElement.closest('li')?.textContent.includes('MacBook Pro')), true);
      });
      await step('a device someone else already unpaired just goes, with a note', async () => {
        await page.evaluate(async () => {
          const me = await (await fetch('/api/v1/auth/me')).json();
          await fetch('/api/v1/sunshine/clients/3F1C9A52-7B0E-4D6A-9C21-5E8B7A0D4F13', { method: 'DELETE', headers: { 'X-VOS-CSRF': me.csrf } });
        });
        await unpairIn(page, "Sam's iPhone").click();
        await page.click('#confirm-ok');
        await page.locator('.notice', { hasText: "Sam's iPhone was already unpaired." }).waitFor();
        assert.deepEqual(await names(page), ['MacBook Pro']);
      });
    },
  },
  {
    id: 'DEV-refresh',
    ui: ['next'],
    // The flow makes the list fail on purpose, then makes Sunshine stop.
    allow: [/status of 502/, /502 GET .*\/api\/v1\/sunshine\/clients$/, /Failed to load resource/, /ERR_EMPTY_RESPONSE|ERR_CONNECTION|net::ERR_/, /\/api\/v1\/(events|status|auth\/me|ping)/],
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      const asked = [];
      page.on('request', (r) => r.url().endsWith('/api/v1/sunshine/clients') && asked.push(r.headers()['x-vos-passive'] ?? ''));
      await step('Refresh list asks for the list once and says how many', async () => {
        const btn = page.getByRole('button', { name: 'Refresh list' });
        await page.route('**/api/v1/sunshine/clients', (route) => route.fulfill({ json: { clients: CLIENTS.map((c) => (c.name === 'MacBook Pro' ? { ...c, enabled: false } : c)) } }));
        await btn.click();
        await page.locator('#announce-polite', { hasText: '3 paired devices.' }).waitFor();
        assert.deepEqual(asked, ['']);
        await page.unroute('**/api/v1/sunshine/clients');
      });
      await step('a device Sunshine disabled says so', async () => {
        const mac = page.locator('#dev-paired li', { hasText: 'MacBook Pro' });
        assert.equal(await mac.locator('[data-part="disabled"]').isVisible(), true);
        assert.equal(await text(page, '#dev-paired li:first-child [data-part="disabled"]:not([hidden])').catch(() => ''), '');
      });
      await step('a failed read is an inline error with Try again', async () => {
        await page.route('**/api/v1/sunshine/clients', (route) => route.fulfill({ status: 502, json: { error: 'Sunshine is not answering: connection refused' } }));
        await page.getByRole('button', { name: 'Refresh list' }).click();
        await page.locator('#dev-paired .region-error').waitFor();
        assert.equal(await text(page, '#dev-paired .region-error-text'), "Couldn't load paired devices. The stream server isn't answering. Restart streaming, or check the log.");
        await page.unroute('**/api/v1/sunshine/clients');
        await page.locator('#dev-paired').getByRole('button', { name: 'Try again' }).click();
        await page.locator('#dev-paired li', { hasText: 'MacBook Pro' }).waitFor();
      });
      await step('with the stream server stopped, the card says so and Restart streaming brings it back', async () => {
        await dev(server, 'preset', { name: 'sunshine-stopped' });
        await page.reload();
        await ready();
        await page.locator('#pair[data-pair="stopped"]').waitFor();
        assert.equal(await text(page, '#dev-pair-lead'), "Streaming is stopped, so devices can't pair or play.");
        await page.click('#dev-sun-restart');
        await page.locator('.notice', { hasText: 'Streaming is restarting.' }).waitFor();
        await page.locator('#pair[data-pair="steps"]').waitFor({ timeout: 8000 });
        await page.locator('#dev-paired li', { hasText: 'MacBook Pro' }).waitFor({ timeout: 8000 });
      });
    },
  },
  {
    id: 'DEV-pin-two-devices',
    ui: ['next'],
    preset: 'pairing-2',
    // Four digits before choosing a device is refused on the page, not sent.
    async run({ page, url, ready, step }) {
      const bodies = pairBodies(page);
      await page.goto(url('/devices'));
      await ready();
      await step('the card says two devices wait, with their addresses', async () => {
        assert.equal(await text(page, '#dev-pair-title'), '2 devices want to pair');
        assert.deepEqual(
          await page.$$eval('#dev-waiting li', (lis) => lis.map((li) => [...li.children].map((c) => c.textContent).join(' · '))),
          ['Steam Deck · 192.168.1.31', 'Pixel 9 · 192.168.1.44'],
        );
      });
      await step('the pad asks which device shows the PIN, and a PIN alone is not sent', async () => {
        await page.click('#dev-enter-pin');
        await page.locator('#pinpad[open]').waitFor();
        await page.locator('#pin-pick:not([hidden])').waitFor();
        assert.equal(await text(page, '#pin-pick legend'), 'Which device shows this PIN?');
        const picks = await page.$$eval('#pin-pick-list .pick', (ls) => ls.map((l) => l.textContent.trim()));
        assert.deepEqual(picks.map((t) => t.split(' · ')[0]), ['Steam Deck', 'Pixel 9']);
        await page.focus('#pin');
        await typePIN(page, '1234');
        await page.locator('#pin-error', { hasText: 'Choose the device that shows this PIN.' }).waitFor();
        assert.deepEqual(bodies, []);
      });
      await step('choosing Pixel 9 names it, and the PIN pairs that one by its id', async () => {
        await page.click('#pin-pick-list .pick:has-text("Pixel 9")');
        assert.equal(await page.inputValue('#pin-device-name'), 'Pixel 9');
        await page.waitForTimeout(400);
        if (!bodies.length) {
          // The four digits stay; one more digit sends them.
          await page.focus('#pin');
          await page.keyboard.press('Backspace');
          await page.keyboard.type('4');
        }
        await page.locator('#pinpad[data-phase="success"]').waitFor({ state: 'attached' });
        assert.deepEqual(bodies, [{ pin: '1234', name: 'Pixel 9', pairing_id: PIXEL.id }]);
      });
      await step('the other device still waits, on the card', async () => {
        await closed(page, 'pinpad');
        await page.locator('#dev-pair-title', { hasText: 'Steam Deck wants to pair' }).waitFor();
        await page.locator('#dev-paired li', { hasText: 'Pixel 9' }).waitFor();
      });
    },
  },
  {
    id: 'DEV-resume-hint',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('Playing now says who started it, what, how and since when', async () => {
        await page.locator('#dev-playing:not([hidden])').waitFor();
        assert.equal(await text(page, '#dev-play-by'), 'Living room TV');
        assert.equal(await text(page, '#dev-play-app'), 'Steam');
        assert.equal(await text(page, '#dev-play-mode'), '3840 × 2160 · 60 Hz');
        assert.equal(await text(page, '#dev-play-hdr'), 'On');
        assert.equal(await page.isVisible('#dev-play-since-row'), true);
      });
      await step('the switching-devices hint is there, and the device that plays is first and hot', async () => {
        assert.equal(await text(page, '#dev-resume-hint'), 'Switching devices or changing the resolution? Quit the game in Moonlight, then start it on the other device.');
        assert.equal((await names(page))[0], 'Living room TV');
        const first = page.locator('#dev-paired li').first();
        assert.equal(await first.getAttribute('data-playing'), 'true');
        assert.equal(await first.locator('[data-part="playing"]').isVisible(), true);
      });
      await step('a device that is not paired under its name shows under Seen before', async () => {
        await dev(server, 'event', { topic: 'session.end', data: {} });
        await dev(server, 'event', { topic: 'session.begin', data: { client: 'Bedroom TV', mode: '1920x1080@60', hdr: false, app: 'Steam', since: '@now' } });
        await page.locator('#dev-play-by', { hasText: 'Bedroom TV' }).waitFor();
        await page.locator('#dev-seen-list [data-part="name"]', { hasText: 'Bedroom TV' }).waitFor({ state: 'attached' });
        await page.click('#dev-seen summary');
        const seen = await page.$$eval('#dev-seen-list [data-part="name"]', (els) => els.map((e) => e.textContent));
        assert.ok(seen.includes('Bedroom TV'), `Seen before lists ${seen.join(', ')}`);
        assert.ok(!seen.includes('Living room TV'));
      });
      await step('End stream asks first (Cancel focused), then Playing now goes and focus lands on the list', async () => {
        await page.click('#dev-end-stream');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'End the stream?');
        assert.equal(await page.evaluate(() => document.activeElement.id), 'confirm-cancel');
        await page.click('#confirm-ok');
        await page.locator('#dev-playing[hidden]').waitFor({ state: 'attached', timeout: 8000 });
        assert.equal(await page.evaluate(() => document.activeElement.id), 'dev-paired-title');
      });
    },
  },
  {
    id: 'DEV-pin-same-phone',
    ui: ['next'],
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      await page.locator('#link[data-link="live"]').waitFor();
      const passive = [];
      page.on('request', (r) => r.url().endsWith('/api/v1/status') && passive.push(r.headers()['x-vos-passive'] ?? ''));
      await step('in Moonlight on the same phone, the page is hidden and its event stream sleeps', async () => {
        await page.evaluate(() => {
          window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
          Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
          Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
          document.dispatchEvent(new Event('visibilitychange'));
        });
        await dev(server, 'event', { topic: 'pairing.state', data: { pairings: [DECK] } });
        await page.waitForTimeout(2500);
        assert.equal(await page.locator('#pinpad[open]').count(), 0);
      });
      await step('back on the page, it asks VaporOS once, quietly, and the pad is there within a second', async () => {
        await page.evaluate(() => {
          Object.defineProperty(document, 'hidden', { configurable: true, get: () => false });
          Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
          document.dispatchEvent(new Event('visibilitychange'));
        });
        await page.locator('#pinpad[open]').waitFor({ timeout: 1000 });
        assert.equal(await text(page, '#pin-title'), 'Steam Deck wants to pair');
        assert.deepEqual(passive, ['1']);
      });
    },
  },
];
