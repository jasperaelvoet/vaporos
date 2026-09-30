// pages/screen.js: Screen (spec-cc-screens §5). This paints Right now;
// screen-more.js, fetched alongside, the cards below. Right now comes from
// the shell's GET /status (its display part is GET /display whole), which
// also keeps it current: a page view asks /status, /sunshine and
// /sunshine/settings, nothing more.

import { api, errorText, signedInBefore } from '../core/api.js';
import { byId, cloneTpl, h, part, setText } from '../core/dom.js';
import { modeLabel, parseMode } from '../fmt.js';
import { settleMain } from '../ui/region.js';
import { setShape } from '../ui/screen-shape.js';
import { current, onStatus, shell } from '../ui/shell.js';

// The reads start before boot (ARCH §7.4) in a tab that was signed in;
// otherwise after it, so a signed-out visit makes no 401 before sign-in.
const reads = () => {
  const r = { sunshine: api('GET', '/sunshine'), settings: api('GET', '/sunshine/settings') };
  for (const p of Object.values(r)) p.catch(() => {});
  return r;
};
export let early = signedInBefore() ? reads() : null;
const more = import('./screen-more.js');

export const S = { display: null, sunshine: null, sunshineErr: null, settings: null };

export function settle(el) {
  el.removeAttribute('aria-busy');
  delete el.dataset.late;
  settleMain();
}

// cardError: a card's inline error (R3).
export async function cardError(prefix, err, lead) {
  const text = await errorText(err);
  byId(`${prefix}-error`).hidden = false;
  setText(`${prefix}-error-text`, `${lead} ${text}`);
}

export function session() {
  const snap = current();
  return (snap.live && snap.live.session) || (S.sunshine && S.sunshine.session) || snap.stream || null;
}

export const streaming = () => (S.display && S.display.state === 'streaming') || !!(S.sunshine && S.sunshine.streaming) || !!session();

// Right now (§5.2): the state, its words and the shape's mode.
function nowModel(d) {
  if (d.profile === 'none') return { state: 'fault', title: 'No supported graphics card', detail: 'Streaming needs an AMD Radeon GPU. The welcome screen still works.' };
  const mode = d.current || '';
  if (d.state === 'streaming') {
    const s = session() || {};
    const m = mode || s.mode || '';
    const hdr = `HDR ${s.hdr ? 'on' : 'off'}`;
    return { state: 'streaming', title: s.client ? `Streaming to ${s.client}` : 'Streaming', detail: m ? `${modeLabel(m)} · ${hdr}` : hdr, mode: m, hdr: !!s.hdr, label: s.client };
  }
  if (d.state === 'gaming') return { state: 'ready', title: 'Steam is running on the virtual screen', detail: mode && modeLabel(mode), mode, label: 'Steam' };
  if (d.state === 'welcome') return { state: '', title: 'The monitor shows the welcome screen', detail: 'The virtual screen is off until a game starts.', mode };
  return { state: '', title: 'Nothing is running', detail: '', mode };
}

let art = null;
let shownState = null;

export function renderNow(d) {
  const card = byId('now');
  const m = nowModel(d);
  const fig = part(card, 'shape');
  if (!art) part(fig, 'frame').append((art = cloneTpl('tpl-scr-art')));
  card.dataset.state = m.state;
  delete card.dataset.phase;
  setShape(fig, m.mode, { hdr: m.hdr, state: m.state });
  if (!parseMode(m.mode)) part(fig, 'readout').textContent = m.state === 'fault' ? 'No virtual screen' : 'No picture';
  const label = part(art, 'label');
  label.textContent = m.label || '';
  label.hidden = !m.label;
  // A new state stretches its words into its cut.
  const title = byId('now-title');
  if (shownState !== m.state || title.textContent !== m.title) {
    const fresh = title.cloneNode(false);
    if (shownState !== null && shownState !== m.state) fresh.dataset.fresh = '';
    fresh.textContent = m.title;
    title.replaceWith(fresh);
  }
  shownState = m.state;
  setText('now-detail', m.detail || '');
  const planes = Number(d.planes) || 0;
  byId('now-layers').hidden = m.state === 'fault' || planes < 2;
  setText('now-layers-text', `${planes} layers: a game draws on its own layer, and the picture may freeze.`);
  setText('now-port', d.virtual_connector || '–');
  byId('now-error').hidden = true;
  settle(card);
}

export function displayFailed(err) {
  cardError('now', err, "Couldn't load the screen.");
  setText('now-title', 'Screen status unknown');
  byId('now').dataset.phase = '';
  for (const id of ['now', 'virtual', 'modes-card']) settle(byId(id));
}

// Ports and layers (§5.7), a disclosure.
const LAYERS = ['Nothing on the virtual screen', 'One layer, as streaming needs'];

function renderPorts(d) {
  const conns = d.connectors || [];
  byId('ports').replaceChildren(...conns.map((c) => {
    const li = cloneTpl('tpl-scr-port');
    part(li, 'name').textContent = c.name;
    const tags = [c.name === d.virtual_connector && ['virtual', 'Virtual screen'], c.physical && ['monitor', 'Monitor'], ['', c.status === 'connected' ? 'Connected' : 'Disconnected']];
    part(li, 'tags').replaceChildren(...tags.filter(Boolean).map(([kind, text]) => h('span', { class: 'scr-tag', dataset: kind ? { kind } : null, text })));
    return li;
  }));
  byId('ports').hidden = !conns.length;
  byId('ports-empty').hidden = !!conns.length;
  const n = Number(d.planes) || 0;
  byId('layers-row').hidden = d.profile === 'none';
  setText('layers', LAYERS[n] || `${n} layers: a game draws on its own layer, and the picture may freeze`);
}

// The other cards (screen-more.js) render only when the display changed,
// so a focused control stays put.
export const views = [renderPorts];
let shown = '';

export function renderDisplay(d) {
  if (!d) return;
  S.display = d;
  renderNow(d);
  const sig = JSON.stringify(d);
  if (sig === shown) return;
  shown = sig;
  for (const fn of views) fn(d);
}

async function start() {
  await shell('screen');
  early = early || reads();
  (await more).start();
  byId('now-retry').addEventListener('click', () => api('GET', '/display').then(renderDisplay, displayFailed));
  // The shell's GET /status is in flight: share it to learn if it fails.
  api('GET', '/status', undefined, { share: true }).then(
    (st) => st.display || S.display || displayFailed(new Error('')),
    (err) => S.display || displayFailed(err),
  );
  onStatus((snap) => {
    const gpu = snap.system && snap.system.gpu;
    if (gpu && gpu.name) setText('now-gpu', gpu.supported === false ? `${gpu.name} (not supported)` : gpu.name);
    if (snap.display) renderDisplay(snap.display);
    else if (S.display) renderNow(S.display);
  });
}

start();
