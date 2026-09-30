#!/usr/bin/env node
// Frame timing of the control center in WebKit (Safari's engine), headless,
// against a dev server someone else started (it starts nothing itself):
//
//   VOS_WEB_DEV=127.0.0.1:8141 VOS_WEB_UI=next VOS_WEB_PRESET=idle \
//     go test ./internal/web -run '^TestDevServer$' -timeout 0
//   node tools/web/e2e/webkit-perf.mjs --base=http://127.0.0.1:8141 --out=DIR
//        [--browser=webkit|chromium] [--runs=3] [--viewports=desktop,phone]
//        [--only=load,scroll,…] [--rest=10] [--against=OTHER/report.json]
//        [--strict]
//
// Every scenario records a requestAnimationFrame delta log in the page and a
// main-thread probe (a setTimeout(0) chain: a gap over 50 ms is a long task,
// rendering on the main thread included). WebKit paints in its GPU process,
// so a slow paint shows as long frames with no long task.
// Each row is the median of --runs runs: avg fps, % frames over 34 ms, the
// worst frame and the long-task ms in the window. At rest (no rAF loop of
// ours) it counts the page's own rAF calls and infinite animations, the
// browser's OS CPU time (every process of that browser build on the
// machine, so run nothing else in it meanwhile), and in Chromium the
// Performance metrics.
// Targets: avg ≥ 55 fps, ≤ 5 % frames > 34 ms, no frame > 100 ms. --strict
// exits 1 on a miss; otherwise it only reports.
//
// The server's /__dev/* controls switch presets and publish events, so
// --base must be the loopback address the dev server listens on.

import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { chromium, webkit } from 'playwright-core';

import { findChrome } from './lib/browser.mjs';
import { settleHeat } from './lib/checks.mjs';
import { list, outDir, parseArgs, writeReport } from './lib/run-helpers.mjs';
import { ui as uiAdapter } from './lib/ui.mjs';

export const TARGET = { fps: 55, slowPct: 5, worst: 100, slowMs: 34 };
export const VIEWPORTS = {
  desktop: { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 },
  phone: { viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true },
};

const args = parseArgs(process.argv.slice(2), {
  base: '',
  out: '',
  browser: 'webkit',
  runs: '1',
  viewports: 'desktop,phone',
  only: '',
  rest: '10',
  password: 'vaporvapor',
  against: '',
  strict: false,
});
if (!args.base) throw new Error('--base=http://127.0.0.1:<port> is required: the dev server to measure');
const BASE = String(args.base).replace(/\/+$/, '');
const runs = Math.max(1, Number(args.runs) || 1);
const restMs = Math.max(1, Number(args.rest) || 10) * 1000;
const out = outDir(args.out, `webkit-perf-${args.browser}`);
const ui = uiAdapter('next');

