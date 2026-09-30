#!/usr/bin/env node
// After `next build` (package.json postbuild): with the live demo off
// (src/content/demo.ts `enabled: false`), removes out/demo/, so the export
// has no /demo/ page and no demo copied from public/demo/ui. Next builds
// every page under src/app, and a static export can't leave one out, so the
// switch takes the page out here. With the demo on it checks that the page
// and the demo's pages are there.
//
//   node scripts/drop-demo.mjs [outDir=out]
import { existsSync, rmSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const SITE = fileURLToPath(new URL('..', import.meta.url));
const OUT = resolve(SITE, process.argv[2] ?? 'out');
const { demo } = await import('../src/content/demo.ts');

if (!existsSync(join(OUT, 'index.html'))) {
  console.error(`drop-demo: ${OUT} holds no index.html (run next build first)`);
  process.exit(2);
}
const dir = join(OUT, 'demo');
if (!demo.enabled) {
  if (existsSync(dir)) rmSync(dir, { recursive: true, force: true });
  console.log('drop-demo: the live demo is off (src/content/demo.ts): out/demo/ removed');
} else if (!existsSync(join(dir, 'index.html'))) {
  console.error('drop-demo: the live demo is on but out/demo/index.html is missing');
  process.exit(1);
} else {
  const ui = existsSync(join(dir, 'ui', 'demo-manifest.json'));
  console.log(`drop-demo: the live demo is on: out/demo/ kept${ui ? ', with the control center in out/demo/ui/' : ' (no out/demo/ui/: the page shows that the demo is missing)'}`);
}
