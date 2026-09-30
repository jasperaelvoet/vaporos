#!/usr/bin/env node
// The website's copy against the control center and against itself (npm test):
//
//   labels   every [[UI label]] in src/content names something the control
//            center really shows, word for word: it must be one of the
//            strings in tests/fixtures/ui-strings.<set>.json, where <set> is
//            content/demo.ts's uiLabels ('legacy' until train 3). The file is
//            generated (gitignored): when it is missing this check runs
//              VOS_WEB_EXPORT_STRINGS=$PWD/tests/fixtures go test ./internal/web -run TestExportStrings
//            from the repo root (CI runs that step itself first).
//   markup   the [[…]] markup parses as a UI label, never as a link, and
//            plain() keeps its words.
//   content  no banned names ("WaterVaporOS", "open source"); every site
//            href in the content is a route the site builds or a file in
//            public/; the home page's FAQ teaser names real questions.
//
//   node --conditions=react-server tests/unit/check-labels.mjs
import { spawnSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { registerTsx } from './tsx-hooks.mjs';

registerTsx();
const SITE = fileURLToPath(new URL('../../', import.meta.url));
const REPO = resolve(SITE, '..');
const CONTENT = join(SITE, 'src/content');

let failures = 0;
const check = (name, cond, detail = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${!cond && detail ? `  (${detail})` : ''}`);
  if (!cond) failures++;
  return cond;
};

// ---------------------------------------------------------------- content
// Every string in every content module, with where it sits (walking the
// exported values, so comments and types never count).
const modules = readdirSync(CONTENT).filter((f) => f.endsWith('.ts') && f !== 'types.ts' && f !== 'index.ts');
const strings = [];
const exportsOf = {};
for (const f of modules) {
  const mod = await import(pathToFileURL(join(CONTENT, f)).href);
  exportsOf[f.replace(/\.ts$/, '')] = mod;
  const seen = new Set();
  const walk = (v, at) => {
    if (typeof v === 'string') strings.push({ at, text: v });
    else if (v && typeof v === 'object' && !seen.has(v)) {
      seen.add(v);
      for (const [k, x] of Object.entries(v)) walk(x, `${at}.${k}`);
    }
  };
  for (const [k, v] of Object.entries(mod)) if (typeof v !== 'function') walk(v, `${f}:${k}`);
}
check(`content: read ${modules.length} modules`, strings.length > 100, `${strings.length} strings`);

// ---------------------------------------------------------------- markup
const { parseRich, plain } = await import(pathToFileURL(join(SITE, 'src/lib/rich-text.ts')).href);
{
  const n = parseRich('Open [[Updates]] and [the guide](/install/).');
  check('markup: [[Updates]] is a UI label', n.some((x) => x.type === 'ui' && x.text === 'Updates'));
  check('markup: a link next to a label stays a link', n.some((x) => x.type === 'link' && x.href === '/install/'));
  check('markup: plain() keeps the label words', plain('Tap [[Check now]].') === 'Tap Check now.');
}

// ---------------------------------------------------------------- labels
const demoFile = join(CONTENT, 'demo.ts');
const uiLabels = existsSync(demoFile) ? ((await import(pathToFileURL(demoFile).href)).demo?.uiLabels ?? 'legacy') : 'legacy';
const fixtures = join(SITE, 'tests/fixtures');
const stringsFile = join(fixtures, `ui-strings.${uiLabels}.json`);
if (!existsSync(stringsFile)) {
  console.log(`labels: ${stringsFile} is missing; exporting the control center's strings`);
  const r = spawnSync('go', ['test', '-count=1', './internal/web', '-run', 'TestExportStrings'], {
    cwd: REPO,
    env: { ...process.env, VOS_WEB_EXPORT_STRINGS: fixtures },
    encoding: 'utf8',
  });
  if (r.status !== 0) console.log(r.error ? String(r.error) : `${r.stdout}${r.stderr}`.trim().split('\n').slice(-5).join('\n'));
}
const labels = [];
for (const s of strings) for (const m of s.text.matchAll(/\[\[([^\]]+)\]\]/g)) labels.push({ at: s.at, label: m[1] });
if (check(`labels: ui-strings.${uiLabels}.json exists`, existsSync(stringsFile), 'go test ./internal/web -run TestExportStrings could not write it')) {
  const ui = new Set(JSON.parse(readFileSync(stringsFile, 'utf8')).map((s) => s.trim().replace(/\s+/g, ' ')));
  const lower = [...ui].map((s) => s.toLowerCase());
  const missing = labels.filter((l) => !ui.has(l.label));
  check(`labels: all ${labels.length} [[labels]] are ${uiLabels} control-center strings`, !missing.length, missing.map((m) => `"${m.label}" (${m.at})`).join(', '));
  for (const m of missing) {
    const near = lower.filter((s) => s.includes(m.label.toLowerCase())).slice(0, 3);
    if (near.length) console.log(`        ~ "${m.label}" appears inside: ${near.map((s) => JSON.stringify(s)).join(', ')}`);
  }
}

