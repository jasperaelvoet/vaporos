// ui/shell.js: every signed-in page's frame (spec-cc-screens §2). One
// active GET /status at load; after that only passive, debounced refreshes,
// so an open page never keeps the box awake. Pieces only some states need
// load on first use, keeping the first paint small (ARCH §3.3).

import { api, url } from '../core/api.js';
import { announce } from '../core/announce.js';
import { boot } from '../core/boot.js';
import { byId, optionalById, part } from '../core/dom.js';
import { link, on, onLink, onReconnect, onVisible } from '../core/live.js';
import { getJSON, loadSnapshot, saveSnapshot } from '../core/store.js';

const LINK = {
  live: ['Live', 'Live updates on'],
  connecting: ['Connecting…', 'Connecting: live updates'],
  offline: ['Offline', 'Offline: live updates retrying'],
};
const OFFLINE = {
  stale: 'Showing the last known state.',
  asleep: 'VaporOS might be asleep. Wake it from Moonlight; this page reconnects by itself.',
};

let snap = {};
const watchers = new Set();
const loaded = {};
let options = { pairNotice: true };

// Literal import() paths, so the tests and the budget can follow them.
export const notices = () => import('./notices.js');
export const scene = () => import('./scene.js');
const strip = () => import('./strip.js');
const restartRow = () => import('./restart-row.js');
const pinpad = () => import('./pinpad.js');

// current: GET /status plus what events said since (state.js's shape).
export const current = () => snap;

// pending is what asks for a restart: /status's reasons without a staged
// update, which leaves the box ready (state.js pendingReasons, MASTER-PLAN
// §1.3), inline so every page's first paint does not load state.js.
function pending(s) {
  const staged = s.update && s.update.staged && s.update.staged.version;
  return ((s.restart && s.restart.reasons) || []).filter((r) => r && r.kind && !(r.kind === 'update' && staged && r.version === staged));
}

export function onStatus(fn) {
  watchers.add(fn);
  if (snap.update || snap.display) fn(snap);
  return () => watchers.delete(fn);
}

export const notify = (text, opts) => notices().then((m) => m.notify(text, opts));

// pairNotice: Home and Devices show a waiting device their own way.
export async function shell(page, { pairNotice = !['home', 'devices'].includes(page) } = {}) {
  options = { pairNotice };
  byId('signout').addEventListener('click', signOut);
  byId('link').addEventListener('click', (e) => notices().then((m) => m.openRecent(e.currentTarget)));
  byId('offline-wake').addEventListener('click', () => scene().then((m) => m.asleep()));
  byId('notices-more').addEventListener('click', (e) => notices().then((m) => m.openRecent(e.currentTarget)));
  if (getJSON('session', 'vos-flash')) notices().then((m) => m.showFlash());
  onLink(renderLink);
  listen();
  const me = await boot(page);
  const kept = loadSnapshot();
  if (kept && kept.wol) snap.wol = kept.wol;
  // Load what an outage needs while VaporOS still answers.
  refresh(false).then(() => {
    scene();
    rememberWolSometimes();
  });
  return me;
}

export async function signOut() {
  try {
    await api('POST', '/auth/logout', {});
  } catch {
    /* signed out either way */
  }
  location.replace(url('/login'));
}

export async function refresh(passive = true) {
  try {
    const st = await api('GET', '/status', undefined, { share: !passive, passive });
    saveSnapshot({ status: st });
    apply({ ...st, live: {} });
  } catch {
    /* the pages say what failed; the shell keeps what it had */
  }
}

let timer = 0;
function soon() {
  clearTimeout(timer);
  timer = setTimeout(() => refresh(true), 600);
}

function apply(next) {
  snap = { ...snap, ...next, live: { ...(snap.live || {}), ...(next.live || {}) } };
  // /status leaves out the Wake-on-LAN adapters; the hold rule needs them.
  if (snap.power && snap.wol) snap.power = { ...snap.power, wol: snap.wol };
  const d = snap.display || {};
  const streaming = !!(snap.stream || snap.live.session || d.state === 'streaming');
  const reasons = pending(snap);
  const waiting = (snap.sunshine && snap.sunshine.pairings) || [];
  if (streaming || loaded.strip) {
    loaded.strip = true;
    strip().then((m) => m.renderStrip(snap));
  }
  if ((reasons.length || loaded.row) && optionalById('restart-row')) {
    loaded.row = true;
    restartRow().then((m) => m.renderRestartRow(snap));
  }
  if (waiting.length || loaded.pin) {
    loaded.pin = true;
    pinpad().then((m) => {
      m.initPinpad({ notice: options.pairNotice });
      m.updatePairings(waiting);
      if (document.documentElement.dataset.page === 'devices' && location.hash === '#pair' && waiting.length && !loaded.pairOpened) {
        loaded.pairOpened = true;
        m.openPinpad();
      }
    });
  }
  document.documentElement.dataset.state = stateOf(snap, streaming, reasons);
  badge('devices', waiting.length ? `${waiting.length} waiting to pair` : '');
  badge('system', reasons.length ? 'restart needed' : '');
  const sys = snap.system || {};
  const host = sys.mdns || (sys.hostname ? `${sys.hostname}.local` : '');
  if (host) for (const id of ['host-name', 'rail-host']) byId(id).textContent = host;
  for (const fn of watchers) fn(snap);
}

