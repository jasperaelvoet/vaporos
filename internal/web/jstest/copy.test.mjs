// Unit tests for static/js/copy.js: T2, T3, T4, T7 and the confirm table.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  CONFIRMS, INSTALL_STEPS, LABELS, UPDATE_PHASES, busyReason, capitalize, confirmCopy, fill, pairPrompt, restartRowText,
} from '../static/js/copy.js';

test('fill and capitalize', () => {
  assert.equal(fill('Version <v> is <x>', { v: '1' }), 'Version 1 is <x>');
  assert.equal(capitalize('sunshine is down'), 'Sunshine is down');
  assert.equal(capitalize(''), '');
});

test('T2: every busy reason in the code has copy', () => {
  const cases = {
    'keep-awake': 'Staying awake until 21:30',
    'manual keep-awake': 'Staying awake: a keep-awake file is set.',
    'streaming to Pixel 9': 'Streaming to Pixel 9',
    'Moonlight stream': 'Someone is streaming',
    'Moonlight stream (waiting for the client to reconnect)': 'Waiting up to 10 min for Moonlight to reconnect',
    'installing update 20260929.143000 (42%)': 'Installing update 20260929.143000',
    'checking for updates': 'Checking for updates',
    'installing an update': 'Installing an update',
    'Steam game': 'A game is running',
    'Steam download/update': 'Steam is downloading',
    'Steam disk activity': 'Steam is using the disk',
    'something new': 'Something new',
  };
  for (const [raw, want] of Object.entries(cases)) assert.equal(busyReason(raw, { until: '21:30' }), want, raw);
  assert.equal(busyReason('web UI in use'), null);
});

test('T3 and T4 cover the backend words', () => {
  for (const p of ['check', 'download', 'write', 'verify', 'install', 'done', 'error', 'idle', 'cancelled']) assert.ok(p in UPDATE_PHASES, p);
  assert.equal(UPDATE_PHASES.check.working, false, 'B1: a check is not an update');
  for (const s of ['probe', 'partition', 'write', 'verify', 'bootloader', 'configure', 'done']) assert.ok(INSTALL_STEPS[s], s);
});

test('T7 labels', () => {
  assert.equal(LABELS.stage, 'Download update');
  assert.equal(LABELS.activate, 'Restart to update');
  assert.equal(fill(LABELS.rollback, { v: '1' }), 'Go back to 1');
  assert.equal(LABELS.sunrestart, 'Restart streaming');
});

test('confirms: danger ones say so, placeholders fill', () => {
  for (const id of ['poweroff', 'poweroff-nowol', 'busy-reboot', 'unpair', 'stop-using', 'sunrestart', 'endstream', 'cancel']) {
    assert.equal(confirmCopy(id, { name: 'x', label: 'x' }).tone, 'danger', id);
  }
  assert.equal(confirmCopy('reboot').tone, 'normal');
  const c = confirmCopy('reboot-staged', { v: '20260929.143000' });
  assert.equal(c.body, 'Version 20260929.143000 starts after the restart. Streams in progress stop.');
  assert.equal(confirmCopy('rollback-now', { v: '1' }).cancel, 'Later');
  for (const id of Object.keys(CONFIRMS)) assert.ok(!/<\w+>/.test(confirmCopy(id, { v: 1, name: 1, label: 1, new: 1, port: 1, old: 1 }).body), id);
  assert.throws(() => confirmCopy('nope'));
});

test('pairing prompt and restart row', () => {
  assert.equal(pairPrompt([]), '');
  assert.equal(pairPrompt([{ name: 'Steam Deck' }]), 'Steam Deck wants to pair');
  assert.equal(pairPrompt([{ name: 'a' }, { name: 'b' }]), '2 devices want to pair');
  assert.equal(restartRowText([{ kind: 'update', version: '2' }]), 'Restart to update to 2.');
  assert.equal(restartRowText([{ kind: 'rollback', version: '1' }]), 'Restart to go back to 1.');
  assert.equal(restartRowText([{ kind: 'display' }]), 'Restart to apply screen changes.');
  assert.equal(restartRowText([{ kind: 'update', version: '2' }, { kind: 'display' }]), 'Restart to finish: version 2 is ready and display changes are waiting.');
  assert.equal(restartRowText([]), '');
});
