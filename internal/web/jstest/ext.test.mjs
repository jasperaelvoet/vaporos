// Unit tests for static/js/ext.js: what System › Extensions and its row on
// System say, from GET /extensions documents (fixtures/base/extensions.json
// and the extensions-* presets).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { FIXTURES, mergePatch } from './lib/fixtures.mjs';
import * as X from '../static/js/ext.js';

const here = dirname(fileURLToPath(import.meta.url));
const read = (p) => JSON.parse(readFileSync(p, 'utf8'));
const base = read(join(FIXTURES, 'base', 'extensions.json'));
const preset = (name) => mergePatch(structuredClone(base), read(join(FIXTURES, 'presets', `${name}.json`)).patch.extensions);
const ext = (doc, id) => doc.extensions.find((x) => x.id === id);
const ctx = X.context;

test('a chip only for a state of design/tokens.json, with its label', () => {
  const tokens = read(join(here, '..', '..', '..', 'design', 'tokens.json')).state;
  for (const c of Object.values(X.CHIPS)) assert.equal(c.label, tokens[c.state].label, c.state);
  assert.deepEqual(X.chip({ state: 'installing', progress: { bytes: 15800000, total: 38400000 } }), { state: 'installing', label: 'Installing · 41%', reason: '' });
  assert.deepEqual(X.chip({ state: 'installing', progress: null }), { state: 'installing', label: 'Installing', reason: '' });
  assert.deepEqual(X.chip({ state: 'installing', progress: { bytes: 0, total: 0 } }).label, 'Installing');
  assert.deepEqual(X.chip({ state: 'restart-needed' }), { state: 'restart-needed', label: 'Restart needed', reason: '' });
  const att = X.chip({ state: 'needs-attention', reason: "the download from truckersmp.com didn't finish. Check the connection. Then try again. And again" });
  assert.deepEqual(att, { state: 'fault', label: 'Needs attention', reason: "The download from truckersmp.com didn't finish. Check the connection." });
  assert.equal(X.chip({ state: 'needs-attention', reason: '' }).reason, 'Something went wrong. Try again, or remove it.');
  for (const s of ['installed', 'not-installed', 'not-in-this-version', 'something-new']) assert.equal(X.chip({ state: s }), null, s);
});

test('facts that are not states stay plain words', () => {
  assert.equal(X.plain({ state: 'installed' }), 'Installed');
  assert.equal(X.plain({ state: 'not-installed' }), 'Not installed');
  assert.equal(X.plain({ state: 'not-in-this-version' }), 'Not in this version');
  assert.equal(X.plain({ state: 'installed', core: true }), 'Always on');
  assert.equal(X.plain({ state: 'needs-attention', core: true }), 'Always on');
  assert.equal(X.plain({ state: 'restart-needed' }), '', 'the chip says it');
  assert.equal(X.plain({ state: 'later' }), '');
});

test('size, upstream and licence', () => {
  assert.equal(X.size(431000000), '431 MB');
  assert.equal(X.size(4100000), '4.1 MB');
  assert.equal(X.size(1420000000), '1.4 GB');
  assert.equal(X.size(0), '');
  assert.equal(X.size(undefined), '');
  assert.equal(X.from(ext(base, 'proton')), 'From CachyOS · LGPL-2.1-or-later and others · 431 MB');
  assert.equal(X.from({ upstream: { name: 'X' } }), 'From X');
  assert.equal(X.progress({ progress: { bytes: 38400000, total: 38400000 } }), 100);
  assert.equal(X.progress({ progress: { bytes: 1, total: 0 } }), null);
});

test('what it can do, in sentences', () => {
  assert.deepEqual(X.can(ext(base, 'coolercontrol')).slice(0, 3), [
    'Runs a system service as root.',
    'Loads kernel modules.',
    'Sets kernel module options (takes effect after a restart).',
  ]);
  assert.deepEqual(X.can(ext(base, 'star-citizen')).slice(0, 2), ['Adds device rules.', 'Adds Star Citizen to your Steam library.']);
  assert.equal(X.can(ext(base, 'coolercontrol')).at(-1), 'Has its own web page, reachable from your network.');
  assert.deepEqual(X.can(ext(base, 'proton')).slice(0, 2), ['Loads kernel modules.', 'Adds a Steam compatibility tool.']);
  assert.deepEqual(X.can({ permissions: ['service', 'user-service', 'sysctl', 'polkit', 'dbus', 'udev', 'made-up'] }), [
    'Runs a system service.',
    'Runs a background service next to Steam.',
    'Changes kernel settings.',
    'Adds system permission rules.',
    'Adds services to the system bus.',
    'Adds device rules.',
  ]);
  // Only what the document carries: no sentence of its own for the rest.
  assert.deepEqual(X.can({}), []);
  assert.deepEqual(X.can({ needs_password: true, runs_as_root: false, settings: [{ key: 'k', restart: true }] }), []);
  assert.deepEqual(X.can({ permissions: ['modules'], module_options: true }), ['Loads kernel modules.', 'Sets kernel module options (takes effect after a restart).']);
});

