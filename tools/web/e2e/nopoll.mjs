#!/usr/bin/env node
// No polling (G-12): once a page has loaded, it may only keep its one
// /api/v1/events stream open (and ping). Idle power-off counts web activity,
// so a page that polls would keep the PC awake forever.
//
//   node tools/web/e2e/nopoll.mjs [--seconds=70] [--clock=MINUTES] [--ui=legacy|next]
//        [--presets=idle,streaming] [--paths=/,/devices] [--out=DIR]
//
// Each preset gets a dev server; each path is loaded signed in (as run.mjs
// does), and a page that did not stay on its path, or never opened the
// event stream, fails: it measured something else. --seconds waits in real
// time, all pages in parallel. --clock=10 instead installs a fake clock and
// runs ten minutes of timers in a moment (flow 9, ARCH §10.4). Passive
// refreshes (X-VOS-Passive or ?passive=1: the scene's probe, a reconnect's
// reload) are reported but allowed; any other API request after load fails.

import { join } from 'node:path';

import { launch } from './lib/browser.mjs';
import { INIT } from './lib/checks.mjs';
import { APP_TOPICS, preset, ROUTES } from './lib/presets.mjs';
import { list, outDir, parseArgs, slug, writeReport } from './lib/run-helpers.mjs';
import { activeUI, buildTestBinary, startServer } from './lib/server.mjs';
import { ui as uiAdapter } from './lib/ui.mjs';

const args = parseArgs(process.argv.slice(2), { seconds: '70', clock: '', ui: '', presets: 'idle', paths: '', out: '', preset: '', path: '' });
const uiName = args.ui || activeUI();
const ui = uiAdapter(uiName);
const out = outDir(args.out, 'cc-nopoll');
const presets = list(args.preset || args.presets);
const paths = list(args.path || args.paths);
if (!paths.length) paths.push(...APP_TOPICS[uiName].map((t) => ROUTES[uiName][t]).filter((p, i, all) => all.indexOf(p) === i));
const allowedAfterLoad = [/\/api\/v1\/events(\?|$)/, /\/api\/v1\/ping(\?|$)/];

// session signs in once per server (sign-in is rate limited, so the pages
// probed side by side share one session, as tabs do) and returns its state.
async function session(browser, s) {
  const context = await browser.newContext();
  try {
    const login = await context.newPage();
    await login.goto(`${s.origin}/login`);
    const status = await login.evaluate(async () => {
      const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: 'vaporvapor' }) });
      return r.status;
    });
    if (status !== 200) throw new Error(`sign-in answered ${status}`);
    return await context.storageState();
  } finally {
    await context.close();
  }
}

// probe watches one signed-in page from its load for the run's time.
async function probe(browser, s, path, storageState) {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, storageState });
  try {
    await context.addInitScript(INIT);
    const page = await context.newPage();
    if (args.clock) await page.clock.install();
    const requests = [];
    page.on('request', (r) =>
      requests.push({ at: Date.now(), method: r.method(), url: r.url().replace(s.origin, ''), passive: r.headers()['x-vos-passive'] === '1' || /[?&]passive=1/.test(r.url()) }),
    );
    await page.goto(s.origin + path, { waitUntil: 'load' });
    await page.waitForFunction(ui.ready, null, { timeout: 8000 });
    // Let the page's first GETs finish, then count from here.
    await page.waitForLoadState('networkidle').catch(() => {});
    await page.waitForTimeout(1000);
    const landed = new URL(page.url()).pathname;
    const loadedAt = Date.now();
    if (args.clock) {
      await page.clock.runFor(Number(args.clock) * 60_000);
      await page.waitForTimeout(1500);
    } else {
      await page.waitForTimeout(Number(args.seconds) * 1000);
    }
    const after = requests.filter((r) => r.at >= loadedAt);
    const api = after.filter((r) => r.url.startsWith('/api/') && !allowedAfterLoad.some((re) => re.test(r.url)));
    const problems = [];
    if (landed !== path) problems.push(`landed on ${landed}, not ${path}: not signed in, or redirected`);
    if (!requests.some((r) => /\/api\/v1\/events/.test(r.url))) problems.push('never opened /api/v1/events');
    for (const r of api.filter((x) => !x.passive)) problems.push(`POLL ${r.method} ${r.url}`);
    return { path, landed, requests: requests.length, after: after.length, events: after.filter((r) => /\/api\/v1\/events/.test(r.url)).length, passive: api.filter((x) => x.passive).map((r) => `${r.method} ${r.url}`), problems };
  } finally {
    await context.close();
  }
}

const bin = buildTestBinary(out);
const { browser, version } = await launch();
let code = 0;
const report = { ui: uiName, browser: version, mode: args.clock ? `fake clock ${args.clock} min` : `${args.seconds} s real time`, runs: [] };
const lines = [`ui=${uiName} ${report.mode} presets=${presets.join(',')} paths=${paths.join(',')}`];
try {
  for (const name of presets) {
    const s = await startServer({ bin, p: preset(name), ui: uiName, logDir: join(out, 'servers'), stateDir: join(out, 'state', slug(name)) });
    try {
      const state = await session(browser, s);
      const runs = await Promise.all(paths.map((p) => probe(browser, s, p, state).catch((err) => ({ path: p, problems: [`harness: ${String(err?.message ?? err).split('\n')[0]}`] }))));
      for (const r of runs) {
        report.runs.push({ preset: name, ...r });
        const passive = r.passive?.length ? `; passive: ${r.passive.join(', ')}` : '';
        lines.push(`${r.problems.length ? 'FAIL' : 'ok  '} ${name} ${r.path}: ${r.after ?? 0} requests after load (${r.events ?? 0} to /events)${passive}`);
        for (const p of r.problems) lines.push(`  ${p}`);
        if (r.problems.length) code = 1;
      }
    } finally {
      await s.stop();
    }
  }
  lines.push(code ? 'FAIL: a page polled, or did not measure what it should' : 'ok: only /events, /ping and passive refreshes after load');
} catch (err) {
  console.error(`nopoll: ${err?.stack ?? err}`);
  code = 2;
} finally {
  await browser.close();
}
writeReport(out, report, lines);
console.log(lines.join('\n'));
process.exit(code);
