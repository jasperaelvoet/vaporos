// core/api.js: /api/v1. With core/live.js the only network user, through
// globalThis.vosTransport (the website's demo supplies it).

import { getString, remove, setString } from './store.js';

export const API = '/api/v1';

// Resolved per call, so a demo runtime installed before the page is used.
const T = () => globalThis.vosTransport ?? { fetch: (...a) => fetch(...a), EventSource: globalThis.EventSource };
export const transport = T;

export const session = { csrf: '', setupCode: '', me: null, page: '', booting: true, skew: 0 };

export class ApiError extends Error {
  constructor(status, message, { method = 'GET', path = '', retryAfter = 0 } = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.method = method;
    this.path = path;
    this.retryAfter = retryAfter;
  }
}

// errorText is an error's copy (T5). messages.js loads on the first error;
// it cannot load when VaporOS is unreachable, which needs no table.
export const NETWORK = "Can't reach VaporOS. Check that it's on and on your network.";
export async function errorText(err, vars = {}) {
  if (err instanceof ApiError && err.status === 0) return NETWORK;
  try {
    const { messageFor } = await import('../messages.js');
    if (!(err instanceof ApiError)) return messageFor({ status: 500, message: String(err?.message ?? err) }, {}, vars);
    return messageFor(err, { method: err.method, path: err.path }, vars);
  } catch {
    const t = String(err?.message ?? err);
    return t ? t.charAt(0).toUpperCase() + t.slice(1) : NETWORK;
  }
}

// Every link a script builds goes through url(): the base is "" on the box,
// the demo's path on the website.
export const base = () => document.documentElement.dataset.base || '';
export const url = (path) => base() + path;

export function here() {
  const b = base();
  const p = location.pathname;
  return (b && p.startsWith(b) ? p.slice(b.length) || '/' : p) + location.search + location.hash;
}

// serverNow is the box's clock (Date headers): durations count from its times.
export const serverNow = () => Date.now() + session.skew;

// ---------- setup code ----------

const CODE_KEY = 'vos-setup-code';

// The setup code goes on every request and survives a reload, in this tab only.
export function useSetupCode(code) {
  session.setupCode = code || '';
  if (code) setString('session', CODE_KEY, code);
  else remove('session', CODE_KEY);
}

export const storedSetupCode = () => getString('session', CODE_KEY);

// ---------- requests ----------

async function send(method, path, body, passive) {
  const headers = { Accept: 'application/json' };
  const init = { method, headers, credentials: 'same-origin', cache: 'no-store' };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  if (method !== 'GET' && session.csrf) headers['X-VOS-CSRF'] = session.csrf;
  if (session.setupCode) headers['X-VOS-Setup'] = session.setupCode;
  if (passive) headers['X-VOS-Passive'] = '1';
  try {
    const res = await T().fetch(API + path, init);
    const date = Date.parse(res.headers.get('Date') || '');
    if (Number.isFinite(date)) session.skew = date - Date.now();
    return res;
  } catch {
    throw new ApiError(0, "Can't reach VaporOS.", { method, path });
  }
}

const shared = new Map(); // GET path → {at, promise}

// api resolves to the decoded JSON ({text: true}: the text). share: reuse an
// identical GET from the last 2 s. passive: X-VOS-Passive, a refresh nobody
// asked for, which does not keep the box awake. quiet401: throw, no sign-in.
export function api(method, path, body, opts = {}) {
  if (method === 'GET' && opts.share) {
    const s = shared.get(path);
    if (s && Date.now() - s.at < 2000) return s.promise;
    const promise = call(method, path, body, opts);
    shared.set(path, { at: Date.now(), promise });
    promise.catch(() => shared.delete(path));
    return promise;
  }
  return call(method, path, body, opts);
}

async function call(method, path, body, { text = false, quiet401 = false, passive = false } = {}) {
  let res = await send(method, path, body, passive);
  if (res.status === 403 && method !== 'GET' && session.csrf) {
    // The token rotated (a sign-in in another tab): fetch it, try once more.
    const before = session.csrf;
    try {
      await refreshMe();
    } catch {
      /* keep the 403 */
    }
    if (session.csrf && session.csrf !== before) res = await send(method, path, body, passive);
  }
  if (res.status === 401 && !quiet401) {
    // While boot is still asking /auth/me, its one redirect wins (CRIT 36).
    if (!session.booting) goLogin({ ended: true });
    throw new ApiError(401, 'Your session ended. Sign in again.', { method, path });
  }
  if (!res.ok) {
    const retryAfter = Number(res.headers.get('Retry-After')) || 0;
    throw new ApiError(res.status, await serverText(res), { method, path, retryAfter });
  }
  if (text) return res.text();
  if (!(res.headers.get('Content-Type') || '').includes('json')) return {};
  try {
    return await res.json();
  } catch {
    return {};
  }
}

async function serverText(res) {
  try {
    const j = await res.json();
    if (j && typeof j.error === 'string') return j.error;
  } catch {
    /* not JSON */
  }
  return '';
}

export async function refreshMe() {
  const me = await api('GET', '/auth/me', undefined, { quiet401: true });
  session.me = me;
  session.csrf = me.csrf || '';
  return me;
}

const SEEN = 'vos-signed-in';

// Only a tab that was signed in says "You were signed out" (N4, CRIT 36).
export function markSignedIn() {
  setString('session', SEEN, '1');
}

export function goLogin({ ended = false } = {}) {
  if (document.documentElement.dataset.page === 'login') return;
  const q = new URLSearchParams({ next: here() });
  if (ended && getString('session', SEEN) === '1') {
    q.set('ended', '1');
    remove('session', SEEN);
  }
  location.replace(url(`/login?${q}`));
}

// ping polls the public /ping (no session, no activity); null when down.
export async function ping() {
  try {
    const r = await T().fetch(`${API}/ping`, { cache: 'no-store', credentials: 'same-origin' });
    return r.ok ? await r.json() : null;
  } catch {
    return null;
  }
}
