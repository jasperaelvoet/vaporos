// Flows over Screen (spec-cc-screens §5): Right now, stream quality, the
// virtual screen, resolutions, the stream server, ports and layers. Each ID
// is a row of tools/web/e2e/parity.json (owner C3). See legacy.spec.mjs for
// the flow format.

import assert from 'node:assert/strict';

import { armed, closed, dev, write } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const notice = (page, words) => page.locator('#notices .notice', { hasText: words }).first().waitFor();
const focusedId = (page) => page.evaluate(() => document.activeElement?.id ?? '');
const until = (page, fn, arg) => page.waitForFunction(fn, arg, { timeout: 5000 });


async function open(t, path = '/screen') {
  await t.page.goto(t.url(path));
  await t.ready();
}

// live waits for the event stream, so a /__dev event arrives live.
const live = (page) => until(page, () => document.getElementById('link').dataset.link === 'live');

// patchJSON answers GET path with the real answer changed by fn.
async function patchJSON(page, path, fn) {
  await page.route(`**/api/v1${path}`, async (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    try {
      // Node resolves the fetch itself, and knows no vapor.local.
      const res = await route.fetch({ url: route.request().url().replace('//vapor.local:', '//127.0.0.1:') });
      await route.fulfill({ status: res.status(), contentType: 'application/json', body: JSON.stringify(fn(await res.json())) });
    } catch {
      // The page went on (a reload) while this answer was in flight.
    }
  });
}

// save presses Save the way a person would: scrolled into the middle of
// the screen, never under the tab bar coming back as the field lets go.
async function save(page) {
  const body = write(page, 'PUT', '/sunshine/settings');
  await page.locator('#quality-save').evaluate((el) => el.scrollIntoView({ block: 'center' }));
  await page.click('#quality-save');
  return body;
}

async function press(page, sel) {
  await page.locator(sel).evaluate((el) => el.scrollIntoView({ block: 'center' }));
  await page.click(sel);
}

