// Unit tests for static/legacy/js/fmt.js. Run by TestJavaScript (go test)
// when Node is installed, or directly: node --test internal/web/jstest/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  bytes, duration, ago, parseMode, modeLabel, groupModes, safeNext, normalizeCode, hostnameError,
  passwordError, sshKeys, sshKeyError, phaseLabel, percent, plural,
} from '../static/legacy/js/fmt.js';

test('bytes uses decimal units like drive vendors', () => {
  assert.equal(bytes(0), '0 B');
  assert.equal(bytes(999), '999 B');
  assert.equal(bytes(1500), '1.5 kB');
  assert.equal(bytes(512e9), '512 GB');
  assert.equal(bytes(2e12), '2.0 TB');
  assert.equal(bytes(1234567890), '1.2 GB');
  assert.equal(bytes(-1), '–');
  assert.equal(bytes('nope'), '–');
});

test('duration keeps the two largest units', () => {
  assert.equal(duration(0), '0 s');
  assert.equal(duration(59), '59 s');
  assert.equal(duration(60), '1 min');
  assert.equal(duration(3725), '1 h 2 min');
  assert.equal(duration(7200), '2 h');
  assert.equal(duration(90000), '1 d 1 h');
  assert.equal(duration(-5), '0 s');
});

test('ago is relative to now', () => {
  const now = Date.parse('2026-09-29T12:00:00Z');
  assert.equal(ago('2026-09-29T11:59:50Z', now), 'just now');
  assert.equal(ago('2026-09-29T11:55:00Z', now), '5 min ago');
  assert.equal(ago('2026-09-29T09:00:00Z', now), '3 h ago');
  assert.equal(ago('2026-09-27T12:00:00Z', now), '2 d ago');
  assert.equal(ago('garbage', now), '');
});

test('modes parse, label and group', () => {
  assert.deepEqual(parseMode('2560x1600@120'), { w: 2560, h: 1600, r: 120 });
  assert.deepEqual(parseMode(' 1920x1080@59.94 '), { w: 1920, h: 1080, r: 59.94 });
  assert.equal(parseMode('2560x1600'), null);
  assert.equal(parseMode('axb@c'), null);
  assert.equal(modeLabel('3840x2160@60'), '3840 × 2160 · 60 Hz');
  assert.equal(modeLabel('odd'), 'odd');
  assert.deepEqual(groupModes(['1920x1080@120', '3840x2160@60', '1920x1080@60', 'bad', '1920x1080@60', '2560x1440@60', '2560x1600@120']), [
    { w: 3840, h: 2160, rates: [60] },
    { w: 2560, h: 1600, rates: [120] },
    { w: 2560, h: 1440, rates: [60] },
    { w: 1920, h: 1080, rates: [60, 120] },
  ]);
});

test('safeNext only allows paths on this origin', () => {
  assert.equal(safeNext('/updates'), '/updates');
  assert.equal(safeNext('/pair?x=1#y'), '/pair?x=1#y');
  assert.equal(safeNext('//evil.example'), '/');
  assert.equal(safeNext('/\\evil.example'), '/');
  assert.equal(safeNext('https://evil.example/'), '/');
  assert.equal(safeNext('javascript:alert(1)'), '/');
  assert.equal(safeNext('/login?next=/'), '/');
  assert.equal(safeNext('/setup'), '/');
  assert.equal(safeNext('/loginx'), '/loginx');
  assert.equal(safeNext(null), '/');
  // URL parsers strip tab, CR and LF, so these would become "//evil.example".
  assert.equal(safeNext('/\t/evil.example'), '/');
  assert.equal(safeNext('/\n/evil.example'), '/');
  assert.equal(safeNext('/\r\\evil.example'), '/');
  assert.equal(safeNext('/\u0000/evil.example'), '/');
  assert.equal(safeNext('/l\togin'), '/');
  // Paths that only look odd stay on this origin.
  assert.equal(safeNext('/./login'), '/');
  assert.equal(safeNext('/storage/../setup'), '/');
  assert.equal(safeNext('/%2F%2Fevil.example'), '/%2F%2Fevil.example');
  assert.equal(safeNext('/updates?x=1'), '/updates?x=1');
});

test('normalizeCode tidies typed setup codes', () => {
  assert.equal(normalizeCode('abcd-efgh'), 'ABCD-EFGH');
  assert.equal(normalizeCode(' abcd efgh '), 'ABCD-EFGH');
  assert.equal(normalizeCode('abcdefgh'), 'ABCD-EFGH');
  assert.equal(normalizeCode('abc'), 'ABC');
  assert.equal(normalizeCode(undefined), '');
});

test('hostnameError follows RFC 1123 labels', () => {
  assert.equal(hostnameError('vapor'), '');
  assert.equal(hostnameError('living-room-2'), '');
  assert.notEqual(hostnameError(''), '');
  assert.notEqual(hostnameError('Vapor'), '');
  assert.notEqual(hostnameError('-vapor'), '');
  assert.notEqual(hostnameError('vapor-'), '');
  assert.notEqual(hostnameError('va_por'), '');
  assert.notEqual(hostnameError('vapor.local'), '');
  assert.notEqual(hostnameError('a'.repeat(64)), '');
  assert.equal(hostnameError('a'.repeat(63)), '');
});

test('passwordError', () => {
  assert.equal(passwordError('longenough', 'longenough'), '');
  assert.match(passwordError('short', 'short'), /8 characters/);
  assert.match(passwordError('longenough', 'different1'), /match/);
  assert.equal(passwordError('', '', { optional: true }), '');
  assert.match(passwordError('', '', { optional: false }), /8 characters/);
  assert.match(passwordError('longenough', '', { optional: true }), /match/);
});

test('ssh keys are split and checked', () => {
  assert.deepEqual(sshKeys('ssh-ed25519 AAAA a@b\n\n# note\n  ecdsa-sha2-nistp256 AAAB=  \r\n'), ['ssh-ed25519 AAAA a@b', 'ecdsa-sha2-nistp256 AAAB=']);
  assert.equal(sshKeyError('ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB+x/y= me@laptop'), '');
  assert.equal(sshKeyError('sk-ssh-ed25519@openssh.com AAAAGnNr'), '');
  assert.notEqual(sshKeyError('ssh-foo AAAA'), '');
  assert.notEqual(sshKeyError('ssh-ed25519'), '');
  assert.notEqual(sshKeyError('ssh-ed25519 not*base64'), '');
  assert.notEqual(sshKeyError('-----BEGIN OPENSSH PRIVATE KEY-----'), '');
});

test('phaseLabel, percent, plural', () => {
  assert.equal(phaseLabel('download'), 'Downloading');
  assert.equal(phaseLabel('write_slot'), 'Write slot');
  assert.equal(phaseLabel(''), 'Working');
  assert.equal(percent(42.6), 43);
  assert.equal(percent(140), 100);
  assert.equal(percent(-3), 0);
  assert.equal(percent('x'), 0);
  assert.equal(plural(1, 'device'), '1 device');
  assert.equal(plural(3, 'device'), '3 devices');
});
