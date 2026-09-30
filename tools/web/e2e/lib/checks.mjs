// Per-load checks (ARCH §10.3, MASTER-PLAN §6.3). Each check returns
// { ok, details }. Which ones fail a run is the UI adapter's call (ui.mjs).

import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8');

export const AXE_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'];

// INIT runs in every page before its own scripts. It records CSP violations
// and the layout-shift and LCP entries for the report. The page's CSP stays
// enforced: nothing here bypasses it.
export const INIT = `(() => {
  const w = window;
  w.__vos = { csp: [], cls: 0, lcp: 0 };
  document.addEventListener('securitypolicyviolation', (e) => {
    w.__vos.csp.push(e.violatedDirective + ' ' + (e.blockedURI || '') + ' ' + (e.sourceFile || '') + ':' + (e.lineNumber || 0));
  });
  try {
    new PerformanceObserver((l) => { for (const e of l.getEntries()) if (!e.hadRecentInput) w.__vos.cls += e.value; })
      .observe({ type: 'layout-shift', buffered: true });
    new PerformanceObserver((l) => { for (const e of l.getEntries()) w.__vos.lcp = e.startTime; })
      .observe({ type: 'largest-contentful-paint', buffered: true });
  } catch {}
})();`;

// watch collects what a page logs from now on: console errors and warnings,
// uncaught errors, failed requests and HTTP errors on its own origin, and
// every request it makes.
export function watch(page, origin) {
  const log = { console: [], errors: [], failed: [], http: [], requests: [] };
  page.on('console', (m) => {
    if (m.type() === 'error' || m.type() === 'warning') log.console.push(`${m.type()}: ${m.text()}`);
  });
  page.on('pageerror', (e) => log.errors.push(e.message));
  page.on('request', (r) => log.requests.push({ at: Date.now(), method: r.method(), url: r.url() }));
  page.on('requestfailed', (r) => {
    const err = r.failure()?.errorText ?? '';
    // An EventSource closed by navigation or by the page is not a failure.
    if (r.url().includes('/api/v1/events') && /ERR_ABORTED/.test(err)) return;
    log.failed.push(`${r.method()} ${r.url()} ${err}`);
  });
  page.on('response', (r) => {
    if (r.url().startsWith(origin) && r.status() >= 400) log.http.push(`${r.status()} ${r.request().method()} ${r.url()}`);
  });
  return log;
}

const allowed = (allow, line) => allow.some((re) => re.test(line));

// logChecks turns what watch collected into checks, dropping the messages a
// page is designed to produce (its allow list).
export async function logChecks(page, log, allow = []) {
  const csp = await page.evaluate(() => window.__vos?.csp ?? []).catch(() => []);
  const keep = (xs) => xs.filter((x) => !allowed(allow, x));
  const consoleLines = keep(log.console);
  return {
    console: { ok: consoleLines.length === 0 && log.errors.length === 0, details: [...consoleLines, ...log.errors.map((e) => `pageerror: ${e}`)] },
    csp: { ok: csp.length === 0, details: csp },
    requests: { ok: keep(log.failed).length === 0 && keep(log.http).length === 0, details: [...keep(log.failed), ...keep(log.http)] },
  };
}

export async function overflow(page) {
  const r = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, inner: window.innerWidth }));
  return { ok: r.scroll <= r.inner + 1, details: r.scroll > r.inner + 1 ? [`scrollWidth ${r.scroll} > innerWidth ${r.inner}`] : [] };
}

export async function landmarks(page, needNav) {
  const r = await page.evaluate(() => ({
    main: document.querySelectorAll('main, [role="main"]').length,
    nav: document.querySelectorAll('nav, [role="navigation"]').length,
    h1: document.querySelectorAll('h1').length,
    lang: document.documentElement.lang,
  }));
  const details = [];
  if (r.main !== 1) details.push(`${r.main} main landmarks, want 1`);
  if (needNav && r.nav < 1) details.push('no navigation landmark');
  if (!r.lang) details.push('<html> has no lang');
  return { ok: details.length === 0, details };
}

