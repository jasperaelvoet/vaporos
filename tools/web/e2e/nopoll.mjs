#!/usr/bin/env node
// No polling (G-12): once Home has loaded, the page may only keep its one
// /api/v1/events stream open (and ping). Idle power-off counts web activity,
// so a page that polls would keep the PC awake forever.
//
//   node tools/web/e2e/nopoll.mjs [--seconds=70] [--clock=MINUTES] [--ui=legacy|next]
//        [--preset=idle] [--path=/] [--out=DIR]
//
// --seconds waits in real time. --clock=10 instead installs a fake clock
// and runs ten minutes of timers in a moment (flow 9, ARCH §10.4).

import { join } from 'node:path';

import { launch } from './lib/browser.mjs';
import { INIT } from './lib/checks.mjs';
import { preset } from './lib/presets.mjs';
import { outDir, parseArgs, writeReport } from './lib/run-helpers.mjs';
import { activeUI, buildTestBinary, startServer } from './lib/server.mjs';
import { ui as uiAdapter } from './lib/ui.mjs';

const args = parseArgs(process.argv.slice(2), { seconds: '70', clock: '', ui: '', preset: 'idle', path: '/', out: '' });
const uiName = args.ui || activeUI();
const ui = uiAdapter(uiName);
const out = outDir(args.out, 'cc-nopoll');
const allowedAfterLoad = [/\/api\/v1\/events(\?|$)/, /\/api\/v1\/ping(\?|$)/];

const bin = buildTestBinary(out);
const s = await startServer({ bin, p: preset(args.preset), ui: uiName, logDir: join(out, 'servers'), stateDir: join(out, 'state') });
const { browser, version } = await launch();
let code = 0;
const report = { ui: uiName, preset: args.preset, path: args.path, browser: version, requests: [], offending: [] };
try {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  await context.addInitScript(INIT);
  const page = await context.newPage();
  if (args.clock) await page.clock.install();
  const requests = [];
  page.on('request', (r) => requests.push({ at: Date.now(), method: r.method(), url: r.url().replace(s.origin, '') }));
  await page.goto(s.origin + args.path, { waitUntil: 'load' });
  await page.waitForFunction(ui.ready, null, { timeout: 5000 });
  // Let the page's first GETs finish, then count from here.
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(1000);
  const loadedAt = Date.now();
  if (args.clock) {
    await page.clock.runFor(Number(args.clock) * 60_000);
    await page.waitForTimeout(1000);
  } else {
    await page.waitForTimeout(Number(args.seconds) * 1000);
  }
  const after = requests.filter((r) => r.at >= loadedAt);
  report.requests = requests;
  report.offending = after.filter((r) => r.url.startsWith('/api/') && !allowedAfterLoad.some((re) => re.test(r.url)));
  const events = after.filter((r) => /\/api\/v1\/events/.test(r.url)).length;
  const lines = [
    `ui=${uiName} preset=${args.preset} path=${args.path} ${args.clock ? `fake clock ${args.clock} min` : `${args.seconds} s real time`}`,
    `${requests.length} requests in all, ${after.length} after load (${events} to /events)`,
    ...report.offending.map((r) => `POLL ${r.method} ${r.url}`),
  ];
  if (report.offending.length) {
    code = 1;
    lines.push('FAIL: the page made API requests after load other than /events and /ping');
  } else {
    lines.push('ok: only /events and /ping after load');
  }
  writeReport(out, report, lines);
  console.log(lines.join('\n'));
} catch (err) {
  console.error(`nopoll: ${err?.stack ?? err}`);
  code = 2;
} finally {
  await browser.close();
  await s.stop();
}
process.exit(code);
