// pages/home.js: Home (spec-cc-screens §3, MASTER-PLAN §3.5 C1). The shell's
// GET /status plus update.progress and power.idle; the words come from the
// pure summary.js and state.js, loaded beside /auth/me. Text is written only
// when it changes; the announcer speaks on a change of state, never a tick.

import { api, errorText, serverNow, url } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, h, icon, part, setVar } from '../core/dom.js';
import { link, on, onLink } from '../core/live.js';
import { getString, remove, setString } from '../core/store.js';
import { holdButton } from '../ui/hold.js';
import { settleMain } from '../ui/region.js';
import { current, notify, onStatus, refresh, scene, shell } from '../ui/shell.js';

const pure = Promise.all([import('../summary.js'), import('../state.js'), import('../fmt.js'), import('../ui/screen-shape.js')])
  .then((mods) => Object.assign({}, ...mods));
const sheets = () => import('../ui/sheet.js');
const dialog = () => import('../ui/dialog.js');
const thermal = () => import('../ui/thermal.js');

const DISMISSED = 'vos-dismissed-failed';
const HOLD = 'Hold to confirm, or tap to be asked first.';
const BTN = /* classes */ { primary: 'btn primary', ghost: 'btn ghost', holdPrimary: 'btn primary hold', holdGhost: 'btn ghost hold', smallPrimary: 'btn small primary', smallGhost: 'btn small ghost' };
const NEEDLE = { ready: 'ready', streaming: 'streaming', updating: 'updating', 'restart-needed': 'restart', asleep: 'asleep' };
const NET = /dial|lookup|timeout|deadline|connection|no such host|unreachable|Get "/i;

let M = null;
let painted = false;
let failure = ''; // GET /status failed before it ever answered
let asleep = false;
let awakeOn = false;
let heroActs = '';
let cardsOpen = false;
const live = { progress: null, error: null, power: null, powerSig: '' };
const sun = { checked: false, pending: false, err: null, doc: null, retried: false };
const said = { hero: '', power: '' };
const cards = new Map(); // id → {li, sig}

const put = (el, text) => {
  if (el.textContent !== text) el.textContent = text;
};
const answered = () => !!(current().update || current().display);
const again = () => {
  sun.checked = false;
  refresh(true);
};
const host = (snap) => M.hostOf(snap, byId('hero-host').textContent.replace(/\.local$/, ''));
// The viewfinder names the machine as the prototype does: vapor, not
// vapor.local, which the app bar already says.
const shortHost = (snap) => host(snap).replace(/\.local$/, '');

// The shell's /status with Home's live events. When its sunshine part
// failed, one GET /sunshine says why: 503 starting, 502 not answering (T1).
function snapshot() {
  const s = current();
  const snap = { ...s, live: { ...(s.live || {}), progress: live.progress } };
  // power.idle is newer than /status's power until the next /status.
  const sig = JSON.stringify({ ...s.power, wol: 0 });
  if (sig !== live.powerSig) {
    live.powerSig = sig;
    live.power = null;
  }
  if (s.power && live.power) snap.power = { ...s.power, ...live.power };
  if (!answered()) return snap;
  if (s.sunshine) {
    sun.checked = false;
    return snap;
  }
  if (!sun.checked) {
    sun.checked = sun.pending = true;
    api('GET', '/sunshine', undefined, { share: true, passive: true })
      .then((doc) => Object.assign(sun, { err: null, doc }), (e) => Object.assign(sun, { doc: null, err: { status: e.status, message: e.message } }))
      .then(() => {
        sun.pending = false;
        if (sun.err?.status === 503 && !sun.retried) {
          sun.retried = true; // one more look after 5 s (§3.2 #4)
          setTimeout(again, 5000);
        }
        render();
      });
  }
  if (sun.pending) snap.pending = true;
  else if (sun.err) snap.sunshineError = sun.err;
  else snap.sunshine = sun.doc;
  return snap;
}

