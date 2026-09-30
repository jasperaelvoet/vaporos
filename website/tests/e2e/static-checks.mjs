#!/usr/bin/env node
// Static assertions on exported builds of the site (no browser): the
// download card's state per build, the ISO name, the release key, the files
// GitHub Pages needs, every root-relative URL under /vaporos, and absolute
// canonical, og:url, og:image and sitemap URLs.
//
//   node tests/e2e/static-checks.mjs --fixture=<out> [--none=<out>] [--unknown=<out>] [--live=<out>]
//        [--release=tests/fixtures/release-latest.json] [--key=../keys/release.pub]
//
// fixture: built with VAPOROS_RELEASE_FIXTURE=<--release>; none: with /dev/null;
// unknown: with a failing answer; live: from the real API (ready or none).
// At least one of --fixture and --live (CI checks its one real build with --live).
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';

const opt = Object.fromEntries(
  process.argv.slice(2).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.replace(/^--/, ''), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
if (!opt.fixture && !opt.live) {
  console.error('usage: node tests/e2e/static-checks.mjs --fixture=<out> [--none=<out>] [--unknown=<out>] [--live=<out>] [--release=<json>] [--key=<release.pub>]');
  process.exit(2);
}
const SITE = 'https://jasperaelvoet.github.io/vaporos/';
const BASE = '/vaporos';
const release = opt.fixture ? JSON.parse(readFileSync(resolve(opt.release ?? 'tests/fixtures/release-latest.json'), 'utf8')) : null;
const ISO = release?.assets.find((a) => a.name.endsWith('.iso')).name;
const KEY = resolve(opt.key ?? '../keys/release.pub');

let failures = 0;
const check = (name, cond, detail = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${detail ? `  (${detail})` : ''}`);
  if (!cond) failures++;
};

function htmlFiles(dir) {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...htmlFiles(p));
    else if (e.name.endsWith('.html')) out.push(p);
  }
  return out;
}
const read = (p) => readFileSync(p, 'utf8');
const states = (html) => [...html.matchAll(/data-state="([a-z]+)"/g)].map((m) => m[1]);
const isoLinks = (dir) => htmlFiles(dir).filter((f) => /href="[^"]+\.iso"/.test(read(f)));

// Every build: pages exist, URLs stay under the base path, metadata is absolute.
function common(name, dir) {
  const pages = ['index.html', 'download/index.html', 'install/index.html', 'faq/index.html', '404.html'];
  const missing = pages.filter((p) => !existsSync(join(dir, p)));
  check(`${name}: every page exported`, !missing.length, missing.join(' '));
  check(`${name}: .nojekyll and 404.html for GitHub Pages`, existsSync(join(dir, '.nojekyll')) && existsSync(join(dir, '404.html')));
  const outside = [];
  for (const f of htmlFiles(dir)) {
    for (const m of read(f).matchAll(/\s(?:href|src|action|poster)="(\/[^"]*)"/g)) {
      if (m[1] !== BASE && !m[1].startsWith(`${BASE}/`)) outside.push(`${relative(dir, f)}: ${m[1]}`);
    }
  }
  check(`${name}: every root-relative href/src is under ${BASE}`, !outside.length, outside.slice(0, 5).join(', '));
  for (const [page, path] of [['index.html', ''], ['download/index.html', 'download/'], ['install/index.html', 'install/'], ['faq/index.html', 'faq/']]) {
    if (!existsSync(join(dir, page))) continue;
    const h = read(join(dir, page));
    const canonical = /<link rel="canonical" href="([^"]+)"/.exec(h)?.[1];
    const ogUrl = /<meta property="og:url" content="([^"]+)"/.exec(h)?.[1];
    const ogImage = /<meta property="og:image" content="([^"]+)"/.exec(h)?.[1];
    check(
      `${name}: /${path} canonical, og:url and og:image absolute`,
      canonical === SITE + path && ogUrl === SITE + path && !!ogImage?.startsWith(SITE),
      `${canonical} ${ogUrl} ${ogImage}`,
    );
  }
  const nf = read(join(dir, '404.html'));
  check(`${name}: the 404 page is noindex without a canonical`, /<meta name="robots" content="noindex/.test(nf) && !/rel="canonical"/.test(nf));
  const sitemap = existsSync(join(dir, 'sitemap.xml')) ? read(join(dir, 'sitemap.xml')) : '';
  const locs = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1]);
  // The live demo's page joins them while src/content/demo.ts has it on (scripts/drop-demo.mjs removes it otherwise).
  const demoPage = existsSync(join(dir, 'demo/index.html'));
  check(
    `${name}: sitemap lists the ${demoPage ? 'five pages (with /demo/)' : 'four pages'}, absolute`,
    locs.length === (demoPage ? 5 : 4) && locs.every((l) => l.startsWith(SITE)) && locs.includes(`${SITE}demo/`) === demoPage,
    locs.join(' '),
  );
  check(`${name}: release.pub is keys/release.pub`, existsSync(join(dir, 'release.pub')) && read(join(dir, 'release.pub')) === read(KEY));
}

if (opt.fixture) {
  const fixture = resolve(opt.fixture);
  common('fixture', fixture);
  const dl = read(join(fixture, 'download/index.html'));
  const home = read(join(fixture, 'index.html'));
  check('fixture: download and home cards are ready', states(dl).includes('ready') && states(home).includes('ready'), `${states(dl)} ${states(home)}`);
  check(`fixture: the download page links ${ISO}`, dl.includes(`/${ISO}"`), ISO);
}
if (opt.none) {
  const dir = resolve(opt.none);
  common('none', dir);
  check('none: download and home cards are none', states(read(join(dir, 'download/index.html'))).includes('none') && states(read(join(dir, 'index.html'))).includes('none'));
  const iso = isoLinks(dir);
  check('none: no page links an ISO', !iso.length, iso.map((f) => relative(dir, f)).join(' '));
}
if (opt.unknown) {
  const dir = resolve(opt.unknown);
  common('unknown', dir);
  const dl = read(join(dir, 'download/index.html'));
  check('unknown: the download card is unknown', states(dl).includes('unknown'));
  check('unknown: it links GitHub\'s latest release', dl.includes('href="https://github.com/jasperaelvoet/vaporos/releases/latest"'));
  check("unknown: it never says VaporOS hasn't published a release", !/hasn(&#x27;|')t published a release/.test(dl));
  const iso = isoLinks(dir);
  check('unknown: no page links an ISO', !iso.length, iso.map((f) => relative(dir, f)).join(' '));
}
if (opt.live) {
  const dir = resolve(opt.live);
  common('live', dir);
  const s = states(read(join(dir, 'download/index.html')));
  check('live: the card is ready or none (the lookup worked)', s.includes('ready') || s.includes('none'), s.join(' '));
  if (s.includes('ready')) {
    const iso = /href="(https:\/\/github\.com\/jasperaelvoet\/vaporos\/releases\/download\/[^"]+\/vaporos-[0-9.]+\.iso)"/.exec(read(join(dir, 'download/index.html')))?.[1];
    check('live: the download page links the release ISO on GitHub', !!iso, iso);
  }
}

console.log(failures ? `\n${failures} FAILED` : '\nALL STATIC CHECKS PASSED');
process.exit(failures ? 1 : 0);
