// pages/devices-list.js: the rest of Devices, loaded beside devices.js
// (spec-cc-screens §4.2, §4.5, §4.6): the addresses to add this PC by,
// Playing now, and the paired list (GET /sunshine/clients) with each
// device's last mode from /display devices[], joined by name.

import { api, errorText, serverNow } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, cloneTpl, h, icon, part } from '../core/dom.js';
import { onReconnect } from '../core/live.js';
import { ago, clock, modeLabel, plural } from '../fmt.js';
import { isStreaming, session } from '../state.js';
import { region } from '../ui/region.js';
import { notify } from '../ui/shell.js';

const dialog = () => import('../ui/dialog.js');
const copy = (el) => import('../ui/clipboard.js').then((m) => m.bindCopy(el));

let snap = {};
let paired = null;
let err = null;
let hooks;
let reg;
let retries = 0;
let listKey = '';
let seenKey = '';
let addrKey = '';
let known = null; // uuids shown so far: a new one arrives hot

// clients is what the pair card needs to know of the list; null before.
export const clients = () => (paired || err ? { list: paired, err } : null);

export function init(opts) {
  hooks = opts;
  reg = region(byId('dev-paired'));
  byId('dev-end-stream').addEventListener('click', endStream);
  byId('dev-refresh').addEventListener('click', userRefresh);
  byId('dev-refresh').disabled = false;
  copy(byId('dev-addrs'));
  onReconnect(() => reload(true));
  reload(false);
}

export function update(s) {
  snap = s;
  host();
  playing();
  if (paired) render();
}

// IPv4, then IPv6 that isn't link-local, then the .local name (J/pair).
function host() {
  const sys = snap.system;
  if (!sys) return;
  const mdns = sys.mdns || (sys.hostname ? `${sys.hostname}.local` : '');
  if (sys.hostname) byId('dev-host').textContent = sys.hostname;
  if (mdns) {
    byId('dev-pair-host').textContent = mdns;
    const code = byId('dev-addr-mdns');
    code.textContent = mdns;
    code.nextElementSibling.querySelector('.sr-only').textContent = ` ${mdns}`;
  }
  const ips = Array.isArray(sys.ips) ? sys.ips : [];
  const list = [...ips.filter((a) => /^\d+\.\d+\.\d+\.\d+$/.test(a)), ...ips.filter((a) => a.includes(':') && !/^fe[89ab]/i.test(a))];
  if (list.join() === addrKey) return;
  addrKey = list.join();
  const ul = byId('dev-addrs');
  for (const li of ul.querySelectorAll('[data-ip]')) li.remove();
  list.forEach((a, i) => {
    const li = cloneTpl('tpl-dev-addr');
    li.dataset.ip = a;
    const code = part(li, 'value');
    code.id = `dev-addr-${i}`;
    code.textContent = a;
    part(li, 'copy').dataset.copyTarget = code.id;
    part(li, 'copy-name').textContent = ` ${a}`;
    ul.insertBefore(li, ul.lastElementChild);
  });
  copy(ul);
}

function playing() {
  const box = byId('dev-playing');
  if (!isStreaming(snap)) {
    if (!box.hidden && box.contains(document.activeElement)) byId('dev-paired-title').focus();
    box.hidden = true;
    return;
  }
  const ss = session(snap) || {};
  const m = ss.mode || (snap.display && snap.display.current);
  const since = ss.since ? clock(ss.since) : '';
  byId('dev-play-by').textContent = ss.client || 'A device';
  byId('dev-play-app').textContent = ss.app || '';
  byId('dev-play-app-row').hidden = !ss.app;
  byId('dev-play-mode').textContent = m ? modeLabel(m) : 'Unknown';
  byId('dev-play-hdr').textContent = ss.hdr ? 'On' : 'Off';
  byId('dev-play-since').textContent = since;
  byId('dev-play-since-row').hidden = !since;
  box.hidden = false;
}

// busy keeps a button focusable, which disabled would not (ARCH §6.10).
const isBusy = (btn) => btn.getAttribute('aria-busy') === 'true';
function busy(btn, on) {
  for (const a of ['aria-busy', 'aria-disabled']) {
    if (on) btn.setAttribute(a, 'true');
    else btn.removeAttribute(a);
  }
}

async function endStream(e) {
  const btn = e.currentTarget;
  if (isBusy(btn) || !(await (await dialog()).confirmDialog({ id: 'endstream' }))) return;
  busy(btn, true);
  try {
    await api('POST', '/sunshine/end-stream', {});
    notify('The stream ended.', { kind: 'ok' });
  } catch (x) {
    notify(await errorText(x), { kind: 'error' });
  }
  busy(btn, false);
}

export async function reload(passive) {
  try {
    const r = await api('GET', '/sunshine/clients', undefined, { passive });
    paired = Array.isArray(r.clients) ? r.clients : [];
    err = null;
    retries = 0;
    render();
  } catch (x) {
    err = x;
    // Sunshine is still getting ready: look again by itself, a few times.
    if (x.status === 503 && retries++ < 6) setTimeout(() => reload(true), 5000);
    if (!paired || !passive) {
      paired = null;
      listKey = '';
      byId('dev-paired-count').textContent = '';
      const text = await errorText(x);
      reg.error(new Error(`Couldn't load paired devices. ${text}`), () => {
        reg.loading();
        reload(false);
      });
    }
  }
  hooks.changed();
}

async function userRefresh(e) {
  const btn = e.currentTarget;
  if (isBusy(btn)) return;
  busy(btn, true);
  await reload(false);
  busy(btn, false);
  if (paired) announce(`${plural(paired.length, 'paired device')}.`);
}

