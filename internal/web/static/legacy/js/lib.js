// lib.js: the runtime every page shares. It talks to /api/v1 (CSRF and
// setup-code headers, errors, 401 → sign-in), gates pages on /auth/me, keeps
// one EventSource for live updates, and provides the few UI primitives the
// pages need: element building, toasts, a confirm dialog, busy buttons and
// the restart overlay. No framework: the pages are small and the appliance
// must never depend on anything outside this binary.

import { normalizeCode } from './fmt.js';

export * from './fmt.js';

const API = '/api/v1';

// session is what /auth/me told us. csrf goes on every mutating request;
// setupCode on every request made while installing or setting up.
export const session = { csrf: '', setupCode: '', me: null, page: '' };

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

// ---------- DOM ----------

export const byId = (id) => document.getElementById(id);
export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

// h builds an element. Text is always set as text, never parsed as HTML, so
// values from the API cannot inject markup.
export function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  append(el, kids);
  return el;
}

function append(el, kids) {
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false || kid === '') continue;
    el.append(kid instanceof Node ? kid : String(kid));
  }
}

// fill replaces an element's children.
export function fill(el, ...kids) {
  if (!el) return el;
  el.replaceChildren();
  append(el, kids);
  return el;
}

const SVG = 'http://www.w3.org/2000/svg';

// icon references a symbol from the page's inline sprite (icons.go).
export function icon(name, cls = '') {
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('class', cls ? `icon ${cls}` : 'icon');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  const use = document.createElementNS(SVG, 'use');
  use.setAttribute('href', `#i-${name}`);
  svg.append(use);
  return svg;
}

export function badge(text, tone = '') {
  return h('span', { class: tone ? `badge badge-${tone}` : 'badge', text });
}

export function setText(id, text) {
  const el = byId(id);
  if (el) el.textContent = text;
  return el;
}

// showError puts msg in a .field-error element, hiding it when empty.
export function showError(el, msg) {
  if (typeof el === 'string') el = byId(el);
  if (!el) return;
  el.textContent = msg || '';
  el.hidden = !msg;
}

// kv fills a <dl class="kv"> from [label, value] pairs; value may be a node.
export function kv(dl, rows) {
  fill(dl, rows.filter(Boolean).map(([k, v]) => [h('dt', { text: k }), h('dd', {}, v == null || v === '' ? '–' : v)]));
}

