#!/usr/bin/env node
// The site's gate on one real build (pages.yml, after `npm run build`), headless:
// static checks, the metadata audit, the size budgets, then the export served
// like GitHub Pages for screenshots with zero console errors and no overflow
// at 1440, 768 and 390 px plus reduced motion, and axe with the keyboard pass.
//
//   node tests/e2e/ci.mjs [outDir=out] [--report=DIR] [--commit=<sha>] [--port=4343]
//
// --report   where screenshots and reports go (default: a folder in the
//            system temp directory; CI passes $RUNNER_TEMP/site-e2e)
// --commit   the sha the build carries in <meta name="vaporos-commit">
import { spawn, spawnSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const argv = process.argv.slice(2);
const opt = Object.fromEntries(
  argv.filter((a) => a.startsWith('--')).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.slice(2), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
const OUT = resolve(ROOT, argv.find((a) => !a.startsWith('--')) ?? 'out');
const REPORT = resolve(String(opt.report ?? join(tmpdir(), 'vaporos-site-ci')));
const PORT = Number(opt.port ?? 4343);
const BASE = `http://127.0.0.1:${PORT}/vaporos/`;
mkdirSync(REPORT, { recursive: true });

const results = [];
function step(name, args) {
  console.log(`\n▶ ${name}\n  node ${args.join(' ')}`);
  const r = spawnSync(process.execPath, args, { cwd: ROOT, stdio: 'inherit' });
  results.push({ name, ok: r.status === 0 });
}

step('static checks', ['tests/e2e/static-checks.mjs', `--live=${OUT}`]);
step('metadata', ['tests/e2e/metadata.mjs', OUT, ...(opt.commit ? [`--commit=${opt.commit}`] : [])]);

const server = spawn(process.execPath, ['scripts/serve.mjs', OUT, String(PORT), '--gzip', '--quiet'], { cwd: ROOT, stdio: ['ignore', 'pipe', 'inherit'] });
const stop = () => server.kill();
process.on('SIGINT', () => {
  stop();
  process.exit(130);
});
try {
  await new Promise((ok, fail) => {
    server.stdout.on('data', (d) => (String(d).startsWith('serve:') ? ok() : null));
    server.on('exit', (code) => fail(new Error(`serve exited (${code})`)));
  });
  step('budgets', ['scripts/budget.mjs', OUT, `--live=${BASE}`, `--json=${join(REPORT, 'budget.json')}`]);
  step('shoot 1440/768/390', ['scripts/shoot.mjs', BASE, join(REPORT, 'shots'), '--widths=1440,768,390', '--strict']);
  step('shoot 390 reduced motion', ['scripts/shoot.mjs', BASE, join(REPORT, 'shots-rm'), '--widths=390', '--reduced-motion', '--strict']);
  step('axe', ['tests/e2e/axe.mjs', BASE, `--out=${REPORT}`]);
} finally {
  stop();
}

console.log('\n== site e2e');
for (const r of results) console.log(`${r.ok ? 'PASS' : 'FAIL'}  ${r.name}`);
const failed = results.filter((r) => !r.ok).length;
console.log(failed ? `\n${failed} FAILED (reports in ${REPORT})` : `\nSITE E2E PASSED (reports in ${REPORT})`);
process.exit(failed ? 1 : 0);
