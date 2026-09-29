#!/usr/bin/env node
// The site's size budgets (spec-website §12), checked on a static export.
// No dependencies; exit 1 when a budget is broken.
//
//   node scripts/budget.mjs [outDir=out] [--live=<baseUrl>] [--json=<file>]
//
// Static part (always), for every out/**/*.html except demo/ui/** and
// Next's duplicate _not-found/ and 404/ copies:
//   js      gzip (level 9) of the same-origin <script src> the HTML loads
//           (noModule polyfills excluded: modern browsers never fetch them)
//   css     gzip of its stylesheets
//   html    gzip of the HTML itself, inline RSC payload included
//   fonts   the fonts it preloads: at most 2, at most 110 kB together
// and for the export as a whole:
//   og      every og.png (the share cards): at most 300 kB each
//   tv      every tv/* still: at most 150 kB each
//   images  any other image over 150 kB fails
//
// --live=<baseUrl> (a served export, scripts/serve.mjs) also loads each page
// headless at 1440×900 and 390×844 and checks, from the network log:
//   js5s    gzip of every same-origin script fetched by 5 s after `load`
//   img     images fetched on first view (no scrolling)
import { gzipSync } from 'node:zlib';
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { extname, join, relative, resolve } from 'node:path';

const argv = process.argv.slice(2);
const opt = Object.fromEntries(
  argv.filter((a) => a.startsWith('--')).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.slice(2), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
const OUT = resolve(argv.find((a) => !a.startsWith('--')) ?? 'out');
const BASE = '/vaporos';
if (!existsSync(join(OUT, 'index.html'))) {
  console.error(`budget: ${OUT} holds no index.html (build first: npm run build)`);
  process.exit(2);
}

const kB = 1000;
// Per page kind: the §12 table. 'demo' is the /demo/ page, once it exists.
const BUDGET = {
  home: { js: 205 * kB, js5s: 290 * kB, css: 25 * kB, html: 40 * kB, img: 120 * kB },
  content: { js: 195 * kB, js5s: 215 * kB, css: 25 * kB, html: 25 * kB, img: 60 * kB },
  demo: { js: 200 * kB, js5s: 215 * kB, css: 25 * kB, html: 15 * kB, img: 60 * kB },
  notFound: { js: 190 * kB, js5s: 200 * kB, css: 25 * kB, html: 10 * kB, img: 60 * kB },
};
const FONTS = { count: 2, bytes: 110 * kB };
const OG_MAX = 300 * kB;
const IMAGE_MAX = 150 * kB;

const kindOf = (page) => (page === '/' ? 'home' : page === '/404.html' ? 'notFound' : page === '/demo/' ? 'demo' : 'content');
const fmt = (n) => `${(n / kB).toFixed(1)} kB`;
const gz = (buf) => gzipSync(buf, { level: 9 }).length;
const gzCache = new Map();
function gzFile(file) {
  if (!gzCache.has(file)) gzCache.set(file, gz(readFileSync(file)));
  return gzCache.get(file);
}

function walk(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}
const files = walk(OUT);
const rel = (f) => `/${relative(OUT, f).split('\\').join('/')}`;
// '/vaporos/_next/…' → out/_next/…; anything else (external, data:) → null.
function local(url) {
  if (!url.startsWith(`${BASE}/`)) return null;
  const f = join(OUT, decodeURIComponent(url.slice(BASE.length).split(/[?#]/)[0]));
  return existsSync(f) && statSync(f).isFile() ? f : null;
}
const attr = (tag, name) => new RegExp(`\\s${name}="([^"]*)"`, 'i').exec(tag)?.[1];

let failures = 0;
const rows = [];
const report = { out: OUT, pages: [], assets: [], live: [] };
function check(scope, what, value, max, detail = '') {
  const ok = value <= max;
  if (!ok) failures++;
  rows.push(`${ok ? 'PASS' : 'FAIL'}  ${scope.padEnd(12)} ${what.padEnd(8)} ${fmt(value).padStart(9)} / ${fmt(max).padStart(9)}${detail ? `  ${detail}` : ''}`);
  return ok;
}

// ---------------------------------------------------------------- pages
const pages = files
  .filter((f) => f.endsWith('.html'))
  .map(rel)
  .filter((p) => !p.startsWith('/demo/ui/') && !p.startsWith('/_not-found/') && !p.startsWith('/404/'))
  .map((p) => (p.endsWith('/index.html') ? p.slice(0, -'index.html'.length) : p))
  .sort();

for (const page of pages) {
  const file = join(OUT, page.endsWith('/') ? `${page}index.html` : page);
  const html = readFileSync(file, 'utf8');
  const b = BUDGET[kindOf(page)];
  const scripts = new Set();
  const styles = new Set();
  const fonts = new Set();
  const missing = [];
  for (const [tag] of html.matchAll(/<script\b[^>]*>/gi)) {
    const src = attr(tag, 'src');
    if (!src || /\snomodule(=|\s|>)/i.test(tag)) continue;
    const f = local(src);
    if (f) scripts.add(f);
    else if (src.startsWith('/')) missing.push(src);
  }
  for (const [tag] of html.matchAll(/<link\b[^>]*>/gi)) {
    const relAttr = (attr(tag, 'rel') ?? '').toLowerCase();
    const href = attr(tag, 'href');
    if (!href) continue;
    const f = local(href);
    if (relAttr === 'stylesheet') {
      if (f) styles.add(f);
      else if (href.startsWith('/')) missing.push(href);
    } else if (relAttr === 'preload' && attr(tag, 'as') === 'font') {
      if (f) fonts.add(f);
      else if (href.startsWith('/')) missing.push(href);
    } else if (relAttr === 'preload' && attr(tag, 'as') === 'script' && f) {
      scripts.add(f);
    }
  }
  const sum = (set) => [...set].reduce((n, f) => n + gzFile(f), 0);
  const entry = {
    page,
    kind: kindOf(page),
    js: sum(scripts),
    css: sum(styles),
    html: gz(Buffer.from(html)),
    fonts: [...fonts].map((f) => ({ file: rel(f), bytes: statSync(f).size })),
    missing,
  };
  report.pages.push(entry);
  check(page, 'js', entry.js, b.js, `${scripts.size} scripts`);
  check(page, 'css', entry.css, b.css, `${styles.size} files`);
  check(page, 'html', entry.html, b.html);
  const fontBytes = entry.fonts.reduce((n, f) => n + f.bytes, 0);
  check(page, 'fonts', fontBytes, FONTS.bytes, `${entry.fonts.length} preloaded${entry.fonts.length > FONTS.count ? ` (FAIL: more than ${FONTS.count})` : ''}`);
  if (entry.fonts.length > FONTS.count) failures++;
  if (missing.length) {
    failures++;
    rows.push(`FAIL  ${page.padEnd(12)} missing  ${missing.join(' ')}`);
  }
}

// ---------------------------------------------------------------- assets
const IMG = new Set(['.png', '.jpg', '.jpeg', '.webp', '.avif', '.gif', '.svg']);
for (const f of files) {
  const p = rel(f);
  if (p.startsWith('/demo/ui/') || !IMG.has(extname(f).toLowerCase())) continue;
  const size = statSync(f).size;
  if (/\/og\.png$/.test(p)) {
    report.assets.push({ file: p, bytes: size });
    check(p, 'og', size, OG_MAX);
  } else if (p.startsWith('/tv/')) {
    report.assets.push({ file: p, bytes: size });
    check(p, 'tv', size, IMAGE_MAX);
  } else if (size > IMAGE_MAX) {
    report.assets.push({ file: p, bytes: size });
    check(p, 'image', size, IMAGE_MAX);
  }
}

// ---------------------------------------------------------------- live
if (opt.live) {
  const { chromium } = await import('playwright-core');
  const { headlessShell } = await import('./chrome.mjs');
  const base = String(opt.live).endsWith('/') ? String(opt.live) : `${opt.live}/`;
  const origin = new URL(base).origin;
  const browser = await chromium.launch({ executablePath: headlessShell(), headless: true, args: ['--hide-scrollbars'] });
  try {
    for (const [width, height] of [
      [1440, 900],
      [390, 844],
    ]) {
      const phone = width < 600;
      const ctx = await browser.newContext({ viewport: { width, height }, isMobile: phone, hasTouch: phone });
      // The download card's release lookup must not reach GitHub (rate limits, determinism).
      await ctx.route('https://api.github.com/**', (r) => r.fulfill({ status: 404, contentType: 'application/json', body: '{"message":"Not Found"}' }));
      for (const page of pages.filter((p) => p !== '/404.html')) {
        const p = await ctx.newPage();
        const got = [];
        let loaded = Infinity;
        p.on('response', async (res) => {
          const url = res.url();
          if (!url.startsWith(origin)) return;
          const type = res.request().resourceType();
          if (type !== 'script' && type !== 'image') return;
          const at = Date.now();
          try {
            const body = await res.body();
            got.push({ url, type, at, gz: type === 'script' ? gz(body) : body.length });
          } catch {}
        });
        await p.goto(new URL(page.replace(/^\//, ''), base).href, { waitUntil: 'load' });
        loaded = Date.now();
        await p.waitForTimeout(5000);
        await p.close();
        const b = BUDGET[kindOf(page)];
        const js = got.filter((g) => g.type === 'script' && g.at <= loaded + 5000).reduce((n, g) => n + g.gz, 0);
        const img = got.filter((g) => g.type === 'image').reduce((n, g) => n + g.gz, 0);
        report.live.push({ page, width, js5s: js, img, requests: got.map((g) => ({ url: g.url.slice(origin.length), type: g.type, bytes: g.gz })) });
        check(`${page}@${width}`, 'js5s', js, b.js5s);
        check(`${page}@${width}`, 'img', img, b.img);
      }
      await ctx.close();
    }
  } finally {
    await browser.close();
  }
}

console.log(rows.join('\n'));
if (opt.json) writeFileSync(resolve(String(opt.json)), `${JSON.stringify(report, null, 2)}\n`);
console.log(failures ? `\n${failures} BUDGET(S) BROKEN` : `\nALL BUDGETS MET (${pages.length} pages)`);
process.exit(failures ? 1 : 0);