export default [
  // ------------------------------------------------------------ Right now
  {
    id: 'SCR-now-state',
    ui: ['next'],
    async run(t) {
      const { page, server, step } = t;
      await step('the monitor shows the welcome screen: a cool picture, in words', async () => {
        await open(t);
        assert.equal(await text(page, '#now-title'), 'The monitor shows the welcome screen');
        assert.equal(await text(page, '#now-detail'), 'The virtual screen is off until a game starts.');
        assert.equal(await page.getAttribute('#now', 'data-state'), '');
        assert.equal(await page.getAttribute('#now [data-part="shape"]', 'aria-hidden'), 'true');
      });
      await step('a stream starts: the card heats up and names the device, without a reload', async () => {
        await live(page);
        await dev(server, 'script', { name: 'stream' });
        await until(page, () => document.getElementById('now-title').textContent === 'Streaming to Living room TV');
        assert.equal(await page.getAttribute('#now', 'data-state'), 'streaming');
        assert.equal(await text(page, '#now [data-part="label"]'), 'Living room TV');
      });
      await step('the stream ends: back to the welcome screen', async () => {
        await dev(server, 'script', { name: 'stream-end' });
        await until(page, () => document.getElementById('now-title').textContent === 'The monitor shows the welcome screen');
      });
    },
  },
  {
    id: 'SCR-now-mode',
    ui: ['next'],
    preset: 'streaming',
    async run(t) {
      const { page, server, step } = t;
      await step('the detail and the shape carry the mode and HDR', async () => {
        await open(t);
        assert.equal(await text(page, '#now-detail'), '3840 × 2160 · 60 Hz · HDR on');
        assert.equal(await text(page, '#now [data-part="readout"]'), '3840 × 2160 · 60 Hz · HDR');
        assert.equal(await page.getAttribute('#now [data-part="shape"]', 'data-hdr'), 'true');
      });
      await step('a phone takes over in portrait: the screen reshapes to its aspect', async () => {
        await live(page);
        await dev(server, 'event', { topic: 'session.begin', data: { client: "Sam's iPhone", mode: '1290x2796@120', hdr: false } });
        await until(page, () => document.getElementById('now-detail').textContent === '1290 × 2796 · 120 Hz · HDR off');
        const shape = await page.evaluate(() => {
          const el = document.querySelector('#now [data-part="shape"]');
          return { w: el.style.getPropertyValue('--mode-w'), h: el.style.getPropertyValue('--mode-h'), a: Number(el.style.getPropertyValue('--mode-aspect')) };
        });
        assert.deepEqual([shape.w, shape.h], ['1290', '2796']);
        assert.ok(shape.a > 0.45 && shape.a < 0.47, `aspect ${shape.a}`);
        // The frame morphs into the new aspect (--vos-dur-stretch).
        await until(page, () => {
          const r = document.querySelector('#now [data-part="frame"]').getBoundingClientRect();
          return r.height > r.width * 2;
        });
      });
    },
  },
  {
    id: 'SCR-now-nogpu',
    ui: ['next'],
    preset: 'no-gpu',
    async run(t) {
      const { page, step } = t;
      await step('no supported GPU: the fault, in words, on a cold picture', async () => {
        await open(t);
        assert.equal(await text(page, '#now-title'), 'No supported graphics card');
        assert.equal(await text(page, '#now-detail'), 'Streaming needs an AMD Radeon GPU. The welcome screen still works.');
        assert.equal(await page.getAttribute('#now', 'data-state'), 'fault');
      });
      await step('every display setting is disabled', async () => {
        for (const sel of ['#hdr', '#port', '#add-mode']) assert.equal(await page.isDisabled(sel), true, sel);
        for (const b of await page.locator('.scr-extra button').all()) assert.equal(await b.isDisabled(), true);
        assert.equal(await page.isVisible('#modes-empty'), true);
        assert.equal(await text(page, '#modes-empty'), 'No resolutions yet. They appear once a graphics card drives the virtual screen.');
      });
    },
  },
  {
    id: 'SCR-now-graphics',
    ui: ['next'],
    preset: 'no-gpu',
    async run(t) {
      const { page, server, step } = t;
      await step('an unsupported card is named as such', async () => {
        await open(t);
        await until(page, () => document.getElementById('now-gpu').textContent !== '–');
        assert.equal(await text(page, '#now-gpu'), 'NVIDIA GeForce RTX 4070 (not supported)');
      });
      await step('a supported card is named plainly', async () => {
        await dev(server, 'preset', { name: 'idle' });
        await open(t);
        await until(page, () => document.getElementById('now-gpu').textContent === 'AMD Radeon RX 9070 XT');
      });
    },
  },

  // -------------------------------------------------------- stream quality
  {
    id: 'SCR-quality-encoder',
    ui: ['next'],
    // The load-failure step answers the settings with 500 on purpose.
    allow: [/status of 500/, /500 GET .*\/api\/v1\/sunshine\/settings$/],
    async run(t) {
      const { page, step } = t;
      await step('the old /streaming lands on this card', async () => {
        await page.goto(t.url('/streaming'));
        await page.waitForURL((u) => new URL(u).pathname === '/screen' && new URL(u).hash === '#stream');
        await t.ready();
        await until(page, () => {
          const r = document.getElementById('stream').getBoundingClientRect();
          return r.top >= 0 && r.top < innerHeight / 2;
        });
      });
      await step('only what the server accepts is offered (A1), in a fieldset', async () => {
        const values = await page.$$eval('#q-encoder input[name="encoder"]', (rs) => rs.map((r) => r.value));
        assert.deepEqual(values, ['vulkan', 'vaapi', 'software']);
        assert.equal(await page.isChecked('#q-enc-vulkan'), true);
        assert.equal(await text(page, '#q-encoder legend'), 'Video encoder');
        assert.equal(await page.isDisabled('#quality-save'), true);
      });
      await step('saving VA-API sends only the encoder', async () => {
        await page.check('#q-enc-vaapi');
        assert.equal(await page.isDisabled('#quality-save'), false);
        assert.deepEqual(await save(page), { encoder: 'vaapi' });
        await notice(page, 'Saved. Streaming restarts to use it.');
        await until(page, () => document.getElementById('quality-save').disabled);
      });
      await step('a saved encoder the server no longer offers stays selectable', async () => {
        await patchJSON(page, '/sunshine/settings', (s) => ({ ...s, encoder: 'nvenc' }));
        await open(t);
        const values = await page.$$eval('#q-encoder input[name="encoder"]', (rs) => rs.map((r) => r.value));
        assert.deepEqual(values, ['vulkan', 'vaapi', 'software', 'nvenc']);
        assert.equal(await page.isChecked('#q-enc-nvenc'), true);
        await page.unrouteAll({ behavior: 'ignoreErrors' });
      });
      await step('a load failure stays in the card, with Try again (B8)', async () => {
        await page.route('**/api/v1/sunshine/settings', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"reading sunshine.conf: permission denied"}' }));
        await open(t);
        assert.equal(await text(page, '#quality-error-text'), "Couldn't load the stream settings. Reading sunshine.conf: permission denied");
        assert.equal(await page.isDisabled('#q-bitrate'), true);
        assert.equal(await page.isVisible('#quality-save'), false);
        await page.unrouteAll({ behavior: 'ignoreErrors' });
        await page.click('#quality-retry');
        await until(page, () => !document.getElementById('q-bitrate').disabled);
        assert.equal(await page.isVisible('#quality-error'), false);
        assert.equal(await page.isVisible('#quality-save'), true);
      });
    },
  },
  {
    id: 'SCR-quality-bitrate',
    ui: ['next'],
    async run(t) {
      const { page, server, step } = t;
      await step('the presets set the number; the chosen one is pressed', async () => {
        await open(t);
        assert.equal(await page.inputValue('#q-bitrate'), '150');
        assert.equal(await page.getAttribute('#q-presets [data-mbps="150"]', 'aria-pressed'), 'true');
        await page.click('#q-presets [data-mbps="50"]');
        assert.equal(await page.inputValue('#q-bitrate'), '50');
        assert.equal(await page.getAttribute('#q-presets [data-mbps="50"]', 'aria-pressed'), 'true');
      });
      await step('an edit survives a stream starting (B9)', async () => {
        await live(page);
        await dev(server, 'script', { name: 'stream' });
        await until(page, () => document.getElementById('server-streaming').textContent === 'Yes');
        await page.waitForTimeout(800);
        assert.equal(await page.inputValue('#q-bitrate'), '50');
      });
      await step('out of range: an inline error on the field, focused', async () => {
        await page.fill('#q-bitrate', '2000');
        await press(page, '#quality-save');
        await page.locator('#q-bitrate-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#q-bitrate-error'), 'Use a whole number from 0 to 1000.');
        assert.equal(await page.getAttribute('#q-bitrate', 'aria-invalid'), 'true');
        assert.equal(await focusedId(page), 'q-bitrate');
      });
      await step('Auto saves 0, in kbps, and says when it applies while streaming', async () => {
        await press(page, '#q-presets [data-mbps="0"]');
        assert.equal(await page.inputValue('#q-bitrate'), '0');
        assert.deepEqual(await save(page), { bitrate_kbps_max: 0 });
        await notice(page, 'Saved. It applies after this stream ends.');
      });
    },
  },
  {
    id: 'SCR-quality-gamepad',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('all eight controller types, in words', async () => {
        await open(t);
        const opts = await page.$$eval('#q-gamepad option', (os) => os.map((o) => `${o.value}:${o.textContent}`));
        assert.deepEqual(opts, ['auto:Automatic', 'xone:Xbox One', 'xseries:Xbox Series', 'x360:Xbox 360', 'ds4:DualShock 4', 'ds5:DualSense', 'switch:Switch Pro', 'generic:Generic']);
        assert.equal(await page.inputValue('#q-gamepad'), 'xone');
      });
      await step('choosing DualSense saves only the gamepad', async () => {
        await page.selectOption('#q-gamepad', 'ds5');
        assert.deepEqual(await save(page), { gamepad: 'ds5' });
        await notice(page, 'Saved.');
      });
    },
  },
  {
    id: 'SCR-quality-audio',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the field shows even when the server leaves audio_sink out (A2)', async () => {
        await patchJSON(page, '/sunshine/settings', (s) => {
          const { audio_sink: _, ...rest } = s;
          return rest;
        });
        await open(t);
        assert.equal(await page.isVisible('#q-audio'), true);
        assert.equal(await page.inputValue('#q-audio'), '');
        assert.equal(await page.getAttribute('#q-audio', 'placeholder'), 'Default');
        await page.unroute('**/api/v1/sunshine/settings');
      });
      await step('a name the server would refuse is caught under the field', async () => {
        await page.fill('#q-audio', 'hdmi#1');
        await press(page, '#quality-save');
        await page.locator('#q-audio-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#q-audio-error'), 'Use the sink name exactly as the system lists it.');
      });
      await step('a sink name saves', async () => {
        await page.fill('#q-audio', 'alsa_output.pci-0000_03_00.1.hdmi-stereo');
        assert.deepEqual(await save(page), { audio_sink: 'alsa_output.pci-0000_03_00.1.hdmi-stereo' });
        await notice(page, 'Saved.');
      });
    },
  },

  // -------------------------------------------------------- virtual screen
  {
    id: 'SCR-virtual-hdr',
    ui: ['next'],
    allow: [/status of 500/, /500 PUT .*\/api\/v1\/display\/settings$/],
    async run(t) {
      const { page, step } = t;
      await step('the switch applies at once', async () => {
        await open(t);
        assert.equal(await page.getAttribute('#hdr', 'role'), 'switch');
        assert.equal(await page.isChecked('#hdr'), true);
        const body = write(page, 'PUT', '/display/settings');
        await page.click('#hdr');
        assert.deepEqual(await body, { hdr: false });
        await open(t);
        assert.equal(await page.isChecked('#hdr'), false);
      });
      await step('a failed save flips it back and says why', async () => {
        await page.route('**/api/v1/display/settings', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"saving config: disk full"}' }));
        await page.click('#hdr');
        await notice(page, 'Saving config: disk full');
        await until(page, () => !document.getElementById('hdr').checked);
      });
    },
  },
  {
    id: 'SCR-virtual-port-current',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the port the virtual screen uses is marked current, then the free ones', async () => {
        await open(t);
        const opts = await page.$$eval('#port option', (os) => os.map((o) => o.textContent));
        assert.deepEqual(opts, ['DP-1 (current)', 'DP-2', 'DP-3']);
        assert.equal(await text(page, '#now-port'), 'DP-1');
        assert.equal(await page.isVisible('#port-move'), false);
      });
    },
  },
  {
    id: 'SCR-virtual-port-move',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('choosing another port says when it moves and offers Move', async () => {
        await open(t);
        await page.selectOption('#port', 'DP-2');
        assert.equal(await text(page, '#port-note'), 'Moves to DP-2 when VaporOS restarts.');
        await page.locator('#port-move').waitFor();
      });
      await step('Move asks first (C-port); Cancel changes nothing', async () => {
        await page.click('#port-move');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Move the virtual screen to DP-2?');
        assert.equal(await text(page, '#confirm-body'), 'It moves when VaporOS restarts. Plug the monitor into DP-1 only after that.');
        await page.click('#confirm-cancel');
        await closed(page, 'confirm');
        assert.equal(await page.inputValue('#port'), 'DP-2');
      });
      await step('confirmed, it moves on the next restart, and the restart row says so', async () => {
        await page.click('#port-move');
        const body = write(page, 'PUT', '/display/settings');
        await page.click('#confirm-ok');
        assert.deepEqual(await body, { virtual_connector: 'DP-2' });
        await notice(page, 'The virtual screen moves to DP-2 when VaporOS restarts.');
        await page.locator('#restart-row:not([hidden])').waitFor();
        assert.equal(await text(page, '#restart-text'), 'Restart to apply screen changes.');
        await until(page, () => document.getElementById('port').options[0].textContent === 'DP-2 (current)');
      });
    },
  },

  // ------------------------------------------------------------ resolutions
  {
    id: 'SCR-res-intro',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the intro says how the virtual screen picks its size', async () => {
        await open(t);
        assert.equal(
          await text(page, '.scr-intro'),
          'VaporOS switches the virtual screen to what each Moonlight device asks for. A device with an unusual screen gets the closest size now and its exact size after the next restart.',
        );
      });
    },
  },
  {
    id: 'SCR-res-table',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('one row per size, largest first, with its rates', async () => {
        await open(t);
        const sizes = await page.$$eval('#modes .scr-mode-size', (xs) => xs.map((x) => x.textContent));
        assert.equal(sizes[0], '3840 × 2160');
        assert.ok(sizes.includes('2360 × 1640'));
        assert.equal(sizes.length, 12);
      });
      await step('in use and extra are said in words, not only in colour (D8)', async () => {
        const row = page.locator('#modes .scr-mode', { hasText: '1920 × 1080' });
        assert.equal(await row.locator('[data-kind="current"]').textContent(), '60 Hz (in use)');
        const extra = page.locator('#modes .scr-mode', { hasText: '2360 × 1640' });
        assert.equal(await extra.locator('[data-kind="extra"]').textContent(), '120 Hz (extra)');
        assert.equal(await page.locator('#modes [aria-label]').count(), 0);
      });
    },
  },
  {
    id: 'SCR-res-extra',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('a mode a device asked for is listed with the device, and Remove names it', async () => {
        await open(t);
        const item = page.locator('#asked .scr-extra');
        assert.equal(await item.count(), 1);
        assert.equal(await item.locator('[data-part="mode"]').textContent(), '2360 × 1640 · 120 Hz');
        assert.match(await item.locator('[data-part="sub"]').textContent(), /^Sam's iPad · \d+ d ago$/);
        assert.equal(await page.isVisible('#added-group'), false);
        await page.getByRole('button', { name: 'Remove 2360 × 1640 · 120 Hz' }).waitFor();
      });
      await step('Remove forgets it (C10), focus stays in the section and a restart is asked for', async () => {
        const del = armed(page.waitForRequest((r) => r.method() === 'DELETE'));
        await page.getByRole('button', { name: 'Remove 2360 × 1640 · 120 Hz' }).click();
        assert.match((await del).url(), /\/api\/v1\/display\/modes\/2360x1640(@|%40)120$/);
        await notice(page, 'Removed 2360 × 1640 · 120 Hz. Restart VaporOS to finish.');
        await page.locator('#extras-empty:not([hidden])').waitFor();
        assert.equal(await focusedId(page), 'extras-title');
        await page.locator('#restart-row:not([hidden])').waitFor();
      });
    },
  },
  {
    id: 'SCR-res-add',
    ui: ['next'],
    // The server refuses a mode past the pixel clock with 400.
    allow: [/status of 400/, /400 POST .*\/api\/v1\/display\/modes$/],
    async run(t) {
      const { page, step } = t;
      await step('Add a resolution opens a sheet with focus on Width', async () => {
        await open(t);
        await page.click('#add-mode');
        await page.locator('#add-sheet[open]').waitFor();
        assert.equal(await focusedId(page), 'add-w');
      });
      await step('the client limits are the server limits (D2)', async () => {
        await page.fill('#add-w', '100');
        await page.fill('#add-h', '1080');
        await page.fill('#add-hz', '60');
        await page.click('#add-submit');
        await page.locator('#add-w-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#add-w-error'), 'Too small: use at least 320 × 200.');
        await page.fill('#add-w', '2560');
        await page.fill('#add-hz', '300');
        await page.click('#add-submit');
        await page.locator('#add-hz-error:not([hidden])').waitFor();
        assert.equal(await text(page, '#add-hz-error'), 'Use 24 to 240 Hz.');
      });
      await step('a mode past the pixel clock is refused by the server, under Refresh', async () => {
        await page.fill('#add-w', '7680');
        await page.fill('#add-h', '4320');
        await page.fill('#add-hz', '120');
        await page.click('#add-submit');
        await until(page, () => document.getElementById('add-hz-error').textContent === 'Too much for the virtual screen: try a lower refresh rate.');
      });
      await step('2560 × 1080 at 144 Hz posts its mode; the restart row appears without a reload', async () => {
        await page.fill('#add-w', '2560');
        await page.fill('#add-h', '1080');
        await page.fill('#add-hz', '144');
        const body = write(page, 'POST', '/display/modes');
        await page.click('#add-submit');
        assert.deepEqual(await body, { mode: '2560x1080@144' });
        await closed(page, 'add-sheet');
        await notice(page, 'Added 2560 × 1080 · 144 Hz. Restart VaporOS to use it.');
        await page.locator('#restart-row:not([hidden])').waitFor();
        await page.locator('#added .scr-extra', { hasText: '2560 × 1080 · 144 Hz' }).waitFor();
        assert.equal(await focusedId(page), 'add-mode');
      });
    },
  },

  // ---------------------------------------------------------- stream server
  {
    id: 'SCR-server-badge',
    ui: ['next'],
    preset: 'sunshine-stopped',
    allow: [/status of 50[23]/, /50[23] GET .*\/api\/v1\/sunshine(\/settings)?$/],
    async run(t) {
      const { page, server, step } = t;
      for (const [preset, words] of [['sunshine-stopped', 'Stopped'], ['sunshine-starting', 'Starting'], ['idle', 'Running']]) {
        await step(`${preset}: the badge reads ${words}`, async () => {
          await dev(server, 'preset', { name: preset });
          await open(t);
          await until(page, (w) => document.getElementById('server-badge').textContent === w, words);
        });
      }
      await step('a stream server that runs but does not answer (502): the badge reads Not answering (§5.6)', async () => {
        const body = JSON.stringify({ error: 'Sunshine is not answering: context deadline exceeded' });
        await page.route('**/api/v1/sunshine', (r) => (r.request().method() === 'GET' ? r.fulfill({ status: 502, contentType: 'application/json', body }) : r.fallback()));
        await open(t);
        await until(page, (w) => document.getElementById('server-badge').textContent === w, 'Not answering');
        await page.unrouteAll({ behavior: 'ignoreErrors' });
      });
    },
  },
  {
    id: 'SCR-server-version',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step("Sunshine's version, in mono", async () => {
        await open(t);
        assert.equal(await text(page, '#server-version'), '2026.928.143000');
        assert.match(await page.getAttribute('#server-version', 'class'), /\bmono\b/);
      });
    },
  },
  {
    id: 'SCR-server-streaming',
    ui: ['next'],
    async run(t) {
      const { page, server, step } = t;
      await step('Streaming reads No, then Yes once a stream starts', async () => {
        await open(t);
        assert.equal(await text(page, '#server-streaming'), 'No');
        await live(page);
        await dev(server, 'script', { name: 'stream' });
        await until(page, () => document.getElementById('server-streaming').textContent === 'Yes');
      });
    },
  },
  {
    id: 'SCR-server-restart',
    ui: ['next'],
    preset: 'streaming',
    async run(t) {
      const { page, server, step } = t;
      await step('while streaming, Restart streaming asks first, with Cancel focused (C-sunrestart)', async () => {
        await open(t);
        await page.click('#server-restart');
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Restart streaming?');
        assert.equal(await focusedId(page), 'confirm-cancel');
        const post = write(page, 'POST', '/sunshine/restart');
        await page.click('#confirm-ok');
        assert.deepEqual(await post, {});
        await notice(page, 'Streaming is restarting.');
      });
      await step('with nobody streaming it restarts at once', async () => {
        await dev(server, 'preset', { name: 'idle' });
        await open(t);
        const post = write(page, 'POST', '/sunshine/restart');
        await page.click('#server-restart');
        await post;
        assert.equal(await page.locator('#confirm[open]').count(), 0);
      });
    },
  },
  {
    id: 'SCR-server-log-link',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('View log opens System › Logs in this tab (C3)', async () => {
        await open(t);
        const link = page.getByRole('link', { name: 'View log' });
        assert.equal(await link.getAttribute('target'), null);
        await link.click();
        await page.waitForURL((u) => new URL(u).pathname === '/system/logs');
      });
    },
  },

  // ------------------------------------------------------- ports and layers
  {
    id: 'SCR-ports-list',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the disclosure lists each port with what it is', async () => {
        await open(t);
        assert.equal(await page.isVisible('#ports'), false);
        await page.click('#ports-box summary');
        const rows = await page.$$eval('#ports li', (ls) => ls.map((l) => l.textContent.replace(/\s+/g, ' ').trim()));
        assert.deepEqual(rows, ['DP-1Virtual screenConnected', 'DP-2Disconnected', 'DP-3Disconnected', 'HDMI-A-1MonitorConnected']);
      });
    },
  },
  {
    id: 'SCR-ports-layers',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('one layer is what streaming needs', async () => {
        await open(t);
        await page.click('#ports-box summary');
        assert.equal(await text(page, '#layers'), 'One layer, as streaming needs');
        assert.equal(await page.isVisible('#now-layers'), false);
      });
      await step('a game on its own layer is warned about, in Right now too', async () => {
        const two = (d) => ({ ...d, planes: 2 });
        await patchJSON(page, '/display', two);
        await patchJSON(page, '/status', (s) => ({ ...s, display: s.display && two(s.display) }));
        await open(t);
        await page.click('#ports-box summary');
        assert.equal(await text(page, '#layers'), '2 layers: a game draws on its own layer, and the picture may freeze');
        assert.equal(await text(page, '#now-layers'), '2 layers: a game draws on its own layer, and the picture may freeze.');
      });
    },
  },
];
