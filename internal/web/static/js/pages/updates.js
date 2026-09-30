// pages/updates.js: System › Updates (spec-cc-screens §7): the status and
// the two copies from GET /update and live events (updates-more.js wires
// the buttons). GET is the truth (R1): replays never show as updating (B1).

import { api, serverNow, signedInBefore } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, cloneTpl, h, part, setText, setVar } from '../core/dom.js';
import { on, onReconnect } from '../core/live.js';
import { settleMain } from '../ui/region.js';
import { onStatus, shell } from '../ui/shell.js';
import { UPDATE_PHASES } from '../copy.js';
import { ago, bytes, compareVersions as cmp, percent, phaseLabel } from '../fmt.js';

// The read starts before boot (ARCH §7.4) in a tab that was signed in;
// otherwise after it, so a signed-out visit makes no 401 before sign-in.
const read = () => api('GET', '/update');
const early = signedInBefore() ? read() : null;
early?.catch(() => {});
const more = import('./updates-more.js');

// live: working progress; failed: a live error; checking: a stage's check;
// asked: Check now runs; starting: Download pressed, no progress yet.
export const S = { u: null, live: null, failed: null, checking: false, asked: false, starting: false };

const KEYS = ['booted', 'staged', 'failed', 'available', 'checked', 'last_error', 'held'];
const working = (p) => !!(p && UPDATE_PHASES[p.phase] && UPDATE_PHASES[p.phase].working);
const dot = (s) => String(s).replace(/([^.!?])$/, '$1.');

