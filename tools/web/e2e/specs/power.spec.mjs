// Flows over System › Power (spec-cc-screens §8, DECISIONS NEW-1): the
// state as a heat level in its seven cases, idle power-off, stay awake, and
// Wake-on-LAN with the Wake card. Each ID is a row of
// tools/web/e2e/parity.json (owner C4). See legacy.spec.mjs for the flow
// format.

import assert from 'node:assert/strict';

import { dev, write } from '../lib/dev.mjs';

const PATH = '/system/power';
const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const until = (page, fn, arg, timeout = 5000) => page.waitForFunction(fn, arg, { timeout });
const title = (page, want) => until(page, (w) => document.getElementById('pwr-title').textContent === w, want);
const notice = (page, words) => page.locator('#notices .notice', { hasText: words }).first().waitFor();
const CLOCK = /^\d{1,2}:\d{2}(\s?[AP]M)?$/;

async function open(t, preset) {
  if (preset) await dev(t.server, 'preset', { name: preset });
  await t.page.goto(t.url(PATH));
  await t.ready();
}

async function press(page, sel) {
  await page.locator(sel).evaluate((el) => el.scrollIntoView({ block: 'center' }));
  await page.click(sel);
}

// patch answers GET path with the real answer changed by fn.
async function patch(page, path, fn) {
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

export default [
  {
    id: 'SYS-pwr-state',
    ui: ['next'],
    preset: 'busy-web',
    async run(t) {
      const { page, step } = t;
      await step('only this page keeps it on: the title says until when the PC stays on (B5)', async () => {
        await open(t);
        const m = /^On until about (.+)$/.exec(await text(page, '#pwr-title'));
        assert.ok(m && CLOCK.test(m[1]), await text(page, '#pwr-title'));
        assert.equal(await text(page, '#pwr-detail'), 'Idle power-off starts 15 min after this page closes.');
        assert.equal(await page.getAttribute('#pwr-state', 'data-level'), 'web');
        assert.equal(await text(page, '#pwr-needle'), 'on');
      });
      await step('without busy.web, "web UI in use" is never shown as a reason', async () => {
        const old = (p) => ({ ...p, busy: p.busy && { reason: p.busy.reason }, web_until: undefined });
        await patch(page, '/power', old);
        await patch(page, '/status', (st) => ({ ...st, power: st.power && old(st.power) }));
        await page.reload();
        await t.ready();
        await title(page, 'Powers off after 15 minutes idle');
        await page.waitForTimeout(800);
        assert.equal(await text(page, '#pwr-title'), 'Powers off after 15 minutes idle');
        assert.equal(await text(page, '#pwr-detail'), 'Idle means no stream, no game, no download and no update.');
        await page.unroute('**/api/v1/power');
        await page.unroute('**/api/v1/status');
      });
      await step('staying awake: until when, and Stop', async () => {
        await open(t, 'keep-awake');
        const m = /^Staying awake until (.+)$/.exec(await text(page, '#pwr-title'));
        assert.ok(m && CLOCK.test(m[1]), await text(page, '#pwr-title'));
        assert.equal(await text(page, '#pwr-detail'), 'Idle power-off is paused until then.');
        assert.equal(await page.isVisible('#pwr-state-stop'), true);
        assert.equal(await page.getAttribute('#pwr-state', 'data-level'), 'awake');
      });
      await step('a stream keeps it on: the reason in words, hot', async () => {
        await open(t, 'streaming');
        assert.equal(await text(page, '#pwr-title'), 'Staying on');
        assert.equal(await text(page, '#pwr-detail'), 'Streaming to Living room TV.');
        assert.equal(await page.getAttribute('#pwr-state', 'data-needle'), 'streaming');
      });
      await step('nobody plays: it counts down, cooling, with Stay awake 1 h', async () => {
        await open(t, 'idle-countdown');
        assert.match(await text(page, '#pwr-title'), /^Powers off in [45] min$/);
        assert.equal(await text(page, '#pwr-detail'), 'Nobody has played for 10 min. Start a stream to keep it on.');
        assert.equal(await page.getAttribute('#pwr-state', 'data-level'), 'countdown');
        const a = await text(page, '#pwr-count');
        assert.match(a, /^0[45]:\d\d$/);
        await until(page, (x) => document.getElementById('pwr-count').textContent !== x, a);
        const heat = await page.$eval('#pwr-state', (el) => Number(el.style.getPropertyValue('--p-heat')));
        assert.ok(heat > 0.05 && heat < 0.42, `--p-heat ${heat}`);
        const body = write(page, 'POST', '/power/keep-awake');
        await press(page, '#pwr-state-awake');
        assert.deepEqual(await body, { minutes: 60 });
        await notice(page, 'VaporOS stays on for 1 h.');
        await until(page, () => document.getElementById('pwr-title').textContent.startsWith('Staying awake until'));
      });
      await step('idle power-off off: always on', async () => {
        await open(t, 'idle');
        await press(page, '#pwr-auto');
        await title(page, 'Always on');
        assert.equal(await text(page, '#pwr-detail'), 'Idle power-off is off.');
      });
    },
  },
  {
    id: 'SYS-pwr-idle',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the switch applies at once and answers the whole state', async () => {
        await open(t);
        assert.equal(await page.isChecked('#pwr-auto'), true);
        assert.equal(await page.isChecked('#pwr-after .pwr-radio[value="15"]'), true);
        const body = write(page, 'PUT', '/power');
        await press(page, '#pwr-auto');
        assert.deepEqual(await body, { idle_shutdown: false });
        await notice(page, 'Saved. VaporOS stays on.');
        assert.equal(await page.$eval('#pwr-after', (el) => el.disabled), true);
        assert.equal(await page.isDisabled('#pwr-after .pwr-radio[value="30"]'), true);
        assert.equal(await page.isDisabled('#pwr-save'), true);
        assert.equal(await page.isDisabled('#pwr-1h'), true);
        assert.equal(await text(page, '#pwr-awake-off'), 'Idle power-off is off, so VaporOS stays on anyway.');
        await press(page, '#pwr-auto');
        await notice(page, 'Saved. VaporOS powers off after 15 idle minutes.');
      });
      await step('a preset saves with one Save', async () => {
        assert.equal(await page.isDisabled('#pwr-save'), true);
        await page.check('#pwr-after .pwr-radio[value="30"]');
        const body = write(page, 'PUT', '/power');
        await press(page, '#pwr-save');
        assert.deepEqual(await body, { idle_minutes: 30 });
        await notice(page, 'Saved. VaporOS powers off after 30 idle minutes.');
        await until(page, () => document.querySelector('#pwr-after .pwr-radio[value="30"]').checked && document.getElementById('pwr-save').disabled);
      });
      await step('custom minutes: 0 and 1441 are refused inline, 1 and 1440 are saved (D2)', async () => {
        await page.check('#pwr-after .pwr-radio[value="custom"]');
        assert.equal(await page.evaluate(() => document.activeElement.id), 'pwr-minutes');
        for (const bad of ['0', '1441']) {
          await page.fill('#pwr-minutes', bad);
          await press(page, '#pwr-save');
          assert.equal(await text(page, '#pwr-minutes-error'), 'Use 1 to 1440 minutes.');
          assert.equal(await page.getAttribute('#pwr-minutes', 'aria-invalid'), 'true');
        }
        for (const good of ['1440', '1']) {
          await page.fill('#pwr-minutes', good);
          const body = write(page, 'PUT', '/power');
          await press(page, '#pwr-save');
          assert.deepEqual(await body, { idle_minutes: Number(good) });
          await notice(page, `Saved. VaporOS powers off after ${good} idle minutes.`);
        }
        await page.reload();
        await t.ready();
        await until(page, () => document.querySelector('#pwr-after .pwr-radio[value="custom"]').checked);
        assert.equal(await page.inputValue('#pwr-minutes'), '1');
      });
    },
  },
  {
    id: 'SYS-pwr-awake',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('1 hour keeps it on, shows until when, and Stop appears', async () => {
        await open(t);
        assert.equal(await page.isVisible('#pwr-stop'), false);
        const body = write(page, 'POST', '/power/keep-awake');
        await press(page, '#pwr-1h');
        assert.deepEqual(await body, { minutes: 60 });
        await notice(page, 'VaporOS stays on for 1 h.');
        await until(page, () => document.getElementById('pwr-awake-line').textContent.startsWith('VaporOS stays on until'));
        await page.locator('#pwr-stop').waitFor();
      });
      await step('4 hours, then Stop', async () => {
        let body = write(page, 'POST', '/power/keep-awake');
        await press(page, '#pwr-4h');
        assert.deepEqual(await body, { minutes: 240 });
        await notice(page, 'VaporOS stays on for 4 h.');
        body = write(page, 'POST', '/power/keep-awake');
        await press(page, '#pwr-stop');
        assert.deepEqual(await body, { minutes: 0 });
        await notice(page, 'Stay awake stopped.');
        await until(page, () => document.getElementById('pwr-stop').hidden);
        assert.equal(await text(page, '#pwr-awake-line'), 'Keep VaporOS on for a while, for example during a long download.');
      });
    },
  },
  {
    id: 'SYS-pwr-wol',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the Wake card: Moonlight, the MAC and the broadcast address, save them now (NEW-1)', async () => {
        await open(t);
        assert.equal(await text(page, '#pwr-wake [data-part="moonlight"]'), 'Moonlight wakes it: open Moonlight and pick this PC.');
        assert.equal(await text(page, '#power-wake-mac'), '02:5e:a1:3c:4d:7f');
        assert.equal(await text(page, '#power-wake-bcast'), '192.168.1.255');
        assert.equal(await page.isVisible('#pwr-wake [data-part="save"]'), true);
        const kept = await page.evaluate(() => JSON.parse(localStorage.getItem('vos-last') || '{}').wol);
        assert.equal(kept && kept[0].broadcast, '192.168.1.255');
      });
      await step('each wired adapter with its status; Copy works on plain http', async () => {
        assert.deepEqual(await page.$$eval('#pwr-nics .pwr-nic', (els) => els.map((e) => [e.querySelector('[data-part="name"]').textContent, e.querySelector('[data-part="tag"]').textContent])), [['enp6s0', 'Ready to wake']]);
        assert.equal(await text(page, '#pwr-nics [data-part="addr"]'), '192.168.1.40/24');
        await press(page, '#pwr-wake [data-copy-target="power-wake-bcast"]');
        await until(page, () => document.querySelector('#pwr-wake [data-copy-target="power-wake-bcast"]').hasAttribute('data-copied'));
      });
      await step('Wake-on-LAN off: the adapter says how to fix it, and the state warns', async () => {
        await open(t, 'no-wol');
        assert.equal(await text(page, '#pwr-nics [data-part="tag"]'), 'Switched off');
        assert.equal(await text(page, '#pwr-nics [data-part="fix"]'), "VaporOS switches it on at every start. If it stays off, turn on Wake-on-LAN in the PC's firmware settings.");
        assert.equal(await text(page, '#pwr-nics [data-part="mac"]'), '02:5e:a1:3c:4d:7f');
        assert.equal(await text(page, '#pwr-nics [data-part="copy"]'), 'Copy the MAC address of enp6s0');
        assert.equal(await page.isVisible('#pwr-wake [data-part="nowol"]'), true);
        assert.equal(await text(page, '#pwr-cantwake'), 'Nothing can wake VaporOS remotely. When it powers off, only its power button starts it again.');
      });
      await step('an adapter that cannot wake the PC is neutral', async () => {
        await patch(page, '/power', (p) => ({ ...p, wol: p.wol.map((w) => ({ ...w, supported: false, enabled: false })) }));
        await page.reload();
        await t.ready();
        assert.equal(await text(page, '#pwr-nics [data-part="tag"]'), "Can't wake the PC");
        assert.equal(await text(page, '#pwr-nics [data-part="fix"]'), "This adapter doesn't support Wake-on-LAN.");
      });
    },
  },
  {
    id: 'SYS-pwr-wol-empty',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('while ethtool answers: checking the adapters', async () => {
        await dev(t.server, 'latency', { enabled: true });
        await t.page.goto(t.url(PATH));
        await page.locator('#pwr-wol-wait').waitFor();
        assert.equal(await text(page, '#pwr-wol-wait'), 'Checking network adapters…');
        await t.ready();
        await dev(t.server, 'latency', { enabled: false });
      });
      await step('no wired adapter: the empty copy, and nothing can wake it', async () => {
        await patch(page, '/power', (p) => ({ ...p, wol: [] }));
        await page.reload();
        await t.ready();
        assert.equal(await text(page, '#pwr-nics-empty'), 'No wired network adapter found. Wake-on-LAN needs a network cable.');
        assert.equal(await text(page, '#pwr-cantwake'), 'No wired network adapter, so nothing can wake it remotely.');
        assert.equal(await page.isVisible('#pwr-wake'), false);
      });
    },
  },
];