// REC runs in every document before its own scripts. It logs every frame
// from the start, keeps the log across a same-tab navigation (sessionStorage),
// and counts the page's own requestAnimationFrame calls for the rest check.
const REC = `(() => {
  const w = window, T0 = performance.timeOrigin, raf = w.requestAnimationFrame.bind(w);
  const P = (w.__wkp = { rec: false, frames: [], blocks: [], pageRaf: 0 });
  w.requestAnimationFrame = function (cb) { P.pageRaf++; return raf(cb); };
  const tick = (t) => { if (!P.rec) return; P.frames.push(T0 + t); raf(tick); };
  let last = 0;
  const probe = () => {
    if (!P.rec) return;
    const n = performance.now();
    if (n - last > 50) P.blocks.push([T0 + last, n - last]);
    last = n;
    setTimeout(probe, 0);
  };
  P.start = () => { P.frames = []; P.blocks = []; if (P.rec) return; P.rec = true; last = performance.now(); raf(tick); setTimeout(probe, 0); };
  P.stop = () => { P.rec = false; return { frames: P.frames, blocks: P.blocks, now: T0 + performance.now() }; };
  P.now = () => T0 + performance.now();
  try {
    const c = JSON.parse(sessionStorage.getItem('__wkp') || 'null');
    sessionStorage.removeItem('__wkp');
    P.start();
    if (c && T0 - c.now < 10000) { P.frames = c.frames; P.blocks = c.blocks; }
  } catch { P.start(); }
  addEventListener('pagehide', () => {
    if (!P.rec) return;
    try { sessionStorage.setItem('__wkp', JSON.stringify({ frames: P.frames, blocks: P.blocks, now: T0 + performance.now() })); } catch {}
  });
})();`;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function devCall(path, body) {
  const r = await fetch(`${BASE}/__dev/${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) });
  if (!r.ok) throw new Error(`/__dev/${path}: ${r.status} ${await r.text()}`);
}
const setPreset = (name) => devCall('preset', { name });
const event = (topic, data) => devCall('event', { topic, data });

// metrics turns a log into the row's numbers, over [from, to] (absolute ms).
export function frameStats(log, from = -Infinity, to = Infinity) {
  const f = log.frames.filter((t) => t >= from && t <= to);
  const d = f.slice(1).map((t, i) => t - f[i]);
  const span = f.length > 1 ? f[f.length - 1] - f[0] : 0;
  const long = log.blocks.filter(([at]) => at >= from && at <= to).map(([, ms]) => ms);
  return {
    frames: d.length,
    fps: span ? (d.length * 1000) / span : 0,
    slowPct: d.length ? (100 * d.filter((x) => x > TARGET.slowMs).length) / d.length : 0,
    worst: d.length ? Math.max(...d) : 0,
    longMs: long.reduce((a, b) => a + b, 0),
    longest: long.length ? Math.max(...long) : 0,
  };
}

export const median = (xs) => {
  const s = xs.filter(Number.isFinite).sort((a, b) => a - b);
  if (!s.length) return NaN;
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};

// up waits until the box answers again (after the hold's restart).
async function up() {
  for (let i = 0; i < 100; i++) {
    const ok = await fetch(`${BASE}/api/v1/ping`).then((r) => r.ok, () => false);
    if (ok) return;
    await sleep(100);
  }
}

// open loads a page, once more if the connection dropped (a restart the
// previous scenario asked for closes the kept-alive connections).
async function open(page, path, opts) {
  try {
    await page.goto(BASE + path, opts);
  } catch (err) {
    if (!/connection|ERR_|reset/i.test(String(err?.message))) throw err;
    await up();
    await page.goto(BASE + path, opts);
  }
}

const ready = (page) => page.waitForFunction(ui.ready, null, { timeout: 20000 });
async function settle(page, ms = 1500) {
  await ready(page);
  await settleHeat(page);
  await page.waitForTimeout(ms);
}

const record = {
  start: (page) => page.evaluate(() => window.__wkp.start()),
  now: (page) => page.evaluate(() => window.__wkp.now()),
  stop: (page) => page.evaluate(() => window.__wkp.stop()),
};

async function clickOrTap(page, sel, touch) {
  const el = page.locator(sel).first();
  await el.waitFor({ state: 'visible' });
  if (touch) await el.tap();
  else await el.click();
}

// The scenarios. Each gets a fresh signed-in page on its preset and returns
// the recorded window's stats.
const SCENARIOS = [
  ...['idle', 'streaming', 'update-staging', 'pairing-1'].map((p) => ({
    id: `load:${p}`,
    group: 'load',
    preset: p,
    // The first 3 s from the page's first frame, cold cache.
    async run(page) {
      await open(page, '/', { waitUntil: 'commit' });
      await ready(page);
      await page.waitForTimeout(3200);
      const log = await record.stop(page);
      return frameStats(log, log.frames[0], log.frames[0] + 3000);
    },
  })),
  {
    id: 'state:idle>streaming>idle',
    group: 'state',
    preset: 'idle',
    async run(page) {
      await open(page, '/');
      await settle(page);
      await record.start(page);
      const t0 = await record.now(page);
      await event('session.begin', { client: 'Living room TV', mode: '3840x2160@60', hdr: true, app: 'Steam', since: new Date().toISOString() });
      await sleep(300);
      await event('display.changed', {});
      await sleep(2700);
      await event('session.end', {});
      await sleep(300);
      await event('display.changed', {});
      await sleep(2700);
      const log = await record.stop(page);
      return frameStats(log, t0);
    },
  },
  ...[
    ['/', 'idle'],
    ['/system/power', 'idle'],
    ['/system/updates', 'idle'],
    ['/devices', 'pairing-1'],
    ['/screen', 'idle'],
  ].map(([path, preset]) => ({
    id: `scroll:${path}`,
    group: 'scroll',
    preset,
    // 3 s of scrolling, turning at either end: wheel input, 100 px every
    // 50 ms, or on a touch viewport (mobile WebKit takes no wheel and
    // Playwright has no touch drag) 12 px a frame from the page itself.
    async run(page, touch) {
      await open(page, path);
      await settle(page);
      const vp = page.viewportSize();
      await page.mouse.move(vp.width / 2, vp.height / 2);
      const room = await page.evaluate(() => document.scrollingElement.scrollHeight - innerHeight);
      await record.start(page);
      const t0 = await record.now(page);
      let moved = 0;
      if (touch) {
        moved = await page.evaluate(
          () =>
            new Promise((done) => {
              const room = document.scrollingElement.scrollHeight - innerHeight;
              const end = performance.now() + 3000;
              let dir = 1;
              let moved = 0;
              const step = () => {
                if (performance.now() >= end) return done(moved);
                if (dir > 0 && scrollY >= room - 2) dir = -1;
                else if (dir < 0 && scrollY <= 2) dir = 1;
                scrollBy(0, 12 * dir);
                moved += 12;
                requestAnimationFrame(step);
              };
              requestAnimationFrame(step);
            }),
        );
      } else {
        let dir = 1;
        const end = Date.now() + 3000;
        while (Date.now() < end) {
          const y = await page.evaluate(() => scrollY);
          if (dir > 0 && y >= room - 2) dir = -1;
          else if (dir < 0 && y <= 2) dir = 1;
          await page.mouse.wheel(0, 100 * dir);
          moved += 100;
          await sleep(50);
        }
      }
      await sleep(200);
      const log = await record.stop(page);
      return { ...frameStats(log, t0), scrollRoom: room, scrolledPx: moved };
    },
  })),
  {
    id: 'power-sheet',
    group: 'sheet',
    preset: 'idle',
    async run(page, touch) {
      await open(page, '/');
      await settle(page);
      await page.locator('#home-power:enabled').waitFor();
      await record.start(page);
      const t0 = await record.now(page);
      await clickOrTap(page, '#home-power', touch);
      await page.locator('#power-sheet[open]').waitFor();
      await page.waitForTimeout(1200);
      const log = await record.stop(page);
      return frameStats(log, t0);
    },
  },
  {
    id: 'hold-to-confirm',
    group: 'hold',
    preset: 'idle',
    // A 1.2 s press on Restart (the full hold), then 800 ms of what follows.
    async run(page) {
      await open(page, '/');
      await settle(page);
      await page.locator('#home-power:enabled').waitFor();
      await page.click('#home-power');
      await page.locator('#power-sheet[open]').waitFor();
      await page.waitForTimeout(800);
      await page.locator('#home-reboot').hover();
      await record.start(page);
      const t0 = await record.now(page);
      await page.mouse.down();
      await page.waitForTimeout(1200);
      await page.mouse.up();
      await page.waitForTimeout(800);
      const log = await record.stop(page);
      return frameStats(log, t0);
    },
    // The fake goes down a second after the restart is asked for: wait for
    // that, then wake it, so the next preset is not replaced by the boot.
    after: async () => {
      for (let i = 0; i < 40; i++) {
        const st = await fetch(`${BASE}/__dev/state`).then((r) => r.json(), () => ({}));
        if (st.down) break;
        await sleep(100);
      }
      await devCall('down', { seconds: 0 }).catch(() => {});
      await up();
    },
  },
  {
    id: 'pinpad',
    group: 'pinpad',
    preset: 'pairing-1',
    async run(page, touch) {
      await open(page, '/');
      await settle(page);
      await page.locator('#hero-actions button').waitFor();
      await record.start(page);
      const t0 = await record.now(page);
      await clickOrTap(page, '#hero-actions button', touch);
      await page.locator('#pinpad[open]').waitFor();
      await page.waitForTimeout(600);
      const pad = await page.locator('#keypad').isVisible();
      for (const d of '1234') {
        if (pad) await clickOrTap(page, `#keypad [data-key="${d}"]`, touch);
        else await page.keyboard.type(d);
        await page.waitForTimeout(250);
      }
      await page.waitForTimeout(1500);
      const log = await record.stop(page);
      return frameStats(log, t0);
    },
  },
  {
    id: 'tab-switch',
    group: 'tab',
    preset: 'idle',
    // Home → Devices across documents; the log carries over, so the gap
    // while the next page loads is one of the frames.
    async run(page, touch) {
      await open(page, '/');
      await settle(page);
      await record.start(page);
      const t0 = await record.now(page);
      await clickOrTap(page, '#nav a[href="/devices"]', touch);
      await page.waitForURL((u) => new URL(u).pathname === '/devices');
      await ready(page);
      await page.waitForTimeout(1200);
      const log = await record.stop(page);
      return frameStats(log, t0);
    },
  },
];

