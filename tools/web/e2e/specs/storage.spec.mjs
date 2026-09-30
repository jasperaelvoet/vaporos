// Flows over System › Storage (spec-cc-screens §9): the system drive, game
// drives with Stop using, other drives with Use for games or the reason
// they can't, badges, the folder with Copy, Rescan and the disclosures.
// Each ID is a row of tools/web/e2e/parity.json (owner C4). See
// legacy.spec.mjs for the flow format.

import assert from 'node:assert/strict';

import { closed, dev, request, write } from '../lib/dev.mjs';

const text = async (page, sel) => (await page.textContent(sel))?.trim() ?? '';
const until = (page, fn, arg) => page.waitForFunction(fn, arg, { timeout: 5000 });
const focusedText = (page) => page.evaluate(() => document.activeElement?.textContent?.trim() ?? '');

async function open(t) {
  await t.page.goto(t.url('/system/storage'));
  await t.ready();
}

// drive finds a drive's row by its title.
const drive = (page, name) => page.locator('.sto-drive', { has: page.locator('.sto-drive-name', { hasText: new RegExp(`^${name}$`) }) });
const badges = (row) => row.locator('.sto-badge').allTextContents();

// patchStorage answers GET /storage with the real answer changed by fn.
async function patchStorage(page, fn) {
  await page.route('**/api/v1/storage', async (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    try {
      // Node resolves the fetch itself, and knows no vapor.local.
      const res = await route.fetch({ url: route.request().url().replace('//vapor.local:', '//127.0.0.1:') });
      await route.fulfill({ status: res.status(), contentType: 'application/json', body: JSON.stringify(fn(await res.json())) });
    } catch {
      // The page went on while this answer was in flight.
    }
  });
}


