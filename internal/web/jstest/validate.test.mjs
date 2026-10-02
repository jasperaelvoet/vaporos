// Unit tests for static/js/validate.js: T6, the client's limits equal the
// server's. testdata/validate-vectors.json is shared with the Go side
// (TestValidationParity, VOS_WEB_STRICT=1).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  audioSinkError, bitrateError, channelError, cleanDeviceName, cleanHostname, hostnameError, idleMinutesError, modeError,
  normalizeCode, passwordError, pinError, safeNext, sshKeyError, sshKeys, webNext,
} from '../static/js/validate.js';

const V = JSON.parse(readFileSync(new URL('./testdata/validate-vectors.json', import.meta.url), 'utf8'));
const value = (c) => (c.repeat ? c.repeat.repeat(c.times) : c.v);

test('shared vectors', () => {
  for (const c of V.hostname) assert.equal(hostnameError(c.v) === '', c.ok, `hostname ${JSON.stringify(c.v)}`);
  for (const c of V.password) assert.equal(passwordError(value(c)) === '', c.ok, `password ${c.v ?? `${c.repeat}×${c.times}`}`);
  for (const c of V.mode) assert.equal(modeError(c.w, c.h, c.hz) === '', c.ok || !!c.cvt, `mode ${c.w}x${c.h}@${c.hz}`);
  for (const c of V.channel) assert.equal(channelError(c.v) === '', c.ok, `channel ${JSON.stringify(c.v)}`);
  for (const c of V.audio_sink) assert.equal(audioSinkError(c.v) === '', c.ok, `sink ${JSON.stringify(c.v)}`);
  for (const c of V.bitrate_mbps) assert.equal(bitrateError(c.v) === '', c.ok, `bitrate ${c.v}`);
});

test('hostname copy', () => {
  assert.equal(hostnameError(''), 'Enter a name.');
  assert.equal(hostnameError('localhost'), '"localhost" is reserved. Pick another name.');
  assert.equal(hostnameError('-x'), "The name can't start or end with a dash.");
  assert.equal(cleanHostname('  Vapor2 '), 'vapor2');
});

test('password counts characters and bytes like the server', () => {
  assert.equal(passwordError('short'), 'Use at least 8 characters.');
  assert.equal(passwordError('vaporvapor', 'vaporvapo'), "The passwords don't match.");
  assert.equal(passwordError('', '', { optional: true }), '');
  assert.equal(passwordError('a'.repeat(1025)), 'Use a shorter password (at most 1024 bytes).');
  assert.match(passwordError('abcdefg\uD800h'), /can't store/);
});

test('small rules', () => {
  assert.equal(pinError('1234'), '');
  assert.notEqual(pinError('123'), '');
  assert.notEqual(pinError('12a4'), '');
  assert.equal(cleanDeviceName(' Steam\nDeck '), 'Steam Deck');
  assert.equal([...cleanDeviceName('x'.repeat(200))].length, 128);
  assert.equal(idleMinutesError(1), '');
  assert.equal(idleMinutesError(1440), '');
  assert.notEqual(idleMinutesError(0), '');
  assert.notEqual(idleMinutesError(1441), '');
  assert.notEqual(idleMinutesError(2.5), '');
  assert.equal(normalizeCode(' abcd efgh '), 'ABCD-EFGH');
  assert.equal(normalizeCode('abcd-efgh'), 'ABCD-EFGH');
});

test('ssh keys', () => {
  assert.deepEqual(sshKeys('# c\n\nssh-ed25519 AAAA x\n  ssh-rsa BBBB  '), ['ssh-ed25519 AAAA x', 'ssh-rsa BBBB']);
  assert.equal(sshKeyError('ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI me@box'), '');
  assert.match(sshKeyError('ssh-foo AAAA'), /Not an SSH public key/);
  assert.match(sshKeyError('ssh-rsa AA$A'), /incomplete/);
});

test('safeNext stays on this origin and off sign-in', () => {
  assert.equal(safeNext('/system/updates?x=1#y'), '/system/updates?x=1#y');
  for (const bad of ['//evil.example', '/\\evil.example', 'https://evil.example/', '/\t/evil', '/login', '/setup?code=x', '', null]) {
    assert.equal(safeNext(bad), '/', String(bad));
  }
});

test('webNext goes back to an extension page on this host only', () => {
  const here = { hostname: 'vapor.local', port: '' };
  assert.equal(webNext('http://vapor.local:11987/', here), 'http://vapor.local:11987/');
  assert.equal(webNext('http://vapor.local:11987/dash?x=a%40b#y', here), 'http://vapor.local:11987/dash?x=a%40b#y');
  assert.equal(webNext('http://[fd00::50]:11987/', { hostname: '[fd00::50]', port: '' }), 'http://[fd00::50]:11987/');
  for (const bad of [
    'http://evil.example:11987/', 'https://vapor.local:11987/', 'http://vapor.local/', 'http://vapor.local:80/',
    'http://vapor.local:22/', 'http://user@vapor.local:11987/', 'http://vapor.local:11987@evil.example/',
    'http://vapor.local:11987\\@evil.example/', 'http://vapor.local:11987\t/', '//vapor.local:11987/', '/system',
    'javascript:alert(1)', '', null,
  ]) {
    assert.equal(webNext(bad, here), '', String(bad));
  }
  assert.equal(webNext('http://vapor.local:8080/', { hostname: 'vapor.local', port: '8080' }), '', 'the sign-in page itself');
  assert.equal(webNext('http://vapor.local:11987/', undefined), '');
});