test('what it can do in Steam, by the names of the games', () => {
  const truckers = {
    id: 'truckersmp',
    name: 'TruckersMP',
    steam: {
      compat_tool: 'proton-cachyos-slr',
      forces: [{ app: 227300, name: 'Euro Truck Simulator 2' }, { app: 270880, name: 'American Truck Simulator' }],
      hooks: [{ app: 227300, name: 'Euro Truck Simulator 2' }, { app: 270880, name: 'American Truck Simulator' }],
      shortcuts: [{ name: 'TruckersMP (ETS2)' }, { name: 'TruckersMP (ATS)' }],
    },
  };
  assert.deepEqual(X.can(truckers), [
    'Makes Steam run Euro Truck Simulator 2 and American Truck Simulator with Proton.',
    'Starts Euro Truck Simulator 2 and American Truck Simulator through VaporOS so TruckersMP can join in.',
    'Adds TruckersMP (ETS2) and TruckersMP (ATS) to your Steam library.',
  ]);
  // A game Steam has not installed yet has no name in the document.
  const unnamed = { name: 'TruckersMP', steam: { forces: [{ app: 227300, name: '' }, { app: 270880, name: '' }], hooks: [{ app: 227300, name: '' }], shortcuts: [{ name: '' }] } };
  assert.deepEqual(X.can(unnamed), [
    'Makes Steam run some games with Proton.',
    'Starts a game through VaporOS so TruckersMP can join in.',
    'Adds a shortcut to your Steam library.',
  ]);
  const mixed = { name: 'X', steam: { forces: [{ app: 1, name: 'Alpha' }, { app: 2, name: '' }, { app: 3, name: '' }], hooks: [{ app: 1, name: 'Alpha' }, { app: 2, name: '' }], shortcuts: [] } };
  assert.deepEqual(X.can(mixed), [
    'Makes Steam run Alpha and 2 other games with Proton.',
    'Starts Alpha and one other game through VaporOS so X can join in.',
  ]);
  // Star Citizen: a shortcut, and a web page and module options beside it.
  assert.deepEqual(X.can({ name: 'Star Citizen', permissions: [], module_options: true, steam: { compat_tool: 'proton-cachyos-slr', forces: [], hooks: [], shortcuts: [{ name: 'Star Citizen' }] }, web: { port: 1, label: 'x' } }), [
    'Sets kernel module options (takes effect after a restart).',
    'Adds Star Citizen to your Steam library.',
    'Has its own web page, reachable from your network.',
  ]);
  // The core Proton only names a tool: nothing to add to its permissions.
  assert.deepEqual(X.can({ permissions: ['compat-tool'], steam: { compat_tool: 'proton-cachyos-slr', forces: [], hooks: [], shortcuts: [] } }), ['Adds a Steam compatibility tool.']);
  assert.deepEqual(X.can({ steam: null }), []);
});

test('the fold is named by what it holds', () => {
  assert.equal(X.moreLabel(2, 1), 'What it can do and downloads');
  assert.equal(X.moreLabel(1, 0), 'What it can do');
  assert.equal(X.moreLabel(0, 1), 'What it downloads');
  assert.equal(X.moreLabel(0, 0), 'Good to know');
});

