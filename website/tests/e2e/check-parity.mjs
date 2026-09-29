#!/usr/bin/env node
// Text parity with the Astro site this one replaced: every visible line of
// the fact-checked Astro pages must still exist on the matching Next page, or
// at least in src/content. Lines that a deliberate fact fix replaced are
// listed in tests/e2e/parity-allow.json with the reason; they are reported,
// not counted. Also lists Next-only lines, to review for invented claims.
//
//   node tests/e2e/check-parity.mjs <nextUrl> <contentDir> [astroUrl=http://127.0.0.1:4331/vaporos/] [--allow=tests/e2e/parity-allow.json]
//
// Serve the archived Astro build first (scripts/serve.mjs <astro-dist> 4331).
// The GitHub API is stubbed with an empty list, so both download cards keep
// what their build baked in.
import { readFileSync, readdirSync } from 'node:fs';
import { chromium } from 'playwright-core';
import { headlessShell } from '../../scripts/chrome.mjs';

const args = process.argv.slice(2);
const flag = (k) => args.find((a) => a.startsWith(`--${k}=`))?.slice(k.length + 3);
const [NEXT, dir, ASTRO = 'http://127.0.0.1:4331/vaporos/'] = args.filter((a) => !a.startsWith('--'));
if (!NEXT || !dir) {
  console.error('usage: node tests/e2e/check-parity.mjs <nextUrl> <contentDir> [astroUrl] [--allow=tests/e2e/parity-allow.json]');
  process.exit(2);
}
const norm = (s) => s.replace(/\s+/g, ' ').replace(/ ([.,;:)])/g, '$1').trim();
const allow = JSON.parse(readFileSync(flag('allow') ?? new URL('./parity-allow.json', import.meta.url), 'utf8'));
const allowed = new Map(allow.lines.map((l) => [norm(l.line).toLowerCase(), l.why]));
const usedAllow = new Set();

const b = await chromium.launch({ executablePath: headlessShell(), headless: true });
const ctx = await b.newContext({ viewport: { width: 1440, height: 900 } });
await ctx.route('https://api.github.com/**', (r) =>
  r.fulfill({ status: 200, headers: { 'access-control-allow-origin': '*', 'content-type': 'application/json' }, body: '[]' }),
);
async function lines(url) {
  const p = await ctx.newPage();
  await p.goto(url, { waitUntil: 'load' });
  await p.evaluate(() => {
    document.querySelectorAll('details').forEach((d) => (d.open = true));
    document.querySelectorAll('.sr-only').forEach((e) => {
      e.style.cssText = 'position:static;width:auto;height:auto;clip:auto;overflow:visible;white-space:normal';
      e.className = '';
    });
  });
  const t = await p.evaluate(() => document.body.innerText);
  await p.close();
  return t.split('\n').map(norm).filter(Boolean);
}
// Every string in src/content, markup stripped: where copy may live without being rendered on a page.
const slash = (u) => (u.endsWith('/') ? u : `${u}/`);
const contentText = norm(
  readdirSync(dir)
    .map((f) => readFileSync(`${dir}/${f}`, 'utf8'))
    .join('\n')
    .replace(/\\'/g, "'")
    .replace(/\*\*|`|\+\+/g, '')
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1'),
).toLowerCase();
let missing = 0;
let changed = 0;
try {
  for (const path of ['', 'download/', 'install/', 'faq/', 'nope/']) {
    const a = await lines(`${slash(ASTRO)}${path}`);
    const n = await lines(`${slash(NEXT)}${path}`);
    const nText = n.join(' \n ').toLowerCase();
    const notOnPage = a.filter((l) => !nText.includes(l.toLowerCase()));
    const inContentOnly = notOnPage.filter((l) => contentText.includes(l.toLowerCase()));
    const gone = notOnPage.filter((l) => !contentText.includes(l.toLowerCase()));
    const deliberate = gone.filter((l) => allowed.has(l.toLowerCase()));
    const notAnywhere = gone.filter((l) => !allowed.has(l.toLowerCase()));
    deliberate.forEach((l) => usedAllow.add(l.toLowerCase()));
    const extra = n.filter((l) => !a.join(' \n ').toLowerCase().includes(l.toLowerCase()));
    const list = (xs) => (xs.length ? `\n     ${xs.join('\n     ')}` : '');
    console.log(`\n== /${path}  astro lines ${a.length}, next lines ${n.length}`);
    console.log(`   astro lines not on the next page but in src/content: ${inContentOnly.length}${list(inContentOnly)}`);
    console.log(`   astro lines replaced by a deliberate fact fix: ${deliberate.length}${list(deliberate.map((l) => `${l}\n       ↳ ${allowed.get(l.toLowerCase())}`))}`);
    console.log(`   astro lines MISSING everywhere: ${notAnywhere.length}${list(notAnywhere)}`);
    console.log(`   next lines not in astro: ${extra.length}${list(extra)}`);
    missing += notAnywhere.length;
    changed += deliberate.length;
  }
} finally {
  await b.close();
}
const unused = [...allowed.keys()].filter((k) => !usedAllow.has(k));
if (unused.length) console.log(`\nparity-allow.json entries no Astro line needed (stale?): ${unused.length}\n     ${unused.join('\n     ')}`);
console.log(missing ? `\n${missing} astro lines missing` : `\nEVERY ASTRO LINE IS PORTED${changed ? ` (${changed} replaced by deliberate fact fixes, see parity-allow.json)` : ''}`);
process.exit(missing ? 1 : 0);
