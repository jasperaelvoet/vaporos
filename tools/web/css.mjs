#!/usr/bin/env node
// css.mjs builds the control center's stylesheet with the pinned Tailwind CLI.
//
//   node tools/web/css.mjs [build]    write internal/web/static/app.css and
//                                     static/pages/<page>.css
//   node tools/web/css.mjs --check    build in memory; exit 1 unless every
//                                     committed file is byte-identical
//   node tools/web/css.mjs --watch    rebuild whenever an input changes
//   node tools/web/css.mjs --utilities  list the utility classes in the build,
//                                     to spot words Tailwind picked up from JS
//
// The shell's input is internal/web/styles/app.css: Tailwind's preflight,
// theme and utilities, the tokens, the fonts and the shell's components.
// Each internal/web/styles/pages/<page>.css is built on its own into
// static/pages/<page>.css, which only the pages that name it in web.go's
// registry (page.Styles) link after app.css: one page never pays for
// another's rules. Page stylesheets are plain CSS in @layer components; they
// use the tokens, which app.css keeps whole (theme(static)) for them.
// Every output starts with
//   /*! vaporos-css inputs=<16 hex> tailwind=<version> */
// (lib/css-hash.mjs), which TestAppCSSFresh recomputes in Go.
//
// Exit codes: 0 ok, 1 stale (--check), 2 tools missing or not the pinned
// versions, 3 the output breaks a rule the Go tests enforce, 4 Tailwind failed.

import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, watch, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

import { header, inputHash } from './lib/css-hash.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, '..', '..');
const entry = 'internal/web/styles/app.css';
const output = 'internal/web/static/app.css';
const pagesIn = 'internal/web/styles/pages';
const pagesOut = 'internal/web/static/pages';
const modules = join(here, 'node_modules');

function fail(code, msg) {
  console.error(`css: ${msg}`);
  process.exit(code);
}

// pinnedTailwind checks that the installed CLI and core are the versions
// package.json pins, and returns that version.
function pinnedTailwind() {
  const pins = JSON.parse(readFileSync(join(here, 'package.json'), 'utf8')).devDependencies;
  const want = pins.tailwindcss;
  if (pins['@tailwindcss/cli'] !== want) {
    fail(2, `package.json pins @tailwindcss/cli ${pins['@tailwindcss/cli']} but tailwindcss ${want}; bump them together`);
  }
  for (const pkg of ['tailwindcss', '@tailwindcss/cli']) {
    const file = join(modules, pkg, 'package.json');
    if (!existsSync(file)) fail(2, `${pkg} is not installed: run npm ci --prefix tools/web`);
    const got = JSON.parse(readFileSync(file, 'utf8')).version;
    if (got !== want) fail(2, `${pkg} ${got} is installed but ${want} is pinned: run npm ci --prefix tools/web`);
  }
  return want;
}