// view: the status card (§7.2), its banners and the versions (§7.4).
export function view(u, s = S) {
  const booted = u.booted || '';
  let prog = working(s.live) ? s.live : u.busy && working(u.progress) ? u.progress : null;
  const staged = u.staged && u.staged.version && u.staged.version !== booted ? u.staged.version : '';
  const nb = u.next_boot;
  // A restart that starts neither the running nor the staged version.
  let next = '';
  if (nb && nb.version && nb.version !== booted && nb.version !== staged) next = nb.version;
  else if (nb === undefined && !staged && u.held && u.held.version === booted && u.other_slot) next = u.other_slot.version || '';
  const avail = !staged && u.available && u.available.version && cmp(u.available.version, booted) > 0 ? u.available : null;
  if (!prog && s.starting) prog = { phase: 'check', percent: 0, version: (avail && avail.version) || '' };
  const checking = !prog && (s.asked || s.checking || (u.busy && (!u.progress || u.progress.phase === 'check')));
  const checked = u.checked ? `Checked ${ago(u.checked, serverNow())}.` : '';
  const older = cmp(next, booted) < 0;
  let st;
  if (prog) {
    const v = prog.version || (avail && avail.version) || '';
    st = { key: 'working', state: 'updating', title: v ? `Installing version ${v}` : 'Installing an update', detail: 'Keep playing. It switches over when VaporOS restarts.' };
  } else if (next) {
    st = { key: 'next', state: 'restart-needed', title: `Version ${next} starts on the next restart`, detail: older ? `You chose to go back from ${booted}.` : `You chose to return to it from ${booted}.` };
  } else if (staged) {
    st = { key: 'staged', state: 'ready', title: `Version ${staged} is ready`, detail: "Restart to switch over. If it doesn't start, VaporOS goes back by itself.", meta: u.staged.at ? `Prepared ${ago(u.staged.at, serverNow())}` : '' };
  } else if (avail) {
    st = { key: 'available', state: 'ready', title: `Version ${avail.version} is available`, detail: `${avail.size ? `${bytes(avail.size)} download. ` : ''}${checked}` };
  } else if (u.checked) {
    st = { key: 'current', state: 'ready', title: 'VaporOS is up to date', detail: `Version ${booted}. ${checked}` };
  } else {
    st = { key: 'never', state: '', title: `VaporOS ${booted}`, detail: 'Not checked yet.' };
  }
  Object.assign(st, { prog, staged, next, avail, checking });

  // last_error survives a reload; a stage's refusal shows only live.
  const b = [];
  const le = String(u.last_error || '');
  if (le.startsWith('check: ')) {
    b.push(['warn', `Couldn't check for updates: ${/dial|lookup|host|timeout|refused|unreachable|tls|certificate|connect/i.test(le) ? "the update server couldn't be reached. Check the PC's internet connection." : dot(le.slice(7))}`]);
  } else if (le) {
    const m = /VaporOS (\S+) did not start correctly; still running (\S+)/.exec(le);
    b.push(['danger', m ? `Version ${m[1]} didn't start, so VaporOS kept ${m[2]}.` : `The last update didn't install: ${dot(le)}`]);
  }
  const live = s.failed && s.failed.error;
  if (live && live !== le) {
    const known = /on trial/i.test(live) ? 'The running version is still being checked after the last update. Try again once it has fully started.'
      : /starts next/i.test(live) ? 'Another version starts on the next restart. Restart first.' : '';
    b.push(['danger', known ? `Couldn't install the update. ${known}` : `Couldn't install the update: ${dot(live)}`]);
  }
  if (u.held && u.held.version && !next) b.push(['hint', `You went back from version ${u.held.version}, so automatic updates skip it and anything older. Newer versions still install.`]);
  st.banners = b;

  // B3: "Previous" only when nothing is staged; Go back mirrors Rollback.
  const o = u.other_slot;
  const ov = (o && o.version) || '';
  const failed = (u.failed || []).includes(ov);
  const bad = failed || (!!o && o.bootable === false && Number(o.tries_done) > 1);
  let x;
  if (prog) x = { label: 'Downloading', version: prog.version || (avail && avail.version) || '', sub: `${phaseLabel(prog.phase)} · ${percent(prog.percent)}%`, heat: 'fill' };
  else if (staged || next) x = { label: 'Next restart', version: staged || next, sub: staged ? 'Starts on the next restart' : older ? 'You chose to go back to it' : 'You chose to return to it', heat: 'warm' };
  else if (!o) x = { label: 'Previous', version: 'Unknown', sub: 'Nothing to go back to right now', heat: 'empty' };
  else if (!ov || ov === booted) x = { label: 'Previous', version: 'None', sub: 'Nothing kept yet', heat: 'empty' };
  else x = { label: 'Previous', version: ov, sub: failed ? "Didn't start before" : bad ? "Can't start (used up its tries)" : 'Can go back', heat: bad ? 'cold' : 'cool', back: true };
  x.why = !ov || ov === booted ? "There's no earlier version on this PC yet."
    : staged ? 'An update is waiting. Restart first.'
      : next ? `Version ${next} already starts on the next restart.`
        : u.busy || prog ? 'Wait for the update to finish.'
          : bad ? `Version ${ov} couldn't start, so you can't go back to it.` : '';
  x.ov = x.back ? ov : '';
  st.other = x;
  st.run = next || staged ? 'Until the next restart' : 'Running now';
  return st;
}

let shownKey = '';

