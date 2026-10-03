// Unit tests for static/js/pages/devices-scale.js: a device's interface
// size in words, the 10% steps and the edit the page shows before VaporOS
// answers (CONTRACTS PUT /display/screens/{id}).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { autoWords, bounds, canStep, edit, isDefault, kindGlyph, liveScreen, percent, sizeWords, steamNote, stepSize } from '../static/js/pages/devices-scale.js';
import { session } from '../static/js/state.js';

const TV = { id: '27eddd2e84ba', name: 'Living room TV', kind: 'tv', kind_from: 'name', guess: 'tv', size: 1, steam_auto: false, savable: true };

test('liveScreen joins session.begin to GET /display screens by id', () => {
  const display = { screens: [{ ...TV, size: 1.2 }, { id: 'aaaaaaaaaaaa', name: 'Other' }] };
  assert.equal(liveScreen(display, { screen: { id: TV.id } }).size, 1.2);
  assert.equal(liveScreen(display, { client: 'x' }), null);
  assert.equal(liveScreen(display, null), null);
  // Not listed yet: session.begin's own words; ui_scale 0 is Steam's own size.
  const own = liveScreen({}, { screen: { id: 'bbbbbbbbbbbb', name: 'Deck', kind: 'handheld', kind_from: 'resolution', ui_scale: 0 } });
  assert.deepEqual([own.kind, own.size, own.steam_auto, own.savable], ['handheld', 1, true, true]);
  // A device that can't be told apart: id "" and nothing saved.
  const none = liveScreen({ screens: [{ ...TV, id: '', savable: false }] }, { screen: { id: '' } });
  assert.equal(none.savable, false);
  assert.equal(liveScreen({}, { screen: { id: '', kind: 'tv' } }).savable, false);
});

test('a screen told apart after session.begin is savable, by /status stream', () => {
  const since = '2026-09-29T11:22:00Z';
  const begin = { client: 'roth', since, screen: { id: '', name: 'roth', kind: 'phone', kind_from: 'resolution' } };
  const display = { screens: [{ ...TV, id: 'abcabcabcabc', name: 'roth', kind: 'phone', savable: true }] };
  // Only session.begin: its id "" is no longer listed, so nothing is savable.
  assert.equal(liveScreen(display, session({ stream: begin, live: { session: begin } })).savable, false);
  // /status has the screen's own id now.
  const snap = { stream: { client: 'roth', since, screen: { id: 'abcabcabcabc' } }, live: { session: begin } };
  const sc = liveScreen(display, session(snap));
  assert.deepEqual([sc.id, sc.savable], ['abcabcabcabc', true]);
  // Sizing turned off since: /status drops the screen, and so does Playing now.
  assert.equal(liveScreen(display, session({ stream: { client: 'roth', since }, live: { session: begin } })), null);
});

test('Playing now says what the size is', () => {
  assert.equal(sizeWords(TV), 'Automatic · looks like a TV');
  assert.equal(sizeWords({ ...TV, size: 1.137 }), 'Automatic · looks like a TV · 114%');
  assert.equal(sizeWords({ ...TV, kind: 'unknown', kind_from: 'default' }), 'Automatic');
  assert.equal(sizeWords({ ...TV, kind: 'phone', kind_from: 'you' }), 'Phone · 100%');
  assert.equal(sizeWords({ ...TV, kind: 'tablet', kind_from: 'you', size: 1.1 }), 'Tablet · 110%');
  assert.equal(sizeWords({ ...TV, steam_auto: true, size: 1.4 }), "Steam's own size");
});

