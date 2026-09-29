// Copies the files the site takes from the rest of the repo, before every
// build and dev start (package.json prebuild/predev). Nothing it writes is
// committed (website/.gitignore): the repo keeps one copy of each.
//
//   ../keys/release.pub                         → public/release.pub
//   ../design/fonts/site/anybody-variable.woff2 → src/fonts/  (Anybody, wdth 56-136)
//   ../internal/web/static/fonts/{monasans,martianmono}-*.woff2 → src/fonts/
//   ../internal/display/welcome/fonts/*.ttf     → src/fonts/ttf/  (satori, for OG images)
//
// Every font is checked against the size and sha256 that design/fonts/fonts.json
// records for it, so a stale or hand-edited copy fails the build instead of
// shipping. src/lib/fonts.ts loads the WOFF2 files with next/font/local.
//
// The key's source can be overridden with VAPOROS_KEY_FILE (environment or
// .env*.local, loaded the way Next loads them).
import { createHash } from 'node:crypto';
import { copyFileSync, existsSync, mkdirSync, readFileSync } from 'node:fs';
import { basename, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import nextEnv from '@next/env';

const site = fileURLToPath(new URL('..', import.meta.url));
const repo = resolve(site, '..');
nextEnv.loadEnvConfig(site, process.env.npm_lifecycle_event === 'predev', { info() {}, error: console.error });

const fail = (msg) => {
  console.error(`copy-assets: ${msg}`);
  process.exit(1);
};
const rel = (p) => relative(repo, p);

// ---------------------------------------------------------------- the key
const keySrc = resolve(site, process.env.VAPOROS_KEY_FILE || '../keys/release.pub');
if (!existsSync(keySrc)) fail(`${keySrc} is missing (build from website/ in the repo, or set VAPOROS_KEY_FILE)`);
copyFileSync(keySrc, resolve(site, 'public/release.pub'));

// ---------------------------------------------------------------- fonts
const manifestPath = resolve(repo, 'design/fonts/fonts.json');
if (!existsSync(manifestPath)) fail(`${rel(manifestPath)} is missing`);
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));

/** @type {{ from: string, to: string, bytes: number, sha256: string }[]} */
const fonts = [];
const fontDir = resolve(site, 'src/fonts');
const ttfDir = resolve(fontDir, 'ttf');

// The site's variable Anybody: it travels between the three cuts.
for (const f of manifest.site ?? []) {
  const from = resolve(repo, f.out);
  fonts.push({ from, to: resolve(fontDir, basename(from)), ...f.result });
}
// The static body and mono faces the control center ships (the display cuts
// come from the variable file above), and every TV face for the OG renderer.
for (const f of manifest.faces ?? []) {
  if (f.role !== 'display' && f.web) {
    const from = resolve(repo, f.web);
    fonts.push({ from, to: resolve(fontDir, basename(from)), ...f.result.web });
  }
  if (f.tv) {
    const from = resolve(repo, f.tv);
    fonts.push({ from, to: resolve(ttfDir, basename(from)), ...f.result.tv });
  }
}
if (!fonts.some((f) => f.to.endsWith('anybody-variable.woff2'))) fail(`${rel(manifestPath)} lists no site font`);

mkdirSync(ttfDir, { recursive: true });
for (const f of fonts) {
  if (!existsSync(f.from)) fail(`${rel(f.from)} is missing (python3 tools/fonts/fonts.py builds it)`);
  const data = readFileSync(f.from);
  const sum = createHash('sha256').update(data).digest('hex');
  if (data.length !== f.bytes || sum !== f.sha256) {
    fail(`${rel(f.from)} is ${data.length} bytes, sha256 ${sum.slice(0, 12)}…; fonts.json records ${f.bytes} bytes, ${String(f.sha256).slice(0, 12)}… (run python3 tools/fonts/fonts.py --check)`);
  }
  copyFileSync(f.from, f.to);
}

console.log(`copy-assets: public/release.pub from ${rel(keySrc)}; ${fonts.length} fonts into src/fonts (checked against fonts.json)`);
