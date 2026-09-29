// Unit tests for static/js/state.js and summary.js. Every preset in internal/web/fixtures
// carries an "expect"; its documents, built as the dev server builds them,
// must give that hero, those restart reasons and those cards.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { presets } from './lib/fixtures.mjs';
import {
  canWake, powerPlan, restartReasons, restartRow, snapshotFromStatus, stripModel, updateProgress, wakeTarget,
} from '../static/js/state.js';
import { contextCards, heroModel, powerLine, statusLine } from '../static/js/summary.js';

const NOW = Date.parse('2026-09-29T12:00:00Z');

test('every preset gives its expected hero, restart reasons and cards', () => {
  for (const { name, preset, snap } of presets(NOW)) {
    const want = preset.expect;
    if (!want || want.hero === null) continue; // the page goes elsewhere (sign-in, setup)
    const h = heroModel(snap);
    assert.equal(h.key, want.hero, `${name}: hero`);
    assert.deepEqual(restartReasons(snap).map((r) => r.kind), want.restart, `${name}: restart reasons`);
    assert.deepEqual(contextCards(snap).map((c) => c.id), want.cards, `${name}: cards`);
    if (want.attention) assert.equal(contextCards(snap)[0].id, 'pair', `${name}: attention`);
  }
});

test('B1: a replayed check never shows as updating', () => {
  const snap = { update: { booted: '1', busy: false, progress: null }, display: { state: 'welcome' }, sunshine: { running: true }, live: { progress: { phase: 'check', percent: 0 } } };
  assert.equal(updateProgress(snap), null);
  assert.equal(heroModel(snap).key, 'ready');
  snap.live.progress = { phase: 'write', percent: 40, version: '2' };
  assert.equal(heroModel(snap).key, 'updating');
  assert.equal(heroModel(snap).progress, 0.4);
});

test('restart reasons without next_boot fall back to staged and held', () => {
  assert.deepEqual(restartReasons({ update: { booted: '1', staged: { version: '2' } } }), [{ kind: 'update', version: '2' }]);
  assert.deepEqual(restartReasons({ update: { booted: '2', held: { version: '2' }, other_slot: { version: '1' } } }), [{ kind: 'rollback', version: '1' }]);
  assert.deepEqual(restartReasons({ update: { booted: '2', next_boot: null, staged: { version: '3' } } }), []);
  assert.deepEqual(restartReasons({ restart: { needed: true, reasons: [{ kind: 'display' }] } }), [{ kind: 'display', version: '' }]);
});

test('the restart row picks activate only for the staged version', () => {
  assert.deepEqual(restartRow({ update: { booted: '1', staged: { version: '2' }, next_boot: { version: '2' } } }), { show: true, text: 'Restart to update to 2.', action: 'activate', version: '2' });
  assert.equal(restartRow({ update: { booted: '2', next_boot: { version: '1' } } }).action, 'reboot');
  assert.equal(restartRow({ update: { booted: '1', next_boot: null } }).show, false);
});

test('snapshotFromStatus keeps the stream as the session', () => {
  const snap = snapshotFromStatus({ sunshine: { running: true, streaming: false, pairings: [] }, stream: { client: 'TV', mode: '3840x2160@60', hdr: true, since: '2026-09-29T11:22:00Z' }, display: {}, update: {} });
  assert.equal(snap.sunshine.session.client, 'TV');
  const s = stripModel(snap, NOW);
  assert.equal(s.show, true);
  assert.equal(s.line, '3840×2160 · 60 Hz · HDR');
  assert.equal(s.sinceText, '38 min');
  assert.equal(s.name, 'Now streaming: TV, 3840 by 2160 at 60 hertz, HDR. Show details.');
  assert.equal(stripModel({ display: { state: 'welcome' } }).show, false);
});

test('status line uses the top-level checked time (B2)', () => {
  assert.equal(statusLine({ booted: '1', checked: '2026-09-29T11:20:00Z', config: { auto: 'stage' } }, NOW), 'VaporOS 1 · Up to date · checked 40 min ago');
  assert.equal(statusLine({ booted: '1', config: { auto: 'stage' } }, NOW), 'VaporOS 1 · Not checked yet');
  assert.equal(statusLine({ booted: '1', config: { auto: 'off' } }, NOW), 'VaporOS 1 · Automatic updates are off');
});

test('power line: six variants', () => {
  const base = { idle_shutdown: true, idle_minutes: 15, wol: [], busy: null, shutdown_in: null };
  assert.equal(powerLine({ ...base, idle_shutdown: false }, { now: NOW }), 'Always on.');
  assert.equal(powerLine(base, { now: NOW }), 'Powers off after 15 min without anyone playing.');
  assert.equal(powerLine({ ...base, busy: { reason: 'web UI in use' } }, { now: NOW }), 'Powers off after 15 min without anyone playing.');
  assert.equal(powerLine({ ...base, busy: { reason: 'Steam game' } }, { now: NOW }), 'Staying on: A game is running.');
  assert.equal(powerLine(base, { now: NOW, shutdownIn: 300 }), 'Powers off in 5 min if nobody plays.');
  assert.match(powerLine({ ...base, busy: { reason: 'web UI in use', web: true }, web_until: '2026-09-29T12:05:00Z' }, { now: NOW }), /^Only this page keeps it on\. It powers off about .+ if nobody plays\.$/);
  assert.match(powerLine({ ...base, keep_awake_until: '2026-09-29T13:00:00Z', busy: { reason: 'keep-awake' } }, { now: NOW }), /^Staying awake until .+\.$/);
});

test('hold rule', () => {
  const idle = { update: { booted: '1', busy: false }, display: { state: 'welcome' }, power: { wol: [{ enabled: true }] } };
  assert.equal(powerPlan('reboot', idle).hold, true);
  assert.equal(powerPlan('reboot', idle).confirm.id, 'reboot');
  const staged = { ...idle, update: { booted: '1', staged: { version: '2' }, next_boot: { version: '2' } } };
  assert.equal(powerPlan('reboot', staged).confirm.id, 'reboot-staged');
  assert.equal(powerPlan('reboot', staged).hint, 'Restarting also installs version 2.');
  const streaming = { ...idle, display: { state: 'streaming' }, stream: { client: 'Pixel 9' } };
  assert.deepEqual([powerPlan('reboot', streaming).hold, powerPlan('reboot', streaming).hint], [false, 'Ends the stream to Pixel 9.']);
  const nowol = { ...idle, power: { wol: [{ enabled: false }] } };
  assert.equal(powerPlan('poweroff', nowol).hold, false);
  assert.equal(powerPlan('poweroff', nowol).confirm.id, 'poweroff-nowol');
  const busy = { ...idle, update: { booted: '1', busy: true } };
  assert.equal(powerPlan('poweroff', busy).confirm.id, 'busy-poweroff');
  assert.equal(powerPlan('reboot', busy).hold, false);
});

test('wake target prefers an armed adapter with an address', () => {
  const wol = [{ iface: 'a', supported: true }, { iface: 'b', enabled: true }, { iface: 'c', enabled: true, broadcast: '192.168.1.255' }];
  assert.equal(wakeTarget(wol).iface, 'c');
  assert.equal(wakeTarget([{ iface: 'a', supported: true }]).iface, 'a');
  assert.equal(wakeTarget([]), null);
  assert.equal(canWake({ wol }), true);
  assert.equal(canWake({ wol: [{ enabled: false }] }), false);
});
