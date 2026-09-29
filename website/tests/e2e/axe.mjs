#!/usr/bin/env node
// Accessibility gate (G-16): axe-core on every page of a served export, plus
// a keyboard pass. Headless only.
//
//   node tests/e2e/axe.mjs <baseUrl> [--out=DIR] [--pages=/,/download/,…] [--widths=1440,768,390]
//        [--reduced-motion] [--release=<release.json>|none]
//
// Per page and width it scrolls through the page (so scroll-revealed content
// has its final look), returns to the top, waits for animations and runs
// axe with the WCAG 2.2 AA tags plus best practices, excluding iframes (the
// live demo gets its own run). Serious and critical violations fail; moderate
// and minor ones are listed. The keyboard pass tabs through the page: the
// first stop must be the skip link, and every stop must be visible and show a
// focus indicator (an outline or a box shadow that the unfocused element
// lacks). Writes <out>/axe.json when --out is given.
import { readFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { join, resolve } from 'node:path';
import { chromium } from 'playwright-core';
import { headlessShell } from '../../scripts/chrome.mjs';

const argv = process.argv.slice(2);
const opt = Object.fromEntries(
  argv.filter((a) => a.startsWith('--')).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.slice(2), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
const baseArg = argv.find((a) => !a.startsWith('--'));
if (!baseArg) {
  console.error('usage: node tests/e2e/axe.mjs <baseUrl> [--out=DIR] [--pages=/,/download/] [--widths=1440,768,390] [--reduced-motion]');
  process.exit(2);
}
const base = baseArg.endsWith('/') ? baseArg : `${baseArg}/`;
const list = (v, d) => (typeof v === 'string' && v ? v.split(',').map((s) => s.trim()).filter(Boolean) : d);
const PAGES = list(opt.pages, ['/', '/download/', '/install/', '/faq/', '/nope/']);
const WIDTHS = list(opt.widths, ['1440', '768', '390']).map(Number);
const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'];
const BLOCKING = new Set(['serious', 'critical']);
const MAX_TABS = 60;

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8');
const AXE_VERSION = JSON.parse(readFileSync(require.resolve('axe-core/package.json'), 'utf8')).version;

const release = opt.release && opt.release !== 'none' ? JSON.parse(readFileSync(resolve(String(opt.release)), 'utf8')) : null;
async function stubGitHub(ctx) {
  await ctx.route('https://api.github.com/**', (route) => {
    const url = route.request().url();
    const headers = { 'access-control-allow-origin': '*', 'content-type': 'application/json; charset=utf-8' };
    if (release && /\/releases\/latest(\?|$)/.test(url)) return route.fulfill({ status: 200, headers, body: JSON.stringify(release) });
    if (/\/releases(\?|$)/.test(url)) return route.fulfill({ status: 200, headers, body: JSON.stringify(release ? [release] : []) });
    return route.fulfill({ status: 404, headers, body: '{"message":"Not Found"}' });
  });
}

// Runs in the page: scroll to the end in viewport steps and back, then wait
// for the finite animations to finish.
async function settle(page) {
  await page.evaluate(async () => {
    const wait = (ms) => new Promise((r) => setTimeout(r, ms));
    const doc = document.scrollingElement || document.documentElement;
    for (let y = 0; y < doc.scrollHeight; y += Math.round(innerHeight * 0.8)) {
      if (window.lenis?.scrollTo) window.lenis.scrollTo(y, { immediate: true, force: true });
      window.scrollTo({ top: y, behavior: 'instant' });
      await wait(120);
    }
    if (window.lenis?.scrollTo) window.lenis.scrollTo(0, { immediate: true, force: true });
    window.scrollTo({ top: 0, behavior: 'instant' });
    const deadline = performance.now() + 4000;
    const finite = () =>
      document.getAnimations().filter((a) => a.playState === 'running' && Number.isFinite(a.effect?.getComputedTiming?.().endTime));
    while (finite().length && performance.now() < deadline) await wait(100);
    await wait(400);
  });
}

// Runs in the page: tab stops from the keyboard, in order.
async function keyboard(page) {
  const stops = [];
  // Transitions off, so a focus style reads at its end value both ways.
  await page.addStyleTag({ content: '*,*::before,*::after{transition:none!important;animation-duration:0s!important}' });
  await page.evaluate(() => {
    document.activeElement?.blur?.();
    window.scrollTo({ top: 0, behavior: 'instant' });
  });
  let first = null;
  for (let i = 0; i < MAX_TABS; i++) {
    await page.keyboard.press('Tab');
    const s = await page.evaluate(() => {
      const el = document.activeElement;
      if (!el || el === document.body) return null;
      const name = (e) =>
        e.tagName.toLowerCase() +
        (e.id ? `#${e.id}` : '') +
        (e.getAttribute('href') ? `[href="${e.getAttribute('href')}"]` : '') +
        (e.textContent?.trim() ? ` "${e.textContent.trim().replace(/\s+/g, ' ').slice(0, 40)}"` : '');
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      // The focus indicator: what changes on the element or its pseudo-elements
      // between focused and blurred (then focus goes back for the next Tab).
      const PROPS = ['outline-style', 'outline-width', 'outline-color', 'box-shadow', 'border-color', 'background-color', 'color', 'text-decoration-line', 'opacity'];
      const look = () =>
        [null, '::before', '::after']
          .map((pseudo) => {
            const c = getComputedStyle(el, pseudo);
            return PROPS.map((p) => c.getPropertyValue(p)).join('|');
          })
          .join('||');
      const focused = look();
      el.blur();
      const blurred = look();
      el.focus({ preventScroll: true });
      const ring = focused !== blurred;
      const inIframe = el.tagName === 'IFRAME';
      return {
        el: name(el),
        visible: r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && Number(cs.opacity) > 0,
        inView: r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth,
        ring,
        focusVisible: el.matches(':focus-visible'),
        skip: /skip/i.test(el.textContent ?? '') && (el.getAttribute('href') ?? '').startsWith('#'),
        inIframe,
        key: name(el) + Math.round(r.top + scrollY),
      };
    });
    if (!s) continue;
    if (first && s.key === first) break; // wrapped around
    first ??= s.key;
    stops.push(s);
    if (s.inIframe) break;
  }
  return stops;
}

const browser = await chromium.launch({
  executablePath: headlessShell(),
  headless: true,
  args: ['--hide-scrollbars', '--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'],
});
const report = { baseUrl: base, axe: AXE_VERSION, tags: TAGS, reducedMotion: !!opt['reduced-motion'], runs: [] };
let blocking = 0;
try {
  for (const width of WIDTHS) {
    const phone = width < 600;
    const height = phone ? 844 : width < 1024 ? 1024 : 900;
    const ctx = await browser.newContext({
      viewport: { width, height },
      isMobile: phone,
      hasTouch: phone,
      reducedMotion: opt['reduced-motion'] ? 'reduce' : 'no-preference',
    });
    await stubGitHub(ctx);
    for (const path of PAGES) {
      const page = await ctx.newPage();
      const url = new URL(path.replace(/^\//, ''), base).href;
      await page.goto(url, { waitUntil: 'load', timeout: 30_000 });
      await page.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => {});
      await settle(page);
      await page.addScriptTag({ content: AXE });
      const res = await page.evaluate(
        async (tags) => {
          const r = await window.axe.run({ exclude: [['iframe']] }, { runOnly: { type: 'tag', values: tags }, resultTypes: ['violations', 'incomplete'] });
          const pick = (v) => ({
            id: v.id,
            impact: v.impact,
            help: v.help,
            nodes: v.nodes.map((n) => ({ target: n.target.join(' '), summary: n.failureSummary?.split('\n').slice(0, 3).join(' ') ?? '' })),
          });
          return { violations: r.violations.map(pick), incomplete: r.incomplete.map((v) => ({ id: v.id, n: v.nodes.length })) };
        },
        TAGS,
      );
      const stops = phone ? [] : await keyboard(page); // phones have no Tab key
      const kb = [];
      if (!phone) {
        if (!stops.length) kb.push('no focusable element reached with Tab');
        else if (!stops[0].skip) kb.push(`the first Tab stop is ${stops[0].el}, not the skip link`);
        for (const s of stops) {
          if (!s.visible) kb.push(`focus lands on an invisible element: ${s.el}`);
          else if (!s.ring) kb.push(`no focus indicator on ${s.el}`);
        }
      }
      const bad = res.violations.filter((v) => BLOCKING.has(v.impact));
      blocking += bad.length + kb.length;
      report.runs.push({ path, width, ...res, keyboard: { stops, problems: kb } });
      const other = res.violations.filter((v) => !BLOCKING.has(v.impact));
      console.log(
        `${String(width).padStart(5)} ${path.padEnd(12)} blocking=${bad.length} other=${other.length} incomplete=${res.incomplete.reduce((n, v) => n + v.n, 0)}` +
          (phone ? '' : ` tabstops=${stops.length} keyboard=${kb.length}`),
      );
      for (const v of bad) console.log(`        ! ${v.impact} ${v.id}: ${v.help}\n            ${v.nodes.slice(0, 4).map((n) => n.target).join('\n            ')}`);
      for (const v of other) console.log(`        ~ ${v.impact} ${v.id}: ${v.help} (${v.nodes.length})`);
      for (const k of kb) console.log(`        ! keyboard: ${k}`);
      await page.close();
    }
    await ctx.close();
  }
} finally {
  await browser.close();
}
if (opt.out) {
  mkdirSync(resolve(String(opt.out)), { recursive: true });
  writeFileSync(join(resolve(String(opt.out)), 'axe.json'), `${JSON.stringify(report, null, 2)}\n`);
}
console.log(blocking ? `\n${blocking} BLOCKING ACCESSIBILITY PROBLEM(S)` : `\nAXE CLEAN (axe-core ${AXE_VERSION}, ${report.runs.length} runs, no serious or critical violations)`);
process.exit(blocking ? 1 : 0);
