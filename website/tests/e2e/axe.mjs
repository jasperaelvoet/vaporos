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
// and minor ones are listed. The page must not scroll sideways (WCAG 1.4.10:
// 320 is 1280 px at 400% zoom). Text whose contrast axe can't decide (over a
// gradient, a canvas, the heat) is measured on the rendered pixels instead,
// and so is every fitted headline line (the visible copy of an sr-only
// headline, which axe skips as aria-hidden), below the fixed nav. At 1024 ×
// 768 the hero is also measured 0.7 s into the live view's intro breath.
// The keyboard pass tabs through the page, at phone widths too (without
// touch, with the menu opened first): the first stop must be the skip link,
// and every stop must be visible, scrolled into view and not covered (WCAG
// 2.4.11: no fixed bar or open menu over it), and show a focus indicator (a
// style that changes between focused and blurred).
// Writes <out>/axe.json when --out is given.
import { readFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { join, resolve } from 'node:path';
import { inflateSync } from 'node:zlib';
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
const WIDTHS = list(opt.widths, ['1440', '1024', '768', '390', '320']).map(Number);
const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'];
const BLOCKING = new Set(['serious', 'critical']);
const MAX_TABS = 150;

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

// Runs in the page: tab stops from the keyboard, in order. With `menu`, the
// mobile menu is opened first (as a keyboard user on a narrow window would).
async function keyboard(page, { menu = false } = {}) {
  const stops = [];
  // Transitions off, so a focus style reads at its end value both ways.
  await page.addStyleTag({ content: '*,*::before,*::after{transition:none!important;animation-duration:0s!important}' });
  // Start from the top of the document, whatever was scrolled or focused before.
  await page.evaluate(() => {
    document.activeElement?.blur?.();
    window.lenis?.scrollTo?.(0, { immediate: true, force: true });
    window.scrollTo({ top: 0, behavior: 'instant' });
    const start = document.createElement('span');
    start.tabIndex = -1;
    start.id = '__axe_start';
    document.body.prepend(start);
    start.focus({ preventScroll: true });
  });
  if (menu) await page.evaluate(() => document.querySelector('.site-menu')?.setAttribute('open', ''));
  for (let i = 0; i < MAX_TABS; i++) {
    await page.keyboard.press('Tab');
    if (i === 0) await page.evaluate(() => document.getElementById('__axe_start')?.remove());
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
      // WCAG 2.4.11: a focused element is never entirely hidden (a fixed nav over it).
      const pts = [
        [0.5, 0.5],
        [0.15, 0.2],
        [0.85, 0.2],
        [0.15, 0.8],
        [0.85, 0.8],
      ].map(([fx, fy]) => [r.left + r.width * fx, r.top + r.height * fy]);
      const seen = pts.some(([x, y]) => {
        const hit = document.elementFromPoint(x, y);
        return !!hit && (el === hit || el.contains(hit) || hit.contains(el));
      });
      const inIframe = el.tagName === 'IFRAME';
      // Wrapped around to the first stop: the pass is done.
      const wrapped = window.__axeFirstStop === el;
      window.__axeFirstStop ??= el;
      return {
        wrapped,
        el: name(el),
        visible: r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && Number(cs.opacity) > 0,
        inView: r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth,
        obscured: !seen,
        ring,
        focusVisible: el.matches(':focus-visible'),
        skip: /skip/i.test(el.textContent ?? '') && (el.getAttribute('href') ?? '').startsWith('#'),
        inIframe,
      };
    });
    if (!s) continue;
    if (s.wrapped) break;
    stops.push(s);
    if (s.inIframe) break;
  }
  return stops;
}

// ---------------------------------------------------------------- pixel contrast
// What axe can't decide (text over gradients, canvases, the heat): hide the
// text, photograph its box, and take the 10th-percentile contrast of the text
// colour against the pixels under it (REDLINE's _build/contrast.mjs method).
// Needs 4.5:1, or 3:1 for large text (24 px, or 18.66 px bold).
function decodePNG(buf) {
  let o = 8;
  let w = 0;
  let h = 0;
  let ct = 6;
  const idat = [];
  while (o < buf.length) {
    const len = buf.readUInt32BE(o);
    const type = buf.toString('ascii', o + 4, o + 8);
    const d = buf.subarray(o + 8, o + 8 + len);
    if (type === 'IHDR') {
      w = d.readUInt32BE(0);
      h = d.readUInt32BE(4);
      ct = d[9];
    } else if (type === 'IDAT') idat.push(d);
    o += 12 + len;
  }
  const bpp = ct === 6 ? 4 : 3;
  const raw = inflateSync(Buffer.concat(idat));
  const out = new Uint8Array(w * h * 3);
  const stride = w * bpp;
  let prev = Buffer.alloc(stride);
  for (let y = 0; y < h; y++) {
    const f = raw[y * (stride + 1)];
    const line = Buffer.from(raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1)));
    for (let x = 0; x < stride; x++) {
      const a = x >= bpp ? line[x - bpp] : 0;
      const b = prev[x];
      const c = x >= bpp ? prev[x - bpp] : 0;
      const pa = Math.abs(b - c);
      const pb = Math.abs(a - c);
      const pc = Math.abs(a + b - 2 * c);
      const paeth = pa <= pb && pa <= pc ? a : pb <= pc ? b : c;
      line[x] = (line[x] + (f === 1 ? a : f === 2 ? b : f === 3 ? (a + b) >> 1 : f === 4 ? paeth : 0)) & 255;
    }
    for (let x = 0; x < w; x++) for (let k = 0; k < 3; k++) out[(y * w + x) * 3 + k] = line[x * bpp + k];
    prev = line;
  }
  return { w, h, d: out };
}
const lin = (c) => ((c /= 255) <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const lum = (r, g, b) => 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);

