#!/usr/bin/env node
// The share cards (src/app/og.png/card.tsx), rendered outside Next (npm test):
// each is a 1200×630 PNG under the budget, its headline fits the column at a
// readable size, and the heat under the words stays in the cool bands (h0–h2,
// where bone text keeps at least 11:1), as on the TV.
//
//   node --conditions=react-server tests/unit/check-og.mjs [--out=DIR]
//
// --out keeps the PNGs. The fonts come from src/fonts/ttf; when the prebuild
// copy is missing this runs scripts/copy-assets.mjs first.
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { registerTsx } from './tsx-hooks.mjs';

const SITE = fileURLToPath(new URL('../../', import.meta.url));
const out = process.argv.find((a) => a.startsWith('--out='))?.slice(6);
let failures = 0;
const check = (name, cond, detail = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${detail ? `  (${detail})` : ''}`);
  if (!cond) failures++;
  return cond;
};

if (!existsSync(join(SITE, 'src/fonts/ttf/anybody-hot.ttf'))) {
  const r = spawnSync(process.execPath, ['scripts/copy-assets.mjs'], { cwd: SITE, stdio: 'inherit' });
  if (r.status !== 0) {
    console.log('FAIL  the fonts for the cards could not be copied (scripts/copy-assets.mjs)');
    process.exit(1);
  }
}
registerTsx();
const { renderCard, CARDS, OG_SIZE } = await import('../../src/app/og.png/card.tsx');

check('cards are 1200×630', OG_SIZE.width === 1200 && OG_SIZE.height === 630);
if (out) mkdirSync(resolve(out), { recursive: true });
for (const name of CARDS) {
  const res = await renderCard(name);
  const png = Buffer.from(await res.arrayBuffer());
  const isPng = png.subarray(0, 8).toString('hex') === '89504e470d0a1a0a';
  const w = isPng ? png.readUInt32BE(16) : 0;
  const h = isPng ? png.readUInt32BE(20) : 0;
  check(`${name}: a ${w}×${h} PNG`, isPng && w === 1200 && h === 630);
  check(`${name}: ${(png.length / 1000).toFixed(0)} kB, under 300 kB`, png.length < 300_000);
  check(`${name}: served as image/png`, res.headers.get('content-type') === 'image/png', res.headers.get('content-type'));
  if (out) writeFileSync(join(resolve(out), `og-${name}.png`), png);
}

// The words against each card's field: no word on a band hotter than h2
// (12 bands, so heat < 3/12), where bone text keeps at least 11:1, as the TV.
const { cardLayout } = await import('../../src/app/og.png/card.tsx');
const { vField } = await import('../../src/app/og.png/thermal.ts');
for (const name of CARDS) {
  const { card, words, source } = await cardLayout(name);
  if (card.ground === 'light') continue; // ash words on white-hot rings
  const f = vField(source, OG_SIZE.height);
  let hottest = 0;
  for (const b of words) for (let y = b.top; y <= b.bottom; y += 4) for (let x = b.left; x <= b.right; x += 4) hottest = Math.max(hottest, f(x, y));
  const right = Math.max(...words.map((b) => b.right));
  check(`${name}: ${words.length} lines of words, right edge ${right} px, sit on the cool bands`, hottest < 0.25, `hottest ${hottest.toFixed(3)}`);
}

console.log(failures ? `\n${failures} FAILED` : '\nALL SHARE CARD CHECKS PASSED');
process.exit(failures ? 1 : 0);
