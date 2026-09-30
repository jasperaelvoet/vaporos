#!/usr/bin/env node
// Runtime budgets (ARCH §11, MASTER-PLAN §6.3): the control center measured
// the way a mid-range phone on home Wi-Fi sees it, headless.
//
//   node tools/web/e2e/perf.mjs [--runs=5] [--paths=/,/devices,…] [--out=DIR]
//        [--ui=next] [--report-only]
//
// Each run is Chromium with CDP CPU throttling 4x and 20 ms / 30 Mbit/s down,
// 10 up; each number is the p75 of --runs runs:
//   cold load (empty cache) of each page: FCP ≤ 600 ms, LCP ≤ 1,000 ms, no
//     long task over 50 ms before the page is ready;
//   the same with the dev server's realistic API latency on
//     (/__dev/latency): CLS ≤ 0.02, because the skeletons must hold the
//     final layout while the answers come in;
//   warm tab switches (Home → Devices → Screen → System → a System
//     sub-page and back): the new page's first paint ≤ 250 ms after the
//     click, its view transition ≤ 350 ms, and no static request.
// Exit 1 on any miss unless --report-only (CI runners are noisy).

import { join } from 'node:path';

import { launch } from './lib/browser.mjs';
import { INIT } from './lib/checks.mjs';
import { dev } from './lib/dev.mjs';
import { APP_TOPICS, preset, ROUTES } from './lib/presets.mjs';
import { list, outDir, parseArgs, writeReport } from './lib/run-helpers.mjs';
import { activeUI, buildTestBinary, startServer } from './lib/server.mjs';
import { ui as uiAdapter } from './lib/ui.mjs';

export const BUDGETS = { fcp: 600, lcp: 1000, longTask: 50, cls: 0.02, switchFcp: 250, vt: 350, staticPerSwitch: 0 };
const PROFILE = { cpu: 4, latency: 20, down: (30e6 / 8) | 0, up: (10e6 / 8) | 0 };

const args = parseArgs(process.argv.slice(2), { runs: '5', paths: '', out: '', ui: '', 'report-only': false });
const uiName = args.ui || activeUI();
const ui = uiAdapter(uiName);
const runs = Math.max(1, Number(args.runs) || 5);
const paths = list(args.paths).length ? list(args.paths) : APP_TOPICS[uiName].map((t) => ROUTES[uiName][t]).filter((p, i, all) => all.indexOf(p) === i);
const out = outDir(args.out, 'cc-perf');

// PERF_INIT adds long tasks and the view transition to INIT's observers.
const PERF_INIT = `${INIT}
(() => {
  const w = window;
  w.__vos.long = [];
  w.__vos.vt = null;
  try {
    new PerformanceObserver((l) => { for (const e of l.getEntries()) w.__vos.long.push({ at: e.startTime, ms: e.duration }); })
      .observe({ type: 'longtask', buffered: true });
  } catch {}
  addEventListener('pagereveal', (e) => {
    if (!e.viewTransition) return;
    const vt = e.viewTransition;
    const start = performance.now();
    w.__vos.vt = { types: [] };
    // head.js names the types in its own pagereveal handler, after this one.
    const done = (ms) => { w.__vos.vt.ms = ms; w.__vos.vt.types = [...(vt.types || [])]; };
    vt.finished.then(() => done(performance.now() - start), () => done(-1));
  });
})();`;

export const p75 = (xs) => {
  const s = xs.filter((x) => Number.isFinite(x)).sort((a, b) => a - b);
  return s.length ? s[Math.min(s.length - 1, Math.ceil(s.length * 0.75) - 1)] : NaN;
};