export default [
  {
    id: 'SYS-sto-system-drive',
    ui: ['next'],
    async run(t) {
      const { page, server, step } = t;
      await step('the system drive reads its free space on the heat meter', async () => {
        await open(t);
        assert.equal(await text(page, '#sysdrive .sto-free'), '612 GB free of 960 GB');
        assert.equal(await page.getAttribute('#sysdrive-meter', 'aria-valuetext'), '612 GB free of 960 GB');
        assert.equal(await page.getAttribute('#sysdrive-meter', 'aria-valuenow'), '36');
        assert.equal(await page.getAttribute('#sysdrive-meter', 'data-level'), 'ok');
        assert.equal(await page.isVisible('#sysdrive-note'), false);
      });
      await step('the disclosure lists the running system drive and its partition', async () => {
        await page.click('#sysdisks-box summary');
        const item = page.locator('#sysdisks .sto-sysdisk').first();
        assert.equal(await text(page, '#sysdisks .sto-sysdisk-name'), 'System drive');
        assert.match(await item.textContent(), /Samsung SSD 990 PRO 1TB · \/dev\/nvme0n1/);
        assert.equal(await text(page, '#sysdisks .sto-part'), 'vos_data · ext4 · 960 GB · /state');
      });
      await step('another disk with VaporOS on it is explained', async () => {
        await patchStorage(page, (s) => ({
          disks: [...s.disks, { path: '/dev/sde3', parent: '/dev/sde', model: 'Kingston A400', size: 240e9, uuid: '1b2c3d4e-0000-4000-8000-000000000001', label: 'vos_data', fstype: 'ext4', is_system: true, steam_library: false, adopted: false, registered: false }],
        }));
        await open(t);
        await page.click('#sysdisks-box summary');
        const other = page.locator('#sysdisks .sto-sysdisk', { hasText: 'Another drive with VaporOS on it' });
        await other.waitFor();
        assert.match(await other.textContent(), /Kingston A400 · \/dev\/sde.*VaporOS can't use it for games while that install is there\./);
        await page.unroute('**/api/v1/storage');
      });
      await step('an almost full system drive says so and the meter goes cold', async () => {
        await dev(server, 'preset', { name: 'disk-low' });
        await open(t);
        assert.equal(await page.getAttribute('#sysdrive-meter', 'data-level'), 'danger');
        assert.equal(await text(page, '#sysdrive-note'), 'Almost out of space. Move games to a game drive, or uninstall some in Steam.');
      });
    },
  },
  {
    id: 'SYS-sto-badges',
    ui: ['next'],
    preset: 'storage-pending',
    async run(t) {
      const { page, server, step } = t;
      await step('a drive in Steam and one Steam gets soon, in words', async () => {
        await open(t);
        assert.deepEqual(await badges(drive(page, 'Games')), ['Steam library', 'Mounted', 'In Steam']);
        const lib = drive(page, 'Library2');
        assert.deepEqual(await badges(lib), ['Steam library', 'Mounted', 'Added to Steam soon']);
        assert.equal(await lib.locator('.sto-badge', { hasText: 'Added to Steam soon' }).getAttribute('data-tone'), 'hot');
        assert.equal(
          await lib.locator('.sto-line').textContent(),
          "VaporOS adds it to Steam the next time Steam isn't running, at the latest after a restart. To use it now, add this folder in Steam: Settings → Storage → Add Drive.",
        );
      });
      await step('a game drive that is not attached says Not connected', async () => {
        await dev(server, 'preset', { name: 'storage-missing' });
        await open(t);
        const old = drive(page, 'OldSSD');
        assert.deepEqual(await badges(old), ['Not connected', 'In Steam']);
        assert.equal(await old.locator('.sto-folder').isVisible(), false);
        assert.equal(await old.getByRole('button', { name: 'Stop using OldSSD' }).isVisible(), true);
      });
      await step('an adopted drive that did not mount says so, and asks to add its folder', async () => {
        await patchStorage(page, (s) => ({ disks: s.disks.map((d) => (d.label === 'Games' ? { ...d, mounted_at: undefined, free: undefined, registered: false } : d)) }));
        await open(t);
        assert.deepEqual(await badges(drive(page, 'Games')), ['Steam library', 'Not mounted']);
      });
    },
  },
  {
    id: 'SYS-sto-folder-hint',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('a game drive shows the folder to add in Steam, with Copy', async () => {
        await page.addInitScript(() => {
          const real = document.execCommand.bind(document);
          window.__copied = [];
          document.execCommand = (cmd, ...a) => {
            if (cmd === 'copy') window.__copied.push(document.querySelector('.copy-scratch')?.value ?? String(getSelection()));
            return cmd === 'copy' ? true : real(cmd, ...a);
          };
        });
        await open(t);
        const games = drive(page, 'Games');
        assert.equal(await games.locator('.sto-path').textContent(), '/var/mnt/Games');
        await games.getByRole('button', { name: 'Copy the folder /var/mnt/Games' }).click();
        await until(page, () => document.querySelector('.sto-drive [data-copied]') !== null);
        assert.deepEqual(await page.evaluate(() => window.__copied), ['/var/mnt/Games']);
      });
      await step("adopting a drive shows the server's hint, and its library folder", async () => {
        const lib = drive(page, 'Library2');
        await lib.getByRole('button', { name: 'Use Library2 for games' }).click();
        const notice = page.locator('#notices .notice', { hasText: 'Its installed games appear in Steam without downloading.' });
        await notice.waitFor();
        assert.match(await notice.textContent(), /\/var\/mnt\/Library2\/SteamLibrary is one of Steam's game libraries\./);
        await until(page, () => [...document.querySelectorAll('#games .sto-path')].some((p) => p.textContent === '/var/mnt/Library2/SteamLibrary'));
      });
    },
  },
  {
    id: 'SYS-sto-use-for-games',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('a drive that cannot hold games says why and has no button', async () => {
        await open(t);
        const cam = drive(page, 'CAMERA');
        assert.equal(await cam.locator('.sto-line').textContent(), "exFAT can't hold Steam games. Use ext4, btrfs, xfs, f2fs or NTFS.");
        assert.equal(await cam.getByRole('button').count(), 0);
      });
      await step('an NTFS drive asks first; Cancel sends nothing', async () => {
        let posted = 0;
        page.on('request', (r) => r.method() === 'POST' && r.url().endsWith('/storage/libraries') && posted++);
        await drive(page, 'WINDATA').getByRole('button', { name: 'Use WINDATA for games' }).click();
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'WINDATA is a Windows drive');
        assert.equal(await text(page, '#confirm-body'), 'Its games may not start from NTFS. Use it anyway?');
        await page.click('#confirm-cancel');
        await closed(page, 'confirm');
        assert.equal(posted, 0);
      });
      await step('Use anyway adopts it: it moves to Game drives and keeps the focus', async () => {
        const body = write(page, 'POST', '/storage/libraries');
        await drive(page, 'WINDATA').getByRole('button', { name: 'Use WINDATA for games' }).click();
        await page.click('#confirm-ok');
        assert.deepEqual(await body, { uuid: 'E0A133F2B4C5D6E7' });
        await page.locator('#games .sto-drive-name', { hasText: 'WINDATA' }).waitFor();
        await until(page, () => document.activeElement?.textContent === 'WINDATA');
        assert.equal(await focusedText(page), 'WINDATA');
      });
      await step('a drive without a filesystem ID says so', async () => {
        await patchStorage(page, (s) => ({ disks: s.disks.map((d) => (d.label === 'Library2' ? { ...d, uuid: '' } : d)) }));
        await open(t);
        const lib = drive(page, 'Library2');
        assert.equal(await lib.locator('.sto-line').textContent(), "Can't be used: it has no filesystem ID.");
        assert.equal(await lib.getByRole('button', { name: /for games/ }).count(), 0);
      });
    },
  },
  {
    id: 'SYS-sto-stop-using',
    ui: ['next'],
    allow: [/status of 409/, /409 DELETE .*\/api\/v1\/storage\/libraries\//],
    async run(t) {
      const { page, step } = t;
      await step('a drive in use is left alone, in words', async () => {
        await open(t);
        await page.route('**/api/v1/storage/libraries/*', (r) =>
          r.request().method() === 'DELETE' ? r.fulfill({ status: 409, contentType: 'application/json', body: '{"error":"/var/mnt/Games is in use"}' }) : r.fallback(),
        );
        await drive(page, 'Games').getByRole('button', { name: 'Stop using Games' }).click();
        await page.locator('#confirm[open]').waitFor();
        assert.equal(await text(page, '#confirm-title'), 'Stop using Games?');
        assert.equal(await page.getAttribute('#confirm', 'data-tone'), 'danger');
        await page.click('#confirm-ok');
        await page.locator('#notices .notice', { hasText: 'Games is in use. Quit the game running from it, then try again.' }).waitFor();
        await page.unroute('**/api/v1/storage/libraries/*');
      });
      await step('Stop using forgets it: the game drives are empty, and say what to do', async () => {
        const del = request(page, 'DELETE', '/storage/libraries/5e1daa01-7c2b-4f3a-9d8e-0a1b2c3d4e5f');
        await drive(page, 'Games').getByRole('button', { name: 'Stop using Games' }).click();
        await page.click('#confirm-ok');
        await del;
        await page.locator('#notices .notice', { hasText: 'Games is no longer used for games.' }).waitFor();
        await page.locator('#games-empty').waitFor();
        assert.equal(await text(page, '#games-empty'), 'No game drives yet. Add one below to keep games off the system drive.');
        assert.equal(await drive(page, 'Games').locator('.sto-actions').getByRole('button', { name: 'Use Games for games' }).isVisible(), true);
      });
    },
  },
  {
    id: 'SYS-sto-rescan',
    ui: ['next'],
    allow: [/status of 500/, /500 GET .*\/api\/v1\/storage$/],
    async run(t) {
      const { page, step } = t;
      await step('Rescan asks again, busy while it looks', async () => {
        await open(t);
        let gets = 0;
        await page.route('**/api/v1/storage', async (r) => {
          gets++;
          await new Promise((res) => setTimeout(res, 400));
          await r.fallback();
        });
        await page.click('#rescan');
        await until(page, () => document.getElementById('rescan').getAttribute('aria-busy') === 'true');
        assert.equal(await text(page, '#rescan-label'), 'Rescanning…');
        await until(page, () => !document.getElementById('rescan').hasAttribute('aria-busy'));
        assert.equal(gets, 1);
        assert.equal(await text(page, '#rescan-label'), 'Rescan');
        await page.unroute('**/api/v1/storage');
      });
      await step('a failed listing is an inline error with Try again', async () => {
        await page.route('**/api/v1/storage', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"lsblk: exit status 1"}' }));
        await open(t);
        await page.locator('#drives-error').waitFor();
        assert.equal(await text(page, '#drives-error-text'), "Couldn't list the drives. Lsblk: exit status 1");
        await page.unroute('**/api/v1/storage');
        await page.click('#drives-retry');
        await drive(page, 'Games').waitFor();
        assert.equal(await page.isVisible('#drives-error'), false);
      });
    },
  },
  {
    id: 'SYS-sto-help',
    ui: ['next'],
    async run(t) {
      const { page, step } = t;
      await step('the help disclosure keeps the three steps and the filesystems', async () => {
        await open(t);
        assert.equal(await page.isVisible('#steam-help .sto-steps'), false);
        await page.click('#steam-help summary');
        assert.equal(await page.locator('#steam-help .sto-steps li').count(), 3);
        assert.match(await text(page, '#steam-help'), /can't hold Steam games\. Use ext4, btrfs, xfs, f2fs or NTFS\./);
        assert.match(await text(page, '#steam-help'), /Don't see a drive\? A drive with no filesystem needs formatting first\./);
      });
    },
  },
];
