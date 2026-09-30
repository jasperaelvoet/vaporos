// Unit tests for static/js/heatmap.js, the heat fields' painter. Run by
// TestJavaScript (go test) when Node is installed, or directly:
// node --test internal/web/jstest/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { lattice, lut, paint, table, turbulence } from '../static/js/heatmap.js';

const vectors = JSON.parse(readFileSync(new URL('../../../design/heat-vectors.json', import.meta.url), 'utf8'));
const tokens = readFileSync(new URL('../styles/tokens.css', import.meta.url), 'utf8');
const bands = vectors.bands.split(' ').map(Number);
const air = vectors.air.split(' ').map(Number);
const pal = (map) => ({ stops: vectors.maps[map].stops, bands });

test('the tables are the ones internal/brand generates', () => {
  for (const map of Object.keys(vectors.maps)) {
    const t = table(vectors.maps[map].stops, bands);
    assert.equal(t.length, bands[0] * bands[1]);
    [0, 1, 2].forEach((c) => {
      assert.equal(t.map((e) => e[c].toFixed(3)).join(' '), vectors.maps[map].table[c], `${map} channel ${c}`);
    });
  }
});

test('tokens.css hands the painter the same noise and bands', () => {
  assert.match(tokens, new RegExp(`--vos-heat-air: ${vectors.air.replace(/\./g, '\\.')};`));
  assert.match(tokens, new RegExp(`--vos-heat-bands: ${vectors.bands.replace(/\./g, '\\.')};`));
});

// The Filter Effects spec's feTurbulence init(), transliterated from its C.
function specInit(lSeed) {
  const RAND_m = 2147483647;
  const random = (s) => {
    s = 16807 * (s % 127773) - 2836 * Math.floor(s / 127773);
    return s <= 0 ? s + RAND_m : s;
  };
  if (lSeed <= 0) lSeed = -(lSeed % (RAND_m - 1)) + 1;
  if (lSeed > RAND_m - 1) lSeed = RAND_m - 1;
  const sel = [];
  const grad = [[], [], [], []];
  let i;
  for (let k = 0; k < 4; k++) {
    for (i = 0; i < 256; i++) {
      sel[i] = i;
      const g = [0, 0];
      for (let j = 0; j < 2; j++) g[j] = (((lSeed = random(lSeed)) % 512) - 256) / 256;
      const s = Math.sqrt(g[0] * g[0] + g[1] * g[1]);
      grad[k][i] = [g[0] / s, g[1] / s];
    }
  }
  while (--i) {
    const k = sel[i];
    const j = (lSeed = random(lSeed)) % 256;
    sel[i] = sel[j];
    sel[j] = k;
  }
  return { sel, grad };
}

test('the noise lattice is the spec reference code for seed 11', () => {
  const { L, G } = lattice(air[3]);
  const ref = specInit(air[3]);
  assert.deepEqual(Array.from(L.slice(0, 256)), ref.sel);
  assert.deepEqual(Array.from(L.slice(256, 512)), ref.sel);
  for (const k of [0, 1]) {
    for (let i = 0; i < 256; i++) {
      assert.equal(G[k][i * 2], ref.grad[k][i][0]);
      assert.equal(G[k][i * 2 + 1], ref.grad[k][i][1]);
    }
  }
  assert.deepEqual(Array.from(L.slice(0, 4)), [147, 46, 223, 84]);
});

test('turbulence is 8-bit and one half on the lattice', () => {
  const at = (ch, x, y) => Math.round(turbulence(air, ch, x, y) * 255);
  assert.equal(at(0, 0, 0), 128);
  assert.equal(at(1, 0, 0), 128);
  assert.deepEqual([at(0, 100, 50), at(1, 100, 50), at(0, 333, 777), at(1, 333, 777)], [104, 111, 59, 99]);
});

test('the discrete lookup keeps the contour and both ends', () => {
  const l = lut(pal('heat'));
  const hex = (i) => (l[i] >>> 0).toString(16);
  assert.equal(hex(0), 'ff1b050a'); // the floor: band 0, #0a051b
  assert.equal(hex(254), 'ff7deefa');
  assert.equal(hex(255), 'ff7deefa'); // the last entry, never past it
  // grey 23 is the first of band 1: the darker contour entry
  const t = table(pal('heat').stops, bands);
  const px = (e) => (255 << 24) | (Math.round(e[2] * 255) << 16) | (Math.round(e[1] * 255) << 8) | Math.round(e[0] * 255);
  assert.equal(l[Math.ceil((10 * 255) / 120)], px(t[10]) >>> 0);
  assert.ok(t[10][0] < t[11][0]);
});

test('Home at Ready paints the same pixels as ever', () => {
  const src = [[200, 420, 330, 150, 0.06, 1, 0, 0], [232, 64, 126, 178, 0.2, 1, 0, 0], [96, 110, 104, 104, 0.14, 1, 0, 0], [214, 150, 168, 168, 0.43, 1, 0, 0]];
  const px = paint({ W: 100, H: 90, res: 1, w: 400, h: 360, src, air, pal: pal('heat') });
  assert.equal(px.length, 9000);
  assert.equal(createHash('sha256').update(Buffer.from(px.buffer)).digest('hex').slice(0, 16), 'c22d341cc4744c05');
  // a source at opacity 0 draws nothing: all floor
  const none = paint({ W: 20, H: 20, res: 1, w: 400, h: 360, src: src.map((q) => [...q.slice(0, 4), 0, 1, 0, 0]), air, pal: pal('cold') });
  assert.ok(none.every((v) => v === lut(pal('cold'))[0]));
});
