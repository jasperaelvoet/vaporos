// Flows over the installer wizard on the live ISO (spec-cc-screens §15,
// MASTER-PLAN §3.5 C5): the setup code and its waiver, the drives with the
// image's minimum size and version, an early stop on an image the probe
// rejects, repair keeping the name and time zone, the libraries, the
// choices surviving a reload (never the password), the warning when another
// drive already has VaporOS, the install itself and its failure, and the
// restart that hands over to <hostname>.local. Each ID is a row of
// tools/web/e2e/parity.json (owner C5). See legacy.spec.mjs for the format.
//
// The dev server's installer (internal/web/devserver_install_test.go) takes
// about 18 s to install and 20 s to restart into the installed system.

import assert from 'node:assert/strict';

import { dev } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const focusedId = (page) => page.evaluate(() => document.activeElement?.id ?? '');
const visible = (page, sel) => page.isVisible(sel);
const pane = (page) => page.getAttribute('#wizard', 'data-step');

const CODE = 'ABCD-EFGH';
const WRONG = "That code isn't right. Check the screen connected to the PC.";
// A wrong setup code is answered 403, which the browser logs.
const REFUSED = [/status of 403/, /403 GET .*\/api\/v1\/install\/(status|probe)/];
// While the PC restarts nothing answers, and the installed system refuses
// a name that is not its own (421).
const DOWN = [/Failed to load resource/, /net::ERR_/, /status of 421/, /421 GET .*\/api\/v1\/ping/, /\/api\/v1\/(ping|events|install\/status)/];

async function open(page, url, ready, path = `/setup?code=${CODE}`) {
  await page.goto(url(path));
  await ready();
}

async function pick(page, disk) {
  await page.locator(`input[name="disk"][value="${disk}"]`).check({ force: true });
}

// toInstall walks from the drive step to the summary: the drive, a name and
// password (erase), the extras as offered.
async function toSummary(page, { disk = '/dev/nvme0n1', host = 'vapor', password = 'correct horse', mode } = {}) {
  await pick(page, disk);
  await page.click('#disk-next');
  await page.locator('#account-title').waitFor();
  if (mode) await page.locator(`input[name="mode"][value="${mode}"]`).check({ force: true });
  if (await visible(page, '#hostname')) await page.fill('#hostname', host);
  await page.fill('#password', password);
  await page.fill('#password2', password);
  await page.click('#account-form button[type="submit"]');
  await page.locator('#extras-title').waitFor();
  await page.click('#extras-form button[type="submit"]');
  await page.locator('#summary-title').waitFor();
}

async function install(page) {
  if (await visible(page, '#erase-confirm')) await page.fill('#erase-confirm', 'ERASE');
  await page.click('#install-btn');
  await page.locator('#progress-title').waitFor();
}

// probeWith answers GET /install/probe with change(probe) applied: the
// dev server's answer, asked on its loopback address (Node cannot resolve
// the *.local name the browser maps).
async function probeWith(page, server, change) {
  await page.route('**/api/v1/install/probe*', async (r) => {
    try {
      const res = await fetch(`http://127.0.0.1:${server.port}/api/v1/install/probe`, { headers: { 'X-VOS-Setup': CODE } });
      const body = await res.json();
      change(body);
      await r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    } catch {
      /* the page moved on while the probe was in flight */
    }
  });
}

