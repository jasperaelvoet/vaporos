#!/usr/bin/env node
// css.mjs builds the control center's stylesheet with the pinned Tailwind CLI.
//
//   node tools/web/css.mjs [build]    write internal/web/static/app.css
//   node tools/web/css.mjs --check    build to a temp file; exit 1 unless the
//                                     committed file is byte-identical
//   node tools/web/css.mjs --watch    rebuild whenever an input changes
//   node tools/web/css.mjs --utilities  list the utility classes in the build,
//                                     to spot words Tailwind picked up from JS
//
// The input is internal/web/styles/app.css. The output starts with
//   /*! vaporos-css inputs=<16 hex> tailwind=<version> */
// (lib/css-hash.mjs), which TestAppCSSFresh recomputes in Go.
//
// Exit codes: 0 ok, 1 stale (--check), 2 tools missing or not the pinned
// versions, 3 the output breaks a rule the Go tests enforce, 4 Tailwind failed.

import { spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, renameSync, rmSync, watch, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

import { header, inputHash } from './lib/css-hash.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, '..', '..');
const entry = 'internal/web/styles/app.css';
const output = 'internal/web/static/app.css';
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
function checkRules(css, version) {
  const banner = `/*! tailwindcss v${version} | MIT License | https://tailwindcss.com */`;
  if (!css.startsWith(banner)) fail(3, `the build does not start with Tailwind's banner ${banner}`);
  if (/@import/.test(css)) fail(3, 'the build still contains an @import (TestCSPCompliance)');
  if (/url\((['"]?)(https?:)?\/\//.test(css)) fail(3, 'the build loads something external (TestCSPCompliance)');
}

// build runs Tailwind and returns the complete file, header included.
export function build() {
  const version = pinnedTailwind();
  const dir = mkdtempSync(join(tmpdir(), 'vaporos-css-'));
  try {
    const out = join(dir, 'app.css');
    const cli = join(modules, '@tailwindcss', 'cli', 'dist', 'index.mjs');
    // NODE_PATH lets `@import "tailwindcss"` in internal/web/styles resolve
    // to tools/web/node_modules: no node_modules ever sits under internal/.
    const r = spawnSync(process.execPath, [cli, '-i', entry, '-o', out, '--minify'], {
      cwd: root,
      env: { ...process.env, NODE_PATH: modules },
      encoding: 'utf8',
    });
    if (r.status !== 0) fail(4, `tailwindcss failed:\n${r.stderr || r.stdout || r.error}`);
    const css = readFileSync(out, 'utf8');
    checkRules(css, version);
    return `${header(inputHash(root), version)}\n${css}\n`;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function sizes(css) {
  const raw = Buffer.byteLength(css);
  const gz = gzipSync(css, { level: 9 }).length;
  return `${raw.toLocaleString('en')} bytes, ${gz.toLocaleString('en')} gzipped`;
}

function write(css) {
  const dst = join(root, output);
  if (existsSync(dst) && readFileSync(dst, 'utf8') === css) {
    console.log(`css: ${output} unchanged (${sizes(css)})`);
    return;
  }
  const tmp = `${dst}.${process.pid}.tmp`;
  writeFileSync(tmp, css);
  renameSync(tmp, dst);
  console.log(`css: wrote ${output} (${sizes(css)}) ${css.slice(0, css.indexOf('\n'))}`);
}

// rules splits minified CSS into one rule per line, for a readable diff.
function rules(css) {
  return css.replace(/}/g, '}\n').split('\n');
}

function check() {
  const want = build();
  const dst = join(root, output);
  const got = existsSync(dst) ? readFileSync(dst, 'utf8') : '';
  if (got === want) {
    console.log(`css: ${output} is up to date (${want.slice(0, want.indexOf('\n'))})`);
    return;
  }
  if (!got) {
    console.error(`css: ${output} is missing`);
  } else {
    const a = rules(got);
    const b = rules(want);
    const shown = [];
    for (let i = 0; i < Math.max(a.length, b.length) && shown.length < 40; i++) {
      if (a[i] === b[i]) continue;
      if (a[i] !== undefined) shown.push(`- ${a[i]}`);
      if (b[i] !== undefined) shown.push(`+ ${b[i]}`);
    }
    console.error(shown.join('\n'));
  }
  fail(1, `${output} is stale: run npm --prefix tools/web run css and commit it with its inputs`);
}

function utilities() {
  const css = build();
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
