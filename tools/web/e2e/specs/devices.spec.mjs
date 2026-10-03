// Flows over Devices (spec-cc-screens §4, MASTER-PLAN §3.5 C2): the three
// steps with the store links and the addresses, the pair card and the PIN
// pad driven by pairing.state (one device, two, and the same phone coming
// back from Moonlight), the paired list with each device's last mode,
// Refresh and Unpair, and Playing now with End stream. Each ID is a row of
// tools/web/e2e/parity.json (owner C2). The DEV-scale-* flows are each
// device's interface size (CONTRACTS Display policy, Scaling), which the
// eight-page UI never had (BEYOND_PARITY). See legacy.spec.mjs for the format.

import assert from 'node:assert/strict';

import { armed, closed, dev, write } from '../lib/dev.mjs';

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

// The streaming preset's TV (internal/web/fixtures/base/display.json).
const TV_SCREEN = '27eddd2e84ba';
const PHONE_SCREEN = '4bb2226e22c8'; // Sam's iPhone
// The streaming preset's sim.size_ranges: below this a 4K TV's scale is
// Steam's minimum, and above PHONE_MAX a 2796x1290 phone's its maximum.
const TV_MIN = 0.5548518518518518;
const PHONE_MAX = 1.6744186046511629;
const RESUMED = "This stream was resumed. Changes apply from this device's next start.";
const nbsp = (t) => t.replace(/\u00a0/g, ' ');
const until = (page, fn, arg) => page.waitForFunction(fn, arg, { timeout: 5000 });
const glyphOf = (li) => li.locator('[data-part="glyph"] use').getAttribute('href');

// adjust opens the size sheet from Playing now.
async function adjust(page) {
  await page.locator('#dev-size-adjust').evaluate((el) => el.scrollIntoView({ block: 'center' }));
  await page.click('#dev-size-adjust');
  await page.locator('#size-sheet[open]').waitFor();
  // Done sliding in: a trial click waits for it to hold still.
  await page.locator('#size-title').click({ trial: true });
}

