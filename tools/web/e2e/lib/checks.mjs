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
// in the viewport once focused, and follow DOM order.
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
      return { end: false, name, visible: outline || shadow, inView, backwards: !!(order & 2), wrapped: el === window.__vosFirstFocus };
    });
    if (r.end || r.wrapped) break;
    if (stops === 0) await page.evaluate(() => (window.__vosFirstFocus = document.activeElement));
    stops++;
    if (!r.visible) details.push(`${r.name}: no visible focus indicator`);
    if (!r.inView) details.push(`${r.name}: focused but outside the viewport`);
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
    for (const el of document.querySelectorAll('body *')) {
      const cs = getComputedStyle(el);
      if (!/(hidden|clip)/.test(cs.overflowX + cs.overflowY) || !(el.textContent || '').trim()) continue;
      if (el.closest('[hidden], [aria-hidden="true"]') || cs.display === 'none') continue;
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
export async function axe(page, known = []) {
  await page.evaluate(AXE);
  const r = await page.evaluate(
    (tags) => window.axe.run(document, { runOnly: { type: 'tag', values: tags }, resultTypes: ['violations'] }),
    AXE_TAGS,
  );
  const fmt = (v) => `${v.impact} ${v.id}: ${v.help} (${v.nodes.length}× e.g. ${v.nodes[0]?.target?.join(' ')})`;
  const blocking = r.violations.filter((v) => (v.impact === 'serious' || v.impact === 'critical') && !known.includes(v.id));
  const other = r.violations.filter((v) => !blocking.includes(v));
  return { ok: blocking.length === 0, details: blocking.map(fmt), warnings: other.map((v) => fmt(v) + (known.includes(v.id) ? ' (known)' : '')) };
}

export async function perf(page) {
  const r = await page.evaluate(() => {
    const nav = performance.getEntriesByType('navigation')[0];
    const fcp = performance.getEntriesByName('first-contentful-paint')[0];
    return { fcp: fcp ? Math.round(fcp.startTime) : null, lcp: Math.round(window.__vos?.lcp ?? 0), cls: Number((window.__vos?.cls ?? 0).toFixed(4)), load: nav ? Math.round(nav.loadEventEnd) : null };
  });
  return { ok: true, details: [], metrics: r };
}
