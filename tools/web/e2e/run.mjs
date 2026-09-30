#!/usr/bin/env node
// The control center's headless browser harness (ARCH §10, MASTER-PLAN §6.3).
//
//   node tools/web/e2e/run.mjs [--ui=legacy|next] [--matrix=smoke|full]
//        [--out=DIR] [--only=ID,…] [--presets=a,b] [--viewports=phone,…]
//        [--schemes=dark,light] [--shots=all|fail|none] [--settle]
//        [--no-flows] [--no-loads] [--concurrency=N] [--list]
//
// It builds internal/web's test binary once, runs the dev server once per
// preset on a free port, and loads each page in headless Chromium from
// http://vapor.local:<port> (an insecure context, like the box). Every load
// is checked for its status, readiness, console, CSP, failed requests,
// overflow, landmarks, focus (and what covers it), target size, axe and the
// contrast of text over the heat fields, from pixels; flows from
// e2e/specs/*.spec.mjs run after. Every shot waits (at most 3 s) for the
// heat fields to be painted at rest; --settle also finishes every finite
// animation first, for captures to compare pixel by pixel. Output goes
// only to --out: report.json, summary.txt, screenshots and server logs.
// Exit 1 on a failure.

import { mkdirSync, readdirSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { launch } from './lib/browser.mjs';
import * as checks from './lib/checks.mjs';
import { flowRuns, hasAlias, loads, PRESETS, preset, VIEWPORTS } from './lib/presets.mjs';
import { list, outDir, parseArgs, pool, slug, writeReport } from './lib/run-helpers.mjs';
import { activeUI, buildTestBinary, startServer } from './lib/server.mjs';
import { ui as uiAdapter } from './lib/ui.mjs';

const here = dirname(fileURLToPath(import.meta.url));

const args = parseArgs(process.argv.slice(2), {
  ui: '',
  matrix: 'smoke',
  out: '',
  only: '',
  presets: '',
  viewports: '',
  schemes: '',
  shots: '',
  settle: false,
  'no-flows': false,
  'no-loads': false,
  concurrency: '4',
  list: false,
});
const uiName = args.ui || activeUI();
const ui = uiAdapter(uiName);
const shots = args.shots || (args.matrix === 'full' ? 'all' : 'fail');
// Before a shot: the heat fields at rest (and with --settle, every finite
// animation finished).
const still = (page) => checks.settleHeat(page, { finish: !!args.settle });

// ---- plan

async function loadSpecs() {
  const dir = join(here, 'specs');
  let files = [];
  try {
    files = readdirSync(dir).filter((f) => f.endsWith('.spec.mjs')).sort();
  } catch {
    return [];
  }
  const flows = [];
  for (const f of files) {
    const mod = await import(pathToFileURL(join(dir, f)).href);
    for (const flow of mod.default ?? []) flows.push({ ...flow, spec: basename(f, '.spec.mjs') });
  }
  return flows;
}

function selectFlows(all) {
  const only = list(args.only);
  return all.filter((f) => {
    if (f.ui && !f.ui.includes(uiName)) return false;
    if (!only.length) return true;
    return only.some((o) => f.id === o || f.id.startsWith(o) || f.spec === o || f.spec === basename(o, '.spec.mjs'));
  });
}

let presets = PRESETS;
if (args.presets) presets = list(args.presets).map(preset);
let plannedLoads = args['no-loads'] || (args.only && !args.presets) ? [] : loads(uiName, args.matrix, presets);
if (args.viewports) plannedLoads = plannedLoads.filter((l) => list(args.viewports).includes(l.viewport));
if (args.schemes) plannedLoads = plannedLoads.filter((l) => list(args.schemes).includes(l.scheme));
const plannedFlows = args['no-flows'] ? [] : selectFlows(await loadSpecs());

if (args.list) {
  for (const l of plannedLoads) console.log(`load ${l.preset} ${l.path} ${l.viewport} ${l.scheme}${l.motion === 'reduce' ? ' reduced-motion' : ''}${l.forcedColors ? ' forced-colors' : ''}`);
  for (const f of plannedFlows) for (const r of flowRuns(args.matrix)) console.log(`flow ${f.id} (${f.spec}) ${f.preset ?? 'idle'} ${r.viewport} ${r.scheme}`);
  process.exit(0);
}

// ---- run

const out = outDir(args.out, `cc-${args.matrix}`);
mkdirSync(join(out, 'shots'), { recursive: true });
const started = new Date();
console.log(`e2e: ui=${uiName} matrix=${args.matrix} out=${out}`);
const bin = buildTestBinary(out);
const { browser, executablePath, version } = await launch();
console.log(`e2e: ${version} at ${executablePath}`);

const report = { ui: uiName, matrix: args.matrix, started: started.toISOString(), browser: version, servers: [], skipped: [], loads: [], flows: [], strays: [] };
let serverNo = 0;

// A spec that arms a waiter (page.waitForRequest) before an action that then
// throws leaves the waiter to reject later with nobody listening. That flow
// has already failed; the stray rejection must not take the whole run (and
// its report) down with it, so it is recorded and the run goes on.
process.on('unhandledRejection', (err) => {
  const line = String(err?.message ?? err).split('\n')[0];
  report.strays.push(line);
  console.error(`e2e: stray rejection (a waiter a failed flow left behind): ${line}`);
});

async function server(p) {
  const s = await startServer({ bin, p, ui: uiName, logDir: join(out, 'servers'), stateDir: join(out, 'state', `${p.name}-${serverNo++}`) });
  if (s.served !== uiName) {
    await s.stop();
    throw new Error(`the dev server serves the ${s.served} UI, not ${uiName}: --ui=next needs the dev server v2 (VOS_WEB_UI) and pages in the next set`);
  }
  report.servers.push({ preset: p.name, origin: s.origin, presets: s.features.presets, log: s.log });
  return s;
}

function contextOptions(cell) {
  const v = VIEWPORTS[cell.viewport];
  return {
    viewport: { width: v.width, height: v.height },
    deviceScaleFactor: v.deviceScaleFactor,
    isMobile: !!v.isMobile,
    hasTouch: !!v.hasTouch,
    colorScheme: cell.scheme,
    reducedMotion: cell.motion ?? 'no-preference',
    forcedColors: cell.forcedColors ?? 'none',
  };
}

// signIn gives the context a session on a dev server v2, which runs the
// real sign-in (password vaporvapor). The older fake is signed in globally.
async function signIn(context, s, p) {
  if (!s.features.presets || ['entry', 'installer'].includes(p.group)) return;
  const page = await context.newPage();
  await page.goto(`${s.origin}/login`);
  await page.evaluate(() =>
    fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: 'vaporvapor' }) }),
  );
  await page.close();
}