// throttled applies the profile to page and counts the static files that
// really went to the network (not those the HTTP or memory cache served).
async function throttled(context, page) {
  const cdp = await context.newCDPSession(page);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: PROFILE.cpu });
  await cdp.send('Network.enable');
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: PROFILE.latency, downloadThroughput: PROFILE.down, uploadThroughput: PROFILE.up });
  const net = { sent: new Map(), cached: new Set(), reset() { this.sent.clear(); this.cached.clear(); } };
  cdp.on('Network.requestWillBeSent', (e) => e.request.url.includes('/static/') && net.sent.set(e.requestId, e.request.url));
  cdp.on('Network.requestServedFromCache', (e) => net.cached.add(e.requestId));
  cdp.on('Network.responseReceived', (e) => (e.response.fromDiskCache || e.response.fromPrefetchCache) && net.cached.add(e.requestId));
  net.count = () => [...net.sent.keys()].filter((id) => !net.cached.has(id)).length;
  return net;
}

async function signedInContext(browser, s) {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  await context.addInitScript(PERF_INIT);
  const login = await context.newPage();
  await login.goto(`${s.origin}/login`);
  await login.evaluate(() =>
    fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: 'vaporvapor' }) }),
  );
  await login.close();
  return context;
}

const metrics = (page) =>
  page.evaluate(() => {
    const fcp = performance.getEntriesByName('first-contentful-paint')[0];
    const ready = window.__vosReadyAt || performance.now();
    return {
      fcp: fcp ? fcp.startTime : NaN,
      lcp: window.__vos.lcp || NaN,
      cls: window.__vos.cls,
      longest: Math.max(0, ...window.__vos.long.filter((t) => t.at < ready).map((t) => t.ms)),
      vt: window.__vos.vt,
    };
  });

async function waitReady(page) {
  await page.waitForFunction(ui.ready, null, { timeout: 15000 });
  await page.evaluate(() => (window.__vosReadyAt = performance.now()));
  // LCP and layout shifts settle once the answers are painted.
  await page.waitForTimeout(800);
}

// cold: every page from an empty cache, runs times; latency: the dev
// server's realistic API delays on, for CLS.
async function cold(browser, s, path, latency) {
  const samples = [];
  for (let i = 0; i < runs; i++) {
    const context = await signedInContext(browser, s);
    try {
      const page = await context.newPage();
      await throttled(context, page);
      await page.goto(s.origin + path, { waitUntil: 'load' });
      await waitReady(page);
      if (latency) await page.waitForTimeout(3000);
      samples.push(await metrics(page));
    } finally {
      await context.close();
    }
  }
  return samples;
}

// warm: one context walks the tabs; each hop is measured on the page it
// lands on, and counts the static files it fetched.
async function warm(browser, s) {
  const hops = [];
  const context = await signedInContext(browser, s);
  try {
    const page = await context.newPage();
    const net = await throttled(context, page);
    const route = ['/', '/devices', '/screen', '/system', '/system/power', '/system', '/'];
    for (let pass = 0; pass < runs; pass++) {
      await page.goto(s.origin + route[0]);
      await waitReady(page);
      for (const to of route.slice(1)) {
        net.reset();
        const from = new URL(page.url()).pathname;
        const link = page.locator(`#nav a[href="${to}"], a.back[href="${to}"], .row-link[href="${to}"], a[href="${to}"]`).first();
        await link.evaluate((el) => el.scrollIntoView({ block: 'center' }));
        const t0 = Date.now();
        await link.click();
        await page.waitForURL((u) => new URL(u).pathname === to);
        await waitReady(page);
        const m = await metrics(page);
        hops.push({ from, to, pass, clickToNav: Date.now() - t0, fcp: m.fcp, vt: m.vt && m.vt.ms, vtTypes: m.vt && m.vt.types, statics: net.count() });
      }
    }
  } finally {
    await context.close();
  }
  return hops;
}