function heroOf(snap) {
  if (asleep) {
    return {
      key: 'asleep', state: 'asleep', reason: '', title: 'Asleep', progress: null,
      detail: 'VaporOS stopped answering. Wake it from Moonlight, or press its power button. This page reconnects by itself.',
      // Home hides the offline banner that says the same: the hero offers its key.
      chips: [`Waiting for ${host(snap)}`], actions: [{ id: 'wake', label: 'How to wake it', quiet: true }],
    };
  }
  if (failure) return { ...M.heroModel({ display: {}, update: {}, sunshineError: { status: 0 } }), detail: failure, actions: [{ id: 'retry', label: 'Try again' }] };
  return M.heroModel(snap.pending ? {} : snap);
}

// "Ready / to stream": the lead word keeps its size and the rest drops to
// half, as on the TV; anything else is one balanced heading.
function renderTitle(title) {
  const el = byId('hero-title');
  if (el.dataset.title === title) return;
  el.dataset.title = title;
  const m = /^(\S+) (to .+)$/.exec(title);
  el.toggleAttribute('data-long', !m && title.length > 13);
  el.replaceChildren(...(m ? [h('span', { class: 'hero-word-lead', text: m[1] }), ' ', h('span', { class: 'hero-word-rest', text: m[2] })] : [h('span', { class: 'hero-word-lead', text: title })]));
}

function renderHero(m, snap) {
  const hero = byId('hero');
  hero.dataset.state = m.state;
  hero.dataset.reason = m.reason;
  const pair = m.attention === 'pair';
  if (pair) hero.dataset.attention = 'pair';
  else delete hero.dataset.attention;
  // Idle power-off is close: the field cools and the needle says idle.
  hero.toggleAttribute('data-idle', !!m.idle);
  // Not live, not yet asleep: the words are the last known ones, so the
  // field cools and dims and the needle says so (NEW-1).
  const stale = !asleep && painted && link.state !== 'live';
  hero.toggleAttribute('data-stale', stale);
  // The field is painted off this thread; the stale dim is CSS.
  thermal().then((t) => t.heat(hero.querySelector('.heat-field'), pair ? 'pair' : m.state, { idle: !!m.idle }));
  put(byId('hero-needle'), stale ? 'last seen' : pair ? 'pairing' : m.idle ? 'idle' : NEEDLE[m.state] || m.reason || 'checking');
  renderTitle(m.title);
  put(byId('hero-host'), shortHost(snap));
  const gpu = byId('hero-gpu');
  put(gpu, snap.system?.gpu?.name || '');
  gpu.hidden = !gpu.textContent;

  // The virtual screen exists only while a stream runs: drawn then, at the
  // client's mode. Otherwise its mode is a tag under the word.
  const streaming = m.state === 'streaming';
  const mode = (streaming && M.session(snap)?.mode) || snap.display?.current || '';
  const shape = part(hero, 'shape');
  M.setShape(shape, mode, { state: m.state });
  shape.toggleAttribute('data-shown', streaming && !!mode);

  const prog = m.progress != null ? M.updateProgress(snap) : null;
  put(byId('hero-detail'), prog ? m.detail.replace(/^.*?%\.\s*/, '') : m.detail);
  const label = mode ? M.modeLabel(mode) : '';
  const ul = byId('hero-chips');
  const sig = `${streaming}|${m.chips.join('|')}`;
  if (ul.dataset.chips !== sig) {
    ul.dataset.chips = sig;
    // While streaming the viewfinder's readout shows the mode (the list
    // still says it in words); otherwise the tag does.
    ul.replaceChildren(...m.chips.map((c) => h('li', { class: streaming && c === label ? 'sr-only' : 'hero-tag', 'data-tone': c === 'HDR' ? 'hot' : null, 'data-kind': c === label ? 'mode' : null, text: c })));
  }
  const box = byId('hero-progress');
  box.hidden = !prog;
  if (prog) {
    const pct = M.percent(prog.percent);
    const phase = M.phaseLabel(prog.phase);
    put(byId('hero-phase'), phase);
    setVar(box, '--pct', pct);
    setVar(box, '--progress', pct / 100);
    byId('hero-bar').setAttribute('aria-valuenow', String(pct));
    byId('hero-bar').setAttribute('aria-valuetext', `${phase}, ${pct} percent`);
  }
  renderActions(m, snap);
}

