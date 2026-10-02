// Unit tests for static/js/messages.js: T5, pinned with the servers' words.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DECODE, NETWORK, messageFor } from '../static/js/messages.js';

const err = (status, message, extra = {}) => ({ status, message, ...extra });

test('network, rate limit and decode errors', () => {
  assert.equal(messageFor(err(0, 'fetch failed'), { path: '/system' }), NETWORK);
  assert.equal(messageFor(err(429, 'too many', { retryAfter: 12 }), { method: 'POST', path: '/auth/login' }), 'Too many tries. Try again in 12 s.');
  assert.equal(messageFor(err(400, 'bad request body: invalid character'), { method: 'PUT', path: '/power' }), DECODE);
  assert.equal(messageFor(err(400, 'idle_minutes must be 1-1440'), { method: 'PUT', path: '/power' }), 'Idle_minutes must be 1-1440');
});

test('T5 rows', () => {
  const rows = [
    [503, 'Sunshine is still being set up; try again in a few seconds', 'GET', '/sunshine', 'Streaming is starting. Try again in a few seconds.'],
    [502, 'Sunshine is not answering: timeout', 'GET', '/sunshine/clients', "The stream server isn't answering. Restart streaming, or check the log."],
    [400, 'pairing failed: check the PIN and try again', 'POST', '/sunshine/pair', "That PIN didn't work. Check Moonlight and try again."],
    [409, 'no device is waiting to pair: start pairing in Moonlight, then enter the PIN it shows', 'POST', '/sunshine/pair', "Moonlight isn't waiting any more. Start pairing again in Moonlight."],
    [409, '2 devices are waiting to pair; choose which one this PIN is for', 'POST', '/sunshine/pair', 'More than one device is waiting. Choose the one that shows this PIN.'],
    [502, 'context deadline exceeded', 'POST', '/sunshine/pair', "Moonlight didn't finish pairing. Start again in Moonlight."],
    [404, 'no such client', 'DELETE', '/sunshine/clients/abc', 'Steam Deck was already unpaired.'],
    [500, 'settings saved, but Sunshine did not restart', 'PUT', '/sunshine/settings', "Saved, but streaming didn't restart. Restart it below."],
    [502, 'dial tcp: no such host', 'POST', '/update/check', "Couldn't reach the update server. Check the PC's internet connection."],
    [409, 'an update is already running', 'POST', '/update/stage', 'An update is already running.'],
    [409, 'version 2 is still being installed', 'POST', '/update/activate', 'Wait for the update to finish.'],
    [409, 'no update is staged', 'POST', '/update/activate', "There's no update waiting. Check again."],
    [409, 'version 2 failed to start before', 'POST', '/update/rollback', "Version 2 didn't start before, so VaporOS won't go back to it."],
    [400, '8192x8192@240: needs a 5000.0 MHz pixel clock (max 600 MHz)', 'POST', '/display/modes', 'Too much for the virtual screen: try a lower refresh rate.'],
    [400, '4096x2160@60: 4096x2160 is never offered', 'POST', '/display/modes', "4096 × 2160 can't be used; try 3840 × 2160."],
    [409, 'no supported GPU', 'PUT', '/display/settings', 'Needs a supported graphics card.'],
    [409, '/var/mnt/Games is in use; quit the game running from it and try again', 'DELETE', '/storage/libraries/u-1', 'Games is in use. Quit the game running from it, then try again.'],
    [400, '"localhost" is reserved', 'PUT', '/system/hostname', '"localhost" is reserved. Pick another name.'],
    [400, 'invalid hostname', 'PUT', '/system/hostname', 'Use 1–63 lowercase letters, digits or dashes, not starting or ending with a dash.'],
    [403, 'wrong password', 'POST', '/auth/password', "That's not the current password."],
    [403, 'wrong password', 'POST', '/extensions/coolercontrol', "That's not the VaporOS password."],
    [403, 'wrong password', 'PUT', '/extensions/coolercontrol/settings', "That's not the VaporOS password."],
    [503, 'VaporOS is busy checking other sign-ins; try again in a moment', 'POST', '/extensions/coolercontrol', 'VaporOS is busy checking other passwords. Try again in a moment.'],
    [409, 'TruckersMP is not in this version of VaporOS', 'POST', '/extensions/truckersmp', 'TruckersMP is not in this version of VaporOS'],
    [401, 'wrong password', 'POST', '/auth/login', "That password isn't right."],
    [503, 'busy', 'POST', '/auth/login', 'VaporOS is busy checking other sign-ins. Try again in a moment.'],
    [403, 'wrong setup code', 'POST', '/auth/setup', "That setup code isn't right. Use the code on the screen connected to the PC."],
    [409, 'install running', 'POST', '/install/reboot', 'The install is still running.'],
  ];
  for (const [s, text, method, path, want] of rows) {
    assert.equal(messageFor(err(s, text), { method, path }, { v: '2', name: 'Steam Deck', label: 'Games' }), want, `${method} ${path} ${s}`);
  }
});

test('unknown errors show the server text, capitalised', () => {
  assert.equal(messageFor(err(409, 'disk is busy'), { method: 'POST', path: '/install' }), 'Disk is busy');
  assert.equal(messageFor(err(500, ''), { path: '/x' }), 'VaporOS is busy or restarting. Try again in a moment.');
});