// invalid marks a field with a message via the browser's own validation UI,
// clearing it on the next edit.
export function invalid(input, msg) {
  input.setCustomValidity(msg);
  input.reportValidity();
  input.addEventListener('input', () => input.setCustomValidity(''), { once: true });
  return false;
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------- API ----------

const CODE_KEY = 'vos-setup-code';

// useSetupCode sends code as X-VOS-Setup from now on and remembers it for
// this tab, so a reload mid-install does not ask for it again.
export function useSetupCode(code) {
  session.setupCode = code || '';
  try {
    if (code) sessionStorage.setItem(CODE_KEY, code);
    else sessionStorage.removeItem(CODE_KEY);
  } catch { /* private mode: header only */ }
}

// initSetupCode picks up the setup code from the prefilled field (the server
// copies ?code= from the QR code into it), the URL or this tab's storage,
// and removes it from the address bar so it is not shared by accident.
export function initSetupCode() {
  const field = byId('setup-code');
  const params = new URLSearchParams(location.search);
  let stored = '';
  try {
    stored = sessionStorage.getItem(CODE_KEY) || '';
  } catch { /* ignore */ }
  const code = normalizeCode((field && field.value) || params.get('code') || stored);
  if (params.has('code')) {
    params.delete('code');
    const q = params.toString();
    history.replaceState(null, '', location.pathname + (q ? `?${q}` : '') + location.hash);
  }
  if (field) {
    field.value = code;
    field.addEventListener('blur', () => {
      field.value = normalizeCode(field.value);
    });
  }
  useSetupCode(code);
  return code;
}

async function send(method, path, body) {
  const headers = { Accept: 'application/json' };
  const init = { method, headers, credentials: 'same-origin', cache: 'no-store' };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  if (method !== 'GET' && session.csrf) headers['X-VOS-CSRF'] = session.csrf;
  if (session.setupCode) headers['X-VOS-Setup'] = session.setupCode;
  try {
    return await fetch(API + path, init);
  } catch {
    throw new ApiError(0, "Can't reach VaporOS. Check that it is on and on your network.");
  }
}

// api calls /api/v1<path> and returns the decoded JSON (or text with
// {text:true}). A 401 sends the visitor to sign in unless quiet401 is set;
// any other failure throws an ApiError carrying the server's message.
export async function api(method, path, body, { text = false, quiet401 = false } = {}) {
  let res = await send(method, path, body);
  if (res.status === 403 && method !== 'GET' && session.csrf) {
    // The CSRF token belongs to the session; if it rotated (sign-in in
    // another tab), fetch the current one and try once more.
    const before = session.csrf;
    try {
      await refreshMe();
    } catch { /* keep the 403 */ }
    if (session.csrf && session.csrf !== before) res = await send(method, path, body);
  }
  if (res.status === 401 && !quiet401) {
    goLogin();
    throw new ApiError(401, 'Your session ended. Sign in again.');
  }
  if (!res.ok) throw new ApiError(res.status, await errorMessage(res));
  if (text) return res.text();
  if (!(res.headers.get('Content-Type') || '').includes('json')) return {};
  try {
    return await res.json();
  } catch {
    return {};
  }
}

async function errorMessage(res) {
  try {
    const j = await res.json();
    if (j && typeof j.error === 'string' && j.error) return j.error.charAt(0).toUpperCase() + j.error.slice(1);
  } catch { /* not JSON */ }
  switch (res.status) {
    case 403: return "That isn't allowed from here.";
    case 404: return "This VaporOS doesn't support that yet.";
    case 409: return 'Something else is in progress. Try again in a moment.';
    case 429: return 'Too many attempts. Wait a few minutes and try again.';
    case 502: case 503: case 504: return 'VaporOS is busy or restarting. Try again in a moment.';
    default: return `Something went wrong (${res.status}).`;
  }
}

export async function refreshMe() {
  const me = await api('GET', '/auth/me', undefined, { quiet401: true });
  session.me = me;
  session.csrf = me.csrf || '';
  return me;
}

export function goLogin() {
  if (location.pathname === '/login') return;
  const here = location.pathname + location.search + location.hash;
  location.replace(`/login?next=${encodeURIComponent(here)}`);
}

// halt is returned while the browser navigates away, so page code after
// `await boot()` never runs against the wrong state.
const halt = () => new Promise(() => {});

// boot runs before any page code: wires the shell, asks /auth/me and sends
// the visitor where they belong (installer and first-run go to /setup,
// signed-out to /login). It resolves with /auth/me once the page may render.
export async function boot(page, { auth = true } = {}) {
  session.page = page;
  initChrome();
  let me;
  try {
    me = await refreshMe();
  } catch (e) {
    fatal(e.message);
    return halt();
  }
  if ((me.installer || me.needs_setup) && page !== 'setup') {
    location.replace('/setup' + location.search);
    return halt();
  }
  if (auth && !me.authenticated) {
    goLogin();
    return halt();
  }
  document.documentElement.classList.add('ready');
  if (auth) {
    on('system.message', (m) => m && m.text && toast(m.text, m.level === 'error' ? 'error' : m.level === 'warn' || m.level === 'warning' ? 'warn' : 'info'));
    if (page !== 'pair') {
      on('pairing.pending', (p) => toast(p && p.name ? `${p.name} wants to pair.` : 'A device wants to pair.', 'info', { href: '/pair', label: 'Enter PIN' }));
    }
    connectEvents();
  }
  return me;
}

function fatal(msg) {
  document.documentElement.classList.add('failed');
  const box = byId('fatal');
  if (box) box.hidden = false;
  if (msg) setText('fatal-text', msg);
}

function initChrome() {
  const bar = byId('appbar');
  const menu = byId('menu-btn');
  if (bar && menu) {
    const set = (open) => {
      bar.classList.toggle('open', open);
      menu.setAttribute('aria-expanded', String(open));
    };
    menu.addEventListener('click', () => set(!bar.classList.contains('open')));
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && bar.classList.contains('open')) {
        set(false);
        menu.focus();
      }
    });
    document.addEventListener('click', (e) => {
      if (bar.classList.contains('open') && !bar.contains(e.target)) set(false);
    });
  }
  const logout = byId('logout');
  logout?.addEventListener('click', () => busy(logout, async () => {
    await api('POST', '/auth/logout', {});
    location.replace('/login');
  }));
  byId('fatal-retry')?.addEventListener('click', () => location.reload());
  byId('overlay')?.addEventListener('cancel', (e) => e.preventDefault());
  byId('overlay-reload')?.addEventListener('click', () => location.reload());
}

// ---------- live events (SSE) ----------