// The state's own action, redrawn only when it changes, so focus stays.
function renderActions(m, snap) {
  const box = byId('hero-actions');
  const key = m.actions.map((a) => `${a.id}:${a.label}`).join('|');
  if (key !== heroActs) {
    heroActs = key;
    box.replaceChildren(...m.actions.map((a, i) => heroAction(a, i === 0)));
    if (!box.querySelector('.hold')) put(byId('hero-hold-hint'), '');
  }
  const hold = box.querySelector('.hold');
  if (hold) {
    const plan = M.powerPlan('reboot', snap);
    hold.vosHold.setHold(plan.hold, !plan.hold ? plan.hint : hold.dataset.hold === 'reboot' && plan.hint ? `${HOLD} ${plan.hint}` : HOLD);
  }
}

function heroAction(a, first) {
  // White-hot is for the thing to act on, never a way somewhere (quiet).
  const primary = first && !a.quiet;
  if (a.href) return h('a', { class: primary ? BTN.primary : BTN.ghost, href: url(a.href), text: a.label });
  if (a.id === 'activate' || a.id === 'reboot') {
    const btn = h('button', { class: primary ? BTN.holdPrimary : BTN.holdGhost, type: 'button', 'data-hold': a.id, 'aria-describedby': 'hero-hold-hint' },
      icon('restart'), a.label, h('span', { class: 'hold-fill', 'aria-hidden': 'true' }));
    const v = () => M.stagedVersion(snapshot());
    btn.vosHold = holdButton(btn, {
      hint: byId('hero-hold-hint'),
      key: a.label,
      confirm: () => dialog().then((d) => d.confirmDialog(a.id === 'activate' ? { id: 'activate', vars: { v: v() } } : M.powerPlan('reboot', snapshot()).confirm)),
      run: () => scene().then((s) => s.powerAction(a.id, { confirmed: true, snap: snapshot(), version: v() })),
    });
    return btn;
  }
  const btn = h('button', { class: primary ? BTN.primary : BTN.ghost, type: 'button', text: a.label });
  btn.addEventListener('click', async () => {
    if (a.id === 'stream-details') (await sheets()).openSheet(byId('stream-sheet'), { invoker: btn });
    else if (a.id === 'pin') (await import('../ui/pinpad.js')).openPinpad({ from: btn });
    else if (a.id === 'wake') (await scene()).asleep({ from: link.since });
    else if (a.id === 'retry') load();
    else if (a.id === 'sunrestart') {
      await write(btn, () => api('POST', '/sunshine/restart', {}), 'Streaming is restarting.');
      setTimeout(again, 3000);
    }
  });
  return btn;
}

// write runs one request from a button: busy while it runs, a notice after.
// The button stays focusable (disabled would drop focus to <body>); presses
// while it is busy do nothing.
async function write(btn, call, done) {
  if (btn.getAttribute('aria-busy') === 'true') return null;
  btn.setAttribute('aria-busy', 'true');
  btn.setAttribute('aria-disabled', 'true');
  try {
    const r = await call();
    notify(typeof done === 'function' ? done(r) : done, { kind: 'ok' });
    return r;
  } catch (err) {
    notify(await errorText(err), { kind: 'error' });
    return null;
  } finally {
    btn.removeAttribute('aria-busy');
    btn.removeAttribute('aria-disabled');
  }
}