// mode keeps "2796 × 1290" and "120 Hz" whole when a narrow row wraps.
const mode = (s, hdr) => modeLabel(s, hdr).replace(/ × /g, ' × ').replace(/ Hz/g, ' Hz');
const used = (d, now) => [d.last_seen ? `Last used ${ago(d.last_seen, now)}` : '', d.mode ? mode(d.mode, d.hdr) : ''].filter(Boolean).join(' · ');
const devices = () => (snap.display && Array.isArray(snap.display.devices) ? snap.display.devices : []);

// Playing first, then the most recently used, else by name (§4.6).
function rows() {
  const seen = new Map(devices().map((d) => [d.name, d]));
  const ss = isStreaming(snap) ? session(snap) : null;
  const now = serverNow();
  return paired
    .map((c) => {
      const name = String(c.name || '').trim();
      const d = (name && seen.get(name)) || null;
      const on = !!(ss && ss.client && ss.client === name);
      const sub = on ? (ss.mode ? mode(ss.mode, ss.hdr) : '') : d ? used(d, now) : '';
      return { uuid: c.uuid, name, label: name || 'Unnamed device', enabled: c.enabled !== false, on, at: (d && Date.parse(d.last_seen)) || 0, sub };
    })
    .sort((a, b) => b.on - a.on || b.at - a.at || a.label.localeCompare(b.label));
}

function render() {
  const all = rows();
  byId('dev-paired-count').textContent = ` · ${plural(all.length, 'device')}`;
  seenBefore(all);
  const key = JSON.stringify(all.map((r) => [r.uuid, r.label, r.enabled, r.on, r.sub]));
  if (key === listKey) return;
  listKey = key;
  const fresh = known ? all.filter((r) => !known.has(r.uuid)) : [];
  known = new Set(all.map((r) => r.uuid));
  if (!all.length) {
    reg.empty('No devices yet. Pair one above to start playing.');
    return;
  }
  const keep = document.activeElement && document.activeElement.closest('[data-uuid]');
  const ul = h('ul', { class: 'rows', role: 'list' }, all.map((r) => row(r, fresh.includes(r))));
  reg.ready(() => ul);
  if (fresh.length) hooks.paired(fresh[fresh.length - 1].label);
  if (keep) unpairButton(keep.dataset.uuid)?.focus();
}

const unpairButton = (uuid) => byId('dev-paired').querySelector(`[data-uuid="${CSS.escape(uuid)}"] [data-part="unpair"]`);

function row(r, fresh) {
  const li = cloneTpl('tpl-dev-row');
  li.dataset.uuid = r.uuid;
  li.dataset.playing = String(r.on);
  if (fresh) {
    li.dataset.fresh = 'true';
    li.addEventListener('animationend', () => delete li.dataset.fresh, { once: true });
  }
  part(li, 'glyph').append(glyph(r.name));
  part(li, 'name').textContent = r.label;
  const sub = part(li, 'sub');
  sub.textContent = r.sub;
  sub.hidden = !r.sub;
  part(li, 'playing').hidden = !r.on;
  part(li, 'disabled').hidden = r.enabled;
  part(li, 'unpair-name').textContent = ` ${r.label}`;
  part(li, 'unpair').addEventListener('click', (e) => unpair(r, e.currentTarget));
  return li;
}

async function unpair(r, btn) {
  if (isBusy(btn) || !(await (await dialog()).confirmDialog({ id: 'unpair', vars: { name: r.label } }))) return;
  busy(btn, true);
  try {
    await api('DELETE', `/sunshine/clients/${encodeURIComponent(r.uuid)}`);
    notify(`${r.label} is unpaired.`, { kind: 'ok' });
  } catch (x) {
    // 404: someone unpaired it already, so it goes all the same.
    notify(await errorText(x, { name: r.label }), { kind: x.status === 404 ? 'info' : 'error' });
    if (x.status !== 404) return busy(btn, false);
  }
  // Focus moves to the next row's Unpair, else the one before, else Refresh.
  const li = btn.closest('li');
  const next = li.nextElementSibling || li.previousElementSibling;
  paired = paired.filter((c) => c.uuid !== r.uuid);
  render();
  ((next && unpairButton(next.dataset.uuid)) || byId('dev-refresh')).focus();
  reload(true);
}

// Seen before: names in clients.json that aren't paired under that name.
function seenBefore(all) {
  const names = new Set(all.map((r) => r.name));
  const extra = devices().filter((d) => d.name && !names.has(d.name));
  byId('dev-seen').hidden = !extra.length;
  byId('dev-seen-count').textContent = extra.length ? ` · ${extra.length}` : '';
  const key = JSON.stringify(extra);
  if (key === seenKey) return;
  seenKey = key;
  const now = serverNow();
  byId('dev-seen-list').replaceChildren(...extra.map((d) => {
    const li = cloneTpl('tpl-dev-seen');
    part(li, 'glyph').append(glyph(d.name));
    part(li, 'name').textContent = d.name;
    part(li, 'sub').textContent = used(d, now);
    return li;
  }));
}

// ⟦cc-device-glyph⟧: a kind guessed from the name; the name says the rest.
const GLYPHS = [
  [/\b(tv|shield|chromecast|bravia|fire ?stick|living ?room|bedroom)\b/i, () => icon('tv')],
  [/\b(deck|ally|legion go|switch|handheld)\b/i, () => icon('gamepad')],
  [/\b(iphone|pixel|galaxy|phone|android|ipad|tablet)\b/i, () => icon('phone')],
  [/\b(macbook|laptop|notebook|thinkpad|surface|chromebook)\b/i, () => icon('laptop')],
  [/\b(pc|desktop|imac|mac mini|monitor)\b/i, () => icon('monitor')],
];

function glyph(name) {
  const hit = GLYPHS.find(([re]) => re.test(name || ''));
  return hit ? hit[1]() : icon('devices');
}