const topics = new Map(); // topic → Set of handlers
const reconnectFns = new Set();
let source = null;
let opened = false;
let retryDelay = 1000;
let retryTimer = 0;
let eventsAuth = true;

// on subscribes to one SSE topic (CONTRACTS.md "Events") and returns an
// unsubscribe function. Handlers get the decoded data.
export function on(topic, fn) {
  let set = topics.get(topic);
  if (!set) {
    set = new Set();
    topics.set(topic, set);
    if (source) listen(source, topic);
  }
  set.add(fn);
  return () => set.delete(fn);
}

// onReconnect runs fn after the event stream comes back from a drop, when
// the page may have missed events and should reload its data.
export function onReconnect(fn) {
  reconnectFns.add(fn);
}

function listen(es, topic) {
  es.addEventListener(topic, (ev) => {
    let data;
    try {
      data = JSON.parse(ev.data);
    } catch {
      return;
    }
    for (const fn of topics.get(topic) || []) {
      try {
        fn(data);
      } catch (e) {
        console.error(`event ${topic}:`, e);
      }
    }
  });
}

// connectEvents opens the stream. With {auth:false} (installer, setup) a
// refused stream is retried quietly instead of sending anyone to /login;
// those pages poll as well, so events only make them faster.
export function connectEvents({ auth = true } = {}) {
  eventsAuth = auth;
  if (source || typeof EventSource === 'undefined') return;
  setLive('connecting');
  const es = new EventSource(API + '/events');
  source = es;
  for (const t of topics.keys()) listen(es, t);
  es.onopen = () => {
    setLive('live');
    retryDelay = 1000;
    if (opened) for (const fn of reconnectFns) fn();
    opened = true;
  };
  es.onerror = () => {
    if (es.readyState !== EventSource.CLOSED) {
      setLive('connecting'); // the browser retries by itself
      return;
    }
    // The server refused the stream (signed out, restarting, not yet up):
    // back off, check the session, and open a new one.
    es.close();
    source = null;
    setLive('offline');
    clearTimeout(retryTimer);
    retryTimer = setTimeout(async () => {
      if (eventsAuth) {
        try {
          const me = await refreshMe();
          if (!me.authenticated) {
            goLogin();
            return;
          }
        } catch { /* offline: just retry */ }
      }
      connectEvents({ auth: eventsAuth });
    }, retryDelay);
    retryDelay = Math.min(retryDelay * 2, 30000);
  };
}

// Phones suspend background tabs; refresh when the page is looked at again.
let hiddenAt = 0;
document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    hiddenAt = Date.now();
  } else if (hiddenAt && Date.now() - hiddenAt > 30000 && document.documentElement.classList.contains('ready')) {
    for (const fn of reconnectFns) fn();
  }
});

function setLive(state) {
  const el = byId('live');
  if (!el) return;
  el.dataset.state = state;
  setText('live-label', { live: 'Live', connecting: 'Connecting', offline: 'Offline' }[state] || state);
}

// ---------- feedback ----------

const TOAST_ICONS = { info: 'info', ok: 'check', warn: 'alert', error: 'alert' };

// toast shows a short message; errors stay longer. action is {href,label}.
export function toast(text, level = 'info', action = null) {
  const box = byId('toasts');
  if (!box) return;
  const el = h('div', { class: `toast toast-${level}` },
    icon(TOAST_ICONS[level] || 'info'),
    h('span', { class: 'toast-text', text }),
    action ? h('a', { class: 'toast-action', href: action.href, text: action.label }) : null,
    h('button', { class: 'toast-close', type: 'button', 'aria-label': 'Dismiss', onclick: () => el.remove() }, icon('close')));
  box.append(el);
  while (box.children.length > 3) box.firstElementChild.remove();
  setTimeout(() => {
    el.classList.add('leaving');
    setTimeout(() => el.remove(), 300);
  }, level === 'error' ? 9000 : 5000);
}

// confirmDialog asks before something drastic. Resolves true on confirm.
export function confirmDialog({ title, body = '', confirm = 'Continue', danger = false }) {
  const dlg = byId('confirm');
  if (!dlg || typeof dlg.showModal !== 'function') return Promise.resolve(window.confirm(body ? `${title}\n\n${body}` : title));
  setText('confirm-title', title);
  setText('confirm-body', body);
  const ok = byId('confirm-ok');
  ok.textContent = confirm;
  ok.className = danger ? 'btn btn-danger-solid' : 'btn btn-primary';
  return new Promise((resolve) => {
    dlg.returnValue = '';
    dlg.addEventListener('close', () => resolve(dlg.returnValue === 'ok'), { once: true });
    dlg.showModal();
    ok.focus();
  });
}