function renderKeys(snap) {
  const p = snap.power;
  const until = p?.keep_awake_until && Date.parse(p.keep_awake_until) > serverNow() ? p.keep_awake_until : '';
  const off = !!p && !p.idle_shutdown && !until;
  const awake = byId('home-awake');
  awakeOn = !!until;
  // A toggle keeps its name (Stay awake 1 h); pressed says it is on, and
  // the line under it says until when (APG button pattern).
  awake.setAttribute('aria-pressed', String(awakeOn));
  const sub = byId('home-awake-sub');
  put(sub, until ? `Awake until ${M.clock(until, new Date(serverNow()))}` : off ? 'Idle power-off is off' : '');
  sub.hidden = !sub.textContent;
  awake.disabled = asleep || !p || off;
  const plans = ['reboot', 'poweroff'].map((k) => M.powerPlan(k, snap));
  const text = [plans.some((x) => x.hold) ? HOLD : 'Tap to be asked first.', ...new Set(plans.map((x) => x.hint).filter(Boolean))].join(' ');
  ['home-reboot', 'home-poweroff'].forEach((id, i) => {
    byId(id).disabled = asleep || !answered();
    byId(id).vosHold.setHold(plans[i].hold, text);
  });
  byId('home-power').disabled = asleep || !answered();
}

function renderLines(snap) {
  const el = byId('home-power-line');
  el.hidden = asleep; // what kept it on no longer holds
  const line = snap.power ? M.powerLine(snap.power, { now: serverNow() }) : "Couldn't read the power settings.";
  if (el.dataset.line !== line) {
    el.dataset.line = line;
    el.replaceChildren(...[h('span', { text: line }), !snap.power && ' ', !snap.power && h('button', { class: BTN.smallGhost, type: 'button', text: 'Try again', onclick: () => load() })].filter(Boolean));
  }
  put(byId('power-sheet-line'), snap.power ? line : '');
  // Speak when what keeps it on changes, not each minute of a countdown.
  const kind = line.replace(/\d+/g, '#');
  if (painted && kind !== said.power) announce(line);
  said.power = kind;
  const status = byId('home-status');
  const words = snap.update ? M.statusLine(snap.update, serverNow()) : "Couldn't read the update status.";
  if (status.dataset.line !== words) {
    status.dataset.line = words;
    // The version is set in the mono face, as everywhere else.
    const v = snap.update?.booted;
    const at = v ? words.indexOf(v) : -1;
    status.replaceChildren(h('span', {}, ...(at < 0 ? [words] : [words.slice(0, at), h('span', { class: 'mono', text: v }), words.slice(at + v.length)])));
  }
  renderFacts(snap);
}

// This PC's facts, from GET /status (the side column from the rail up).
function renderFacts(snap) {
  const sys = snap.system;
  if (!sys) return;
  put(byId('pc-addr'), host(snap));
  put(byId('pc-ip'), (sys.ips || []).find((ip) => !ip.includes(':')) || (sys.ips || [])[0] || 'Unknown');
  put(byId('pc-gpu'), sys.gpu?.name || 'None found');
  put(byId('pc-cpu'), sys.cpu || 'Unknown');
  put(byId('pc-up'), sys.uptime_s != null ? M.duration(sys.uptime_s) : 'Unknown');
}

// One context card, then "Show <n> more" (MASTER-PLAN §3.5). A device
// waiting for its PIN goes above the hero, because the PIN is timed.
function renderCards(snap) {
  const list = answered() && !asleep ? M.contextCards(snap, { dismissedFailed: getString('local', DISMISSED), liveError: live.error }) : [];
  const pair = list[0]?.id === 'pair' ? list[0] : null;
  const rest = pair ? list.slice(1) : list;
  byId('home-pair').hidden = !pair;
  if (pair) place(byId('home-pair'), pair, 2, true);
  else drop('pair');
  const ul = byId('cards-list');
  for (const id of [...cards.keys()]) if (id !== 'pair' && !rest.some((c) => c.id === id)) drop(id);
  rest.forEach((c, i) => {
    const li = place(ul, c, 3, cardsOpen || (!pair && i === 0));
    if (ul.children[i] !== li) ul.insertBefore(li, ul.children[i] || null);
  });
  const shown = cardsOpen ? rest.length : Math.min(pair ? 0 : 1, rest.length);
  const more = byId('cards-more');
  more.hidden = rest.length <= (pair ? 0 : 1);
  more.setAttribute('aria-expanded', String(cardsOpen));
  put(more, cardsOpen ? 'Show fewer' : `Show ${rest.length - shown} more`);
  byId('cards').hidden = !rest.length;
}