export function render() {
  const u = S.u;
  if (!u) return;
  const st = view(u);
  byId('upd').dataset.state = byId('upd-status').dataset.state = st.state;
  const ch = (u.config && u.config.channel) || 'main';
  const badge = byId('upd-channel');
  badge.textContent = ch === 'main' ? 'Channel main' : `Test channel ${ch}`;
  badge.dataset.tone = ch === 'main' ? '' : 'warn';
  setText('upd-title', st.title);
  setText('upd-detail', st.detail);
  setText('upd-meta', st.meta || '').hidden = !st.meta;
  byId('upd-checking').hidden = !st.checking;
  const p = st.prog;
  byId('upd-progress').hidden = !p;
  if (p) {
    const pct = percent(p.percent);
    const phase = p.phase === 'check' ? 'Starting…' : phaseLabel(p.phase);
    setText('upd-phase', phase);
    setText('upd-pct', p.phase === 'check' ? '' : `${pct}%`);
    const bar = byId('upd-bar');
    setVar(bar, '--progress', pct / 100);
    bar.setAttribute('aria-valuenow', pct);
    bar.setAttribute('aria-valuetext', `${phase}, ${pct} percent`);
    setText('upd-bytes', p.total > 0 ? `${bytes(p.bytes)} of ${bytes(p.total)}` : '');
  }
  byId('upd-banners').replaceChildren(...st.banners.map(([tone, text]) => {
    const li = cloneTpl('tpl-upd-banner');
    li.dataset.tone = tone;
    part(li, 'text').textContent = text;
    return li;
  }));
  byId('upd-restart').hidden = st.key !== 'staged';
  byId('upd-reboot').hidden = st.key !== 'next';
  byId('upd-stage').hidden = st.key !== 'available';
  byId('upd-cancel').hidden = !p || p.phase === 'install' || S.starting;
  const check = byId('upd-check');
  check.disabled = !!p || st.checking;
  check.setAttribute('aria-busy', st.checking);
  setText('upd-check-label', st.checking ? 'Checking…' : 'Check now');

  const ab = u.booted_slot === 'a' ? ['A', 'B'] : u.booted_slot === 'b' ? ['B', 'A'] : ['', ''];
  setText('upd-run-ab', ab[0]);
  setText('upd-other-ab', ab[1]);
  setText('upd-run-version', u.booted || '');
  setText('upd-run-sub', st.run);
  const x = st.other;
  const oc = byId('upd-other');
  oc.dataset.heat = x.heat;
  setVar(oc, '--heat', p ? percent(p.percent) / 100 : 0);
  setText('upd-other-label', x.label);
  setText('upd-other-version', x.version);
  setText('upd-other-sub', x.sub);
  const back = byId('upd-back');
  // The version is right above; the name says it too (T7).
  back.replaceChildren('Go back', x.ov ? h('span', { class: 'sr-only', text: ` to ${x.ov}` }) : '');
  back.disabled = !!x.why;
  setText('upd-back-why', x.why);

  const failed = (u.failed || []).slice().sort(cmp).reverse();
  byId('upd-failed').hidden = !failed.length;
  byId('upd-failed-list').replaceChildren(...failed.map((v) => h('li', { class: 'upd-chip mono', text: v })));

  if (shownKey && shownKey !== st.key) announce(st.title);
  shownKey = st.key;
  byId('upd-status').removeAttribute('aria-busy');
  byId('upd-slots').removeAttribute('aria-busy');
  byId('upd-error').hidden = true;
  settleMain();
  more.then((m) => m.update(u, st));
}

export function take(u) {
  if (!u || typeof u !== 'object') return;
  S.u = u;
  if (u.busy && working(u.progress)) S.starting = false;
  if (!u.busy) S.checking = false;
  render();
}

let reget = 0;
// refresh re-reads /update after ms, passively: nobody asked for it.
export function refresh(ms = 0) {
  clearTimeout(reget);
  reget = setTimeout(() => api('GET', '/update', undefined, { passive: true }).then(take, () => {}), ms);
}

function listen() {
  on('update.progress', (p, live) => {
    if (!live || !p) return;
    if (p.phase === 'check') {
      S.checking = true;
      refresh(5000);
    } else if (working(p)) {
      S.live = p;
      S.starting = S.checking = false;
      S.failed = null;
    } else {
      const was = S.live && S.live.phase;
      S.live = null;
      S.starting = S.checking = false;
      if (p.phase === 'error' && S.u) {
        S.failed = p;
        announce(view(S.u).banners.map((x) => x[1]).pop(), { assertive: true });
      }
      if (p.phase === 'cancelled') more.then((m) => m.cancelled(was));
      refresh(800);
    }
    render();
  });
  // update.state is the whole update-state: replace, don't merge.
  on('update.state', (st) => {
    if (!S.u || !st) return;
    for (const k of KEYS) {
      if (k in st) S.u[k] = st[k];
      else delete S.u[k];
    }
    render();
  });
  onReconnect(() => refresh(0));
}

async function start() {
  listen();
  await shell('updates');
  (await more).start();
  const failed = (err) => more.then((m) => m.failedLoad(err));
  byId('upd-retry').addEventListener('click', () => api('GET', '/update').then(take, failed));
  (early || read()).then(take, failed);
  onStatus((snap) => {
    if (snap.update) take({ ...snap.update });
    // This page says itself what a restart installs; the row keeps the rest.
    const r = (snap.restart && snap.restart.reasons) || [];
    byId('restart-row').toggleAttribute('data-covered', r.length > 0 && r.every((x) => x.kind !== 'display'));
  });
}

start();