async function pixelContrast(page, axeTargets) {
  // Every visible headline line, marked so it can be found again: the lines
  // are aria-hidden copies of an sr-only headline, so axe never sees them.
  const lines = await page.evaluate(() =>
    [...document.querySelectorAll('.headline-fit .ln')]
      .filter((el) => el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden')
      .map((el, i) => {
        el.setAttribute('data-axe-px', String(i));
        return `[data-axe-px="${i}"]`;
      }),
  );
  const targets = [...(axeTargets ?? []), ...lines];
  if (!targets.length) return [];
  const runs = await page.evaluate((sels) => {
    // Any CSS colour (rgb(), color(srgb …), oklch()) → sRGB bytes and alpha, through a canvas.
    const g = document.createElement('canvas').getContext('2d', { willReadFrequently: true });
    const rgba = (css) => {
      g.clearRect(0, 0, 1, 1);
      g.fillStyle = css;
      g.fillRect(0, 0, 1, 1);
      const [r, gr, b, a] = g.getImageData(0, 0, 1, 1).data;
      return [r, gr, b, a / 255]; // getImageData is not premultiplied
    };
    return sels
      .map((sel) => {
        const el = document.querySelector(sel);
        // Hidden from assistive tech: decoration (WCAG 1.4.3 exempts it),
        // except a headline's visible lines, which carry its sr-only text.
        if (!el || (el.closest('[aria-hidden="true"]') && !el.matches('[data-axe-px]'))) return null;
        const cs = getComputedStyle(el);
        const c = rgba(cs.color);
        const size = parseFloat(cs.fontSize);
        const large = size >= 24 || (size >= 18.66 && Number(cs.fontWeight) >= 700);
        return { target: sel, text: (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 40), color: c, need: large ? 3 : 4.5 };
      })
      .filter(Boolean);
  }, targets);
  await page.evaluate(() => {
    const st = document.createElement('style');
    st.id = '__axe_hide_text';
    st.textContent =
      '*,*::before,*::after{color:transparent!important;-webkit-text-fill-color:transparent!important;text-shadow:none!important;text-decoration-color:transparent!important;caret-color:transparent!important}';
    document.head.append(st);
  });
  const out = [];
  try {
    for (const r of runs) {
      const box = await page.evaluate((sel) => {
        const el = document.querySelector(sel);
        if (!el) return null; // re-rendered since axe ran (the release upgrade)
        el.scrollIntoView({ block: 'center', inline: 'nearest' });
        window.lenis?.scrollTo?.(scrollY, { immediate: true, force: true });
        // The text's own box (a Range over its text), not the element's
        // padding, and only the part below the fixed nav (never sample the
        // nav's own pixels as the text's ground).
        const range = document.createRange();
        range.selectNodeContents(el);
        const b = range.getBoundingClientRect();
        const nav = document.querySelector('.site-nav')?.getBoundingClientRect().bottom ?? 0;
        const x = Math.max(0, b.left);
        const y = Math.max(0, nav, b.top);
        return { x, y, width: Math.min(innerWidth, b.right) - x, height: Math.min(innerHeight, b.bottom) - y };
      }, r.target);
      if (!box || box.width < 2 || box.height < 2) continue;
      await page.waitForTimeout(60);
      const img = decodePNG(await page.screenshot({ clip: box }));
      const [tr, tg, tb, ta = 1] = r.color;
      const ratios = [];
      for (let y = 0; y < img.h; y += 2) {
        for (let x = 0; x < img.w; x += 2) {
          const i = (y * img.w + x) * 3;
          const [gr, gg, gb] = [img.d[i], img.d[i + 1], img.d[i + 2]];
          const lg = lum(gr, gg, gb);
          const lt = ta < 1 ? lum(tr * ta + gr * (1 - ta), tg * ta + gg * (1 - ta), tb * ta + gb * (1 - ta)) : lum(tr, tg, tb);
          ratios.push((Math.max(lt, lg) + 0.05) / (Math.min(lt, lg) + 0.05));
        }
      }
      ratios.sort((a, b) => a - b);
      if (ratios.length) out.push({ ...r, ratio: Number(ratios[Math.floor(ratios.length * 0.1)].toFixed(2)) });
    }
  } finally {
    await page.evaluate(() => document.getElementById('__axe_hide_text')?.remove());
  }
  return out;
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
    // 1024 × 768: the short desktop where the hero's words sit closest to the heat.
    const height = phone ? (width < 360 ? 568 : 844) : width === 1024 ? 768 : width < 1024 ? 1024 : 900;
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
      // The hero during the live view's intro: 0.7 s after it appears, before anything settles.
      let intro = [];
      if (path === '/' && width === 1024 && !opt['reduced-motion']) {
        const on = await page
          .waitForFunction(() => document.querySelector('[data-hero]')?.dataset.gl === 'on', null, { timeout: 15_000 })
          .then(() => true)
          .catch(() => false);
        if (on) {
          await page.waitForTimeout(700);
          intro = (await pixelContrast(page, ['.hero-lead', '.hero-kicker'])).map((m) => ({ ...m, when: 'intro' }));
        }
      }
      await settle(page);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
      await page.addScriptTag({ content: AXE });
      const res = await page.evaluate(
        async (tags) => {
          // label-content-name-mismatch (WCAG 2.5.3, level A) is still marked
          // experimental in axe, so it is off unless asked for.
          const r = await window.axe.run(
            { exclude: [['iframe']] },
            { runOnly: { type: 'tag', values: tags }, rules: { 'label-content-name-mismatch': { enabled: true } }, resultTypes: ['violations', 'incomplete'] },
          );
          const pick = (v) => ({
            id: v.id,
            impact: v.impact,
            help: v.help,
            nodes: v.nodes.map((n) => ({ target: n.target.join(' '), summary: n.failureSummary?.split('\n').slice(0, 3).join(' ') ?? '' })),
          });
          const unsure = r.incomplete.find((v) => v.id === 'color-contrast')?.nodes ?? [];
          return {
            violations: r.violations.map(pick),
            incomplete: r.incomplete.map((v) => ({ id: v.id, n: v.nodes.length })),
            contrastTargets: unsure.filter((n) => n.target.length === 1).map((n) => n.target[0]),
          };
        },
        TAGS,
      );
      const measured = [...intro, ...(await pixelContrast(page, res.contrastTargets))];
      const lowContrast = measured.filter((m) => m.ratio < m.need);
      delete res.contrastTargets;
      // Phones have no Tab key, but a narrow window with a keyboard does:
      // tab through the same width without touch, with the menu open.
      let kbPage = page;
      let kbCtx = null;
      if (phone) {
        kbCtx = await browser.newContext({ viewport: { width, height }, reducedMotion: opt['reduced-motion'] ? 'reduce' : 'no-preference' });
        await stubGitHub(kbCtx);
        kbPage = await kbCtx.newPage();
        await kbPage.goto(url, { waitUntil: 'load', timeout: 30_000 });
        await kbPage.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => {});
        await settle(kbPage);
      }
      const stops = await keyboard(kbPage, { menu: phone });
      await kbCtx?.close();
      const kb = [];
      if (!stops.length) kb.push('no focusable element reached with Tab');
      else if (!stops[0].skip) kb.push(`the first Tab stop is ${stops[0].el}, not the skip link`);
      for (const s of stops) {
        if (!s.visible) kb.push(`focus lands on an invisible element: ${s.el}`);
        else if (!s.inView || s.obscured) kb.push(`the focused element is out of view or covered: ${s.el}`);
        else if (!s.ring) kb.push(`no focus indicator on ${s.el}`);
      }
      const reflow = overflow > 0 ? [`the page scrolls ${overflow} px sideways`] : [];
      const bad = res.violations.filter((v) => BLOCKING.has(v.impact));
      blocking += bad.length + kb.length + lowContrast.length + reflow.length;
      report.runs.push({ path, width, height, overflow, ...res, pixelContrast: measured, keyboard: { stops, problems: kb } });
      const other = res.violations.filter((v) => !BLOCKING.has(v.impact));
      console.log(
        `${String(width).padStart(5)} ${path.padEnd(12)} blocking=${bad.length} other=${other.length} incomplete=${res.incomplete.reduce((n, v) => n + v.n, 0)}` +
          ` measured=${measured.length} low=${lowContrast.length} overflow=${overflow}` +
          ` tabstops=${stops.length} keyboard=${kb.length}`,
      );
      for (const r of reflow) console.log(`        ! reflow: ${r}`);
      for (const m of lowContrast) console.log(`        ! contrast ${m.ratio.toFixed(2)} < ${m.need} on the rendered ground${m.when ? ` (${m.when})` : ''}: ${m.target} "${m.text}"`);
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
