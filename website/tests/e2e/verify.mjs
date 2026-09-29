#!/usr/bin/env node
// The site's full local gate (npm run verify): production builds in each
// release state, static checks, the download card's browser half, headless
// screenshots with zero console errors and no horizontal overflow, and
// (optionally) text parity with the archived Astro site. Headless only.
//
//   npm run verify -- [--out=DIR] [--live] [--astro=DIR] [--lock=DIR] [--skip-build]
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
//
// Ports (127.0.0.1): none 4341, fixture 4342, live 4343, unknown 4345, Astro 4331.
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

const builds = [
  { name: 'none', port: 4341, env: { VAPOROS_RELEASE_FIXTURE: '/dev/null' } },
  { name: 'fixture', port: 4342, env: { VAPOROS_RELEASE_FIXTURE: FIXTURE } },
  { name: 'unknown', port: 4345, env: { VAPOROS_RELEASE_FIXTURE: UNKNOWN_FIXTURE } },
  ...(opt.live ? [{ name: 'live', port: 4343, env: { VAPOROS_RELEASE_FIXTURE: '' } }] : []),
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

// ---------------------------------------------------------------- served
const servers = [];
function serve(dir, port) {
  return new Promise((ok, fail) => {
    if (!existsSync(dir)) return fail(new Error(`${dir} is missing (run without --skip-build)`));
    const p = spawn(process.execPath, ['scripts/serve.mjs', dir, String(port), '--quiet'], { cwd: ROOT, stdio: ['ignore', 'pipe', 'inherit'] });
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

  step('release hook', 'node', ['tests/e2e/check-release-hook.mjs', url(4341), url(4342), FIXTURE, url(4345)]);
  step('shoot fixture 1440/768/390', 'node', ['scripts/shoot.mjs', url(4342), join(OUT, 'shots'), '--widths=1440,768,390', `--release=${FIXTURE}`, '--strict']);
  step('shoot fixture 390 reduced motion', 'node', ['scripts/shoot.mjs', url(4342), join(OUT, 'shots-rm'), '--widths=390', '--reduced-motion', `--release=${FIXTURE}`, '--strict']);
  step('shoot none 390', 'node', ['scripts/shoot.mjs', url(4341), join(OUT, 'shots-none'), '--widths=390', '--pages=/,/download/', '--strict']);
  if (opt.live) step('shoot live 1440/390', 'node', ['scripts/shoot.mjs', url(4343), join(OUT, 'shots-live'), '--widths=1440,390', '--release=live', '--strict']);
  if (opt.astro) step('parity with the Astro site', 'node', ['tests/e2e/check-parity.mjs', url(4342), 'src/content', 'http://127.0.0.1:4331/vaporos/']);
} finally {
  stop();
}

console.log('\n== verify');
for (const r of results) console.log(`${r.ok ? 'PASS' : 'FAIL'}  ${r.name}`);
const failed = results.filter((r) => !r.ok).length;
console.log(failed ? `\n${failed} FAILED (output in ${OUT})` : `\nSITE VERIFIED (output in ${OUT})`);
process.exit(failed ? 1 : 0);