// The page's temperature (T1) for the chrome: the tab mark and the link dot.
function stateOf(s, streaming, reasons) {
  const u = s.update || {};
  if (streaming) return 'streaming';
  if (u.busy && u.progress && ['download', 'write', 'verify', 'install'].includes(u.progress.phase)) return 'updating';
  if ((s.display && s.display.profile === 'none') || (s.sunshine && s.sunshine.running === false)) return 'fault';
  return reasons.length ? 'restart-needed' : 'ready';
}

function badge(tab, text) {
  const a = document.querySelector(`.tab[data-tab="${tab}"]`);
  if (!a) return;
  part(a, 'badge').hidden = !text;
  part(a, 'badge-text').textContent = text ? `, ${text}` : '';
}

let idleWarnedAt = 0;

function listen() {
  on('session.begin', (s) => {
    apply({ stream: s, live: { session: s } });
    soon();
  });
  on('session.end', () => {
    const d = snap.display || {};
    apply({
      stream: null,
      live: { session: null },
      sunshine: { ...(snap.sunshine || {}), streaming: false },
      display: { ...d, state: d.state === 'streaming' ? 'gaming' : d.state },
    });
    soon();
  });
  on('pairing.state', (p) => apply({ sunshine: { ...(snap.sunshine || {}), pairings: (p && p.pairings) || [] } }));
  on('pairing.pending', soon);
  on('sunshine.state', soon);
  on('display.changed', soon);
  on('update.state', soon);
  on('update.progress', (p, live) => {
    if (live && ['done', 'error', 'idle', 'cancelled'].includes(p && p.phase)) soon();
  });
  // Only when the extensions start or stop asking for a restart does the
  // restart row need a new /status.
  on('extensions.state', (d) => {
    const want = !!(d && d.restart && d.restart.needed);
    if (snap.restart && want !== pending(snap).some((r) => r.kind === 'extensions')) soon();
  });
  on('power.idle', (p) => {
    if (p && p.shutdown_in != null && p.shutdown_in <= 30) idleWarnedAt = Date.now();
  });
  // system.message is never replayed (CONTRACTS.md "Events").
  on('system.message', (m) => {
    if (!m || !m.text) return;
    scene().then((sc) => {
      if (sc.suppress.messages) return;
      notify(m.text, { kind: m.level === 'error' ? 'error' : m.level === 'warning' || m.level === 'warn' ? 'warn' : 'info' });
    });
  });
  onReconnect(() => refresh(true));
  onVisible(() => refresh(true));
}

// GET /power runs ethtool: ask for the Wake card's adapters once a day.
function rememberWolSometimes() {
  const s = loadSnapshot();
  if (s && s.wol && Date.now() - Date.parse(s.wolAt || 0) < 86400e3) return;
  api('GET', '/power', undefined, { passive: true }).then((p) => {
    import('./wake.js').then((m) => m.rememberWol(p.wol));
    saveSnapshot({ wolAt: new Date().toISOString() });
    apply({ wol: p.wol });
  }, () => {});
}

// ---- the connection indicator and the offline banner (G8, NEW-1)

let downTimers = [];
let downSince = 0;

function renderLink(l) {
  byId('link').dataset.link = l.state;
  const [text, name] = LINK[l.state] || LINK.connecting;
  byId('link-text').textContent = text;
  byId('link-name').textContent = `${name}. Show recent notices.`;
  for (const t of downTimers) clearTimeout(t);
  downTimers = [];
  if (l.state === 'live') {
    byId('offline').hidden = true;
    if (downSince && Date.now() - downSince > 10000) announce('Back online');
    downSince = 0;
    return;
  }
  if (!document.documentElement.dataset.boot || byId('scene').open) return;
  scene();
  downSince = downSince || l.since || Date.now();
  const at = (ms, fn) => downTimers.push(setTimeout(fn, Math.max(0, ms - (Date.now() - downSince))));
  // The stream dropped within a minute of an idle countdown: it powered off.
  if (idleWarnedAt && Date.now() - idleWarnedAt < 60000) {
    at(3000, () => link.state !== 'live' && scene().then((m) => m.asleep({ off: true })));
    return;
  }
  at(10000, () => banner(OFFLINE.stale, false));
  at(60000, () => {
    banner(OFFLINE.asleep, true);
    scene().then((m) => m.asleep({ from: downSince }));
  });
}

function banner(text, wake) {
  if (link.state === 'live') return;
  // Home's hero says Asleep itself, with the same words: one is enough.
  if (wake && document.documentElement.dataset.page === 'home') {
    byId('offline').hidden = true;
    return;
  }
  byId('offline-text').textContent = text;
  byId('offline-wake').hidden = !wake;
  byId('offline').hidden = false;
}