test('Automatic says what it does and why', () => {
  assert.equal(autoWords(TV), 'looks like a TV, from its name');
  assert.equal(autoWords({ ...TV, kind: 'phone', kind_from: 'resolution' }), 'looks like a phone, from its resolution');
  assert.equal(autoWords({ ...TV, kind: 'handheld', kind_from: 'browser' }), 'looks like a handheld, from its browser');
  assert.equal(autoWords({ ...TV, kind: 'laptop', kind_from: 'history' }), 'looks like a laptop, from its earlier resolutions');
  assert.equal(autoWords({ ...TV, kind_from: 'stream' }), 'looks like a TV, from how it streams');
  // A pick keeps the guess, whose reason GET /display does not give.
  assert.equal(autoWords({ ...TV, kind: 'monitor', kind_from: 'you', guess: 'phone' }), 'looks like a phone');
  assert.equal(autoWords({ ...TV, kind: 'unknown', kind_from: 'default' }), "VaporOS can't tell what it is yet, so it's sized like a TV");
});

test('sizing Steam has a line for each state that needs one', () => {
  for (const s of ['ok', 'idle', '', undefined]) assert.equal(steamNote(s).text, '', String(s));
  for (const s of ['no-debugger', 'unsupported']) assert.deepEqual(steamNote(s), { text: "Can't size Steam right now. Steam's own setting still works.", error: true });
  assert.equal(steamNote('starting').error, false);
  assert.deepEqual(steamNote('off'), { text: '', error: false });
  // A resumed stream keeps its size: calm, and it says when a change applies.
  assert.deepEqual(steamNote('resumed'), { text: "This stream was resumed. Changes apply from this device's next start.", error: false });
});

test('steps of 10% stay on the grid and within 40% to 250%', () => {
  const cases = [
    [1, 1, 1.1], [1, -1, 0.9], [1.137, 1, 1.2], [1.137, -1, 1.1], [1.1, 1, 1.2], [0.7, -1, 0.6],
    [2.45, 1, 2.5], [2.5, 1, 2.5], [0.45, -1, 0.4], [0.4, -1, 0.4], [2.4, 1, 2.5], [0.5, -1, 0.4],
  ];
  for (const [size, dir, want] of cases) assert.equal(stepSize({ size }, dir), want, `${size} ${dir}`);
  assert.equal(canStep({ size: 2.5 }, 1), false);
  assert.equal(canStep({ size: 2.4 }, 1), true);
  assert.equal(canStep({ size: 0.4 }, -1), false);
  assert.equal(canStep({ size: 0.41 }, -1), true);
  assert.equal(percent(1.137), 114);
  assert.equal(percent(undefined), 100);
});

test("steps stop where Steam's own bounds hold the scale", () => {
  // A 2400x1080 phone: beyond 381/225 Steam's maximum holds it.
  const phone = { size: 1.6, size_min: 0.4, size_max: 381 / 225 };
  assert.deepEqual(bounds(phone), [0.4, 381 / 225]);
  assert.equal(canStep(phone, 1), true);
  assert.equal(stepSize(phone, 1), 381 / 225);
  assert.equal(canStep({ ...phone, size: 381 / 225 }, 1), false);
  assert.equal(canStep({ ...phone, size: 1.7 }, 1), false);
  assert.equal(canStep({ ...phone, size: 1.7 }, -1), true);
  // A size stored beyond them steps from the bound: the first tap moves Steam.
  assert.equal(stepSize({ ...phone, size: 2.5 }, -1), 1.6);
  // A 4K TV: below 0.555 Steam's minimum holds it.
  const tv = { size: 0.6, size_min: 0.5548518518518518, size_max: 2.5 };
  assert.equal(stepSize(tv, -1), 0.5548518518518518);
  assert.equal(canStep({ ...tv, size: 0.5548518518518518 }, -1), false);
  assert.equal(canStep({ ...tv, size: 0.4 }, -1), false);
  assert.equal(stepSize({ ...tv, size: 0.4 }, 1), 0.6);
  assert.equal(canStep({ ...tv, size: 2.5 }, 1), false);
  // Nothing moves the scale: neither key does anything.
  const held = { size: 1, size_min: 2.5, size_max: 2.5 };
  assert.deepEqual([canStep(held, 1), canStep(held, -1)], [false, false]);
  // No range (not streaming, an older VaporOS, or nonsense): 40% to 250%.
  assert.deepEqual(bounds({ size: 1 }), [0.4, 2.5]);
  assert.deepEqual(bounds({ size: 1, size_min: 0.2, size_max: 3 }), [0.4, 2.5]);
  assert.deepEqual(bounds({ size: 1, size_min: 2, size_max: 1 }), [0.4, 2.5]);
  assert.deepEqual(bounds(null), [0.4, 2.5]);
});