// focus tabs through the page: every stop must show a focus indicator, sit
// in the viewport once focused, not be entirely covered by the fixed or
// sticky chrome, and follow DOM order.
export async function focus(page, maxStops = 40) {
  await page.evaluate(() => {
    document.activeElement?.blur?.();
    window.scrollTo(0, 0);
  });
  const details = [];
  let stops = 0;
  for (let i = 0; i < maxStops; i++) {
    await page.keyboard.press('Tab');
    const r = await page.evaluate(() => {
      const el = document.activeElement;
      if (!el || el === document.body || el === document.documentElement) return { end: true };
      const prev = window.__vosPrevFocus;
      window.__vosPrevFocus = el;
      const order = prev && prev.isConnected ? prev.compareDocumentPosition(el) : 4;
      const cs = getComputedStyle(el);
      const outline = cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0;
      const shadow = cs.boxShadow && cs.boxShadow !== 'none';
      const rect = el.getBoundingClientRect();
      const inView = rect.bottom > 0 && rect.right > 0 && rect.top < innerHeight && rect.left < innerWidth;
      const name = el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + (el.getAttribute('href') ? `[href="${el.getAttribute('href')}"]` : '');
      // WCAG 2.4.11: the focused element may not be entirely hidden by
      // author content. Sample a 3x3 grid inside it; it is covered when
      // every sample on screen lands on fixed or sticky chrome (the tab bar,
      // the app bar, the strip) that is not the element or around it.
      const pinned = (n) => {
        for (let p = n; p && p !== document.body; p = p.parentElement) {
          const pos = getComputedStyle(p).position;
          if (pos === 'fixed' || pos === 'sticky') return p;
        }
        return null;
      };
      let samples = 0;
      let covered = 0;
      let by = '';
      for (const fx of [0.2, 0.5, 0.8]) {
        for (const fy of [0.2, 0.5, 0.8]) {
          const x = rect.left + rect.width * fx;
          const y = rect.top + rect.height * fy;
          if (x < 0 || y < 0 || x >= innerWidth || y >= innerHeight) continue;
          samples++;
          const hit = document.elementFromPoint(x, y);
          if (!hit || el.contains(hit) || hit.contains(el)) continue;
          const chrome = pinned(hit);
          if (chrome && !chrome.contains(el)) {
            covered++;
            by = chrome.tagName.toLowerCase() + (chrome.id ? '#' + chrome.id : '') + (chrome.classList.length ? '.' + [...chrome.classList].join('.') : '');
          }
        }
      }
      return { end: false, name, visible: outline || shadow, inView, obscured: samples > 0 && covered === samples ? by : '', backwards: !!(order & 2), wrapped: el === window.__vosFirstFocus };
    });
    if (r.end || r.wrapped) break;
    if (stops === 0) await page.evaluate(() => (window.__vosFirstFocus = document.activeElement));
    stops++;
    if (!r.visible) details.push(`${r.name}: no visible focus indicator`);
    if (!r.inView) details.push(`${r.name}: focused but outside the viewport`);
    else if (r.obscured) details.push(`${r.name}: focused but entirely under ${r.obscured} (WCAG 2.4.11)`);
    if (r.backwards) details.push(`${r.name}: focus moved backwards in DOM order`);
  }
  await page.evaluate(() => {
    delete window.__vosPrevFocus;
    delete window.__vosFirstFocus;
  });
  return { ok: details.length === 0, details, stops };
}

// targets checks WCAG 2.5.8: each target is at least 24x24 CSS px, or its
// 24 px circle touches no other target. Links inside a line of text are
// exempt. On a coarse pointer, targets under 44x44 are listed as warnings.
export async function targets(page) {
  return page.evaluate(() => {
    const sel = 'a[href], button, input:not([type="hidden"]), select, textarea, summary, [role="button"], [role="link"], [role="switch"], [role="tab"], [tabindex]:not([tabindex="-1"])';
    const els = [...document.querySelectorAll(sel)].filter((el) => {
      if (el.closest('[hidden], [inert], [aria-hidden="true"]')) return false;
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none';
    });
    const inline = (el) => {
      if (el.tagName !== 'A') return false;
      const p = el.parentElement;
      return !!p && getComputedStyle(el).display.startsWith('inline') && (p.textContent || '').trim().length > (el.textContent || '').trim().length;
    };
    const rects = els.map((el) => el.getBoundingClientRect());
    const name = (el) => el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + ((el.getAttribute('aria-label') || el.textContent || '').trim() ? ` "${(el.getAttribute('aria-label') || el.textContent).trim().slice(0, 30)}"` : '');
    const details = [];
    const warnings = [];
    const coarse = matchMedia('(pointer: coarse)').matches;
    els.forEach((el, i) => {
      const r = rects[i];
      if (coarse && (r.width < 44 || r.height < 44) && !inline(el)) warnings.push(`${name(el)} is ${Math.round(r.width)}x${Math.round(r.height)}, under 44x44 on touch`);
      if ((r.width >= 24 && r.height >= 24) || inline(el)) return;
      const cx = r.left + r.width / 2;
      const cy = r.top + r.height / 2;
      const clash = rects.some((o, j) => {
        if (j === i || els[j].contains(el) || el.contains(els[j])) return false;
        const nx = Math.max(o.left, Math.min(cx, o.right));
        const ny = Math.max(o.top, Math.min(cy, o.bottom));
        return Math.hypot(cx - nx, cy - ny) < 12;
      });
      if (clash) details.push(`${name(el)} is ${Math.round(r.width)}x${Math.round(r.height)} and too close to another target`);
    });
    return { ok: details.length === 0, details, warnings };
  });
}