// cpuOf maps the pid of every process whose command line contains marker
// (the browser under test and its helpers) to its OS CPU time in s; cpuDelta
// sums the growth of the pids in both samples, so another run's processes
// that come or go do not count.
function cpuOf(marker) {
  const m = new Map();
  try {
    const txt = execFileSync('ps', ['-A', '-o', 'pid=,time=,command='], { encoding: 'utf8', maxBuffer: 1 << 24 });
    for (const line of txt.split('\n')) {
      if (!line.includes(marker)) continue;
      const [pid, t] = line.trim().split(/\s+/);
      let s = 0;
      for (const p of t.split(/[:-]/).map(Number)) s = s * 60 + p;
      m.set(pid, s);
    }
  } catch {
    /* no ps: no OS number */
  }
  return m;
}
const cpuDelta = (a, b) => [...b].reduce((sum, [pid, t]) => sum + (a.has(pid) ? t - a.get(pid) : 0), 0);

async function rest(page, path, cdp, marker) {
  await open(page, path);
  await settle(page, 2000);
  await page.evaluate(() => {
    window.__wkp.stop();
    window.__wkp.pageRaf = 0;
  });
  const m0 = cdp ? Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map((m) => [m.name, m.value])) : null;
  const c0 = cpuOf(marker);
  const w0 = Date.now();
  await page.waitForTimeout(restMs);
  const secs = (Date.now() - w0) / 1000;
  const c1 = cpuOf(marker);
  const m1 = cdp ? Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map((m) => [m.name, m.value])) : null;
  const page1 = await page.evaluate(() => ({
    raf: window.__wkp.pageRaf,
    anims: document
      .getAnimations()
      .filter((a) => a.playState === 'running' && a.effect?.getComputedTiming().endTime === Infinity)
      .map((a) => `${a.effect?.target?.id || a.effect?.target?.className || '?'}:${a.animationName || a.constructor.name}`),
  }));
  const r = { path, secs, pageRafPerSec: page1.raf / secs, infiniteAnimations: page1.anims, osCpuPct: (100 * cpuDelta(c0, c1)) / secs };
  if (m0 && m1) {
    const d = (k) => (m1[k] ?? 0) - (m0[k] ?? 0);
    r.taskPct = (100 * d('TaskDuration')) / secs;
    r.scriptPct = (100 * d('ScriptDuration')) / secs;
    r.layoutPct = (100 * (d('LayoutDuration') + d('RecalcStyleDuration'))) / secs;
  }
  return r;
}