// checkRules mirrors what the Go tests enforce, so a bad build fails here
// first, with a clearer message.
function checkRules(name, css, version, shell) {
  const banner = `/*! tailwindcss v${version} | MIT License | https://tailwindcss.com */`;
  if (shell && !css.startsWith(banner)) fail(3, `the build does not start with Tailwind's banner ${banner}`);
  if (/@import/.test(css)) fail(3, `${name} still contains an @import (TestCSPCompliance)`);
  if (/url\((['"]?)(https?:)?\/\//.test(css)) fail(3, `${name} loads something external (TestCSPCompliance)`);
  if (!shell && /@layer\s+(theme|base|utilities)\b/.test(css)) fail(3, `${name}: a page stylesheet holds only @layer components rules`);
}

// pageNames lists styles/pages/<name>.css by name.
function pageNames() {
  const dir = join(root, pagesIn);
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter((f) => f.endsWith('.css') && !f.startsWith('.'))
    .map((f) => f.slice(0, -4))
    .sort();
}

// build runs Tailwind on the shell entry and on each page stylesheet and
// returns every output file (repo-relative path → complete contents,
// header included).
export function build() {
  const version = pinnedTailwind();
  const hash = inputHash(root);
  const dir = mkdtempSync(join(tmpdir(), 'vaporos-css-'));
  const cli = join(modules, '@tailwindcss', 'cli', 'dist', 'index.mjs');
  const jobs = [{ input: entry, output, shell: true }, ...pageNames().map((n) => ({ input: `${pagesIn}/${n}.css`, output: `${pagesOut}/${n}.css`, shell: false }))];
  const files = new Map();
  try {
    for (const [i, job] of jobs.entries()) {
      const out = join(dir, `${i}.css`);
      // NODE_PATH lets `@import "tailwindcss"` in internal/web/styles resolve
      // to tools/web/node_modules: no node_modules ever sits under internal/.
      const r = spawnSync(process.execPath, [cli, '-i', job.input, '-o', out, '--minify'], {
        cwd: root,
        env: { ...process.env, NODE_PATH: modules },
        encoding: 'utf8',
      });
      if (r.status !== 0) fail(4, `tailwindcss failed on ${job.input}:\n${r.stderr || r.stdout || r.error}`);
      const css = readFileSync(out, 'utf8').trim();
      checkRules(job.input, css, version, job.shell);
      files.set(job.output, `${header(hash, version)}\n${css}\n`);
    }
    return files;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

// stale lists built page stylesheets whose source is gone.
function stale(files) {
  const dir = join(root, pagesOut);
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter((f) => f.endsWith('.css'))
    .map((f) => `${pagesOut}/${f}`)
    .filter((f) => !files.has(f));
}

function sizes(css) {
  const raw = Buffer.byteLength(css);
  const gz = gzipSync(css, { level: 9 }).length;
  return `${raw.toLocaleString('en')} bytes, ${gz.toLocaleString('en')} gzipped`;
}

function write(files) {
  for (const [rel, css] of files) {
    const dst = join(root, rel);
    if (existsSync(dst) && readFileSync(dst, 'utf8') === css) {
      console.log(`css: ${rel} unchanged (${sizes(css)})`);
      continue;
    }
    mkdirSync(dirname(dst), { recursive: true });
    const tmp = `${dst}.${process.pid}.tmp`;
    writeFileSync(tmp, css);
    renameSync(tmp, dst);
    console.log(`css: wrote ${rel} (${sizes(css)}) ${css.slice(0, css.indexOf('\n'))}`);
  }
  for (const rel of stale(files)) {
    rmSync(join(root, rel));
    console.log(`css: removed ${rel}: its source is gone`);
  }
}

// rules splits minified CSS into one rule per line, for a readable diff.
function rules(css) {
  return css.replace(/}/g, '}\n').split('\n');
}

function check() {
  const files = build();
  const bad = [];
  for (const [rel, want] of files) {
    const dst = join(root, rel);
    const got = existsSync(dst) ? readFileSync(dst, 'utf8') : '';
    if (got === want) continue;
    bad.push(rel);
    if (!got) {
      console.error(`css: ${rel} is missing`);
      continue;
    }
    const a = rules(got);
    const b = rules(want);
    const shown = [];
    for (let i = 0; i < Math.max(a.length, b.length) && shown.length < 40; i++) {
      if (a[i] === b[i]) continue;
      if (a[i] !== undefined) shown.push(`- ${a[i]}`);
      if (b[i] !== undefined) shown.push(`+ ${b[i]}`);
    }
    console.error(`css: ${rel}:\n${shown.join('\n')}`);
  }
  for (const rel of stale(files)) {
    bad.push(rel);
    console.error(`css: ${rel} is built from a stylesheet that no longer exists`);
  }
  if (bad.length) fail(1, `${bad.join(', ')} ${bad.length === 1 ? 'is' : 'are'} stale: run npm --prefix tools/web run css and commit ${bad.length === 1 ? 'it' : 'them'} with the inputs`);
  const first = files.get(output);
  console.log(`css: ${files.size} stylesheets are up to date (${first.slice(0, first.indexOf('\n'))})`);
}

function utilities() {
  const css = build().get(output);
  const at = css.indexOf('@layer utilities{');
  if (at < 0) return;
  const names = new Set();
  for (const m of css.slice(at).matchAll(/\.((?:\\.|[\w-])+)/g)) names.add(m[1].replace(/\\(.)/g, '$1'));
  console.log([...names].sort().join('\n'));
}

// watchInputs rebuilds after every change under the input directories. Each
// rebuild runs in a child process, so a broken build reports and the watch
// goes on. The lockfile is not watched: restart after npm ci.
function watchInputs() {
  let timer = null;
  const rebuild = () => {
    const r = spawnSync(process.execPath, [fileURLToPath(import.meta.url), 'build'], { stdio: 'inherit' });
    if (r.status !== 0) console.error('css: build failed; waiting for the next change');
  };
  rebuild();
  const dirs = ['internal/web/styles', 'internal/web/templates', 'internal/web/static/js'];
  for (const rel of dirs) {
    const abs = join(root, rel);
    if (!existsSync(abs)) continue;
    watch(abs, { recursive: true }, (_event, name) => {
      const file = name ? `${rel}/${String(name)}` : rel;
      if (/(^|\/)(legacy|node_modules)(\/|$)/.test(file)) return;
      clearTimeout(timer);
      timer = setTimeout(rebuild, 120);
    });
  }
  console.log(`css: watching ${dirs.join(', ')} (Ctrl-C stops)`);
}

const mode = process.argv[2] ?? 'build';
switch (mode) {
  case 'build':
    write(build());
    break;
  case '--check':
    check();
    break;
  case '--watch':
    watchInputs();
    break;
  case '--utilities':
    utilities();
    break;
  default:
    fail(2, `unknown mode ${mode}; use build, --check, --watch or --utilities`);
}
