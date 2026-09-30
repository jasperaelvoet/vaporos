// Flows over System › About (spec-cc-screens §12): the facts about this PC
// in one place, and the temperatures on the heat ramp. Each ID is a row of
// tools/web/e2e/parity.json (owner C4). See legacy.spec.mjs for the flow
// format.

import assert from 'node:assert/strict';

import { dev } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';

async function open(t) {
  await t.page.goto(t.url('/system/about'));
  await t.ready();
}

// patchStatus answers GET /status with the real answer changed by fn.
async function patchStatus(page, fn) {
  await page.route('**/api/v1/status', async (route) => {
    try {
      // Node resolves the fetch itself, and knows no vapor.local.
      const res = await route.fetch({ url: route.request().url().replace('//vapor.local:', '//127.0.0.1:') });
      await route.fulfill({ status: res.status(), contentType: 'application/json', body: JSON.stringify(fn(await res.json())) });
    } catch {
      // The page went on while this answer was in flight.
    }
  });
}
const patchSystem = (page, fn) => patchStatus(page, (s) => ({ ...s, system: fn(s.system) }));

// row reads a fact's value by its term.
const row = (page, term) => page.locator('#about .fact', { has: page.locator('dt', { hasText: new RegExp(`^${term}$`) }) }).locator('dd');

export default [
  {
    id: 'SYS-about-address',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the name and the address this PC answers to', async () => {
        await open(t);
        assert.equal(await text(page, '#abt-name'), 'vapor');
        assert.equal(await text(page, '#abt-address'), 'vapor.local');
      });
      await step('without an mDNS name the address is <name>.local', async () => {
        await patchSystem(page, (s) => ({ ...s, hostname: 'den', mdns: '' }));
        await open(t);
        assert.equal(await text(page, '#abt-address'), 'den.local');
      });
    },
  },
  {
    id: 'SYS-about-ips',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('every address but link-local, IPv4 first', async () => {
        await patchSystem(page, (s) => ({ ...s, ips: ['fe80::5e:a1ff:fe3c:4d7f', '2001:db8::40', '192.168.1.40', '169.254.7.9'] }));
        await open(t);
        assert.deepEqual(await page.evaluate(() => [...document.getElementById('abt-ips').childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent)), ['192.168.1.40', '2001:db8::40']);
      });
    },
  },
  {
    id: 'SYS-about-graphics',
    ui: ['next'],
    async run(t) {
      const { page, server, step } = t;
      await step('a supported card says so in words', async () => {
        await open(t);
        assert.equal(await text(page, '#abt-gpu .abt-gpu-name'), 'AMD Radeon RX 9070 XT (amdgpu)');
        assert.equal(await text(page, '#abt-gpu .abt-badge'), 'Supported');
      });
      await step('an unsupported one too', async () => {
        await dev(server, 'preset', { name: 'no-gpu' });
        await open(t);
        assert.match(await text(page, '#abt-gpu .abt-gpu-name'), /^NVIDIA GeForce RTX 4070/);
        assert.equal(await text(page, '#abt-gpu .abt-badge'), 'Not supported');
      });
      await step('no card at all reads None detected', async () => {
        await patchSystem(page, (s) => ({ ...s, gpu: { vendor: '', name: '', driver: '', supported: false } }));
        await open(t);
        assert.equal(await text(page, '#abt-gpu'), 'None detected');
      });
    },
  },
  {
    id: 'SYS-about-cpu',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the processor, by name', async () => {
        await open(t);
        assert.equal(await text(page, '#abt-cpu'), 'AMD Ryzen 7 9800X3D 8-Core Processor');
      });
    },
  },
  {
    id: 'SYS-about-uptime',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('Up for, on About and on the System nameplate', async () => {
        await patchSystem(page, (s) => ({ ...s, uptime_s: 93600 }));
        await open(t);
        assert.equal(await text(page, '#abt-uptime'), '1 d 2 h');
        await page.goto(t.url('/system'));
        await t.ready();
        assert.match(await text(page, '#plate-meta'), /· up\s1\sd\s2\sh$/);
      });
    },
  },
  {
    id: 'SYS-about-temps',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('every sensor, hottest first, on the heat ramp at its reading', async () => {
        await open(t);
        const names = await page.locator('#abt-temps dt').allTextContents();
        assert.deepEqual(names, ['k10temp Tctl', 'amdgpu edge', 'nvme Composite']);
        assert.deepEqual(await page.locator('#abt-temps .abt-temp-c').allTextContents(), ['49 °C', '41 °C', '39 °C']);
        const t0 = await page.locator('#abt-temps .abt-temp-bar').first().evaluate((el) => el.style.getPropertyValue('--t'));
        assert.equal(t0, '0.356');
        assert.equal(await page.locator('#abt-temps .abt-temp-bar').first().getAttribute('aria-hidden'), 'true');
      });
      await step('no sensors reads None reported', async () => {
        await patchSystem(page, (s) => ({ ...s, temps: [] }));
        await open(t);
        assert.equal(await text(page, '#abt-temps'), 'SensorsNone reported');
      });
    },
  },
  {
    id: 'SYS-about-all',
    ui: ['next'],
    allow: [/status of 500/, /500 GET .*\/api\/v1\/status$/],
    async run(t) {
      const { page, step } = t;
      await step('the software facts, each as a term and its value', async () => {
        await open(t);
        assert.equal(await text(page, '#abt-version'), '20260929.101500');
        assert.equal(await text(page, '#abt-channel'), 'main');
        assert.equal(await text(page, '#abt-slot'), 'A');
        assert.equal(await text(page, '#abt-sunshine'), 'Sunshine 2026.928.143000');
        assert.equal(await page.locator('#about dl').count(), 4);
      });
      await step('a page served by another version than the one answering says which', async () => {
        const served = await text(page, '.rail-version .mono');
        assert.equal(await page.isVisible('#abt-webui-row'), served !== '20260929.101500');
        await patchSystem(page, (s) => ({ ...s, version: served }));
        await open(t);
        assert.equal(await page.isVisible('#abt-webui-row'), false);
        await page.unroute('**/api/v1/status');
      });
      await step('Copy details puts every row on the clipboard as lines', async () => {
        await open(t);
        await page.evaluate(() => {
          window.__copied = [];
          document.execCommand = (cmd) => {
            if (cmd === 'copy') window.__copied.push(document.querySelector('.copy-scratch')?.value ?? '');
            return cmd === 'copy';
          };
        });
        await page.click('#abt-copy');
        await page.locator('#notices .notice', { hasText: 'Copied.' }).waitFor();
        const copied = await page.evaluate(() => window.__copied[0]);
        assert.match(copied, /^Name: vapor\nAddress: vapor\.local\nIP addresses: 192\.168\.1\.40\nVaporOS: 20260929\.101500\n/);
        assert.match(copied, /\nGraphics: AMD Radeon RX 9070 XT \(amdgpu\) Supported\n/);
        assert.match(copied, /\nk10temp Tctl: 49 °C\n/);
      });
      await step('a failed read is one inline error with Try again', async () => {
        await page.route('**/api/v1/status', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"boom"}' }));
        await open(t);
        await page.locator('#about-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#about-error .region-error-text'), "Couldn't load system information.");
        assert.equal(await page.isVisible('#about'), false);
        await page.unroute('**/api/v1/status');
        await page.click('#about-retry');
        await page.locator('#about:not([hidden])').waitFor();
        assert.equal(await text(page, '#abt-name'), 'vapor');
      });
    },
  },
];
