// Unit tests for static/js/state.js and summary.js. Every preset in internal/web/fixtures
// carries an "expect"; its documents, built as the dev server builds them,
// must give that hero, those restart reasons and those cards.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { presets } from './lib/fixtures.mjs';
import {
  canWake, pendingReasons, powerPlan, restartReasons, restartRow, snapshotFromStatus, stagedVersion, stripModel, updateProgress, wakeTarget,
} from '../static/js/state.js';
import { contextCards, heroModel, idleSoon, powerLine, statusLine } from '../static/js/summary.js';

const NOW = Date.parse('2026-09-29T12:00:00Z');

test('every preset gives its expected hero, restart reasons and cards', () => {
  for (const { name, preset, snap } of presets(NOW)) {
    const want = preset.expect;
    if (!want || want.hero === null) continue; // the page goes elsewhere (sign-in, setup)
    const h = heroModel(snap);
    assert.equal(h.key, want.hero, `${name}: hero`);
    // restart: what the restart row and the restart-needed state show (a
    // staged update is ready, not one of them).
    assert.deepEqual(pendingReasons(snap).map((r) => r.kind), want.restart, `${name}: restart reasons`);
    assert.deepEqual(contextCards(snap).map((c) => c.id), want.cards, `${name}: cards`);
    assert.equal(h.attention, want.attention || '', `${name}: attention`);
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

test('a staged update is ready: no restart row, a card instead (MASTER-PLAN §1.3)', () => {
  const staged = { update: { booted: '1', staged: { version: '2' }, next_boot: { version: '2' } }, display: { state: 'welcome' }, sunshine: { running: true } };
  assert.equal(stagedVersion(staged), '2');
  assert.deepEqual(pendingReasons(staged), []);
  assert.equal(restartRow(staged).show, false);
  assert.equal(heroModel(staged).key, 'ready');
  assert.deepEqual(contextCards(staged).map((c) => [c.id, c.actions[0].id]), [['update-ready', 'activate']]);
  // Display changes still ask for a restart; a plain one also starts the update.
  const both = { ...staged, display: { state: 'welcome', reboot_needed: true } };
  assert.deepEqual(restartRow(both), { show: true, text: 'Restart to apply screen changes.', action: 'reboot', version: '' });
  assert.equal(restartRow({ update: { booted: '2', next_boot: { version: '1' } } }).action, 'reboot');
  assert.equal(restartRow({ update: { booted: '1', next_boot: null } }).show, false);
});

test('a forward rollback (a newer next_boot with nothing staged) is a plain restart, never activate', () => {
  const snap = {
    update: { booted: '20260929.101500', staged: null, next_boot: { slot: 'b', version: '20260929.143000' } },
    display: { state: 'welcome' },
    sunshine: { running: true },
    restart: { needed: true, reasons: [{ kind: 'update', version: '20260929.143000' }] },
  };
  assert.equal(stagedVersion(snap), '');
  assert.deepEqual(pendingReasons(snap), [{ kind: 'next', version: '20260929.143000' }]);
  assert.deepEqual(restartRow(snap), { show: true, text: 'Version 20260929.143000 starts on the next restart.', action: 'reboot', version: '' });
  const h = heroModel(snap);
  assert.deepEqual([h.key, h.detail, h.actions.map((a) => a.id)], ['restart-needed', 'Version 20260929.143000 starts on the next restart.', ['reboot']]);
  assert.equal(powerPlan('reboot', snap).confirm.id, 'reboot');
  assert.deepEqual(contextCards(snap).map((c) => c.id), []);
});

test('extensions waiting for a restart, and a kind from a newer VaporOS', () => {
  const base = { update: { booted: '1', next_boot: null }, display: { state: 'welcome' }, sunshine: { running: true } };
  const ext = { ...base, restart: { needed: true, reasons: [{ kind: 'extensions' }] } };
  assert.deepEqual(pendingReasons(ext), [{ kind: 'extensions', version: '' }]);
  assert.deepEqual(restartRow(ext), { show: true, text: 'Restart to apply extension changes.', action: 'reboot', version: '' });
  let h = heroModel(ext);
  assert.deepEqual([h.key, h.title, h.detail, h.reason, h.actions.map((a) => a.id)], ['restart-needed', 'Restart to finish', 'Extension changes are waiting.', 'extensions', ['reboot']]);
  // Never a blank row or a broken hero for a kind this page does not know.
  const odd = { ...base, restart: { needed: true, reasons: [{ kind: 'firmware' }, { kind: 'bios' }, { version: '9' }] } };
  assert.deepEqual(pendingReasons(odd).map((r) => r.kind), ['firmware', 'bios']);
  assert.equal(restartRow(odd).text, 'Restart to finish: other changes are waiting.');
  h = heroModel(odd);
  assert.deepEqual([h.key, h.title, h.detail, h.reason], ['restart-needed', 'Restart to finish', 'Changes are waiting.', 'restart']);
  const one = heroModel({ ...base, restart: { needed: true, reasons: [{ kind: 'firmware' }] } });
  assert.deepEqual([one.title, one.detail], ['Restart to finish', 'Changes are waiting.']);
});

test('pairing is the hero: its words, its key, and no second card', () => {
  const snap = { update: { booted: '1' }, display: { state: 'welcome' }, sunshine: { running: true, pairings: [{ name: 'Steam Deck' }] } };
  const h = heroModel(snap);
  assert.deepEqual([h.key, h.attention, h.title, h.actions.map((a) => a.id)], ['ready', 'pair', 'Steam Deck wants to pair', ['pin']]);
  assert.deepEqual(contextCards(snap).map((c) => c.id), []);
  // While streaming the hero is the stream, so the prompt is a card.
  const streaming = { ...snap, stream: { client: 'TV' }, display: { state: 'streaming' } };
  assert.equal(heroModel(streaming).attention, '');
  assert.deepEqual(contextCards(streaming).map((c) => c.id), ['pair']);
});

test('navigation is never the white-hot action', () => {
  assert.equal(heroModel({ update: { booted: '1' }, display: { state: 'streaming' }, stream: { client: 'TV' } }).actions[0].quiet, true);
  assert.equal(heroModel({ update: { booted: '1', busy: true, progress: { phase: 'write', percent: 10 } }, display: {} }).actions[0].quiet, true);
});

test('an idle power-off within 5 min is a card with Stay awake, unless something keeps it on', () => {
  const snap = (power) => ({ update: { booted: '1' }, display: { state: 'welcome' }, sunshine: { running: true }, power: { idle_shutdown: true, idle_minutes: 15, wol: [{ enabled: true }], busy: null, ...power } });
  assert.equal(idleSoon(snap({ shutdown_in: 240 }).power), 240);
  assert.deepEqual(contextCards(snap({ shutdown_in: 240 })).map((c) => [c.id, c.title, c.actions[0].id]), [['idle-soon', 'Powers off in 4 min', 'awake1h']]);
  assert.equal(heroModel(snap({ shutdown_in: 240 })).idle, true);
  assert.deepEqual(contextCards(snap({ shutdown_in: 900 })).map((c) => c.id), []);
  assert.deepEqual(contextCards(snap({ shutdown_in: 240, busy: { reason: 'Steam game' } })).map((c) => c.id), []);
  assert.deepEqual(contextCards(snap({ shutdown_in: 240, idle_shutdown: false })).map((c) => c.id), []);
});

test('snapshotFromStatus keeps the stream as the session', () => {
  const snap = snapshotFromStatus({ sunshine: { running: true, streaming: false, pairings: [] }, stream: { client: 'TV', mode: '3840x2160@60', hdr: true, since: '2026-09-29T11:22:00Z' }, display: {}, update: {} });
  assert.equal(snap.sunshine.session.client, 'TV');
  const s = stripModel(snap, NOW);
  assert.equal(s.show, true);
  assert.equal(s.line, '3840 × 2160 · 60 Hz · HDR');
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
  assert.equal(powerLine({ ...base, busy: { reason: 'manual keep-awake' } }, { now: NOW }), 'Staying on: a keep-awake file is set.');
  assert.equal(powerLine(base, { now: NOW, shutdownIn: 300 }), 'Powers off in 5 min if nobody plays.');
  assert.match(powerLine({ ...base, busy: { reason: 'web UI in use', web: true }, web_until: '2026-09-29T12:05:00Z' }, { now: NOW }), /^On until about .+\. Idle power-off starts \d+ min after this page closes\.$/);
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
  // The confirm names who is streaming, as the hint does.
  assert.deepEqual(powerPlan('reboot', streaming).confirm, { id: 'reboot-stream', vars: { client: 'Pixel 9' } });
  assert.deepEqual(powerPlan('poweroff', streaming).confirm, { id: 'poweroff-stream', vars: { client: 'Pixel 9' } });
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