async function launchBrowser() {
  if (args.browser === 'webkit') {
    const b = await webkit.launch({ headless: true });
    return { browser: b, marker: 'ms-playwright/webkit-', version: `WebKit ${b.version()}` };
  }
  if (args.browser === 'chromium') {
    const exe = findChrome();
    const b = await chromium.launch({ headless: true, executablePath: exe, args: ['--use-angle=metal', '--enable-gpu', '--ignore-gpu-blocklist'] });
    return { browser: b, marker: exe.split('/').slice(-3, -2)[0] || 'chrome-headless-shell', version: `Chromium ${b.version()}` };
  }
  throw new Error('--browser is webkit or chromium');
}

async function signIn(browser, vpName) {
  const context = await browser.newContext(contextOpts(vpName));
  const page = await context.newPage();
  await open(page, '/login');
  const ok = await page.evaluate(
    (password) =>
      fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }) }).then((r) => r.ok),
    args.password,
  );
  if (!ok) throw new Error('sign-in failed');
  const state = await context.storageState();
  await context.close();
  return state;
}

const contextOpts = (vpName) => ({ ...VIEWPORTS[vpName] });

const only = list(args.only);
const chosen = SCENARIOS.filter((s) => !only.length || only.some((o) => s.id === o || s.group === o || s.id.startsWith(o)));
const vps = list(args.viewports).filter((v) => VIEWPORTS[v]);