// place puts card c into parent, rebuilding it only when its words change
// and keeping focus on the same action.
function place(parent, c, level, shown) {
  const sig = JSON.stringify([c, level]);
  let e = cards.get(c.id);
  if (e?.sig !== sig) {
    const had = e?.li.contains(document.activeElement) ? document.activeElement.dataset.act : '';
    const art = cardEl(c, level);
    const li = level === 2 ? art : h('li', {}, art);
    if (e) e.li.replaceWith(li);
    else parent.append(li);
    e = { li, sig };
    cards.set(c.id, e);
    if (had) li.querySelector(`[data-act="${had}"]`)?.focus();
  }
  if (e.li.parentElement !== parent) parent.append(e.li);
  e.li.hidden = !shown;
  return e.li;
}

// drop removes a card; focus in it moves to the next card, else the list.
function drop(id) {
  const e = cards.get(id);
  if (!e) return;
  cards.delete(id);
  const focused = e.li.contains(document.activeElement);
  const next = e.li.nextElementSibling;
  e.li.remove();
  if (focused) (next?.querySelector('button, a') || byId('cards-h')).focus({ preventScroll: true });
}

function cardEl(c, level) {
  const tid = `card-${c.id}-title`;
  let body = c.body || '';
  if (c.id === 'cant-check' && NET.test(body)) body = "Couldn't reach the update server. Check the PC's internet connection."; // T5
  return h('article', { class: 'ctx-card', 'data-tone': c.tone || null, 'aria-labelledby': tid },
    h(`h${level}`, { class: 'ctx-title', id: tid, text: c.title }),
    h('p', { class: 'ctx-body', text: body.charAt(0).toUpperCase() + body.slice(1) }),
    h('div', { class: 'ctx-actions' }, c.actions.map((a, i) => {
      const cls = i === 0 ? BTN.smallPrimary : BTN.smallGhost;
      if (a.href) return h('a', { class: cls, href: url(a.href), 'data-act': a.id, text: a.label });
      const btn = h('button', { class: cls, type: 'button', 'data-act': a.id, text: a.label });
      btn.addEventListener('click', () => cardDo(c, a.id, btn));
      return btn;
    })));
}

async function cardDo(c, id, btn) {
  const snap = snapshot();
  const avail = snap.update?.available?.version;
  if (id === 'pin') (await import('../ui/pinpad.js')).openPinpad({ from: btn });
  else if (id === 'activate') (await scene()).powerAction('activate', { snap, version: c.version });
  else if (id === 'awake1h') {
    const r = await write(btn, () => api('POST', '/power/keep-awake', { minutes: 60 }), 'VaporOS stays awake for the next hour.');
    if (r) refresh(true);
  }
  else if (id === 'dismiss-failed') {
    setString('local', DISMISSED, c.version);
    render();
  } else if (id === 'stage') await write(btn, () => api('POST', '/update/stage', { version: c.version }), `Downloading version ${c.version}.`);
  else if (id === 'stage-retry') {
    live.error = null;
    await write(btn, () => api('POST', '/update/stage', avail ? { version: avail } : {}), 'Downloading the update again.');
  } else if (id === 'check') {
    await write(btn, () => api('POST', '/update/check', {}), (r) => (r?.available ? `Version ${r.available.version} is available.` : 'VaporOS is up to date.'));
    refresh(true);
  }
}