// busy runs fn while btn shows a spinner and cannot be pressed twice.
// Errors become a toast; the result of fn is returned (undefined on error).
export async function busy(btn, fn) {
  if (btn && btn.getAttribute('aria-busy') === 'true') return undefined;
  if (btn) {
    btn.setAttribute('aria-busy', 'true');
    btn.disabled = true;
  }
  try {
    return await fn();
  } catch (e) {
    if (!(e instanceof ApiError && e.status === 401)) toast(e.message || String(e), 'error');
    return undefined;
  } finally {
    if (btn) {
      btn.removeAttribute('aria-busy');
      btn.disabled = false;
    }
  }
}

// onSubmit handles a form with the browser's validation first, then fn with
// the submit button busy.
export function onSubmit(form, fn) {
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const btn = e.submitter || form.querySelector('[type="submit"]');
    busy(btn, () => fn(form));
  });
}

// ---------- restart and power ----------

async function ping() {
  try {
    const r = await fetch(`${API}/ping`, { cache: 'no-store', credentials: 'same-origin' });
    return r.ok ? await r.json() : null;
  } catch {
    return null;
  }
}

// waitForRestart resolves with /ping's answer once VaporOS went away and came
// back, or null after timeout. A fast restart can slip between two polls, so
// once the wait is long enough an uptime younger than the wait also counts.
export async function waitForRestart({ onDown, timeout = 8 * 60e3 } = {}) {
  const start = Date.now();
  let down = false;
  while (Date.now() - start < timeout) {
    await sleep(2000);
    const p = await ping();
    if (!p) {
      if (!down && onDown) onDown();
      down = true;
      continue;
    }
    if (down) return p;
    if (Date.now() - start > 20000) {
      try {
        const s = await api('GET', '/system', undefined, { quiet401: true });
        if (Number(s.uptime_s) * 1000 < Date.now() - start) return p;
      } catch { /* not signed in any more: keep waiting */ }
    }
  }
  return null;
}

function overlay({ title, text, iconName = '', spinner = true }) {
  const dlg = byId('overlay');
  if (!dlg) return;
  setText('overlay-title', title);
  setText('overlay-text', text);
  fill(byId('overlay-icon'), spinner ? h('span', { class: 'spinner spinner-lg' }) : icon(iconName || 'power'));
  byId('overlay-actions').hidden = true;
  if (!dlg.open) dlg.showModal();
}

const POWER = {
  reboot: {
    path: '/system/reboot', title: 'Restart VaporOS?', confirm: 'Restart',
    body: 'Streams in progress stop. VaporOS is back in about a minute.',
    wait: 'Restarting…',
  },
  poweroff: {
    path: '/system/poweroff', title: 'Power off VaporOS?', confirm: 'Power off', danger: true,
    body: 'Streams in progress stop. Start it again with its power button or from Moonlight (Wake-on-LAN).',
  },
  activate: {
    path: '/update/activate', title: 'Restart to update?', confirm: 'Restart and update',
    body: 'Streams in progress stop. VaporOS restarts into the new version; if it fails to start, it goes back by itself.',
    wait: 'Updating…',
  },
};

// powerAction confirms (unless the caller already asked), calls the API, and
// covers the page until VaporOS is back (or tells the visitor it is off).
export async function powerAction(kind, btn = null, { confirmed = false } = {}) {
  const a = POWER[kind];
  if (!confirmed && !(await confirmDialog(a))) return false;
  const ok = await busy(btn, async () => {
    await api('POST', a.path, {});
    return true;
  });
  if (!ok) return false;
  if (kind === 'poweroff') {
    overlay({ title: 'VaporOS is off', text: 'Start it with its power button, or wake it from Moonlight. This page reconnects when it is back.', iconName: 'moon', spinner: false });
    const back = await waitForRestart({ timeout: 24 * 3600e3 });
    if (back) location.reload();
    return true;
  }
  overlay({ title: a.wait, text: 'VaporOS is restarting. This page reconnects by itself.' });
  const back = await waitForRestart({ onDown: () => setText('overlay-text', 'Waiting for VaporOS to start…') });
  if (back) {
    setText('overlay-text', 'Back online.');
    location.reload();
  } else {
    setText('overlay-text', "VaporOS hasn't come back yet. Check the PC, then reload this page.");
    byId('overlay-actions').hidden = false;
  }
  return true;
}
