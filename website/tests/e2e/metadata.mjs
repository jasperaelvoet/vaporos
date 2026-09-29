#!/usr/bin/env node
// Metadata audit of an exported build (the fixing-metadata skill's rules as
// checks; no browser): per page one title, description, canonical and robots;
// Open Graph and Twitter cards complete, absolute and agreeing with the
// canonical; share images that exist at the size they claim; icons, theme
// colour, viewport and lang; JSON-LD that parses and invents nothing; titles
// and descriptions unique; the sitemap listing exactly the indexable pages.
//
//   node tests/e2e/metadata.mjs <outDir> [--commit=<sha>]
//
// --commit checks <meta name="vaporos-commit"> (pages.yml builds with
// VAPOROS_SITE_COMMIT).
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';

const argv = process.argv.slice(2);
const opt = Object.fromEntries(
  argv.filter((a) => a.startsWith('--')).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.slice(2), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
const OUT = resolve(argv.find((a) => !a.startsWith('--')) ?? 'out');
const SITE = 'https://jasperaelvoet.github.io/vaporos/';
const BASE = '/vaporos';
if (!existsSync(join(OUT, 'index.html'))) {
  console.error(`metadata: ${OUT} holds no index.html`);
  process.exit(2);
}

let failures = 0;
let warnings = 0;
const check = (name, cond, detail = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${!cond && detail ? `  (${detail})` : ''}`);
  if (!cond) failures++;
  return cond;
};
const warn = (name, detail) => {
  console.log(`WARN  ${name}  (${detail})`);
  warnings++;
};

// ---------------------------------------------------------------- parsing
const decode = (s) =>
  s
    .replace(/&#x([0-9a-f]+);/gi, (_, h) => String.fromCodePoint(parseInt(h, 16)))
    .replace(/&#(\d+);/g, (_, d) => String.fromCodePoint(Number(d)))
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&amp;/g, '&');
const attrs = (tag) => Object.fromEntries([...tag.matchAll(/\s([a-zA-Z:-]+)(?:="([^"]*)")?/g)].map((m) => [m[1].toLowerCase(), decode(m[2] ?? '')]));
function head(html) {
  const h = /<head>([\s\S]*?)<\/head>/i.exec(html)?.[1] ?? '';
  const metas = [...h.matchAll(/<meta\b[^>]*>/gi)].map((m) => attrs(m[0]));
  const links = [...h.matchAll(/<link\b[^>]*>/gi)].map((m) => attrs(m[0]));
  const titles = [...h.matchAll(/<title>([\s\S]*?)<\/title>/gi)].map((m) => decode(m[1]));
  const meta = (key, val) => metas.filter((m) => m[key] === val).map((m) => m.content ?? '');
  const ld = [...html.matchAll(/<script type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/gi)].map((m) => m[1]);
  return { metas, links, titles, meta, ld, lang: /<html[^>]*\slang="([^"]*)"/i.exec(html)?.[1] };
}
function png(file) {
  const b = readFileSync(file);
  const sig = b.subarray(0, 8).toString('hex') === '89504e470d0a1a0a';
  return sig ? { width: b.readUInt32BE(16), height: b.readUInt32BE(20), bytes: b.length } : null;
}
// An absolute site URL → the file in the export (or null).
function fileOf(url) {
  if (!url.startsWith(SITE) && !url.startsWith(`${BASE}/`)) return null;
  const path = url.startsWith(SITE) ? url.slice(SITE.length - 1) : url.slice(BASE.length);
  const clean = decodeURIComponent(path.split(/[?#]/)[0]);
  const f = join(OUT, clean.endsWith('/') ? `${clean}index.html` : clean);
  return existsSync(f) ? f : null;
}
function htmlFiles(dir) {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...htmlFiles(p));
    else if (e.name.endsWith('.html')) out.push(p);
  }
  return out;
}

// Pages: every exported page except the live demo's generated control center
// and Next's duplicate 404 copies (_not-found/, 404/).
const pages = htmlFiles(OUT)
  .map((f) => `/${relative(OUT, f).split('\\').join('/')}`)
  .filter((p) => !p.startsWith('/demo/ui/') && !p.startsWith('/_not-found/') && !p.startsWith('/404/'))
  .map((p) => (p.endsWith('/index.html') ? p.slice(0, -'index.html'.length) : p))
  .sort();

const MARKUP = /\*\*|\[\[|\]\]|`|\]\(|\+\+/;
const seenTitles = new Map();
const seenDescriptions = new Map();
const indexable = [];

for (const page of pages) {
  const file = join(OUT, page.endsWith('/') ? `${page}index.html` : page);
  const html = readFileSync(file, 'utf8');
  const h = head(html);
  const is404 = page === '/404.html';
  const p = `${page}:`;
  const one = (label, values) => check(`${p} exactly one ${label}`, values.length === 1, `${values.length} found`) && values[0];

  check(`${p} <html lang="en">`, h.lang === 'en', h.lang);
  check(`${p} viewport meta`, h.meta('name', 'viewport').length === 1);
  check(`${p} theme-color meta`, h.meta('name', 'theme-color').length >= 1 && h.meta('name', 'theme-color').every((c) => /^#[0-9a-f]{6}$/i.test(c)), h.meta('name', 'theme-color').join(' '));
  const charset = h.metas.filter((m) => 'charset' in m).length;
  check(`${p} one charset meta`, charset === 1, `${charset}`);

  const title = one('<title>', h.titles);
  if (title) {
    check(`${p} title has no markup`, !MARKUP.test(title), title);
    if (title.length > 70) warn(`${p} title length`, `${title.length} chars: ${title}`);
    if (!is404) seenTitles.set(title, [...(seenTitles.get(title) ?? []), page]);
  }
  const description = one('description', h.meta('name', 'description'));
  if (description) {
    check(`${p} description is plain text`, !MARKUP.test(description), description);
    if (!is404 && (description.length < 50 || description.length > 170)) warn(`${p} description length`, `${description.length} chars`);
    if (!is404) seenDescriptions.set(description, [...(seenDescriptions.get(description) ?? []), page]);
  }

  const robots = h.meta('name', 'robots');
  check(`${p} at most one robots meta`, robots.length <= 1, robots.join(' | '));
  const canonical = h.links.filter((l) => l.rel === 'canonical').map((l) => l.href);
  const ogUrl = h.meta('property', 'og:url');
  if (is404) {
    check(`${p} the 404 is noindex`, /noindex/.test(robots[0] ?? ''), robots[0]);
    check(`${p} the 404 has no canonical or og:url`, !canonical.length && !ogUrl.length, `${canonical} ${ogUrl}`);
  } else {
    indexable.push(page);
    check(`${p} indexable (no noindex)`, !robots.some((r) => /noindex/.test(r)), robots.join(' '));
    const c = one('canonical', canonical);
    if (c) check(`${p} canonical is ${SITE}${page.slice(1)}`, c === SITE + page.slice(1), c);
    const u = one('og:url', ogUrl);
    if (c && u) check(`${p} og:url equals the canonical`, u === c, `${u} vs ${c}`);
  }

  // Open Graph
  const ogTitle = one('og:title', h.meta('property', 'og:title'));
  if (ogTitle && title) check(`${p} og:title equals <title>`, ogTitle === title, `${ogTitle} vs ${title}`);
  const ogDesc = one('og:description', h.meta('property', 'og:description'));
  if (ogDesc && description) check(`${p} og:description equals the description`, ogDesc === description);
  const ogType = one('og:type', h.meta('property', 'og:type'));
  if (ogType) check(`${p} og:type is website`, ogType === 'website', ogType);
  one('og:site_name', h.meta('property', 'og:site_name'));
  const images = h.meta('property', 'og:image');
  const img = one('og:image', images);
  if (img) {
    check(`${p} og:image is absolute under ${SITE}`, img.startsWith(SITE), img);
    const f = fileOf(img);
    const info = f && png(f);
    if (check(`${p} og:image exists in the export as a PNG`, !!info, img)) {
      const w = Number(h.meta('property', 'og:image:width')[0]);
      const hh = Number(h.meta('property', 'og:image:height')[0]);
      check(`${p} og:image is 1200×630 as declared`, info.width === 1200 && info.height === 630 && w === 1200 && hh === 630, `file ${info.width}×${info.height}, meta ${w}×${hh}`);
      check(`${p} og:image under 1 MB`, info.bytes < 1e6, `${info.bytes} B`);
    }
    const alt = h.meta('property', 'og:image:alt');
    check(`${p} og:image:alt present`, alt.length === 1 && alt[0].length > 0);
  }

  // Twitter
  const card = one('twitter:card', h.meta('name', 'twitter:card'));
  if (card) check(`${p} twitter:card is summary_large_image`, card === 'summary_large_image', card);
  const tImg = one('twitter:image', h.meta('name', 'twitter:image'));
  if (tImg) {
    check(`${p} twitter:image is absolute and exists`, tImg.startsWith(SITE) && !!fileOf(tImg), tImg);
    if (img) check(`${p} twitter:image equals og:image`, tImg === img, `${tImg} vs ${img}`);
  }
  const tTitle = one('twitter:title', h.meta('name', 'twitter:title'));
  if (tTitle && title) check(`${p} twitter:title equals <title>`, tTitle === title);
  one('twitter:description', h.meta('name', 'twitter:description'));

  // Icons
  const icons = h.links.filter((l) => /(^|\s)icon(\s|$)/.test(l.rel ?? ''));
  check(`${p} at least one icon`, icons.length > 0);
  for (const l of [...icons, ...h.links.filter((l) => l.rel === 'apple-touch-icon' || l.rel === 'manifest')]) {
    check(`${p} ${l.rel} ${l.href} is under ${BASE} and exists`, (l.href ?? '').startsWith(`${BASE}/`) && !!fileOf(l.href), l.href);
  }
  const apple = h.links.find((l) => l.rel === 'apple-touch-icon');
  if (check(`${p} apple-touch-icon`, !!apple) && fileOf(apple.href)) {
    const info = png(fileOf(apple.href));
    check(`${p} apple-touch-icon is a 180×180 PNG`, info?.width === 180 && info?.height === 180, info ? `${info.width}×${info.height}` : 'not a PNG');
  }
  const manifest = h.links.find((l) => l.rel === 'manifest');
  if (manifest && fileOf(manifest.href)) {
    let m = null;
    try {
      m = JSON.parse(readFileSync(fileOf(manifest.href), 'utf8'));
    } catch {}
    check(`${p} the manifest parses and names the site`, !!m?.name && Array.isArray(m.icons));
  }

  // JSON-LD: valid, schema.org, nothing invented.
  for (const [i, raw] of h.ld.entries()) {
    let ld = null;
    try {
      ld = JSON.parse(raw);
    } catch {}
    if (!check(`${p} JSON-LD #${i + 1} parses`, !!ld)) continue;
    const text = JSON.stringify(ld);
    check(`${p} JSON-LD #${i + 1} is schema.org`, /schema\.org/.test(String(ld['@context'] ?? '')), String(ld['@context']));
    check(`${p} JSON-LD #${i + 1} invents no rating, review or price`, !/aggregateRating|"review"|"price"|"offers"/i.test(text));
    for (const [, u] of text.matchAll(/"(https:\/\/jasperaelvoet\.github\.io\/vaporos\/[^"]*)"/g)) {
      if (/\.(png|svg|html)$|\/$/.test(u)) check(`${p} JSON-LD URL ${u} exists`, !!fileOf(u) || u === SITE);
    }
  }

  if (opt.commit) {
    const c = h.meta('name', 'vaporos-commit');
    check(`${p} vaporos-commit is ${opt.commit}`, c.length === 1 && c[0] === opt.commit, c.join(' '));
  }
}

for (const [t, where] of seenTitles) check(`title unique: ${t}`, where.length === 1, where.join(' '));
for (const where of seenDescriptions.values()) check(`description unique on ${where[0]}`, where.length === 1, where.join(' '));

const sitemap = existsSync(join(OUT, 'sitemap.xml')) ? readFileSync(join(OUT, 'sitemap.xml'), 'utf8') : '';
const locs = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1]).sort();
const want = indexable.map((p) => SITE + p.slice(1)).sort();
check('sitemap lists exactly the indexable pages', JSON.stringify(locs) === JSON.stringify(want), `sitemap ${locs.join(' ')} | pages ${want.join(' ')}`);

console.log(failures ? `\n${failures} METADATA PROBLEM(S)${warnings ? `, ${warnings} warning(s)` : ''}` : `\nMETADATA CLEAN (${pages.length} pages${warnings ? `, ${warnings} warning(s)` : ''})`);
process.exit(failures ? 1 : 0);