async function runLoad(context, s, l) {
  const page = await context.newPage();
  const log = checks.watch(page, s.origin);
  const result = { ...l, allow: undefined, url: s.origin + l.path, checks: {} };
  const c = result.checks;
  try {
    const resp = await page.goto(s.origin + l.path, { waitUntil: 'load', timeout: 15_000 });
    result.final = page.url().replace(s.origin, '');
    c.status = { ok: resp?.status() === 200, details: resp?.status() === 200 ? [] : [`HTTP ${resp?.status()}`] };
    const ready = await page.waitForFunction(ui.ready, null, { timeout: 3000 }).then(() => true, () => false);
    const failed = ready && (await page.evaluate(ui.failed));
    c.ready = { ok: ready && !failed, details: ready ? (failed ? ['the page shows its fatal error'] : []) : ['not ready within 3 s'] };
    await page.waitForTimeout(300);
    const bare = await page.evaluate(ui.bare);
    let shot = '';
    if (shots === 'all') {
      shot = join('shots', `${slug(l.preset, l.path, l.viewport, l.scheme, l.motion, l.forcedColors ?? '')}.png`);
      await still(page);
      await page.screenshot({ path: join(out, shot), fullPage: true });
    }
    c.overflow = await checks.overflow(page);
    c.landmarks = await checks.landmarks(page, !bare);
    c.axe = await checks.axe(page, ui.axeKnown);
    c.targets = await checks.targets(page);
    c.focus = await checks.focus(page);
    if (!l.forcedColors && ['phone', 'desktop'].includes(l.viewport)) c.heat = await checks.heatContrast(page);
    if (args.matrix === 'full' && l.viewport === 'phone' && l.scheme === 'dark' && l.motion === 'no-preference') c.clipped = await checks.clipped(page);
    c.perf = await checks.perf(page);
    Object.assign(c, await checks.logChecks(page, log, l.allow));
    result.failed = Object.entries(c).filter(([k, v]) => !v.ok && ui.blocking.has(k)).map(([k]) => k);
    if (result.failed.length && shots === 'fail') {
      shot = join('shots', `${slug(l.preset, l.path, l.viewport, l.scheme, l.motion, l.forcedColors ?? '')}.png`);
      await still(page);
      await page.screenshot({ path: join(out, shot), fullPage: true }).catch((e) => console.error(`e2e: screenshot failed: ${e.message}`));
    }
    result.screenshot = shot;
  } catch (err) {
    c.harness = { ok: false, details: [String(err?.message ?? err)] };
    result.failed = ['harness'];
  } finally {
    await page.close();
  }
  return result;
}