test('an edit shows what VaporOS will store', () => {
  const pick = edit({ ...TV, size: 1.3 }, { kind: 'monitor' });
  assert.deepEqual([pick.kind, pick.kind_from, pick.size, pick.steam_auto], ['monitor', 'you', 1, false]);
  // The same kind again keeps the size.
  assert.equal(edit({ ...TV, kind_from: 'you', size: 1.3 }, { kind: 'tv' }).size, 1.3);
  // A size alone pins the kind in effect.
  const sized = edit(TV, { size: 1.2 });
  assert.deepEqual([sized.kind, sized.kind_from, sized.size], ['tv', 'you', 1.2]);
  // Automatic again: the guess, its reason from the answer.
  const auto = edit({ ...TV, kind: 'phone', kind_from: 'you', guess: 'tv', size: 1.5 }, { kind: 'auto' });
  assert.deepEqual([auto.kind, auto.kind_from, auto.size], ['tv', '', 1]);
  // Steam's own size, and back by a size.
  assert.equal(edit(TV, { steam_auto: true }).steam_auto, true);
  assert.equal(edit(TV, { steam_auto: true }).kind_from, 'name');
  assert.equal(edit({ ...TV, steam_auto: true }, { size: 1.1 }).steam_auto, false);
  // Reset.
  const reset = edit({ ...TV, kind: 'laptop', kind_from: 'you', size: 0.8, steam_auto: true }, { kind: 'auto', size: 1 });
  assert.ok(isDefault(reset));
  assert.equal(isDefault({ ...TV, size: 1.04 }), false);
  // A size during the session's veto stores only the size: the kind stays automatic.
  const veto = edit({ ...TV, kind: 'tv', kind_from: 'stream', guess: 'phone' }, { size: 1.1 });
  assert.deepEqual([veto.kind, veto.kind_from, veto.size], ['tv', 'stream', 1.1]);
  // A kind from the stream that is the guess itself is pinned like any other.
  assert.equal(edit({ ...TV, kind: 'monitor', kind_from: 'stream', guess: 'monitor' }, { size: 1.1 }).kind_from, 'you');
  // The bounds go with the kind: a size keeps them, another kind waits for the answer's.
  const ranged = { ...TV, size_min: 0.55, size_max: 2.5 };
  assert.deepEqual(bounds(edit(ranged, { size: 1.1 })), [0.55, 2.5]);
  assert.deepEqual(bounds(edit(ranged, { kind: 'tv' })), [0.55, 2.5]);
  assert.equal(edit(ranged, { kind: 'phone' }).size_max, undefined);
});

test('a device row takes the kind every screen of its name agrees on', () => {
  const screens = [
    { name: 'roth', kind: 'phone' },
    { name: 'roth', kind: 'tv' },
    { name: 'Deck', kind: 'handheld' },
    { name: 'Tab', kind: 'tablet' },
    { name: 'Odd', kind: 'unknown' },
  ];
  assert.equal(kindGlyph('roth', screens), '');
  assert.equal(kindGlyph('Deck', screens), 'gamepad');
  assert.equal(kindGlyph('Tab', screens), 'phone');
  assert.equal(kindGlyph('Odd', screens), '');
  assert.equal(kindGlyph('Nobody', screens), '');
  assert.equal(kindGlyph('Deck', undefined), '');
});