// putAs sends a PUT as another tab would.
const putAs = (page, path, body) =>
  page.evaluate(async ([p, b]) => {
    const me = await (await fetch('/api/v1/auth/me')).json();
    const r = await fetch(`/api/v1${p}`, { method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-VOS-CSRF': me.csrf }, body: JSON.stringify(b) });
    return r.status;
  }, [path, body]);
const putScreen = (page, id, body) => putAs(page, `/display/screens/${id}`, body);

// patchStatus answers GET /status with the real answer changed by fn.
async function patchStatus(page, fn) {
  await page.route('**/api/v1/status', async (route) => {
    try {
      const res = await route.fetch({ url: route.request().url().replace('//vapor.local:', '//127.0.0.1:') });
      await route.fulfill({ status: res.status(), contentType: 'application/json', body: JSON.stringify(fn(await res.json())) });
    } catch {
      // The page went on while this answer was in flight.
    }
  });
}

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
        // Everything spoken or shown as a notice from here on.
        await page.evaluate(() => {
          window.vosSaid = [];
          for (const id of ['announce-polite', 'announce-assertive', 'notices-polite', 'pin-status']) {
            const el = document.getElementById(id);
            new MutationObserver(() => window.vosSaid.push(`${id}: ${el.textContent}`)).observe(el, { childList: true, characterData: true, subtree: true });
          }
        });
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
      await step('a pairing that works is never announced as Moonlight giving up, and is said once', async () => {
        const said = await page.evaluate(() => window.vosSaid);
        assert.ok(!said.some((x) => /stopped waiting/.test(x)), said.join(' | '));
        assert.ok(!said.some((x) => /^announce-.*is paired/.test(x)), said.join(' | '));
        assert.ok(said.some((x) => /^pin-status: Steam Deck is paired\./.test(x)), said.join(' | '));
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
  // ------------------------------------------- interface size (DEV-scale)
  {
    id: 'DEV-scale-fact',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('Playing now says the interface size and offers Adjust', async () => {
        await page.locator('#dev-playing:not([hidden])').waitFor();
        assert.equal(await text(page, '#dev-play-size-row dt'), 'Interface size');
        assert.equal(nbsp(await text(page, '#dev-play-size')), 'Automatic · looks like a TV');
        assert.equal(await page.isVisible('#dev-size-adjust'), true);
        assert.equal(await page.getAttribute('#dev-size-adjust', 'aria-haspopup'), 'dialog');
        assert.equal(await page.isVisible('#dev-size-note'), false);
      });
      await step('a size changed elsewhere shows once GET /display has it', async () => {
        assert.equal(await putScreen(page, TV_SCREEN, { kind: 'monitor', size: 1.2 }), 200);
        await page.locator('#dev-play-size', { hasText: 'Monitor · 120%' }).waitFor();
      });
      await step('a device the box cannot tell apart says why its size cannot be saved, without Adjust', async () => {
        await dev(server, 'event', { topic: 'session.end', data: {} });
        await dev(server, 'event', {
          topic: 'session.begin',
          data: { client: 'roth', mode: '2796x1290@120', hdr: false, app: 'Steam', since: '@now', screen: { id: '', name: 'roth', kind: 'phone', kind_from: 'resolution', ui_scale: 2.7, game_dpi: 168 } },
        });
        await page.locator('#dev-play-by', { hasText: 'roth' }).waitFor();
        await page.locator('#dev-size-note:not([hidden])').waitFor();
        assert.equal(await text(page, '#dev-size-note'), "Can't tell this device apart from others, so its size can't be saved.");
        assert.equal(nbsp(await text(page, '#dev-play-size')), 'Automatic · looks like a phone');
        assert.equal(await page.isVisible('#dev-size-adjust'), false);
      });
      await step('a row takes the kind its screens agree on; a name with no screens keeps its own glyph', async () => {
        await page.locator('#dev-seen-list [data-part="name"]', { hasText: 'roth' }).waitFor({ state: 'attached' });
        assert.equal(await glyphOf(page.locator('#dev-seen-list li', { hasText: 'roth' })), '#i-phone');
        assert.equal(await glyphOf(page.locator('#dev-paired li', { hasText: 'Living room TV' })), '#i-monitor');
        assert.equal(await glyphOf(page.locator('#dev-paired li', { hasText: 'MacBook Pro' })), '#i-laptop');
      });
    },
  },
  {
    id: 'DEV-scale-adjust',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, step }) {
      await page.goto(url('/devices'));
      await ready();
      const path = `/display/screens/${TV_SCREEN}`;
      await step('Adjust opens the sheet for the device playing, automatic, with why', async () => {
        await adjust(page);
        assert.equal(await page.evaluate(() => document.activeElement.id), 'size-title');
        assert.equal(nbsp(await text(page, '#size-for')), 'Living room TV · 3840 × 2160 · 60 Hz');
        assert.equal(await page.isChecked('#size-kinds input[value="auto"]'), true);
        assert.equal(await text(page, '#size-auto'), 'Automatic: looks like a TV, from its name.');
        assert.equal(await text(page, '#size-pct'), '100%');
        assert.equal(await page.getAttribute('#size-reset', 'aria-disabled'), 'true');
        assert.equal(await page.isChecked('#size-steam-auto'), false);
        assert.equal(await page.isVisible('#size-steam'), false);
        // Only Steam's menus: games' text follows only some Linux games, and few steps.
        assert.equal(await text(page, '#size-hint'), "Steam's menus, in 10% steps.");
      });
      await step('+ makes it 10% bigger at once and pins the kind in effect', async () => {
        const body = write(page, 'PUT', path);
        await page.click('#size-up');
        assert.equal(await text(page, '#size-pct'), '110%');
        assert.deepEqual(await body, { size: 1.1 });
        assert.equal(await page.isChecked('#size-kinds input[value="tv"]'), true);
        await page.locator('#dev-play-size', { hasText: 'TV · 110%' }).waitFor();
        assert.equal(await page.getAttribute('#size-reset', 'aria-disabled'), null);
      });
      await step('quick taps go out in order and the last one stays', async () => {
        const bodies = [];
        const seen = (r) => r.method() === 'PUT' && r.url().endsWith(path) && bodies.push(r.postDataJSON());
        page.on('request', seen);
        const last = armed(page.waitForResponse((r) => r.request().method() === 'PUT' && r.url().endsWith(path) && r.request().postDataJSON()?.size === 0.8));
        await page.click('#size-down');
        await page.click('#size-down');
        await page.click('#size-down');
        await until(page, () => document.getElementById('size-pct').textContent === '80%');
        await last;
        page.off('request', seen);
        assert.deepEqual(bodies, [{ size: 1 }, { size: 0.9 }, { size: 0.8 }]);
        await page.locator('#dev-play-size', { hasText: 'TV · 80%' }).waitFor();
      });
      await step('another kind starts at 100%', async () => {
        const body = write(page, 'PUT', path);
        await page.click('#size-kinds .pick:has-text("Monitor")');
        assert.deepEqual(await body, { kind: 'monitor' });
        assert.equal(await text(page, '#size-pct'), '100%');
        await page.locator('#dev-play-size', { hasText: 'Monitor · 100%' }).waitFor();
        assert.equal(await text(page, '#size-auto'), 'Automatic: looks like a TV.');
      });
      await step("Steam's own size hands sizing to Steam, and the kind and size wait", async () => {
        const body = write(page, 'PUT', path);
        await page.click('#size-steam-auto');
        assert.deepEqual(await body, { steam_auto: true });
        assert.equal(await page.isChecked('#size-steam-auto'), true);
        assert.equal(await page.isDisabled('#size-kinds input[value="tv"]'), true);
        assert.equal(await page.getAttribute('#size-up', 'aria-disabled'), 'true');
        await page.locator('#dev-play-size', { hasText: "Steam's own size" }).waitFor();
      });
      await step('Reset is automatic at 100%, sized by VaporOS', async () => {
        const body = write(page, 'PUT', path);
        await page.click('#size-reset');
        assert.deepEqual(await body, { kind: 'auto', size: 1 });
        assert.equal(await page.isChecked('#size-kinds input[value="auto"]'), true);
        assert.equal(await page.isChecked('#size-steam-auto'), false);
        await page.locator('#dev-play-size', { hasText: 'Automatic · looks like a TV' }).waitFor();
        await until(page, () => document.getElementById('size-reset').getAttribute('aria-disabled') === 'true');
      });
      await step('Done closes it and gives focus back to Adjust', async () => {
        await page.click('#size-sheet .sheet-actions .btn.primary');
        await closed(page, 'size-sheet');
        assert.equal(await page.evaluate(() => document.activeElement.id), 'dev-size-adjust');
      });
      await step('a click that does not focus Adjust (Safari) still gets focus back to it', async () => {
        await page.$eval('#dev-size-adjust', (b) => {
          b.blur();
          b.click();
        });
        await page.locator('#size-sheet[open]').waitFor();
        await page.keyboard.press('Escape');
        await closed(page, 'size-sheet');
        await page.waitForTimeout(50); // the sheet's own fallback runs a task later
        assert.equal(await page.evaluate(() => document.activeElement.id), 'dev-size-adjust');
      });
    },
  },
  {
    id: 'DEV-scale-limits',
    ui: ['next'],
    preset: 'streaming',
    allow: [/status of 500/, /500 PUT .*\/api\/v1\/display\/screens\//],
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('at 250% + rests, and − still works', async () => {
        assert.equal(await putScreen(page, TV_SCREEN, { size: 2.5 }), 200);
        await page.locator('#dev-play-size', { hasText: 'TV · 250%' }).waitFor();
        await adjust(page);
        assert.equal(await text(page, '#size-pct'), '250%');
        assert.equal(await page.getAttribute('#size-up', 'aria-disabled'), 'true');
        assert.equal(await page.getAttribute('#size-down', 'aria-disabled'), null);
        const asked = [];
        const seen = (r) => r.method() === 'PUT' && asked.push(r.url());
        page.on('request', seen);
        // aria-disabled keeps it focusable: a press is taken and does nothing.
        await page.click('#size-up', { force: true });
        await page.waitForTimeout(300);
        page.off('request', seen);
        assert.deepEqual(asked, []);
      });
      await step('a change VaporOS refuses goes back, and says why', async () => {
        await page.route('**/api/v1/display/screens/*', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"saving screens: disk full"}' }));
        await page.click('#size-down');
        await page.locator('.notice', { hasText: 'Saving screens: disk full' }).waitFor();
        await until(page, () => document.getElementById('size-pct').textContent === '250%');
        assert.equal(await page.isChecked('#size-kinds input[value="tv"]'), true);
        await page.unroute('**/api/v1/display/screens/*');
      });
      await step('a fault in sizing Steam is one quiet line, here and in Playing now', async () => {
        await page.keyboard.press('Escape');
        await closed(page, 'size-sheet');
        await patchStatus(page, (st) => ({ ...st, display: { ...st.display, steam_ui: 'no-debugger' } }));
        await dev(server, 'event', { topic: 'display.changed', data: {} });
        await page.locator('#dev-size-note:not([hidden])').waitFor();
        assert.equal(await text(page, '#dev-size-note'), "Can't size Steam right now. Steam's own setting still works.");
        assert.equal(await page.getAttribute('#dev-size-note', 'data-tone'), 'error');
        await adjust(page);
        assert.equal(await text(page, '#size-steam'), "Can't size Steam right now. Steam's own setting still works.");
        assert.equal(await page.getAttribute('#size-up', 'aria-disabled'), 'true');
      });
      await step("− rests where Steam's own minimum holds a 4K TV's scale", async () => {
        assert.equal(await putScreen(page, TV_SCREEN, { size: 0.6 }), 200);
        await until(page, () => document.getElementById('size-pct').textContent === '60%');
        assert.equal(await page.getAttribute('#size-down', 'aria-disabled'), null);
        const body = write(page, 'PUT', `/display/screens/${TV_SCREEN}`);
        await page.click('#size-down');
        assert.deepEqual(await body, { size: TV_MIN });
        assert.equal(await text(page, '#size-pct'), '55%');
        assert.equal(await page.getAttribute('#size-down', 'aria-disabled'), 'true');
        assert.equal(await page.getAttribute('#size-up', 'aria-disabled'), null);
      });
      await step('the sheet goes with the stream, and focus lands on the list', async () => {
        await dev(server, 'event', { topic: 'session.end', data: {} });
        await closed(page, 'size-sheet');
        await page.locator('#dev-playing[hidden]').waitFor({ state: 'attached' });
        await until(page, () => document.activeElement.id === 'dev-paired-title');
      });
      await step("+ rests where Steam's maximum holds a phone's, and a size beyond it steps down from there", async () => {
        await page.unroute('**/api/v1/status');
        await dev(server, 'event', {
          topic: 'session.begin',
          data: { client: "Sam's iPhone", mode: '2796x1290@120', hdr: false, app: 'Steam', since: '@now', screen: { id: PHONE_SCREEN, name: "Sam's iPhone", kind: 'phone', kind_from: 'name', ui_scale: 2.7, game_dpi: 168 } },
        });
        await page.locator('#dev-play-by', { hasText: "Sam's iPhone" }).waitFor();
        assert.equal(await putScreen(page, PHONE_SCREEN, { size: 1.6 }), 200);
        await page.locator('#dev-play-size', { hasText: 'Phone · 160%' }).waitFor();
        await adjust(page);
        const up = write(page, 'PUT', `/display/screens/${PHONE_SCREEN}`);
        await page.click('#size-up');
        assert.deepEqual(await up, { size: PHONE_MAX });
        assert.equal(await text(page, '#size-pct'), '167%');
        assert.equal(await page.getAttribute('#size-up', 'aria-disabled'), 'true');
        assert.equal(await putScreen(page, PHONE_SCREEN, { size: 2.5 }), 200);
        await until(page, () => document.getElementById('size-pct').textContent === '250%');
        assert.equal(await page.getAttribute('#size-up', 'aria-disabled'), 'true');
        const down = write(page, 'PUT', `/display/screens/${PHONE_SCREEN}`);
        await page.click('#size-down');
        assert.deepEqual(await down, { size: 1.6 });
        assert.equal(await text(page, '#size-pct'), '160%');
      });
    },
  },
  {
    id: 'DEV-scale-follow',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step("sizing turned off elsewhere is Steam's own size, without Adjust", async () => {
        await page.locator('#dev-play-size', { hasText: 'Automatic · looks like a TV' }).waitFor();
        assert.equal(await putAs(page, '/display/settings', { ui_scaling: false }), 200);
        await page.locator('#dev-play-size', { hasText: "Steam's own size" }).waitFor();
        assert.equal(await page.isVisible('#dev-size-adjust'), false);
        assert.equal(await page.isVisible('#dev-size-note'), false);
      });
      await step('a reload keeps it, though the event stream replays the session.begin that had a size', async () => {
        await page.reload();
        await ready();
        await page.locator('#dev-playing:not([hidden])').waitFor();
        await page.waitForTimeout(1000); // the replay, and the /status after it
        assert.equal(nbsp(await text(page, '#dev-play-size')), "Steam's own size");
        assert.equal(await page.isVisible('#dev-size-adjust'), false);
      });
      await step('turned on again, the size and Adjust come back', async () => {
        assert.equal(await putAs(page, '/display/settings', { ui_scaling: true }), 200);
        await page.locator('#dev-play-size', { hasText: 'Automatic · looks like a TV' }).waitFor();
        assert.equal(await page.isVisible('#dev-size-adjust'), true);
      });
      await step('a device told apart after it began gets its size and Adjust, also after a reload', async () => {
        await dev(server, 'event', { topic: 'session.end', data: {} });
        await dev(server, 'event', {
          topic: 'session.begin',
          data: { client: 'roth', mode: '2400x1080@120', hdr: false, app: 'Steam', since: '@now', screen: { id: '', name: 'roth', kind: 'phone', kind_from: 'resolution', ui_scale: 2.25, game_dpi: 144 } },
        });
        await page.locator('#dev-size-note', { hasText: "Can't tell this device apart" }).waitFor();
        // The scaler found it by RTSP: /status's stream and the screens have its id; session.begin is not sent again.
        const id = 'd96f2aa20204';
        await patchStatus(page, (st) => {
          if (!st.stream || !st.stream.screen) return st;
          const screens = (st.display.screens || []).map((sc) => (sc.id === '' ? { ...sc, id, savable: true } : sc));
          return { ...st, stream: { ...st.stream, screen: { ...st.stream.screen, id } }, display: { ...st.display, screens } };
        });
        await dev(server, 'event', { topic: 'display.changed', data: {} });
        await page.locator('#dev-size-adjust:not([hidden])').waitFor();
        assert.equal(await page.isVisible('#dev-size-note'), false);
        assert.equal(nbsp(await text(page, '#dev-play-size')), 'Automatic · looks like a phone');
        await page.reload();
        await ready();
        await page.locator('#dev-size-adjust:not([hidden])').waitFor();
        await page.waitForTimeout(1000);
        assert.equal(await page.isVisible('#dev-size-adjust'), true);
        assert.equal(await page.isVisible('#dev-size-note'), false);
      });
    },
  },
  {
    id: 'DEV-scale-resumed',
    ui: ['next'],
    preset: 'streaming',
    async run({ page, url, ready, server, step }) {
      await page.goto(url('/devices'));
      await ready();
      await step('a resumed stream says, calmly, that a change applies from the next start', async () => {
        await page.locator('#dev-play-size', { hasText: 'Automatic · looks like a TV' }).waitFor();
        await patchStatus(page, (st) => ({ ...st, display: { ...st.display, steam_ui: 'resumed' } }));
        await dev(server, 'event', { topic: 'display.changed', data: {} });
        await page.locator('#dev-size-note:not([hidden])').waitFor();
        assert.equal(await text(page, '#dev-size-note'), RESUMED);
        assert.equal(await page.getAttribute('#dev-size-note', 'data-tone'), '');
        assert.equal(await page.isVisible('#dev-size-adjust'), true);
      });
      await step('the sheet says so too, and still stores a size', async () => {
        await adjust(page);
        assert.equal(await text(page, '#size-steam'), RESUMED);
        assert.equal(await page.getAttribute('#size-steam', 'data-tone'), '');
        const body = write(page, 'PUT', `/display/screens/${TV_SCREEN}`);
        await page.click('#size-up');
        assert.deepEqual(await body, { size: 1.1 });
        assert.equal(await text(page, '#size-pct'), '110%');
      });
    },
  },
  {
    id: 'DEV-scale-pin-kind',
    ui: ['next'],
    async run({ page, url, ready, server, step }) {
      const bodies = pairBodies(page);
      const hints = [];
      page.on('request', (r) => r.method() === 'POST' && r.url().endsWith('/api/v1/display/hint') && hints.push({ body: r.postDataJSON(), passive: r.headers()['x-vos-passive'] ?? '' }));
      const first = armed(page.waitForRequest((r) => r.url().endsWith('/api/v1/display/hint')));
      await page.goto(url('/devices'));
      await ready();
      await page.locator('#link[data-link="live"]').waitFor();
      await step('signed in, the page tells VaporOS its screen once, passive', async () => {
        await first;
        assert.equal(hints.length, 1);
        assert.equal(hints[0].passive, '1');
        assert.deepEqual(Object.keys(hints[0].body).sort(), ['dpr', 'h', 'touch', 'w']);
        assert.ok(Number.isInteger(hints[0].body.w) && hints[0].body.w > 0);
      });
      await step('a reload within 12 h does not tell it again', async () => {
        await page.reload();
        await ready();
        await page.waitForTimeout(300);
        assert.equal(hints.length, 1);
      });
      await step("a device Moonlight calls roth leaves the name empty, so VaporOS can name it; the pad tells its screen again", async () => {
        await page.locator('#link[data-link="live"]').waitFor();
        const again = armed(page.waitForRequest((r) => r.url().endsWith('/api/v1/display/hint')));
        await dev(server, 'event', { topic: 'pairing.state', data: { pairings: [{ id: 'aa11bb22cc33dd44ee55ff6677889900', name: 'roth', address: '192.168.1.52' }] } });
        await page.locator('#pinpad[open]').waitFor({ timeout: 3000 });
        await again;
        assert.equal(hints.length, 2);
        await page.click('#pinpad .pin-more summary');
        assert.equal(await page.inputValue('#pin-device-name'), '');
        assert.equal(await text(page, '#pin-device-hint'), 'How this device shows in Devices. Leave it empty to name it after this device, when you pair from it.');
      });
      await step('What is it? starts on Automatic and its pick goes with the PIN as kind', async () => {
        assert.equal(await page.isChecked('#pin-kinds input[value=""]'), true);
        await page.click('#pin-kinds .pick:has-text("TV")');
        assert.equal(await page.isChecked('#pin-kinds input[value="tv"]'), true);
        await page.focus('#pin');
        await typePIN(page, '1234');
        await page.locator('#pinpad[data-phase="success"]').waitFor({ state: 'attached' });
        assert.deepEqual(bodies, [{ pin: '1234', name: '', kind: 'tv' }]);
        assert.equal(await text(page, '#pin-status'), 'The device is paired. Pick Steam in Moonlight to play.');
      });
    },
  },
];
