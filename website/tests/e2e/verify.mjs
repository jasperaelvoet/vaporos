#!/usr/bin/env node
// The site's full local gate (npm run verify): production builds in each
// release state, static checks, the metadata audit, the size budgets, the
// download card's browser half, headless screenshots with zero console
// errors and no horizontal overflow, axe with a keyboard pass, optionally
// Lighthouse, and text parity with the archived Astro site. Headless only.
//
//   npm run verify -- [--out=DIR] [--live] [--astro=DIR] [--lock=DIR] [--skip-build]
//                     [--lighthouse] [--runs=N] [--ports=4341]
//
//   --out=DIR      where builds, screenshots and reports go (default: a
//                  vaporos-site-verify folder in the system temp directory;
//                  never inside the repo)
//   --live         also build against the real GitHub API (anonymous) and
//                  shoot that build too
//   --astro=DIR    an archived Astro build to check text parity against
//   --lock=DIR     a directory to mkdir as a lock around the builds, for
//                  checkouts where several agents build (rmdir'd after)
//   --skip-build   reuse the builds already in --out
//   --lighthouse   also run Lighthouse (G-17) on the fixture build, median of
//                  --runs (default 3)
//   --ports=N      first port (default 4341), for running beside another verify
//
// Ports (127.0.0.1): none N, fixture N+1, live N+2, unknown N+4 (4341, 4342,
// 4343, 4345 by default), Astro 4331. Every build is served gzipped, as
// GitHub Pages does.
import { spawn, spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, rmSync, rmdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const opt = Object.fromEntries(
  process.argv.slice(2).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.replace(/^--/, ''), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
const OUT = resolve(opt.out ?? join(tmpdir(), 'vaporos-site-verify'));
if (OUT === ROOT.replace(/\/$/, '') || OUT.startsWith(ROOT)) {
  console.error(`verify: --out must be outside the website (${OUT})`);
  process.exit(2);
}
mkdirSync(OUT, { recursive: true });
const FIXTURE = join(ROOT, 'tests/fixtures/release-latest.json');
const UNKNOWN_FIXTURE = join(OUT, 'release-unknown.json');
writeFileSync(UNKNOWN_FIXTURE, '{"message":"API rate limit exceeded"}\n');

const P0 = Number(opt.ports ?? 4341);
const builds = [
  { name: 'none', port: P0, env: { VAPOROS_RELEASE_FIXTURE: '/dev/null' } },
  { name: 'fixture', port: P0 + 1, env: { VAPOROS_RELEASE_FIXTURE: FIXTURE } },
  { name: 'unknown', port: P0 + 4, env: { VAPOROS_RELEASE_FIXTURE: UNKNOWN_FIXTURE } },
  ...(opt.live ? [{ name: 'live', port: P0 + 2, env: { VAPOROS_RELEASE_FIXTURE: '' } }] : []),
];
const site = (b) => join(OUT, `site-${b.name}`);
const url = (port) => `http://127.0.0.1:${port}/vaporos/`;

const results = [];
function step(name, cmd, args, env = {}) {
  console.log(`\n▶ ${name}\n  ${cmd} ${args.join(' ')}`);
  const r = spawnSync(cmd, args, { cwd: ROOT, stdio: 'inherit', env: { ...process.env, ...env } });
  const ok = r.status === 0;
  results.push({ name, ok });
  return ok;
}

// ---------------------------------------------------------------- builds
if (!opt['skip-build']) {
  const lock = opt.lock ? resolve(opt.lock) : null;
  if (lock) {
    for (;;) {
      try {
        mkdirSync(lock);
        break;
      } catch {
        console.log(`verify: waiting for the build lock ${lock}`);
        await new Promise((r) => setTimeout(r, 5000));
      }
    }
  }
  try {
    for (const b of builds) {
      const env = { ...b.env };
      if (!env.VAPOROS_RELEASE_FIXTURE) delete env.VAPOROS_RELEASE_FIXTURE;
      const clean = { ...process.env };
      delete clean.VAPOROS_RELEASE_FIXTURE;
      delete clean.VAPOROS_RELEASE_REQUIRED;
      console.log(`\n▶ build ${b.name}`);
      const r = spawnSync('npm', ['run', 'build'], { cwd: ROOT, stdio: 'inherit', env: { ...clean, ...env } });
      results.push({ name: `build ${b.name}`, ok: r.status === 0 });
      if (r.status !== 0) throw new Error(`build ${b.name} failed`);
      rmSync(site(b), { recursive: true, force: true });
      cpSync(join(ROOT, 'out'), site(b), { recursive: true });
      rmSync(join(ROOT, 'out'), { recursive: true, force: true });
    }
  } finally {
    if (lock) rmdirSync(lock);
  }
}

// ---------------------------------------------------------------- static
const byName = Object.fromEntries(builds.map((b) => [b.name, b]));
step('static checks', 'node', [
  'tests/e2e/static-checks.mjs',
  `--fixture=${site(byName.fixture)}`,
  `--none=${site(byName.none)}`,
  `--unknown=${site(byName.unknown)}`,
  ...(opt.live ? [`--live=${site(byName.live)}`] : []),
  `--release=${FIXTURE}`,
]);
step('metadata', 'node', ['tests/e2e/metadata.mjs', site(byName.fixture)]);

// ---------------------------------------------------------------- served
const servers = [];
function serve(dir, port) {
  return new Promise((ok, fail) => {
    if (!existsSync(dir)) return fail(new Error(`${dir} is missing (run without --skip-build)`));
    const p = spawn(process.execPath, ['scripts/serve.mjs', dir, String(port), '--gzip', '--quiet'], { cwd: ROOT, stdio: ['ignore', 'pipe', 'inherit'] });
    servers.push(p);
    p.stdout.on('data', (d) => (String(d).startsWith('serve:') ? ok() : null));
    p.on('exit', (code) => fail(new Error(`serve ${port} exited (${code})`)));
  });
}
const stop = () => servers.forEach((p) => p.kill());
process.on('SIGINT', () => {
  stop();
  process.exit(130);
});
try {
  for (const b of builds) await serve(site(b), b.port);
  if (opt.astro) await serve(resolve(opt.astro), 4331);

  const fx = url(byName.fixture.port);
  step('release hook', 'node', ['tests/e2e/check-release-hook.mjs', url(byName.none.port), fx, FIXTURE, url(byName.unknown.port)]);
  step('budgets', 'node', ['scripts/budget.mjs', site(byName.fixture), `--live=${fx}`, `--json=${join(OUT, 'budget.json')}`]);
  step('shoot fixture 1440/768/390', 'node', ['scripts/shoot.mjs', fx, join(OUT, 'shots'), '--widths=1440,768,390', `--release=${FIXTURE}`, '--strict']);
  step('shoot fixture 390 reduced motion', 'node', ['scripts/shoot.mjs', fx, join(OUT, 'shots-rm'), '--widths=390', '--reduced-motion', `--release=${FIXTURE}`, '--strict']);
  step('shoot none 390', 'node', ['scripts/shoot.mjs', url(byName.none.port), join(OUT, 'shots-none'), '--widths=390', '--pages=/,/download/', '--strict']);
  if (opt.live) step('shoot live 1440/390', 'node', ['scripts/shoot.mjs', url(byName.live.port), join(OUT, 'shots-live'), '--widths=1440,390', '--release=live', '--strict']);
  step('axe 1440/1024/768/390/320', 'node', ['tests/e2e/axe.mjs', fx, `--out=${join(OUT, 'axe')}`, `--release=${FIXTURE}`]);
  step('axe 390 reduced motion', 'node', ['tests/e2e/axe.mjs', fx, `--out=${join(OUT, 'axe-rm')}`, '--widths=390', '--reduced-motion', `--release=${FIXTURE}`]);
  if (opt.lighthouse) step('lighthouse', 'node', ['tests/e2e/lighthouse.mjs', fx, `--out=${join(OUT, 'lh')}`, `--runs=${opt.runs ?? 3}`]);
  if (opt.astro) step('parity with the Astro site', 'node', ['tests/e2e/check-parity.mjs', fx, 'src/content', 'http://127.0.0.1:4331/vaporos/']);
} finally {
  stop();
}

console.log('\n== verify');
for (const r of results) console.log(`${r.ok ? 'PASS' : 'FAIL'}  ${r.name}`);
const failed = results.filter((r) => !r.ok).length;
console.log(failed ? `\n${failed} FAILED (output in ${OUT})` : `\nSITE VERIFIED (output in ${OUT})`);
process.exit(failed ? 1 : 0);