test('what it downloads, and when VaporOS cannot check it', () => {
  assert.deepEqual(X.downloads(ext(base, 'truckersmp')), [
    { text: "The TruckersMP mod (about 640 MiB) from download.ets2mp.com, when you install it. It runs as a program, and VaporOS can't check these files.", warn: true },
    { text: "Each new version of the TruckersMP mod from download.ets2mp.com, when it updates. It runs as a program, and VaporOS can't check these files.", warn: true },
    { text: "The list of the mod's files and their checksums from update.ets2mp.com, when it updates. Nothing checks it.", warn: false },
    { text: "TruckersMP's current version and the game versions it supports from api.truckersmp.com, when it updates. Nothing checks it.", warn: false },
  ]);
  assert.deepEqual(X.downloads({ downloads: [{ what: 'fonts', from: 'example.com', checked: 'pinned', runs_code: true, when: 'install' }] }), [
    { text: 'Fonts from example.com, when you install it. VaporOS checks it against a fingerprint it ships.', warn: false },
  ]);
  assert.deepEqual(X.downloads({ downloads: [{ what: 'maps', from: 'example.com', checked: 'none', when: 'launch' }] }), [
    { text: 'Maps from example.com, each time it starts. Nothing checks it.', warn: false },
  ]);
  assert.deepEqual(X.downloads({ downloads: [{ what: 'a patch', from: 'example.com', checked: 'publisher-hash', when: 'later' }] }), [
    { text: "A patch from example.com. Checked against the publisher's own fingerprint.", warn: false },
  ]);
  assert.deepEqual(X.downloads({}), []);
});