async function runPresetLoads([p, cells]) {
  const s = await server(p);
  try {
    const groups = new Map();
    for (const l of cells) {
      const key = `${l.viewport}|${l.scheme}|${l.motion}|${l.forcedColors ?? ''}`;
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(l);
    }
    for (const group of groups.values()) {
      const context = await browser.newContext(contextOptions(group[0]));
      await context.addInitScript(checks.INIT);
      await signIn(context, s, p);
      for (const l of group) {
        const r = await runLoad(context, s, l);
        report.loads.push(r);
        const mark = r.failed.length ? `FAIL ${r.failed.join(',')}` : 'ok';
        console.log(`load ${mark.padEnd(4)} ${p.name} ${l.path} ${l.viewport}/${l.scheme}${l.motion === 'reduce' ? '/reduced' : ''}${l.forcedColors ? '/forced' : ''}${r.final && r.final !== l.path ? ` → ${r.final}` : ''}`);
      }
      await context.close();
    }
  } finally {
    await s.stop();
  }
}

async function runFlow(flow, cell) {
  const p = preset(flow.preset ?? 'idle');
  const result = { id: flow.id, spec: flow.spec, preset: p.name, ...cell, steps: [] };
  let s;
  let context;
  try {
    s = await server(p);
    context = await browser.newContext(contextOptions(cell));
    await context.addInitScript(checks.INIT);
    await signIn(context, s, p);
    const page = await context.newPage();
    const log = checks.watch(page, s.origin);
    const t = {
      page,
      context,
      server: s,
      origin: s.origin,
      ui: uiName,
      url: (path) => s.origin + path,
      ready: () => page.waitForFunction(ui.ready, null, { timeout: 5000 }),
      step: async (name, fn) => {
        result.steps.push(name);
        await fn();
      },
    };
    await flow.run(t);
    const logs = await checks.logChecks(page, log, flow.allow ?? []);
    const bad = Object.entries(logs).filter(([, v]) => !v.ok);
    result.ok = bad.length === 0;
    result.details = bad.flatMap(([k, v]) => v.details.map((d) => `${k}: ${d}`));
    if (!result.ok || shots === 'all') {
      result.screenshot = join('shots', `flow-${slug(flow.id, cell.viewport, cell.scheme)}.png`);
      await still(page);
      await page.screenshot({ path: join(out, result.screenshot), fullPage: true }).catch((e) => console.error(`e2e: screenshot failed: ${e.message}`));
    }
  } catch (err) {
    result.ok = false;
    result.details = [String(err?.stack ?? err)];
  } finally {
    await context?.close();
    await s?.stop();
  }
  console.log(`flow ${result.ok ? 'ok  ' : 'FAIL'} ${flow.id} ${cell.viewport}/${cell.scheme}${result.ok ? '' : `: ${result.details[0]?.split('\n')[0]}`}`);
  return result;
}

