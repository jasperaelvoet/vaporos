// demo/runtime.js: the website's live demo of the control center
// (MASTER-PLAN §3.6 D1, spec-website §8). TestExportDemo puts it on every
// exported page, before the page's own script:
//
//   <script type="module" src="<base>/demo/runtime.js?v=<hash>">
//
// It installs globalThis.vosTransport synchronously, as the module is
// evaluated, so core/api.js and core/live.js answer every /api/v1 request
// and the event stream from the demo's engine (engine.js over fixtures.js)
// and never from the network. The engine and the fixtures are resolved
// lazily: the exporter preloads both beside the page's modules, and only
// the page's first request waits for them. No top-level await (TestJSFloor:
// Safari 15.4 lacks it).
//
// Around the engine it keeps the demo's state across the page loads of the
// multi-page UI (sessionStorage, per export), keeps the demo signed in,
// answers its parent page (the website's DemoStage) over postMessage (spec-
// website §8.5), and leaves nothing that could reach a real PC: sign-out
// is off, the log downloads as a blob, and links to a PC's own address stay
// on the page.

const ME = new URL(import.meta.url);
const V = ME.search; // ?v=<hash>: the same versions of engine.js and fixtures.js
const HASH = ME.searchParams.get('v') || 'dev';
const KEY = `vos-demo:${HASH}`;
const START = 'idle'; // what a fresh demo shows (fixtures/presets/idle.json)
const FRAMED = window.parent !== window;
const PROTO = { type: 'vos-demo', v: 1 };
const AUTO_WAKE_MS = 15000; // alone in a tab, a powered-off demo wakes by itself

const root = document.documentElement;
const base = () => root.dataset.base || '';
// where is the page the frame shows, as the control center names it
// ("/system/updates"): no base, query or trailing slash.
const where = () => {
  const b = base();
  const p = location.pathname;
  const rel = b && p.startsWith(b) ? p.slice(b.length) : p;
  return rel.replace(/(index\.html|\.html)$/, '').replace(/(.)\/+$/, '$1') || '/';
};

// ---------- storage (guarded: private windows and blocked site data throw)