// clipped looks for text cut off by overflow at 200 % text size.
export async function clipped(page) {
  const details = await page.evaluate(async () => {
    const root = document.documentElement;
    const before = root.style.getPropertyValue('font-size');
    root.style.setProperty('font-size', '200%');
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    const out = [];
    // Text only a screen reader gets (.sr-only, and the tab labels the
    // landscape bar keeps for its accessible names) is clipped on purpose.
    const srOnly = (el, cs) => {
      const r = el.getBoundingClientRect();
      return r.width <= 1 || r.height <= 1 || /inset\(50%/.test(cs.clipPath) || /^rect\(0/.test(cs.clip);
    };
    for (const el of document.querySelectorAll('body *')) {
      const cs = getComputedStyle(el);
      if (!/(hidden|clip)/.test(cs.overflowX + cs.overflowY) || !(el.textContent || '').trim()) continue;
      if (el.closest('[hidden], [aria-hidden="true"]') || cs.display === 'none') continue;
      if (srOnly(el, cs)) continue;
      if (el.scrollHeight > el.clientHeight + 2 || el.scrollWidth > el.clientWidth + 2) {
        out.push(`${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}.${[...el.classList].join('.')} clips its text`);
      }
    }
    if (before) root.style.setProperty('font-size', before);
    else root.style.removeProperty('font-size');
    return out;
  });
  return { ok: details.length === 0, details };
}

// axe runs axe-core in the page. DevTools evaluation is not subject to the
// page's CSP, so the policy stays enforced while it runs. Serious and
// critical violations fail, except the rule ids in known, which are listed
// as warnings (a frozen UI's known defects).
//
// In forced colours the browser, not the page, picks the colours, and axe
// still measures the author's (a white-hot button reads 1.06:1), so the
// contrast rule is off there. Contrast axe cannot decide (text over the
// heat fields' pixels) comes back "incomplete": it is listed as a warning,
// and heatContrast measures it from the pixels instead.
export async function axe(page, known = [], context = 'document') {
  await page.evaluate(AXE);
  const r = await page.evaluate(
    ([tags, ctx]) => {
      const forced = matchMedia('(forced-colors: active)').matches;
      const opts = { runOnly: { type: 'tag', values: tags }, resultTypes: ['violations', 'incomplete'] };
      if (forced) opts.rules = { 'color-contrast': { enabled: false } };
      return window.axe.run(ctx === 'document' ? document : document.querySelector(ctx), opts);
    },
    [AXE_TAGS, context],
  );
  const fmt = (v) => `${v.impact} ${v.id}: ${v.help} (${v.nodes.length}× e.g. ${v.nodes[0]?.target?.join(' ')})`;
  const blocking = r.violations.filter((v) => (v.impact === 'serious' || v.impact === 'critical') && !known.includes(v.id));
  const other = r.violations.filter((v) => !blocking.includes(v));
  const unsure = r.incomplete.filter((v) => v.id === 'color-contrast').map((v) => `incomplete ${fmt(v)}`);
  return { ok: blocking.length === 0, details: blocking.map(fmt), warnings: [...other.map((v) => fmt(v) + (known.includes(v.id) ? ' (known)' : '')), ...unsure] };
}

// assertAxe fails a flow step on any serious or critical axe violation, for
// the overlays the per-load checks never open (the PIN pad, sheets,
// confirm, the scene, Recent with notices in it).
export async function assertAxe(page, what) {
  const r = await axe(page);
  if (!r.ok) throw new Error(`axe with ${what}: ${r.details.join('; ')}`);
}

// heatContrast measures text over the heat fields from pixels, which axe
// leaves "incomplete": inside every element that holds an svg.heat-field,
// each text run is hidden, the page is screenshot with animations settled,
// and the text colour is compared with every background pixel under its
// line boxes. A run fails when its worst pixel, or its 10th percentile,
// is under 4.5:1 (3:1 for large text).
export async function heatContrast(page) {
  const png = await pngReader();
  if (!png) return { ok: true, details: [], warnings: ['heat contrast skipped: no PNG decoder in playwright-core'] };
  const items = await page.evaluate(() => {
    const out = [];
    const cv = document.createElement('canvas');
    cv.width = cv.height = 1;
    const cx = cv.getContext('2d', { willReadFrequently: true });
    const rgba = (c) => {
      cx.clearRect(0, 0, 1, 1);
      cx.fillStyle = '#000';
      cx.fillStyle = c;
      cx.fillRect(0, 0, 1, 1);
      return [...cx.getImageData(0, 0, 1, 1).data];
    };
    const roots = [...document.querySelectorAll('svg.heat-field')]
      .map((s) => s.parentElement)
      .filter((r) => r && !r.closest('[hidden], dialog:not([open])') && r.getBoundingClientRect().height > 0);
    const seen = new Set();
    let i = 0;
    for (const root of roots) {
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      for (let t = walker.nextNode(); t; t = walker.nextNode()) {
        const el = t.parentElement;
        if (!t.textContent.trim() || !el || seen.has(el) || el.closest('svg, [hidden], .sr-only')) continue;
        const cs = getComputedStyle(el);
        if (cs.visibility === 'hidden' || cs.display === 'none' || Number(cs.opacity) === 0) continue;
        const range = document.createRange();
        range.selectNodeContents(t);
        const rects = [...range.getClientRects()]
          .filter((r) => r.width > 1 && r.height > 1 && r.bottom > 0 && r.top < innerHeight)
          .map((r) => ({ x: r.x, y: r.y, w: r.width, h: r.height }));
        if (!rects.length) continue;
        seen.add(el);
        el.dataset.vosM = String(i);
        out.push({ id: String(i++), text: t.textContent.trim().slice(0, 40), color: rgba(cs.color), size: parseFloat(cs.fontSize), weight: Number(cs.fontWeight) || 400, rects, tag: el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).join('.') : '') });
      }
    }
    return out;
  });
  if (!items.length) return { ok: true, details: [] };
  const toggle = (hide) =>
    page.evaluate((h) => {
      for (const el of document.querySelectorAll('[data-vos-m]')) {
        for (const p of ['color', '-webkit-text-fill-color', 'text-shadow', 'caret-color']) {
          if (h) el.style.setProperty(p, p === 'text-shadow' ? 'none' : 'transparent', 'important');
          else el.style.removeProperty(p);
        }
        if (!h) delete el.dataset.vosM;
      }
    }, hide);
  await toggle(true);
  let buf;
  try {
    buf = await page.screenshot({ animations: 'disabled' });
  } finally {
    await toggle(false);
  }
  const img = png(buf);
  const scale = img.width / (await page.evaluate(() => innerWidth));
  const details = [];
  for (const it of items) {
    const cs = [];
    const a = it.color[3] / 255;
    for (const r of it.rects) {
      const x0 = Math.max(0, Math.floor(r.x * scale));
      const y0 = Math.max(0, Math.floor(r.y * scale));
      const x1 = Math.min(img.width, Math.ceil((r.x + r.w) * scale));
      const y1 = Math.min(img.height, Math.ceil((r.y + r.h) * scale));
      for (let y = y0; y < y1; y++) {
        for (let x = x0; x < x1; x++) {
          const k = (y * img.width + x) * 4;
          const bg = [img.data[k], img.data[k + 1], img.data[k + 2]];
          cs.push(contrast([0, 1, 2].map((j) => it.color[j] * a + bg[j] * (1 - a)), bg));
        }
      }
    }
    if (!cs.length) continue;
    cs.sort((x, y) => x - y);
    const need = it.size >= 24 || (it.size >= 18.66 && it.weight >= 700) ? 3 : 4.5;
    const min = cs[0];
    const p10 = cs[Math.floor(cs.length * 0.1)];
    if (min < need || p10 < need) details.push(`${it.tag} "${it.text}" is ${min.toFixed(2)}:1 at its worst pixel (10th percentile ${p10.toFixed(2)}:1), under ${need}:1 on the heat field`);
  }
  return { ok: details.length === 0, details, measured: items.length };
}

const channel = (c) => {
  const v = c / 255;
  return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
};
const luminance = ([r, g, b]) => 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
export function contrast(a, b) {
  const la = luminance(a);
  const lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

// pngReader decodes a screenshot with the PNG codec playwright-core bundles
// (its version is pinned exactly in package-lock.json).
let pngCodec;
async function pngReader() {
  if (pngCodec === undefined) {
    try {
      const { PNG } = require('playwright-core/lib/utilsBundle');
      pngCodec = (buf) => PNG.sync.read(buf);
    } catch {
      pngCodec = null;
    }
  }
  return pngCodec;
}

export async function perf(page) {
  const r = await page.evaluate(() => {
    const nav = performance.getEntriesByType('navigation')[0];
    const fcp = performance.getEntriesByName('first-contentful-paint')[0];
    return { fcp: fcp ? Math.round(fcp.startTime) : null, lcp: Math.round(window.__vos?.lcp ?? 0), cls: Number((window.__vos?.cls ?? 0).toFixed(4)), load: nav ? Math.round(nav.loadEventEnd) : null };
  });
  return { ok: true, details: [], metrics: r };
}
