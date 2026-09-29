#!/usr/bin/env node
// Renders the REDLINE share cards outside `next build`, for a quick look and
// for the unit test: the same renderCard() the og.png route handlers call
// (src/app/og.png/card.tsx), through next/og in plain Node.
//
//   node scripts/og.mjs <outDir> [--cards=home,download,install,faq]
//
// Writes <outDir>/og-<card>.png (1200×630). The fonts come from src/fonts/ttf,
// which scripts/copy-assets.mjs fills at prebuild/predev (run it once first).
// TypeScript and TSX are transpiled on the fly with the project's typescript.
import { mkdirSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { registerTsx } from '../tests/unit/tsx-hooks.mjs';

const argv = process.argv.slice(2);
const opt = Object.fromEntries(
  argv.filter((a) => a.startsWith('--')).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.slice(2), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
const outArg = argv.find((a) => !a.startsWith('--'));
if (!outArg) {
  console.error('usage: node scripts/og.mjs <outDir> [--cards=home,download,install,faq]');
  process.exit(2);
}
registerTsx();
const { renderCard } = await import('../src/app/og.png/card.tsx');
const cards = typeof opt.cards === 'string' ? opt.cards.split(',') : ['home', 'download', 'install', 'faq'];
const out = resolve(outArg);
mkdirSync(out, { recursive: true });
for (const name of cards) {
  const t0 = Date.now();
  const res = await renderCard(name);
  const png = Buffer.from(await res.arrayBuffer());
  const file = join(out, `og-${name}.png`);
  writeFileSync(file, png);
  console.log(`og: ${file} (${(png.length / 1000).toFixed(1)} kB, ${Date.now() - t0} ms)`);
}