const { browser, marker, version } = await launchBrowser();
const report = { base: BASE, browser: version, runs, target: TARGET, scenarios: [], rest: [] };
try {
  for (const vp of vps) {
    const samples = new Map();
    for (let run = 0; run < runs; run++) {
      const storageState = await signIn(browser, vp);
      for (const s of chosen) {
        await setPreset(s.preset);
        const context = await browser.newContext({ ...contextOpts(vp), storageState });
        await context.addInitScript(REC);
        const page = await context.newPage();
        let r;
        try {
          r = await s.run(page, !!VIEWPORTS[vp].hasTouch);
        } catch (err) {
          r = { error: String(err?.message ?? err).split('\n')[0] };
          await page.screenshot({ path: join(out, `error-${vp}-${run + 1}-${s.id.replace(/[^a-z0-9-]+/gi, '_')}.png`) }).catch(() => {});
        } finally {
          await context.close();
          if (s.after) await s.after();
        }
        if (!samples.has(s.id)) samples.set(s.id, []);
        samples.get(s.id).push(r);
        process.stderr.write(`${vp} run ${run + 1} ${s.id}: ${r.error ? 'ERROR ' + r.error : `${r.fps.toFixed(1)} fps, ${r.slowPct.toFixed(1)}% slow, worst ${r.worst.toFixed(0)} ms, long ${r.longMs.toFixed(0)} ms`}\n`);
      }
    }
    for (const s of chosen) {
      const xs = samples.get(s.id).filter((x) => !x.error);
      const row = { viewport: vp, id: s.id, group: s.group, preset: s.preset, runs: samples.get(s.id) };
      for (const k of ['fps', 'slowPct', 'worst', 'longMs', 'longest']) row[k] = median(xs.map((x) => x[k]));
      row.errors = samples.get(s.id).filter((x) => x.error).map((x) => x.error);
      row.miss = [];
      if (!xs.length) row.miss.push('no run finished');
      else {
        if (!(row.fps >= TARGET.fps)) row.miss.push(`fps ${row.fps.toFixed(1)} < ${TARGET.fps}`);
        if (!(row.slowPct <= TARGET.slowPct)) row.miss.push(`${row.slowPct.toFixed(1)}% > ${TARGET.slowMs} ms`);
        if (!(row.worst <= TARGET.worst)) row.miss.push(`worst ${row.worst.toFixed(0)} ms > ${TARGET.worst}`);
      }
      report.scenarios.push(row);
    }
    // At rest: no rAF loop of ours, 10 s, Home and System › Power.
    if (!only.length || only.includes('rest')) {
      const storageState = await signIn(browser, vp);
      await setPreset('idle');
      for (const path of ['/', '/system/power']) {
        const context = await browser.newContext({ ...contextOpts(vp), storageState });
        await context.addInitScript(REC);
        const page = await context.newPage();
        const cdp = args.browser === 'chromium' ? await context.newCDPSession(page) : null;
        if (cdp) await cdp.send('Performance.enable');
        try {
          report.rest.push({ viewport: vp, ...(await rest(page, path, cdp, marker)) });
        } catch (err) {
          report.rest.push({ viewport: vp, path, error: String(err?.message ?? err).split('\n')[0] });
        } finally {
          await context.close();
        }
      }
    }
  }
} finally {
  await browser.close();
}