const bin = buildTestBinary(out);
const { browser, version } = await launch();
const report = { ui: uiName, browser: version, profile: PROFILE, budgets: BUDGETS, runs, pages: [], hops: [] };
const lines = [`ui=${uiName} ${version} · CPU ${PROFILE.cpu}x · ${PROFILE.latency} ms · 30/10 Mbit · p75 of ${runs}`];
const misses = [];
try {
  const s = await startServer({ bin, p: preset('idle'), ui: uiName, logDir: join(out, 'servers'), stateDir: join(out, 'state', 'idle') });
  try {
    for (const path of paths) {
      const fast = await cold(browser, s, path, false);
      await dev(s, 'latency', { enabled: true });
      const slow = await cold(browser, s, path, true);
      await dev(s, 'latency', { enabled: false });
      const row = {
        path,
        fcp: Math.round(p75(fast.map((x) => x.fcp))),
        lcp: Math.round(p75(fast.map((x) => x.lcp))),
        longTask: Math.round(p75(fast.map((x) => x.longest))),
        cls: Number(p75(slow.map((x) => x.cls)).toFixed(3)),
        samples: { fast, slow },
      };
      report.pages.push(row);
      const bad = [];
      if (!(row.fcp <= BUDGETS.fcp)) bad.push(`FCP ${row.fcp} ms > ${BUDGETS.fcp}`);
      if (!(row.lcp <= BUDGETS.lcp)) bad.push(`LCP ${row.lcp} ms > ${BUDGETS.lcp}`);
      if (!(row.longTask <= BUDGETS.longTask)) bad.push(`a ${row.longTask} ms task before ready > ${BUDGETS.longTask}`);
      if (!(row.cls <= BUDGETS.cls)) bad.push(`CLS ${row.cls} with API latency > ${BUDGETS.cls}`);
      lines.push(`${bad.length ? 'MISS' : 'ok  '} ${path.padEnd(16)} FCP ${row.fcp} · LCP ${row.lcp} · long task ${row.longTask} · CLS ${row.cls}${bad.length ? ` (${bad.join('; ')})` : ''}`);
      misses.push(...bad.map((b) => `${path}: ${b}`));
    }
    report.hops = await warm(browser, s);
    const byHop = new Map();
    for (const h of report.hops) {
      const k = `${h.from} → ${h.to}`;
      if (!byHop.has(k)) byHop.set(k, []);
      byHop.get(k).push(h);
    }
    for (const [k, hs] of byHop) {
      const fcp = Math.round(p75(hs.map((h) => h.fcp)));
      const vt = Math.round(p75(hs.map((h) => h.vt)));
      const statics = Math.max(...hs.slice(1).map((h) => h.statics), 0);
      const types = hs.find((h) => h.vtTypes)?.vtTypes?.join(',') || 'none';
      const bad = [];
      if (!(fcp <= BUDGETS.switchFcp)) bad.push(`first paint ${fcp} ms > ${BUDGETS.switchFcp}`);
      if (Number.isFinite(vt) && vt > BUDGETS.vt) bad.push(`view transition ${vt} ms > ${BUDGETS.vt}`);
      if (statics > BUDGETS.staticPerSwitch) bad.push(`${statics} static requests on a warm hop`);
      lines.push(`${bad.length ? 'MISS' : 'ok  '} ${k.padEnd(30)} first paint ${fcp} · VT ${Number.isFinite(vt) ? vt : '–'} (${types}) · static ${statics}${bad.length ? ` (${bad.join('; ')})` : ''}`);
      misses.push(...bad.map((b) => `${k}: ${b}`));
    }
  } finally {
    await s.stop();
  }
} catch (err) {
  console.error(`perf: ${err?.stack ?? err}`);
  misses.push(`harness: ${err?.message ?? err}`);
} finally {
  await browser.close();
}
lines.push(misses.length ? `${misses.length} budget misses${args['report-only'] ? ' (reported only)' : ''}` : 'ok: every runtime budget holds');
writeReport(out, report, lines);
console.log(lines.join('\n'));
process.exit(misses.length && !args['report-only'] ? 1 : 0);
