// pages/power.js: System › Power (spec-cc-screens §8): the state as a heat
// level from GET /power and power.idle, counted down here between events;
// power-more.js wires the controls, the adapters and the Wake card.

import { api, errorText, serverNow, signedInBefore } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, setText, setVar } from '../core/dom.js';
import { on, onReconnect } from '../core/live.js';
import { settleMain } from '../ui/region.js';
import { onStatus, shell } from '../ui/shell.js';
import { busyReason } from '../copy.js';
import { clock, duration } from '../fmt.js';

// The read starts before boot (ARCH §7.4) in a tab that was signed in;
// otherwise after it, so a signed-out visit makes no 401 before sign-in.
const read = () => api('GET', '/power');
const early = signedInBefore() ? read() : null;
early?.catch(() => {});
const more = import('./power-more.js');

// p: GET /power (wol kept from the last full answer); at: when shutdown_in
// and idle_seconds were true, so the countdown runs between events.
export const P = { p: null, at: 0 };

const T = (iso) => Date.parse(iso || '') || 0;

// view: the state card, highest priority first (§8.2, T2). heat: 0-1 on the
// Inferno scale; needle: its word.
export function view(p, now = serverNow()) {
  const reason = (p.busy && p.busy.reason) || '';
  const left = p.shutdown_in > 0 ? Math.max(0, p.shutdown_in - Math.floor((now - P.at) / 1000)) : 0;
  const idle = (p.idle_seconds || 0) + Math.floor((now - P.at) / 1000);
  const n = p.idle_minutes || 0;
  if (T(p.keep_awake_until) > now) return { key: 'awake', title: `Staying awake until ${clock(p.keep_awake_until, new Date(now))}`, detail: 'Idle power-off is paused until then.', stop: true, heat: 0.66, needle: 'awake' };
  if (reason === 'manual keep-awake') return { key: 'manual', title: 'Staying awake', detail: 'A keep-awake file is set.', stop: true, heat: 0.66, needle: 'awake' };
  if (reason && !p.busy.web && reason !== 'web UI in use') {
    const hot = /^streaming|Moonlight|game/i.test(reason);
    return { key: 'busy', title: 'Staying on', detail: `${busyReason(reason)}.`, heat: hot ? 0.87 : 0.6, needle: hot ? 'streaming' : 'busy' };
  }
  if (p.busy && p.busy.web && p.idle_shutdown) {
    const off = T(p.web_until) ? clock(new Date(T(p.web_until) + n * 60e3).toISOString(), new Date(now)) : '';
    // Whoever reads this has the page open: the title is the PC's state.
    return { key: 'web', title: off ? `On until about ${off}` : 'On', detail: `Idle power-off starts ${n} min after this page closes.`, heat: 0.42, needle: 'on' };
  }
  if (p.idle_shutdown && left > 0) {
    return { key: 'countdown', title: `Powers off in ${duration(left)}`, detail: `Nobody has played for ${duration(idle)}. Start a stream to keep it on.`, awake: true,
      heat: 0.05 + Math.round(18 * Math.min(1, left / Math.max(60, n * 60))) / 50, needle: 'idle', left };
  }
  if (p.idle_shutdown) return { key: 'policy', title: `Powers off after ${n} minutes idle`, detail: 'Idle means no stream, no game, no download and no update.', heat: 0.42, needle: 'on' };
  return { key: 'always', title: 'Always on', detail: 'Idle power-off is off.', heat: 0.42, needle: 'on' };
}

const two = (x) => String(x).padStart(2, '0');
let shownKey = '';

export function render() {
  const p = P.p;
  if (!p) return;
  const v = view(p);
  const card = byId('pwr-state');
  card.dataset.level = v.key;
  card.dataset.needle = v.needle;
  setVar(card, '--p-heat', v.heat.toFixed(3));
  setText('pwr-needle', v.needle);
  setText('pwr-count', v.left ? `${two(Math.floor(v.left / 60))}:${two(v.left % 60)}` : '');
  setText('pwr-title', v.title);
  setText('pwr-detail', v.detail);
  byId('pwr-state-stop').hidden = !v.stop;
  byId('pwr-state-awake').hidden = !v.awake;
  // The can't-wake banner needs the adapters (GET /power has them).
  const wol = Array.isArray(p.wol) ? p.wol : null;
  const banner = byId('pwr-cantwake');
  banner.hidden = !(p.idle_shutdown && wol && !wol.some((w) => w.enabled));
  banner.textContent = wol && wol.length ? 'Nothing can wake VaporOS remotely. When it powers off, only its power button starts it again.' : 'No wired network adapter, so nothing can wake it remotely.';
  if (shownKey && shownKey !== v.key) announce(v.title);
  shownKey = v.key;
  card.removeAttribute('aria-busy');
  byId('pwr-error').hidden = true;
  settleMain();
  more.then((m) => m.update(p, v));
}

// take merges an answer: /status's power part has no wol.
export function take(p) {
  if (!p || typeof p !== 'object') return;
  P.p = { ...(P.p || {}), ...p };
  if (!('keep_awake_until' in p)) delete P.p.keep_awake_until;
  if (!('web_until' in p)) delete P.p.web_until;
  P.at = serverNow();
  render();
}

async function failedLoad(err) {
  if (P.p) return;
  byId('pwr-error').hidden = false;
  setText('pwr-error-text', `Couldn't load power settings. ${await errorText(err)}`);
  setText('pwr-title', 'Power status unknown');
  setText('pwr-detail', '');
  for (const id of ['pwr-state', 'pwr-idle', 'pwr-awake', 'pwr-wol']) byId(id).removeAttribute('aria-busy');
  settleMain();
}

async function start() {
  on('power.idle', (ev) => {
    if (!P.p || !ev) return;
    const was = (P.p.busy && P.p.busy.reason) || '';
    P.p = { ...P.p, idle_seconds: ev.idle_seconds, shutdown_in: ev.shutdown_in, busy: ev.busy || null };
    P.at = serverNow();
    render();
    // A new reason may bring times the event lacks (web_until, keep-awake).
    if (((ev.busy && ev.busy.reason) || '') !== was) more.then((m) => m.reread());
  });
  onReconnect(() => more.then((m) => m.reread()));
  await shell('power');
  (await more).start();
  byId('pwr-retry').addEventListener('click', () => api('GET', '/power').then(take, failedLoad));
  (early || read()).then(take, failedLoad);
  onStatus((snap) => {
    if (!snap.power || !P.p) return;
    // The shell's copy of the adapters may be a day old; GET /power is not.
    const { wol, ...rest } = snap.power;
    take(rest);
  });
  // The countdown and "until" times move on without a request (§8).
  let tick = 0;
  setInterval(() => {
    if (document.hidden || !P.p) return;
    tick++;
    if (view(P.p).key === 'countdown' || tick % 30 === 0) render();
  }, 1000);
}

start();
