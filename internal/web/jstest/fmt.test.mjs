// Unit tests for static/js/fmt.js. Run by TestJavaScript (go test) when Node
// is installed, or directly: node --test internal/web/jstest/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  ago, bytes, clock, compareVersions, duration, elapsed, groupModes, hostLabel, modeLabel, parseMode, percent,
  phaseLabel, plural, shapeAspect, since,
} from '../static/js/fmt.js';

const vectors = JSON.parse(readFileSync(new URL('../../../design/screen-shape-vectors.json', import.meta.url), 'utf8'));

test('bytes uses decimal units like drive vendors', () => {
  assert.equal(bytes(0), '0 B');
  assert.equal(bytes(1500), '1.5 kB');
  assert.equal(bytes(512e9), '512 GB');
  assert.equal(bytes(2e12), '2.0 TB');
  assert.equal(bytes(-1), '–');
  assert.equal(bytes('nope'), '–');
});

test('duration keeps the two largest units', () => {
  assert.equal(duration(0), '0 s');
  assert.equal(duration(60), '1 min');
  assert.equal(duration(3725), '1 h 2 min');
  assert.equal(duration(90000), '1 d 1 h');
  assert.equal(duration(-5), '0 s');
});

test('elapsed is a stopwatch', () => {
  assert.equal(elapsed(5), '00:05');
  assert.equal(elapsed(760), '12:40');
  assert.equal(elapsed(3723), '1:02:03');
  assert.equal(elapsed(-3), '00:00');
});

test('since and ago count from an RFC 3339 time', () => {
  const now = Date.parse('2026-09-29T12:00:00Z');
  assert.equal(since('2026-09-29T11:22:00Z', now), '38 min');
  assert.equal(since('2026-09-29T11:59:30Z', now), 'just started');
  assert.equal(since('garbage', now), '');
  assert.equal(ago('2026-09-29T11:59:50Z', now), 'just now');
  assert.equal(ago('2026-09-29T11:55:00Z', now), '5 min ago');
  assert.equal(ago('2026-09-27T12:00:00Z', now), '2 d ago');
  assert.equal(clock('garbage'), '');
});

test('modeLabel and shapeAspect follow design/screen-shape-vectors.json', () => {
  for (const c of vectors.cases) {
    assert.equal(modeLabel(c.mode, c.hdr), c.label, c.mode);
    const m = parseMode(c.mode);
    assert.equal(!!m, c.ok, c.mode);
    if (c.ok) {
      assert.deepEqual(m, { w: c.w, h: c.h, hz: c.hz }, c.mode);
      assert.equal(shapeAspect(c.mode), c.aspect, c.mode);
    } else {
      assert.equal(shapeAspect(c.mode), vectors.aspect.unknown, c.mode);
    }
  }
});

test('groupModes groups by size, widest first', () => {
  assert.deepEqual(groupModes(['1920x1080@60', '2560x1440@120', '1920x1080@144', 'bad', '1920x1080@60']), [
    { w: 2560, h: 1440, rates: [120] },
    { w: 1920, h: 1080, rates: [60, 144] },
  ]);
});

test('phaseLabel uses T3 for updates and T4 for installs', () => {
  assert.equal(phaseLabel('check'), 'Starting');
  assert.equal(phaseLabel('download'), 'Downloading');
  assert.equal(phaseLabel('write'), 'Downloading');
  assert.equal(phaseLabel('verify'), 'Verifying');
  assert.equal(phaseLabel('install'), 'Finishing');
  assert.equal(phaseLabel('error'), 'Stopped');
  assert.equal(phaseLabel('cancelled'), 'Cancelled');
  assert.equal(phaseLabel('write', 'install'), 'Copying VaporOS');
  assert.equal(phaseLabel('bootloader', 'install'), 'Setting up start-up');
  assert.equal(phaseLabel('configure', 'install'), 'Saving your settings');
  // Words the backend never sends get no special case (B10).
  assert.equal(phaseLabel('kernel'), 'Kernel');
  assert.equal(phaseLabel('some_step', 'install'), 'Some step');
  assert.equal(phaseLabel(''), 'Working');
});

test('small helpers', () => {
  assert.equal(percent(42.4), 42);
  assert.equal(percent(140), 100);
  assert.equal(percent('x'), 0);
  assert.equal(plural(1, 'device'), '1 device');
  assert.equal(plural(3, 'device'), '3 devices');
  assert.equal(compareVersions('20260929.101500', '20260929.143000'), -1);
  assert.equal(compareVersions('20260929.143000', '20260929.143000'), 0);
  assert.equal(hostLabel({ mdns: 'vapor.local' }), 'vapor.local');
  assert.equal(hostLabel({ hostname: 'den' }), 'den.local');
  assert.equal(hostLabel({}), 'vapor.local');
});
