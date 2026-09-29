// ui/notices.js: notices and Recent (spec-cc-screens §2.6). Errors and
// notices with an action stay; the rest time out, paused while touched.
// Both live regions exist from the first paint, so notices are announced.

import { byId, cloneTpl, part } from '../core/dom.js';
import { getJSON, remove, setJSON } from '../core/store.js';
import { url } from '../core/api.js';

const LIFE = { ok: 4000, info: 4000, warn: 8000 };
const MAX = 3;
const RECENT = 'vos-recent';
const FLASH = 'vos-flash';
const shown = new Map(); // id → {el, timer}
let seq = 0;

// notify shows text. kind: ok, info, warn or error. action: {label,
// onClick} or {label, href}. id merges repeats and lets dismiss() find it.
export function notify(text, { kind = 'info', action = null, id = '', sticky = false } = {}) {
  if (!text) return null;
  const key = id || `n${++seq}`;
  const old = shown.get(key);
  if (old) old.el.remove();
  const el = cloneTpl('tpl-notice');
  el.dataset.kind = kind;
  part(el, 'text').textContent = text;
  part(el, 'close-name').textContent = `Dismiss: ${text}`;
  if (action?.href) {
    const a = part(el, 'link');
    a.href = url(action.href);
    a.textContent = action.label;
    a.hidden = false;
  } else if (action) {
    const b = part(el, 'action');
    b.textContent = action.label;
    b.hidden = false;
    b.addEventListener('click', () => {
      action.onClick?.();
      dismiss(key);
    });
  }
  const entry = { el, timer: 0, left: sticky || action || kind === 'error' ? 0 : LIFE[kind] || LIFE.info, since: 0 };
  part(el, 'close').addEventListener('click', () => dismiss(key));
  const pause = () => {
    if (!entry.timer) return;
    clearTimeout(entry.timer);
    entry.timer = 0;
    entry.left -= Date.now() - entry.since;
  };
  const run = () => {
    if (!entry.left || entry.timer) return;
    entry.since = Date.now();
    entry.timer = setTimeout(() => dismiss(key), Math.max(500, entry.left));
  };
  el.addEventListener('pointerenter', pause);
  el.addEventListener('focusin', pause);
  el.addEventListener('touchstart', pause, { passive: true });
  el.addEventListener('pointerleave', run);
  el.addEventListener('focusout', run);
  byId(kind === 'error' ? 'notices-alert' : 'notices-polite').append(el);
  shown.set(key, entry);
  run();
  keep({ text, kind });
  fold();
  return { id: key, dismiss: () => dismiss(key) };
}

export function dismiss(id) {
  const e = shown.get(id);
  if (!e) return;
  clearTimeout(e.timer);
  shown.delete(id);
  e.el.remove();
  fold();
}

// fold shows the newest three and collapses the rest into "n more".
function fold() {
  const all = [...shown.values()].map((e) => e.el);
  all.forEach((el, i) => {
    el.hidden = i < all.length - MAX;
  });
  const more = byId('notices-more');
  const n = Math.max(0, all.length - MAX);
  more.hidden = n === 0;
  more.textContent = n ? `${n} more` : '';
}

function keep(item) {
  const list = getJSON('session', RECENT, []);
  list.unshift({ ...item, at: new Date().toISOString() });
  setJSON('session', RECENT, list.slice(0, 20));
}

// flash keeps a notice for the next page this tab shows (after a
// navigation or reload).
export function flash(text, kind = 'ok') {
  setJSON('session', FLASH, { text, kind });
}

// showFlash shows the notice the previous page left for this one.
export function showFlash() {
  const f = getJSON('session', FLASH);
  if (!f) return;
  remove('session', FLASH);
  notify(f.text, { kind: f.kind });
}

// openRecent lists this tab's last notices in the Recent sheet.
export function openRecent(invoker) {
  const list = byId('recent-list');
  const items = getJSON('session', RECENT, []);
  list.replaceChildren(...items.map((it) => {
    const li = cloneTpl('tpl-recent');
    li.dataset.kind = it.kind;
    part(li, 'text').textContent = it.text;
    const t = new Date(it.at);
    part(li, 'time').textContent = Number.isNaN(t.getTime()) ? '' : t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    return li;
  }));
  byId('recent-empty').hidden = items.length > 0;
  return import('./sheet.js').then(({ openSheet }) => openSheet(byId('recent'), { invoker }));
}