let exitCode = 0;
try {
  // A dev server from before v2 knows only the VOS_WEB_* switches: learn that
  // from one server, then skip the presets it cannot show.
  const probe = await server(preset('idle'));
  const canPreset = probe.features.presets;
  await probe.stop();
  report.servers.pop();
  const byPreset = new Map();
  for (const l of plannedLoads) {
    const p = preset(l.preset);
    if (!canPreset && !hasAlias(p)) {
      if (!report.skipped.includes(p.name)) report.skipped.push(p.name);
      continue;
    }
    if (!byPreset.has(p)) byPreset.set(p, []);
    byPreset.get(p).push(l);
  }
  if (report.skipped.length) console.log(`e2e: this dev server has no presets yet (dev server v2); skipped ${report.skipped.join(', ')}`);
  const n = Math.max(1, Number(args.concurrency) || 4);
  await pool([...byPreset.entries()], n, runPresetLoads);

  const runnable = plannedFlows.filter((f) => canPreset || hasAlias(preset(f.preset ?? 'idle')));
  for (const f of plannedFlows) if (!runnable.includes(f)) report.skipped.push(`flow ${f.id}`);
  const cells = runnable.flatMap((f) => flowRuns(args.matrix).map((c) => [f, c]));
  report.flows = await pool(cells, n, ([f, c]) => runFlow(f, c));
} catch (err) {
  console.error(`e2e: ${err?.stack ?? err}`);
  report.error = String(err?.message ?? err);
  exitCode = 2;
} finally {
  await browser.close().catch(() => {});
}

report.finished = new Date().toISOString();
const failedLoads = report.loads.filter((l) => l.failed.length);
const failedFlows = report.flows.filter((f) => !f.ok);
const summary = [
  `ui=${uiName} matrix=${args.matrix} browser=${version}`,
  `${report.loads.length} loads, ${failedLoads.length} failed; ${report.flows.length} flow runs, ${failedFlows.length} failed; skipped: ${report.skipped.join(', ') || 'none'}`,
];
for (const l of failedLoads) {
  summary.push(`FAIL ${l.preset} ${l.path} ${l.viewport}/${l.scheme}${l.motion === 'reduce' ? '/reduced' : ''}${l.forcedColors ? '/forced' : ''}`);
  for (const k of l.failed) for (const d of l.checks[k].details.slice(0, 5)) summary.push(`  ${k}: ${d}`);
}
for (const f of failedFlows) summary.push(`FAIL flow ${f.id} ${f.viewport}/${f.scheme}`, ...f.details.slice(0, 5).map((d) => `  ${d.split('\n')[0]}`));
if (report.strays.length) summary.push(`stray rejections left by failed flows: ${report.strays.length}`, ...report.strays.slice(0, 5).map((d) => `  ${d}`));
const warn = new Map();
for (const l of report.loads) {
  for (const [k, v] of Object.entries(l.checks)) {
    if (v.ok || ui.blocking.has(k)) continue;
    for (const d of v.details) warn.set(`${k}: ${d}`, (warn.get(`${k}: ${d}`) ?? 0) + 1);
  }
  for (const w of [...(l.checks.axe?.warnings ?? []), ...(l.checks.targets?.warnings ?? []), ...(l.checks.heat?.warnings ?? [])]) {
    const key = w.replace(/ \(\d+× e\.g\. .*?\)/, '');
    warn.set(key, (warn.get(key) ?? 0) + 1);
  }
}
if (warn.size) {
  const lines = [...warn].map(([d, n]) => `  ${d} (${n}×)`);
  summary.push(`reported, not failing for the ${uiName} UI:`, ...lines.slice(0, 40));
  if (lines.length > 40) summary.push(`  … ${lines.length - 40} more in report.json`);
}
writeReport(out, report, summary);
console.log(summary.join('\n'));
console.log(`e2e: report ${join(out, 'report.json')}`);
if (exitCode === 0 && (failedLoads.length || failedFlows.length)) exitCode = 1;
process.exit(exitCode);