// ---------------------------------------------------------------- content rules
const banned = [/WaterVaporOS/, /\bopen[- ]source\b/i];
const bad = strings.filter((s) => banned.some((b) => b.test(s.text)));
check('content: no "WaterVaporOS" or "open source"', !bad.length, bad.map((b) => b.at).join(', '));

// Site hrefs: markup links [x](/…), and href fields starting with '/'.
const { routes, anchors } = exportsOf.site;
const appDir = join(SITE, 'src/app');
const built = new Set(['/']);
const findPages = (dir, prefix) => {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (!e.isDirectory() || e.name.startsWith('_') || e.name.startsWith('(') || e.name.includes('.')) continue;
    const p = join(dir, e.name);
    if (existsSync(join(p, 'page.tsx'))) built.add(`${prefix}${e.name}/`);
    findPages(p, `${prefix}${e.name}/`);
  }
};
findPages(appDir, '/');
const demoOn = existsSync(demoFile) && !!(await import(pathToFileURL(demoFile).href)).demo?.enabled;
const hrefs = [];
for (const s of strings) {
  for (const m of s.text.matchAll(/\]\((\/[^)\s]*)\)/g)) hrefs.push({ at: s.at, href: m[1] });
  if (/\.href$/.test(s.at) && s.text.startsWith('/')) hrefs.push({ at: s.at, href: s.text });
}
const broken = hrefs.filter(({ href }) => {
  const path = href.split(/[?#]/)[0];
  // The demo's links render only once content/demo.ts turns it on.
  if (path === routes.demo || path.startsWith('/demo/')) return demoOn && !built.has(path) && !existsSync(join(SITE, 'public', path));
  if (built.has(path)) return false;
  return !existsSync(join(SITE, 'public', path)) && !(path === routes.releaseKey);
});
check(`content: all ${hrefs.length} site hrefs resolve to a page or a public file`, !broken.length, broken.map((b) => `${b.href} (${b.at})`).join(', '));
for (const [k, r] of Object.entries(routes)) {
  if (k === 'demo' && !demoOn) continue;
  const path = r.split('#')[0];
  check(`content: routes.${k} (${r}) is built`, built.has(path) || path === routes.releaseKey, [...built].join(' '));
}
check('content: anchors are plain ids', Object.values(anchors).every((a) => /^[a-z][a-z0-9-]*$/.test(a)));
const home = exportsOf.home;
const faq = exportsOf.faq;
if (home?.faqTeaser?.ids && typeof faq?.faqByIds === 'function') {
  const got = faq.faqByIds(home.faqTeaser.ids);
  check(`content: the FAQ teaser's ${home.faqTeaser.ids.length} ids are real questions`, got.length === home.faqTeaser.ids.length, `${got.length} found`);
}

console.log(failures ? `\n${failures} FAILED` : '\nALL LABEL AND CONTENT CHECKS PASSED');
process.exit(failures ? 1 : 0);
