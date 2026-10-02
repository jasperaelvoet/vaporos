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
const ctx = (doc) => ({ names: X.names(doc), enabled: new Set(doc.extensions.filter((x) => x.wanted || x.mounted || x.core).map((x) => x.id)) });

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
  assert.deepEqual(X.can(ext(base, 'coolercontrol')), [
    'Runs a system service as root.',
    'Adds device rules.',
    'Loads kernel modules.',
    'Has settings that take effect after a restart.',
    'Has its own web page, reachable from your network.',
  ]);
  assert.deepEqual(X.can(ext(base, 'proton')), ['Loads kernel modules.', 'Adds a Steam compatibility tool.']);
  assert.deepEqual(X.can({ permissions: ['service', 'user-service', 'sysctl', 'polkit', 'dbus', 'udev', 'made-up'] }), [
    'Runs a system service.',
    'Runs a background service next to Steam.',
    'Changes kernel settings.',
    'Adds system permission rules.',
    'Adds services to the system bus.',
    'Adds device rules.',
  ]);
  assert.deepEqual(X.can({ permissions: ['modules'], needs_password: true }), ['Loads kernel modules.', 'Sets kernel module options.']);
  assert.deepEqual(X.can({}), ['Adds files only: nothing runs on its own.']);
});

test('what it downloads, and when VaporOS cannot check it', () => {
  assert.deepEqual(X.downloads(ext(base, 'truckersmp')), [
    { text: "The TruckersMP mod from truckersmp.com, when it updates. It runs as a program, and VaporOS can't check these files.", warn: true },
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
  // What installing it pulls in, unless that is there already (Proton is).
  assert.deepEqual(X.lines(ext(base, 'truckersmp'), ctx(base)), []);
  const lone = structuredClone(base);
  Object.assign(ext(lone, 'proton'), { core: false, wanted: false, mounted: false });
  assert.deepEqual(X.lines(ext(lone, 'truckersmp'), ctx(lone)), [{ text: 'Installing it also installs CachyOS Proton.', tone: '' }]);
  const att = preset('extensions-attention');
  assert.deepEqual(X.lines(ext(att, 'coolercontrol')), [
    { text: 'Fans and pumps: 4 found.', tone: '' },
    { text: 'Its service stopped. VaporOS starts it again at the next restart.', tone: 'error' },
  ]);
  // A helper's line keeps its words; an unknown tone is plain, an empty line none.
  assert.deepEqual(X.lines({ status: [{ text: 'odd', tone: 'purple' }, { tone: 'error' }] }), [{ text: 'odd.', tone: '' }]);
});

test('install and remove', () => {
  assert.equal(X.canInstall(ext(base, 'coolercontrol')), true);
  assert.equal(X.canInstall(ext(base, 'proton')), false, 'core is always on');
  assert.equal(X.canInstall({ state: 'not-in-this-version' }), false);
  assert.equal(X.canInstall({ state: 'not-installed', wanted: true }), false);
  assert.deepEqual(X.removal(ext(base, 'proton'), ctx(base)), { show: false, why: '' });
  assert.deepEqual(X.removal(ext(base, 'coolercontrol'), ctx(base)), { show: false, why: '' });
  const att = preset('extensions-attention');
  assert.deepEqual(X.removal(ext(att, 'coolercontrol'), ctx(att)), { show: true, why: '' });
  assert.deepEqual(X.removal(ext(att, 'truckersmp'), ctx(att)), { show: true, why: '' });
  // Only an installed extension that needs it holds a removal back.
  const lib = { id: 'lib', wanted: true, required_by: ['a', 'b', 'c'] };
  const names = { a: 'Alpha', b: 'Beta', c: 'Gamma' };
  assert.deepEqual(X.removal(lib, { names, enabled: new Set(['a']) }), { show: true, why: 'Alpha needs it. Remove that first.' });
  assert.deepEqual(X.removal(lib, { names, enabled: new Set(['a', 'c']) }), { show: true, why: 'Alpha and Gamma need it. Remove those first.' });
  assert.equal(X.and(['a', 'b', 'c']), 'a, b and c');
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
  assert.deepEqual(X.rowLine(preset('extensions-attention')), ['2 installed · 1 needs attention', 'cold']);
  const two = preset('extensions-attention');
  ext(two, 'coolercontrol').state = 'needs-attention';
  assert.deepEqual(X.rowLine(two), ['2 installed · 2 need attention', 'cold']);
  assert.deepEqual(X.rowLine({ extensions: [] }), ['None installed', '']);
  assert.deepEqual(X.rowLine(null), ["Couldn't load", 'cold']);
  assert.deepEqual(X.rowLine({}), ["Couldn't load", 'cold']);
});

test('settings, choices and the web page', () => {
  const [overdrive, poll] = ext(base, 'coolercontrol').settings;
  assert.equal(X.settingHint(overdrive), 'Turns on AMD overdrive in the graphics driver. Takes effect after a restart.');
  assert.equal(X.settingHint(poll), 'How often it reads temperatures and fan speeds.');
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