export default [
  {
    id: 'ENTRY-install-code',
    ui: ['next'],
    preset: 'installer-code',
    allow: REFUSED,
    async run({ page, url, ready, step }) {
      await step('without a code the wizard asks for the one on the PC\'s screen', async () => {
        await open(page, url, ready, '/setup');
        assert.equal(await pane(page), 'code');
        assert.equal(await page.title(), 'Install VaporOS');
        assert.equal(await text(page, '#code-title'), 'Enter the setup code');
        assert.equal(await visible(page, '#wiz-head'), false);
        assert.equal(await page.inputValue('#setup-code'), '');
      });
      await step('the viewfinder shows this PC\'s address with the handshake mark the TV draws', async () => {
        assert.equal(await visible(page, '#wiz-vf'), true);
        assert.equal(await page.locator('#wiz-vf svg.handshake').count(), 1);
        assert.equal(await text(page, '#vf-id .evf-match'), "The PC's screen shows the same mark.");
      });
      await step('a wrong code is refused under the field', async () => {
        await page.fill('#setup-code', 'wxyz2345');
        await page.click('#code-submit');
        await page.locator('#setup-code-error', { hasText: WRONG }).waitFor();
        assert.equal(await page.inputValue('#setup-code'), 'WXYZ-2345');
        assert.equal(await page.getAttribute('#setup-code', 'aria-invalid'), 'true');
        assert.equal(await page.getAttribute('#wiz-vf', 'data-heat'), 'cold');
        assert.equal(await focusedId(page), 'setup-code');
      });
      await step('the right code reloads through /setup?code= and opens the drives', async () => {
        await page.fill('#setup-code', 'abcd efgh');
        await page.click('#code-submit');
        await page.locator('#disk-title').waitFor();
        await ready();
        assert.equal(await pane(page), 'disk');
        assert.equal(new URL(page.url()).search, '');
        assert.equal(await page.evaluate(() => sessionStorage.getItem('vos-setup-code')), CODE);
      });
    },
  },
  {
    id: 'ENTRY-install-waiver',
    ui: ['next'],
    preset: 'installer-waived',
    allow: [/status of 403/, /403 GET .*\/api\/v1\/install\/probe/],
    async run({ page, url, ready, step }) {
      await step('with no monitor the code is waived: the wizard opens on the drives', async () => {
        await open(page, url, ready, '/setup');
        assert.equal(await pane(page), 'disk');
        assert.equal(await visible(page, '#code-form'), false);
        assert.equal(await text(page, '#wiz-count'), 'Step 1 of 4');
      });
      await step('a monitor plugged in mid-wizard ends the waiver: back to the code', async () => {
        await page.route('**/api/v1/install/probe*', (r) => r.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"setup code required"}' }), { times: 1 });
        await page.click('#disk-rescan');
        await page.locator('#code-title').waitFor();
        assert.equal(await text(page, '#code-error'), "Enter the setup code shown on the PC's screen to continue.");
        assert.equal(await visible(page, '#wiz-head'), false);
      });
    },
  },
  {
    id: 'ENTRY-install-drive',
    ui: ['next'],
    preset: 'installer-code',
    async run({ page, server, url, ready, step }) {
      await open(page, url, ready);
      await step('the step says what the GPU can do and which version installs', async () => {
        assert.equal(await text(page, '#disk-title'), 'Where should VaporOS go?');
        assert.equal(await text(page, '#gpu-text'), 'AMD Radeon RX 9070 XT is supported for streaming.');
        assert.equal(await page.getAttribute('#gpu-note', 'data-tone'), 'ok');
        assert.equal(await text(page, '#disk-version'), 'Installs VaporOS 20260929.101500.');
        assert.equal(await page.getAttribute('#stepper li[aria-current="step"]', 'data-step'), 'disk');
      });
      await step('the drives: the USB stick it started from is left out, a small one cannot be chosen', async () => {
        const values = await page.$$eval('input[name="disk"]', (xs) => xs.map((x) => [x.value, x.disabled, x.checked]));
        assert.deepEqual(values, [['/dev/nvme0n1', false, false], ['/dev/sda', false, false], ['/dev/sdc', true, false]]);
        const sdc = page.locator('.wiz-opt', { has: page.locator('input[value="/dev/sdc"]') });
        assert.match(await sdc.innerText(), /Kingston DataTraveler[\s\S]*16 GB · USB · \/dev\/sdc[\s\S]*Removable[\s\S]*Too small \(needs 24\.5 GiB\)/);
        assert.match(await page.locator('.wiz-opt', { has: page.locator('input[value="/dev/sda"]') }).innerText(), /Steam library: Games/);
      });
      await step('Next without a drive asks for one', async () => {
        await page.click('#disk-next');
        assert.equal(await text(page, '#disk-error'), 'Choose the drive to install on.');
        assert.equal(await pane(page), 'disk');
      });
      await step('Rescan asks again and keeps the choice', async () => {
        await pick(page, '/dev/sda');
        await page.click('#disk-rescan');
        await page.locator('#disk-rescan:not([aria-busy])').waitFor();
        await page.locator('#disk-list[data-state="ready"]').waitFor();
        assert.equal(await page.isChecked('input[value="/dev/sda"]'), true);
      });
      await step('with one drive big enough, it is chosen already', async () => {
        await dev(server, 'preset', { name: 'installer-one-disk' });
        await open(page, url, ready);
        assert.equal(await page.isChecked('input[value="/dev/nvme0n1"]'), true);
      });
      await step('an unsupported GPU is a warning, not a stop', async () => {
        await probeWith(page, server, (p) => {
          p.gpu = { vendor: 'nvidia', name: 'NVIDIA GeForce RTX 4070', supported: false };
        });
        await open(page, url, ready);
        assert.equal(await text(page, '#gpu-text'), "NVIDIA GeForce RTX 4070 isn't supported yet. VaporOS installs, but streaming needs an AMD Radeon GPU.");
        assert.equal(await page.getAttribute('#gpu-note', 'data-tone'), 'warn');
        await page.unrouteAll({ behavior: 'ignoreErrors' });
      });
      await step('nothing big enough: the drives it found and what it needs', async () => {
        await dev(server, 'preset', { name: 'installer-no-disk' });
        await open(page, url, ready);
        assert.match(await text(page, '#disk-list'), /None of these is big enough\. Connect a drive of at least 24\.5 GiB \(about 26 GB\) and rescan\./);
        await probeWith(page, server, (p) => {
          p.disks = p.disks.filter((d) => d.is_live);
        });
        await page.click('#disk-rescan');
        await page.locator('#disk-list[data-state="empty"]').waitFor();
        assert.equal(await text(page, '#disk-list'), 'No drives found. Connect a drive of at least 24.5 GiB (about 26 GB) and rescan.');
        await page.unrouteAll({ behavior: 'ignoreErrors' });
      });
      await step('an image the probe cannot trust stops the wizard here', async () => {
        await dev(server, 'preset', { name: 'installer-source-error' });
        await open(page, url, ready);
        assert.equal(await text(page, '#disk-fault-text'), "This USB stick's copy of VaporOS can't be installed: manifest.json.sig: the signature does not verify with any trusted key. Write the stick again from a fresh download.");
        assert.equal(await page.getAttribute('#disk-fault', 'data-tone'), 'fault');
        assert.equal(await page.isDisabled('#disk-next'), true);
        assert.equal(await visible(page, '#disk-version'), false);
      });
    },
  },
  {
    id: 'ENTRY-install-name',
    ui: ['next'],
    preset: 'installer-two-vaporos',
    async run({ page, url, ready, step }) {
      await open(page, url, ready);
      await step('a drive with VaporOS says so and offers Repair first', async () => {
        assert.match(await page.locator('.wiz-opt', { has: page.locator('input[value="/dev/sda"]') }).innerText(), /VaporOS installed: den\.local/);
        await pick(page, '/dev/sda');
        await page.click('#disk-next');
        await page.locator('#account-title').waitFor();
        assert.equal(await focusedId(page), 'account-title');
        assert.equal(await text(page, '#mode-field legend'), 'This drive already has VaporOS');
        assert.equal(await page.isChecked('input[name="mode"][value="repair"]'), true);
      });
      await step('repair keeps the name and time zone, and the password is optional', async () => {
        assert.equal(await visible(page, '#hostname-field'), false);
        assert.equal(await text(page, '#repair-keeps'), "Repair keeps this PC's name and time zone. You can rename it later in System › Settings.");
        assert.equal(await text(page, '#password-hint'), 'Leave empty to keep the current password, or set a new one (at least 8 characters).');
        await page.click('#account-form button[type="submit"]');
        await page.locator('#extras-title').waitFor();
        assert.equal(await visible(page, '#timezone-field'), false);
        await page.click('#extras-form button[type="submit"]');
        await page.locator('#summary-title').waitFor();
        assert.match(await text(page, '#summary'), /Install\s*Repair: keep games and settings/);
        assert.match(await text(page, '#summary'), /Address\s*Unchanged[\s\S]*Password\s*Unchanged[\s\S]*Time zone\s*Unchanged/);
        await page.click('#confirm-form [data-back]');
        await page.click('#extras-form [data-back]');
      });
      await step('Erase and install asks for a name, with a live preview', async () => {
        await page.locator('input[name="mode"][value="erase"]').check({ force: true });
        assert.equal(await visible(page, '#hostname-field'), true);
        assert.equal(await text(page, '#password-hint'), "At least 8 characters. You'll use it to sign in here.");
        await page.fill('#hostname', 'Den-2');
        assert.equal(await text(page, '#host-preview'), 'http://den-2.local');
      });
      await step('the name and password follow the server\'s rules, under their fields', async () => {
        await page.fill('#hostname', 'localhost');
        await page.click('#account-form button[type="submit"]');
        assert.equal(await text(page, '#hostname-error'), '"localhost" is reserved. Pick another name.');
        assert.equal(await text(page, '#password-error'), 'Use at least 8 characters.');
        assert.equal(await focusedId(page), 'hostname');
        await page.fill('#hostname', '-den');
        await page.fill('#password', 'correct horse');
        await page.fill('#password2', 'correct horse!');
        await page.click('#account-form button[type="submit"]');
        assert.equal(await text(page, '#hostname-error'), "The name can't start or end with a dash.");
        assert.equal(await text(page, '#password2-error'), "The passwords don't match.");
        await page.fill('#hostname', 'den');
        await page.fill('#password2', 'correct horse');
        await page.click('#account-form button[type="submit"]');
        await page.locator('#extras-title').waitFor();
      });
    },
  },
  {
    id: 'ENTRY-install-games',
    ui: ['next'],
    preset: 'installer-code',
    async run({ page, url, ready, step }) {
      await open(page, url, ready);
      await pick(page, '/dev/nvme0n1');
      await page.click('#disk-next');
      await page.fill('#hostname', 'den');
      await page.fill('#password', 'correct horse');
      await page.fill('#password2', 'correct horse');
      await page.click('#account-form button[type="submit"]');
      await page.locator('#extras-title').waitFor();
      await step('the time zone is the probe\'s, and a library on another drive starts ticked', async () => {
        assert.equal(await page.inputValue('#timezone'), 'Europe/Brussels');
        const games = page.locator('#library-list .wiz-opt', { hasText: 'Games' });
        assert.match(await games.innerText(), /Samsung SSD 870 EVO 1TB · 1\.0 TB · \//);
        assert.equal(await games.locator('input').isChecked(), true);
        assert.equal(await visible(page, '#library-empty'), false);
      });
      await step('an unticked library stays unticked when the visitor comes back', async () => {
        await page.locator('#library-list input').uncheck({ force: true });
        await page.selectOption('#timezone', 'America/Argentina/Buenos_Aires');
        await page.click('#extras-form [data-back]');
        await page.click('#account-form button[type="submit"]');
        await page.locator('#extras-title').waitFor();
        assert.equal(await page.locator('#library-list input').isChecked(), false);
      });
      await step('a reload keeps the drive, name, time zone and libraries, never the password', async () => {
        await page.reload();
        await ready();
        assert.equal(await pane(page), 'disk');
        assert.equal(await page.isChecked('input[value="/dev/nvme0n1"]'), true);
        await page.click('#disk-next');
        assert.equal(await page.inputValue('#hostname'), 'den');
        assert.equal(await page.inputValue('#password'), '');
        assert.equal(await page.evaluate(() => JSON.stringify(sessionStorage).includes('correct horse')), false);
        await page.fill('#password', 'correct horse');
        await page.fill('#password2', 'correct horse');
        await page.click('#account-form button[type="submit"]');
        await page.locator('#extras-title').waitFor();
        assert.equal(await page.inputValue('#timezone'), 'America/Argentina/Buenos_Aires');
        assert.equal(await page.locator('#library-list input').isChecked(), false);
      });
      await step('with no library on another drive, it says where to add drives later', async () => {
        await page.click('#extras-form [data-back]');
        await page.click('#account-form [data-back]');
        await pick(page, '/dev/sda');
        await page.click('#disk-next');
        await page.click('#account-form button[type="submit"]');
        await page.locator('#extras-title').waitFor();
        assert.equal(await text(page, '#library-empty'), 'No Steam libraries found on other drives. You can add drives later in System › Storage.');
      });
    },
  },
  {
    id: 'ENTRY-install-confirm',
    ui: ['next'],
    preset: 'installer-two-vaporos',
    async run({ page, url, ready, step }) {
      await open(page, url, ready);
      await step('the summary lists what will happen, the version included', async () => {
        await toSummary(page, { disk: '/dev/nvme0n1', host: 'den', mode: 'erase' });
        const rows = await page.$$eval('#summary .fact', (fs) => fs.map((f) => [f.querySelector('dt').textContent, f.querySelector('dd').textContent]));
        assert.deepEqual(rows, [
          ['Drive', 'Samsung SSD 990 PRO 1TB (1.0 TB)'],
          ['Install', 'Erase the drive and install'],
          ['Version', '20260929.101500'],
          ['Address', 'http://den.local'],
          ['Password', 'New password set'],
          ['Time zone', 'Europe/Brussels'],
          ['Steam libraries', 'Games'],
        ]);
      });
      await step('another drive with VaporOS is left alone, and the summary says so', async () => {
        assert.equal(await text(page, '#other-vapor-text'), "Samsung SSD 870 EVO 1TB also has VaporOS on it. This install leaves that drive alone, and VaporOS can't use it for games while that install is there.");
      });
      await step('erasing needs ERASE typed (any case) before Install works', async () => {
        assert.equal(await text(page, '#erase-warning'), "Everything on Samsung SSD 990 PRO 1TB (1.0 TB) will be erased. This can't be undone.");
        assert.equal(await page.isDisabled('#install-btn'), true);
        await page.fill('#erase-confirm', 'eras');
        assert.equal(await page.isDisabled('#install-btn'), true);
        await page.fill('#erase-confirm', 'erase');
        assert.equal(await page.isEnabled('#install-btn'), true);
      });
      await step('a repair has nothing to erase', async () => {
        await page.click('#confirm-form [data-back]');
        await page.click('#extras-form [data-back]');
        await page.locator('input[name="mode"][value="repair"]').check({ force: true });
        await page.fill('#password', '');
        await page.fill('#password2', '');
        await page.click('#account-form button[type="submit"]');
        await page.click('#extras-form button[type="submit"]');
        await page.locator('#summary-title').waitFor();
        assert.equal(await visible(page, '#erase-field'), false);
        assert.equal(await page.isEnabled('#install-btn'), true);
      });
    },
  },
  {
    id: 'ENTRY-install-progress',
    ui: ['next'],
    preset: 'installer-code',
    async run({ page, server, url, ready, step }) {
      const bodies = [];
      page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().endsWith('/api/v1/install')) bodies.push(r.postDataJSON());
      });
      await open(page, url, ready);
      await toSummary(page, { host: 'den' });
      await step('Install sends the request CI sends, with the choices', async () => {
        await install(page);
        assert.deepEqual(bodies, [{
          disk: '/dev/nvme0n1', mode: 'erase', hostname: 'den', password: 'correct horse',
          timezone: 'Europe/Brussels', libraries: ['5e1daa01-7c2b-4f3a-9d8e-0a1b2c3d4e5f'], source: '',
        }]);
      });
      await step('progress shows the step in words, the percent and the server\'s message', async () => {
        await page.locator('#progress-step', { hasText: 'Copying VaporOS' }).waitFor({ timeout: 15000 });
        const pct = Number(await page.getAttribute('#progress-bar', 'aria-valuenow'));
        assert.ok(pct >= 10 && pct <= 75, `percent ${pct}`);
        assert.equal(await text(page, '#vf-big'), `${pct}%`);
        assert.match(await text(page, '#progress-message'), /^Writing VaporOS 20260929\.101500 to \/dev\/nvme0n1p2$/);
        assert.equal(await page.getAttribute('#wiz-vf', 'data-heat'), 'write');
        assert.equal(await visible(page, '#wiz-head'), false);
      });
      await step('leaving the page while it installs asks first', async () => {
        const asked = page.waitForEvent('dialog');
        page.close({ runBeforeUnload: true }).catch(() => {});
        const d = await asked;
        assert.equal(d.type(), 'beforeunload');
        await d.dismiss();
        assert.equal(page.isClosed(), false);
      });
      await step('a reload in the middle of it picks the install up again', async () => {
        page.once('dialog', (d) => d.accept());
        await page.reload();
        await page.locator('#progress-title').waitFor();
        assert.equal(await pane(page), 'progress');
      });
      await step('when it is done the wizard moves on', async () => {
        await page.locator('#done-title').waitFor({ timeout: 30000 });
        assert.equal(await page.getAttribute('#wiz-vf', 'data-heat'), 'done');
      });
      await step('a failed install says so with the server\'s own error, and the field goes cold', async () => {
        await dev(server, 'preset', { name: 'installer-failed' });
        await open(page, url, ready);
        await toSummary(page);
        await install(page);
        await page.locator('#progress-failed:not([hidden])').waitFor({ timeout: 20000 });
        assert.equal(await text(page, '#progress-failed'), "The install didn't finish. writing /dev/nvme0n1p2: input/output error");
        assert.equal(await page.getAttribute('#wiz-vf', 'data-heat'), 'failed');
        assert.equal(await focusedId(page), 'retry-btn');
      });
      await step('Back to the summary keeps every choice', async () => {
        await page.click('#retry-btn');
        await page.locator('#summary-title').waitFor();
        assert.match(await text(page, '#summary'), /Password\s*New password set/);
      });
      await step('a reload after a failure opens the drives with the last error', async () => {
        await page.reload();
        await ready();
        assert.equal(await pane(page), 'disk');
        assert.equal(await text(page, '#disk-fault-text'), 'The last install failed: writing /dev/nvme0n1p2: input/output error');
      });
    },
  },
  {
    id: 'ENTRY-install-done',
    ui: ['next'],
    preset: 'installer-code',
    allow: DOWN,
    async run({ page, url, ready, step }) {
      const landed = [];
      await page.route('http://den.local/**', (r) => {
        landed.push(r.request().url());
        r.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>den</title><p>den</p>' });
      });
      await open(page, url, ready);
      await toSummary(page, { host: 'den' });
      await install(page);
      await page.locator('#done-title').waitFor({ timeout: 40000 });
      await step('done: the steps to finish, with the library folder to add in Steam', async () => {
        assert.equal(await text(page, '#done-title'), 'VaporOS is installed');
        assert.equal(await focusedId(page), 'done-title');
        assert.match(await text(page, '.wiz-done'), /^Remove the USB stick from the PC\.\s*Press Restart now\. VaporOS starts in about a minute\./);
        assert.equal(await text(page, '#done-library-paths'), '/var/mnt/Games');
        assert.equal(await text(page, '#vf-needle'), 'installed');
      });
      await step('a reload keeps showing it done', async () => {
        await page.reload();
        await page.locator('#done-title').waitFor();
        assert.equal(await pane(page), 'done');
        assert.equal(await text(page, '#done-library-paths'), '/var/mnt/Games');
      });
      await step('Restart now, with a new name: once this address stops answering, the page moves to http://den.local', async () => {
        await page.click('#reboot-btn');
        await page.locator('#restart-title').waitFor();
        assert.equal(await text(page, '#restart-link'), 'http://den.local');
        await page.waitForURL('http://den.local/login?installed=1', { timeout: 60000 });
        assert.ok(landed.includes('http://den.local/api/v1/ping'), landed.join(' '));
      });
    },
  },
  {
    id: 'ENTRY-install-restart',
    ui: ['next'],
    preset: 'installer-code',
    allow: DOWN,
    async run({ page, url, ready, step }) {
      await open(page, url, ready);
      await toSummary(page, { host: 'vapor', password: 'new password' });
      await install(page);
      await page.locator('#done-title').waitFor({ timeout: 40000 });
      await step('Restart now: the page follows the PC while it is down', async () => {
        await page.click('#reboot-btn');
        await page.locator('#restart-title').waitFor();
        assert.equal(await text(page, '#restart-link'), 'http://vapor.local');
        assert.equal(await text(page, '#restart-ip'), 'http://192.168.1.40');
        await page.locator('#restart-status', { hasText: 'VaporOS is starting from the drive. This takes about a minute.' }).waitFor({ timeout: 15000 });
        assert.equal(await page.getAttribute('#wiz-vf', 'data-heat'), 'down');
      });
      await step('when the installed system answers, sign-in says VaporOS is installed', async () => {
        await page.waitForURL(/\/login\?installed=1$/, { timeout: 60000 });
        await ready();
        assert.equal(await text(page, '#login-lead'), 'VaporOS is installed. Sign in with the password you just chose.');
      });
      await step('the password chosen in the wizard signs in', async () => {
        await page.fill('#login-password', 'new password');
        await page.press('#login-password', 'Enter');
        await page.waitForURL((u) => new URL(u).pathname === '/');
        await ready();
      });
    },
  },
];