function loadSaved() {
  try {
    const raw = sessionStorage.getItem(KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

let engine = null;
let lib = null;
let fixtures = null;
let saving = 0;
let forget = false; // reset: the next page starts fresh

function save() {
  clearTimeout(saving);
  saving = 0;
  if (!engine || forget) return;
  try {
    sessionStorage.setItem(KEY, JSON.stringify(engine.save()));
  } catch {
    /* no storage: every page starts from the preset */
  }
}

const saveSoon = () => {
  if (!saving) saving = setTimeout(save, 200);
};

addEventListener('pagehide', save);
document.addEventListener('visibilitychange', () => document.hidden && save());

// ---------- the engine, resolved on first use

// demoPresets are the presets the demo can show: the installed system,
// signed in. The live ISO's installer and first-run setup have pages of
// their own that the demo leaves out.
const demoPreset = (name) => {
  const p = fixtures && fixtures.presets[name];
  return !!p && p.mode !== 'installer' && p.auth !== 'first-run' && p.auth !== 'signed-out';
};

function startPreset() {
  const asked = new URLSearchParams(location.search).get('preset');
  return asked && demoPreset(asked) ? asked : START;
}

// signedIn reports whether the demo's one browser holds a live session
// (the engine's cookie jar and sessions, as its transport keeps them).
function signedIn(e) {
  const token = e.s.client.cookies.vos_session;
  return !!token && !!e.s.sessions[token];
}

function start(E, fx) {
  lib = E;
  fixtures = fx;
  const saved = loadSaved();
  let e = null;
  if (saved && saved.v === 1) {
    try {
      e = E.createEngine(fx, { restore: saved });
    } catch (err) {
      console.warn('demo: starting over, the saved state did not load:', err);
      e = null;
    }
  }
  if (!e) e = E.createEngine(fx, { preset: startPreset() });
  // The demo is always signed in: there is no password to type.
  if (!signedIn(e)) e.signIn();
  engine = e;
  e.watch(onEngine);
  save();
  if (e.s.down) autoWake();
  return withHooks(E.transport(e));
}

const ready = Promise.all([import(`./engine.js${V}`), import(`./fixtures.js${V}`)]).then(([E, F]) => start(E, F.default));
ready.catch((err) => console.error('demo: the engine did not load:', err));

// ---------- the transport (globalThis.vosTransport)

// jitter is the demo's network: a real box answers in a few hundred
// milliseconds, a Moonlight PIN and an update check take longer.
function jitter(method, path) {
  if (method === 'POST' && path.endsWith('/update/check')) return 1500;
  if (method === 'POST' && path.endsWith('/sunshine/pair')) return 800;
  return 120 + Math.floor(Math.random() * 180);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function withHooks(t) {
  async function demoFetch(input, init = {}) {
    const url = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href);
    if (!url.pathname.startsWith('/api/v1/')) return t.fetch(input, init);
    const method = String(init.method || (typeof input === 'object' && input.method) || 'GET').toUpperCase();
    await sleep(jitter(method, url.pathname));
    const pairing = method === 'POST' && url.pathname === '/api/v1/sunshine/pair' ? waiting() : null;
    const res = await t.fetch(input, init);
    if (pairing && res.ok) paired(pairing, init.body);
    saveSoon();
    return res;
  }
  return { fetch: demoFetch, EventSource: t.EventSource };
}

globalThis.vosTransport = {
  fetch: (input, init) => ready.then((t) => t.fetch(input, init)),
  EventSource: class DemoEventSource extends EventTarget {
    // The engine's event stream, opened once the engine is there. Handlers
    // registered before that are passed on when it opens.
    constructor(url, init = {}) {
      super();
      this.url = new URL(String(url), location.href).href;
      this.withCredentials = !!init.withCredentials;
      this.onopen = null;
      this.onmessage = null;
      this.onerror = null;
      this.inner = null;
      this.closed = false;
      this.types = new Set(['message']);
      ready.then(
        (t) => {
          if (this.closed) return;
          const es = new t.EventSource(url, init);
          this.inner = es;
          es.onopen = () => this.fire('open');
          es.onerror = () => this.fire('error');
          for (const type of this.types) this.forward(type);
        },
        () => {
          this.closed = true;
          this.fire('error');
        },
      );
    }

    get readyState() {
      if (this.closed) return 2;
      return this.inner ? this.inner.readyState : 0;
    }

    addEventListener(type, fn, opts) {
      super.addEventListener(type, fn, opts);
      if (type === 'open' || type === 'error' || this.types.has(type)) return;
      this.types.add(type);
      if (this.inner) this.forward(type);
    }

    forward(type) {
      this.inner.addEventListener(type, (e) => {
        const ev = new MessageEvent(type, { data: e.data, origin: e.origin, lastEventId: e.lastEventId });
        this.dispatchEvent(ev);
        if (type === 'message' && typeof this.onmessage === 'function') this.onmessage.call(this, ev);
      });
    }

    fire(type) {
      const e = new Event(type);
      this.dispatchEvent(e);
      const fn = this[`on${type}`];
      if (typeof fn === 'function') fn.call(this, e);
    }

    close() {
      this.closed = true;
      if (this.inner) this.inner.close();
    }
  },
};
globalThis.vosTransport.EventSource.CONNECTING = 0;
globalThis.vosTransport.EventSource.OPEN = 1;
globalThis.vosTransport.EventSource.CLOSED = 2;

// ---------- what the parent page sees (spec-website §8.5)

function post(msg) {
  if (!FRAMED) return;
  try {
    window.parent.postMessage({ ...PROTO, ...msg }, location.origin);
  } catch {
    /* a parent on another origin gets nothing */
  }
}

// status is GET /status as the page gets it, asked passively (it is not
// the viewer's activity); null while the box is down.
function status() {
  const cookie = `vos_session=${engine.s.client.cookies.vos_session || ''}`;
  const res = engine.handle({ method: 'GET', path: '/api/v1/status', headers: { cookie, 'x-vos-passive': '1' } });
  if (!res) return null;
  try {
    return res.status === 200 ? JSON.parse(res.body) : {};
  } catch {
    return {};
  }
}

// deviceState is the page's temperature (ui/shell.js stateOf), or asleep
// while the box is down.
function deviceState(s) {
  if (!s) return 'asleep';
  const u = s.update || {};
  const d = s.display || {};
  if (s.stream || d.state === 'streaming') return 'streaming';
  if (u.busy && u.progress && ['download', 'write', 'verify', 'install'].includes(u.progress.phase)) return 'updating';
  if (d.profile === 'none' || (s.sunshine && s.sunshine.running === false)) return 'fault';
  const staged = u.staged && u.staged.version;
  const reasons = ((s.restart && s.restart.reasons) || []).filter((r) => !(r.kind === 'update' && staged && r.version === staged));
  return reasons.length ? 'restart-needed' : 'ready';
}

function waiting() {
  const s = engine && !engine.s.down ? status() : null;
  return ((s && s.sunshine && s.sunshine.pairings) || []).map((p) => ({ id: p.id, name: p.name }));
}

function snapshot() {
  const s = status();
  const u = (s && s.update) || {};
  const pairs = (s && s.sunshine && s.sunshine.pairings) || [];
  const stream = s && s.stream;
  return {
    device: deviceState(s),
    streaming: stream ? stream.client || 'Moonlight' : null,
    pairing: pairs.length ? { device: pairs[0].name || 'Moonlight', pin: lib.DEV_PIN } : null,
    update: u.busy && u.progress ? { phase: u.progress.phase, percent: u.progress.percent } : null,
  };
}

let stateTimer = 0;
let lastState = '';
function postState() {
  clearTimeout(stateTimer);
  stateTimer = setTimeout(() => {
    const st = snapshot();
    const json = JSON.stringify(st);
    if (json === lastState) return;
    lastState = json;
    post({ event: 'state', state: st });
  }, 60);
}

function paired(before, body) {
  let id = '';
  let name = '';
  try {
    const b = JSON.parse(body || '{}');
    id = b.pairing_id || '';
    name = (b.name || '').trim();
  } catch {
    /* the page sends JSON */
  }
  const who = before.find((p) => p.id === id) || (before.length === 1 ? before[0] : null);
  post({ event: 'paired', device: name || (who && who.name) || 'Moonlight' });
}

function onEngine(m) {
  saveSoon();
  postState();
  if (m.type === 'down') autoWake();
}

let wakeTimer = 0;
function autoWake() {
  if (FRAMED || wakeTimer) return;
  wakeTimer = setTimeout(() => {
    wakeTimer = 0;
    if (engine && engine.s.down) engine.setDown(0);
  }, AUTO_WAKE_MS);
}

// ---------- in-page notices (the page's own ui/notices.js)

function notice(text) {
  post({ event: 'notice', text });
  const page = document.querySelector('script[type="module"][src*="/js/pages/"]');
  if (!page || !document.getElementById('tpl-notice')) return;
  import(new URL('../ui/notices.js', page.src).href).then(
    (m) => m.notify(text, { kind: 'info', id: 'demo' }),
    () => {},
  );
}

// ---------- commands from the parent page

function command(m) {
  if (m.cmd === 'run' && typeof m.scenario === 'string') {
    if (m.scenario === 'reset') return reset();
    if (!Object.prototype.hasOwnProperty.call(fixtures.scripts, m.scenario)) return;
    engine.runScript(m.scenario);
    saveSoon();
    postState();
  } else if (m.cmd === 'preset' && typeof m.name === 'string') {
    if (!demoPreset(m.name)) {
      notice('The demo shows the installed system only.');
      return;
    }
    engine.loadPreset(m.name);
    if (!signedIn(engine)) engine.signIn();
    save();
    location.reload();
  }
}

// reset starts the demo over from its first state.
function reset() {
  forget = true;
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    /* nothing kept */
  }
  if (engine) engine.stop();
  location.reload();
}

addEventListener('message', (ev) => {
  if (!FRAMED || ev.source !== window.parent || ev.origin !== location.origin) return;
  const m = ev.data;
  if (!m || m.type !== PROTO.type || m.v !== PROTO.v || typeof m.cmd !== 'string') return;
  ready.then(() => command(m));
});

// ready: once the engine is there and the page has booted (or given up),
// so commands the parent queued find a page that listens.
function whenBooted(timeout) {
  return new Promise((resolve) => {
    if (root.dataset.boot) return resolve();
    const obs = new MutationObserver(() => {
      if (root.dataset.boot) done();
    });
    const timer = setTimeout(done, timeout);
    function done() {
      obs.disconnect();
      clearTimeout(timer);
      resolve();
    }
    obs.observe(root, { attributes: true, attributeFilter: ['data-boot'] });
  });
}

ready
  .then(() => whenBooted(3000))
  .then(() => {
    post({ event: 'ready', path: where(), manifest: HASH });
    postState();
  }, () => {});

// ---------- nothing reaches a real PC

// A click on Sign out is stopped before the page's handler: the demo has
// no one to sign out.
const SIGN_OUT = '#signout, #sys-signout';

// A PC's own address (http://vapor.local, an address on the LAN) would
// take the frame to a page that does not exist.
function pcLink(url) {
  const h = url.hostname;
  return url.origin !== location.origin && (h.endsWith('.local') || /^(10|127|192\.168|172\.(1[6-9]|2\d|3[01]))\./.test(h));
}

// noDownloads reports whether the frame the demo runs in is sandboxed
// without allow-downloads: the browser would drop the download (and log an
// error), so the log opens in a tab of its own instead.
function noDownloads() {
  try {
    const s = window.frameElement && window.frameElement.sandbox;
    return !!s && s.length > 0 && !s.contains('allow-downloads');
  } catch {
    return false; // a parent on another origin: nothing to go by
  }
}

// downloadLog answers the Logs page's Download link from the engine: the
// log as a blob, saved as the link names it, or shown in a tab (opened while
// the click still counts, so no popup blocker stops it).
async function downloadLog(a, tab) {
  try {
    const t = await ready;
    const url = new URL(a.href, location.href);
    const res = await t.fetch(url.pathname.slice(base().length) + url.search, { credentials: 'same-origin', cache: 'no-store' });
    if (!res.ok) throw new Error(String(res.status));
    const blob = new Blob([await res.text()], { type: 'text/plain;charset=utf-8' });
    const href = URL.createObjectURL(blob);
    setTimeout(() => URL.revokeObjectURL(href), 60000);
    if (tab) {
      tab.location.href = href;
      return;
    }
    const link = document.createElement('a');
    link.href = href;
    link.download = a.getAttribute('download') || 'sunshine.log';
    link.hidden = true;
    document.body.append(link);
    link.click();
    link.remove();
  } catch {
    if (tab) tab.close();
    notice("The demo couldn't make the log file.");
  }
}

function onClick(e) {
  const el = e.target instanceof Element ? e.target : null;
  if (!el) return;
  if (el.closest(SIGN_OUT)) {
    e.preventDefault();
    e.stopImmediatePropagation();
    notice('Signing out is off in the demo.');
    return;
  }
  const a = el.closest('a[href]');
  if (!a) return;
  const url = new URL(a.href, location.href);
  if (url.origin === location.origin && url.pathname.startsWith(`${base()}/api/v1/`)) {
    e.preventDefault();
    e.stopImmediatePropagation();
    if (e.type !== 'click') return;
    let tab = null;
    if (noDownloads()) {
      tab = window.open('', '_blank');
      if (!tab) {
        notice("Allow pop-ups for this site to see the demo's log.");
        return;
      }
    }
    downloadLog(a, tab);
  } else if (pcLink(url)) {
    e.preventDefault();
    e.stopImmediatePropagation();
    notice(`On your network this opens ${url.host}. The demo has no PC behind it.`);
  }
}

addEventListener('click', onClick, true);
addEventListener('auxclick', onClick, true);

// Nothing typed is kept: no password manager offers to save the demo's.
for (const input of document.querySelectorAll('input[type="password"]')) input.autocomplete = 'off';