test('the lines under a card', () => {
  const proton = ext(base, 'proton');
  assert.deepEqual(X.lines(proton).map((l) => l.text), ['Steam runs Windows games with it. VaporOS adds it for you and keeps it apart like every extension.']);
  assert.deepEqual(X.lines({ ...proton, mounted: false, state: 'installing' }).map((l) => l.text), [
    'Steam runs Windows games with it. VaporOS adds it for you and keeps it apart like every extension.',
    "Windows games use Valve's Proton until this finishes.",
  ]);
  const restart = preset('extensions-restart');
  assert.deepEqual(X.lines(ext(restart, 'coolercontrol')), [{ text: 'It is added at the next restart.', tone: '' }]);
  assert.deepEqual(X.lines({ state: 'restart-needed', wanted: false, mounted: true }), [{ text: 'It is removed at the next restart.', tone: '' }]);
  assert.deepEqual(X.lines({ state: 'restart-needed', wanted: true, mounted: true }), [{ text: 'Its changes take effect at the next restart.', tone: '' }]);
  assert.match(X.lines({ state: 'not-in-this-version' })[0].text, /^This version of VaporOS doesn't have it\./);
  // The box's own reason, when it gives one, in at most two sentences.
  assert.deepEqual(X.lines({ state: 'not-in-this-version', reason: 'VaporOS 20261003.0915 dropped it. Remove it, or roll back. Then restart.' }), [
    { text: 'VaporOS 20261003.0915 dropped it. Remove it, or roll back.', tone: '' },
  ]);
  // What installing it pulls in, unless that is there already (Proton is).
  assert.deepEqual(X.lines(ext(base, 'truckersmp'), ctx(base)), []);
  const lone = structuredClone(base);
  Object.assign(ext(lone, 'proton'), { core: false, wanted: false, mounted: false });
  assert.deepEqual(X.lines(ext(lone, 'truckersmp'), ctx(lone)), [{ text: 'Installing it also installs CachyOS Proton.', tone: '' }]);
  // What it requires, directly or not, and only what a restart would not
  // keep anyway: one removed until the restart comes back with it.
  const chain = { extensions: [
    { id: 'a', name: 'A', requires: ['b'] },
    { id: 'b', name: 'B', requires: ['c', 'd'] },
    { id: 'c', name: 'C', requires: ['a'], mounted: true },
    { id: 'd', name: 'D', wanted: true },
  ] };
  assert.deepEqual(X.adds(ext(chain, 'a'), ctx(chain)), ['c', 'b']);
  assert.deepEqual(X.lines(ext(chain, 'a'), ctx(chain)), [{ text: 'Installing it also installs C and B.', tone: '' }]);
  assert.deepEqual(X.lines(ext(chain, 'd'), ctx(chain)), [], 'wanted: nothing to install');
  const att = preset('extensions-attention');
  assert.deepEqual(X.lines(ext(att, 'coolercontrol')), [
    { text: "CoolerControl isn't running. Restart VaporOS, or remove it.", tone: 'warning' },
  ]);
  assert.deepEqual(X.lines(ext(att, 'truckersmp')).map((l) => l.tone), ['', 'warning', '']);
  // A helper's line keeps its words; an unknown tone is plain, an empty line none.
  assert.deepEqual(X.lines({ status: [{ text: 'odd', tone: 'purple' }, { tone: 'error' }] }), [{ text: 'odd.', tone: '' }]);
});

test('install and remove', () => {
  assert.equal(X.canInstall(ext(base, 'coolercontrol')), true);
  assert.equal(X.canInstall(ext(base, 'proton')), false, 'core is always on');
  assert.equal(X.canInstall({ state: 'not-in-this-version' }), false);
  assert.equal(X.canInstall({ state: 'not-installed', wanted: true }), false);
  assert.equal(X.canInstall({ state: 'installing', wanted: false }), false, 'on its way already');
  assert.equal(X.canInstall({ state: 'needs-attention', wanted: false, mounted: true }), true);
  // Removed until the restart: it can come back before, and is not removed twice.
  const gone = { id: 'cc', state: 'restart-needed', wanted: false, mounted: true };
  assert.equal(X.canInstall(gone), true);
  assert.deepEqual(X.removal(gone, { names: {}, enabled: new Set() }), { show: false, why: '' });
  assert.deepEqual(X.removal(ext(base, 'proton'), ctx(base)), { show: false, why: '' });
  assert.deepEqual(X.removal(ext(base, 'coolercontrol'), ctx(base)), { show: false, why: '' });
  const att = preset('extensions-attention');
  assert.deepEqual(X.removal(ext(att, 'coolercontrol'), ctx(att)), { show: true, why: '' });
  assert.deepEqual(X.removal(ext(att, 'truckersmp'), ctx(att)), { show: true, why: '' });
  // Only one a restart keeps (wanted or core) holds a removal back.
  const lib = { id: 'lib', wanted: true, required_by: ['a', 'b', 'c'] };
  const names = { a: 'Alpha', b: 'Beta', c: 'Gamma' };
  assert.deepEqual(X.removal(lib, { names, enabled: new Set(['a']) }), { show: true, why: 'Alpha needs it. Remove that first.' });
  assert.deepEqual(X.removal(lib, { names, enabled: new Set(['a', 'c']) }), { show: true, why: 'Alpha and Gamma need it. Remove those first.' });
  const doc = { extensions: [lib, { id: 'a', name: 'Alpha', mounted: true }, { id: 'b', name: 'Beta', core: true }, { id: 'c', name: 'Gamma', wanted: true }] };
  assert.deepEqual([...ctx(doc).enabled].sort(), ['b', 'c', 'lib'], 'mounted alone is not kept');
  assert.deepEqual(X.removal(lib, ctx(doc)), { show: true, why: 'Beta and Gamma need it. Remove those first.' });
  assert.equal(X.and(['a', 'b', 'c']), 'a, b and c');
});

test('why adding asks for the password', () => {
  assert.equal(X.passwordHint(ext(base, 'coolercontrol'), ctx(base)), 'CoolerControl runs as root, so VaporOS asks for its password.');
  assert.equal(X.passwordHint({ id: 'k', name: 'Kernel thing', module_options: true }, ctx(base)), 'Kernel thing changes kernel settings, so VaporOS asks for its password.');
  // needs_password covers what it also installs: the hint names that one.
  const doc = { extensions: [{ id: 'app', name: 'App', requires: ['fans'] }, { id: 'fans', name: 'Fans', runs_as_root: true }] };
  assert.equal(X.passwordHint(ext(doc, 'app'), ctx(doc)), 'Fans runs as root, so VaporOS asks for its password.');
  ext(doc, 'fans').wanted = true;
  assert.equal(X.passwordHint(ext(doc, 'app'), ctx(doc)), '', 'already in: the default hint');
  assert.equal(X.wantsPassword({ status: 403, message: 'Adding CoolerControl needs the admin password' }), true);
  assert.equal(X.wantsPassword({ status: 403, message: 'the password is wrong' }), true);
  assert.equal(X.wantsPassword({ status: 403, message: 'csrf token mismatch' }), false);
  assert.equal(X.wantsPassword({ status: 409, message: 'needs the admin password' }), false);
  assert.equal(X.wantsPassword(null), false);
});

test('the drives a drive setting offers', () => {
  const storage = read(join(FIXTURES, 'base', 'storage.json'));
  assert.deepEqual(X.drives(storage.disks), [
    { path: '/var/mnt/Games', text: 'Games · 420 GB free', system: false },
    { path: '/state', text: 'System drive · 612 GB free', system: true },
  ]);
  const disks = [
    { label: 'Old', adopted: true, missing: true, mounted_at: '/var/mnt/Old' },
    { label: 'Bare', adopted: false, mounted_at: '/run/media/Bare' },
    { label: 'VOS_ESP', is_system: true, mounted_at: '/efi' },
    { label: 'vos_data', is_system: true },
    { model: 'WD Blue', adopted: true, mounted_at: '/var/mnt/wd' },
    { label: 'Twice', adopted: true, mounted_at: '/var/mnt/wd' },
  ];
  assert.deepEqual(X.drives(disks), [{ path: '/var/mnt/wd', text: 'WD Blue', system: false }]);
  assert.deepEqual(X.drives(null), []);
});

test('the restart card names what the next restart changes', () => {
  assert.equal(X.restartText(preset('extensions-restart')), 'CoolerControl and TruckersMP are added at the next restart.');
  const drop = { extensions: [{ id: 'a', name: 'A', state: 'restart-needed', wanted: false, mounted: true }] };
  assert.equal(X.restartText(drop), 'A is removed at the next restart.');
  drop.extensions.push({ id: 'b', name: 'B', state: 'restart-needed', wanted: true, mounted: false });
  assert.equal(X.restartText(drop), 'B is added and A is removed at the next restart.');
  assert.equal(X.restartText({ extensions: [] }), 'Changes to extensions take effect at the next restart.');
  // When no card explains it, the server's reason does.
  assert.equal(X.restartText({ extensions: [], restart: { needed: true, reason: 'restart to finish adding CoolerControl' } }), 'Restart to finish adding CoolerControl.');
});

test('a helper line that repeats a note is said once', () => {
  const proton = { ...ext(base, 'proton'), status: [{ text: 'Steam runs Windows games with it.' }, { text: 'Version 10.0-3' }] };
  assert.deepEqual(X.lines(proton).map((l) => l.text), [
    'Steam runs Windows games with it. VaporOS adds it for you and keeps it apart like every extension.',
    'Version 10.0-3.',
  ]);
});

test('the Extensions row on System', () => {
  assert.deepEqual(X.rowLine(base), ['1 installed', '']);
  assert.deepEqual(X.rowLine(preset('extensions-installing')), ['1 installed · installing CoolerControl · 41%', 'hot']);
  assert.deepEqual(X.rowLine(preset('extensions-restart')), ['1 installed · restart needed', 'hot']);
  assert.deepEqual(X.rowLine(preset('extensions-attention')), ['3 installed · 1 needs attention', 'cold']);
  const two = preset('extensions-attention');
  ext(two, 'coolercontrol').state = 'needs-attention';
  assert.deepEqual(X.rowLine(two), ['3 installed · 2 need attention', 'cold']);
  assert.deepEqual(X.rowLine({ extensions: [] }), ['None installed', '']);
  assert.deepEqual(X.rowLine(null), ["Couldn't load", 'cold']);
  assert.deepEqual(X.rowLine({}), ["Couldn't load", 'cold']);
});

test('settings, choices and the web page', () => {
  const [curves, it87] = ext(base, 'coolercontrol').settings;
  assert.equal(X.settingHint(curves), "Lets CoolerControl set an AMD card's fan curve. This turns on the card's overclocking controls, so a wrong setting there can make the card unstable. Takes effect after a restart.");
  assert.match(X.settingHint(it87), /^Makes fans on some ITE chips show up\..* Takes effect after a restart\.$/);
  const [disk] = ext(base, 'star-citizen').settings;
  assert.equal(X.settingHint(disk), disk.help, 'no restart: the help alone');
  assert.equal(X.settingHint({ restart: true }), 'Takes effect after a restart.');
  assert.equal(X.choiceLabel('every_two_seconds'), 'Every two seconds');
  assert.equal(X.webLabel(ext(base, 'coolercontrol')), 'Open CoolerControl');
  assert.equal(X.webLabel({ web: { label: 'Open CoolerControl' } }), 'Open CoolerControl', 'never "Open Open"');
  assert.equal(X.webLabel({ name: 'Thing', web: { port: 1 } }), 'Open Thing');
  assert.equal(X.webURL(ext(base, 'coolercontrol'), 'vapor.local'), 'http://vapor.local:11987/');
  assert.equal(X.webURL(ext(base, 'coolercontrol'), '[fd00::5]'), 'http://[fd00::5]:11987/');
  assert.equal(X.webURL(ext(base, 'proton'), 'vapor.local'), '');
  assert.equal(X.webURL({ web: { port: 70000 } }, 'vapor.local'), '');
  assert.equal(X.webURL({ web: { port: 11987 } }, ''), '');
});
