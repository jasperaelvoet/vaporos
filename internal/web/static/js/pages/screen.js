// pages/screen.js: Screen (spec-cc-screens §5). This module paints Right now,
// the page's first thing; screen-more.js, fetched alongside, runs the cards
// below it. The page asks /display, /sunshine and /sunshine/settings once;
// then the shell's passive /status refreshes (which carry GET /display
// whole) keep the display current.

import { api, errorText } from '../core/api.js';
import { byId, cloneTpl, h, part, setText } from '../core/dom.js';
import { modeLabel, parseMode } from '../fmt.js';
import { settleMain } from '../ui/region.js';
import { setShape } from '../ui/screen-shape.js';
import { current, onStatus, shell } from '../ui/shell.js';

// The reads start before boot (ARCH §7.4); a 401 is boot's to handle.
export const early = {
  display: api('GET', '/display', undefined, { share: true }),
  sunshine: api('GET', '/sunshine'),
  settings: api('GET', '/sunshine/settings'),
};
for (const p of Object.values(early)) p.catch(() => {});
const more = import('./screen-more.js');

export const S = { display: null, sunshine: null, sunshineErr: null, settings: null };

export function settle(el) {
  el.removeAttribute('aria-busy');
  delete el.dataset.late;
  settleMain();
}

// cardError shows a card's inline error (R3): <prefix>-error with its text.
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

// nowModel is Right now (§5.2): the state, its words and the shape's mode.
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
  // A new state stretches its words into its cut (styles/pages/screen.css).
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

// views are the other cards' renderers (screen-more.js); they run only when
// the display changed, so a focused control stays put.
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
  (await more).start();
  byId('now-retry').addEventListener('click', () => api('GET', '/display').then(renderDisplay, displayFailed));
  early.display.then(renderDisplay, (err) => S.display || displayFailed(err));
  onStatus((snap) => {
    const gpu = snap.system && snap.system.gpu;
    if (gpu && gpu.name) setText('now-gpu', gpu.supported === false ? `${gpu.name} (not supported)` : gpu.name);
    if (snap.display) renderDisplay(snap.display);
    else if (S.display) renderNow(S.display);
  });
}

start();