// The table, with the other report's numbers beside it when --against.
const other = args.against ? JSON.parse(readFileSync(args.against, 'utf8')) : null;
const key = (r) => `${r.viewport} ${r.id}`;
const was = new Map((other?.scenarios ?? []).map((r) => [key(r), r]));
const f1 = (x) => (Number.isFinite(x) ? x.toFixed(1) : '–');
const f0 = (x) => (Number.isFinite(x) ? x.toFixed(0) : '–');
const lines = [`${version} · ${BASE} · median of ${runs} · target ≥ ${TARGET.fps} fps, ≤ ${TARGET.slowPct}% > ${TARGET.slowMs} ms, worst ≤ ${TARGET.worst} ms`];
const head = ['', 'viewport', 'scenario'.padEnd(26), 'fps'.padStart(5), '>34%'.padStart(6), 'worst'.padStart(6), 'long'.padStart(6)];
if (other) head.push(' | was: fps'.padStart(11), '>34%'.padStart(6), 'worst'.padStart(6), 'long'.padStart(6));
lines.push(head.join(' '));
for (const r of report.scenarios) {
  const cells = [r.miss.length ? 'MISS' : 'ok  ', r.viewport.padEnd(8), r.id.padEnd(26), f1(r.fps).padStart(5), f1(r.slowPct).padStart(6), f0(r.worst).padStart(6), f0(r.longMs).padStart(6)];
  const o = was.get(key(r));
  if (other) cells.push(' |'.padEnd(6) + f1(o?.fps).padStart(5), f1(o?.slowPct).padStart(6), f0(o?.worst).padStart(6), f0(o?.longMs).padStart(6));
  if (r.errors.length) cells.push(`(${r.errors.length} errors: ${r.errors[0]})`);
  lines.push(cells.join(' '));
}
for (const r of report.rest) {
  lines.push(
    r.error
      ? `rest ${r.viewport} ${r.path}: ERROR ${r.error}`
      : `rest ${r.viewport.padEnd(8)} ${r.path.padEnd(14)} page rAF ${f1(r.pageRafPerSec)}/s · OS CPU ${f1(r.osCpuPct)}%` +
          (Number.isFinite(r.taskPct) ? ` · main thread ${f1(r.taskPct)}% (script ${f1(r.scriptPct)}%, style+layout ${f1(r.layoutPct)}%)` : '') +
          ` · infinite animations ${r.infiniteAnimations.length ? r.infiniteAnimations.join(', ') : 'none'}`,
  );
}
const misses = report.scenarios.filter((r) => r.miss.length);
lines.push(misses.length ? `${misses.length} of ${report.scenarios.length} scenarios miss the target` : `ok: all ${report.scenarios.length} scenarios meet the target`);
writeReport(out, report, lines);
console.log(lines.join('\n'));
console.log(JSON.stringify({ browser: version, base: BASE, runs, rows: report.scenarios.map(({ runs: _, ...r }) => r), rest: report.rest, report: join(out, 'report.json') }));
process.exit(misses.length && args.strict ? 1 : 0);