function render() {
  if (answered()) failure = '';
  if (!M || (!answered() && !failure && !asleep)) return;
  const snap = snapshot();
  const m = heroOf(snap);
  renderHero(m, snap);
  renderKeys(snap);
  renderLines(snap);
  renderCards(snap);
  if (painted && `${m.key}|${m.title}` !== said.hero) announce(m.title);
  said.hero = `${m.key}|${m.title}`;
  if (painted) return;
  painted = true;
  byId('home').removeAttribute('aria-busy');
  settleMain();
  // After Restart to update: say that the new version runs (§2.5).
  const want = getString('session', 'vos-expect-version');
  if (want && snap.update) {
    remove('session', 'vos-expect-version');
    if (snap.update.booted === want) notify(`VaporOS ${want} is running.`, { kind: 'ok' });
  }
}

// load asks for /status (the shell's refresh) and learns whether it failed.
function load(again = true) {
  if (again) refresh(false);
  api('GET', '/status', undefined, { share: true, passive: true }).then(() => {
    if (!answered()) refresh(true); // this answer was ours, not the shell's
  }, async (err) => {
    if (err.status === 401 || answered()) return;
    failure = await errorText(err);
    render();
  });
}

function wire() {
  byId('home-awake').addEventListener('click', async (e) => {
    const start = !awakeOn;
    const r = await write(e.currentTarget, () => api('POST', '/power/keep-awake', { minutes: start ? 60 : 0 }), start ? 'VaporOS stays awake for the next hour.' : 'Staying awake is off.');
    if (r) refresh(true);
  });
  byId('home-power').addEventListener('click', (e) => {
    const from = e.currentTarget;
    sheets().then((s) => s.openSheet(byId('power-sheet'), { invoker: from }));
  });
  [['home-reboot', 'reboot', 'Restart'], ['home-poweroff', 'poweroff', 'Power off']].forEach(([id, kind, key]) => {
    byId(id).vosHold = holdButton(byId(id), {
      hint: byId('home-hold-hint'),
      key,
      // One layer at a time: the sheet steps aside for the question, and
      // focus waits on Power, where it comes back after a Cancel.
      confirm: () => {
        byId('power-sheet').close();
        byId('home-power').focus({ preventScroll: true });
        return dialog().then((d) => d.confirmDialog(M.powerPlan(kind, snapshot()).confirm));
      },
      run: () => {
        byId('power-sheet').close();
        scene().then((s) => s.powerAction(kind, { confirmed: true, snap: snapshot() }));
      },
    });
  });
  byId('cards-more').addEventListener('click', () => {
    cardsOpen = !cardsOpen;
    render();
  });
  on('update.progress', (p, isLive) => {
    if (!isLive || !p) return;
    live.progress = M.working(p) ? p : null;
    if (p.phase === 'error') live.error = p;
    else if (['done', 'idle', 'cancelled'].includes(p.phase)) live.error = null;
    render();
  });
  on('power.idle', (p, isLive) => {
    if (!isLive || !p) return;
    live.power = { idle_seconds: p.idle_seconds, shutdown_in: p.shutdown_in ?? null };
    if ('busy' in p) live.power.busy = p.busy;
    render();
  });
  // Down for a minute after being live: most likely asleep (NEW-1). The
  // page keeps the last known state and the hero says so.
  let wasLive = false;
  let timer = 0;
  onLink((l) => {
    clearTimeout(timer);
    if (painted) render(); // the hero goes stale, or fresh again
    if (l.state === 'live') {
      wasLive = true;
      if (asleep) {
        asleep = false;
        render();
      }
    } else if (wasLive) {
      timer = setTimeout(() => {
        asleep = true;
        render();
      }, Math.max(0, 60000 - (Date.now() - (l.since || Date.now()))));
    }
  });
}

shell('home').then(async () => {
  load(false);
  M = await pure;
  wire();
  onStatus(render);
});
