// fmt.js: pure formatting. modeLabel and shapeAspect follow
// design/screen-shape-vectors.json, as internal/brand's ModeLabel does.

import { INSTALL_STEPS, UPDATE_PHASES, capitalize } from './copy.js';

const UNITS = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];

// Decimal units, as drive vendors print sizes.
export function bytes(n) {
  n = Number(n);
  if (!Number.isFinite(n) || n < 0) return '–';
  let i = 0;
  while (n >= 1000 && i < UNITS.length - 1) {
    n /= 1000;
    i++;
  }
  return `${n.toFixed(i > 0 && n < 10 ? 1 : 0)} ${UNITS[i]}`;
}

export function duration(seconds) {
  let s = Math.max(0, Math.floor(Number(seconds) || 0));
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (d) return h ? `${d} d ${h} h` : `${d} d`;
  if (h) return m ? `${h} h ${m} min` : `${h} h`;
  if (m) return `${m} min`;
  return `${s} s`;
}

export function elapsed(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0));
  const two = (n) => String(n).padStart(2, '0');
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h ? `${h}:${two(m)}:${two(s % 60)}` : `${two(m)}:${two(s % 60)}`;
}

export function since(iso, now = Date.now()) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '';
  const s = Math.max(0, Math.round((now - t) / 1000));
  return s < 60 ? 'just started' : duration(Math.floor(s / 60) * 60);
}

export function ago(iso, now = Date.now()) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '';
  const s = Math.round((now - t) / 1000);
  if (s < 45) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 86400 * 30) return `${Math.round(s / 86400)} d ago`;
  return new Date(t).toLocaleDateString();
}

export function clock(iso, now = new Date()) {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '';
  const time = t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return t.toDateString() === now.toDateString() ? time : `${t.toLocaleDateString([], { weekday: 'short' })} ${time}`;
}

const MODE = /^(\d{2,5})x(\d{2,5})@(\d{1,3}(?:\.\d+)?)$/;

export function parseMode(s) {
  const m = MODE.exec(String(s ?? '').trim());
  if (!m) return null;
  return { w: Number(m[1]), h: Number(m[2]), hz: Number(m[3]) };
}

export function modeLabel(s, hdr = false) {
  const m = parseMode(s);
  if (!m) return String(s ?? '');
  return `${m.w} × ${m.h} · ${m.hz} Hz${hdr ? ' · HDR' : ''}`;
}

export const ASPECT = { min: 0.4, max: 3.6, unknown: 1.7778 };

export function shapeAspect(s) {
  const m = parseMode(s);
  if (!m || !m.h) return ASPECT.unknown;
  const a = Math.min(ASPECT.max, Math.max(ASPECT.min, m.w / m.h));
  return Math.round(a * 10000) / 10000;
}

export function groupModes(modes) {
  const by = new Map();
  for (const s of modes || []) {
    const m = parseMode(s);
    if (!m) continue;
    const key = `${m.w}x${m.h}`;
    if (!by.has(key)) by.set(key, { w: m.w, h: m.h, rates: [] });
    const g = by.get(key);
    if (!g.rates.includes(m.hz)) g.rates.push(m.hz);
  }
  const out = [...by.values()];
  for (const g of out) g.rates.sort((a, b) => a - b);
  out.sort((a, b) => b.w - a.w || b.h - a.h);
  return out;
}

// T3 or T4; words the backend never sends get no special case (B10).
export function phaseLabel(phase, kind = 'update') {
  const p = String(phase || '').toLowerCase();
  if (kind === 'install') {
    if (p in INSTALL_STEPS) return INSTALL_STEPS[p];
  } else if (p in UPDATE_PHASES) {
    return UPDATE_PHASES[p].label;
  }
  return p ? capitalize(p.replace(/[_-]+/g, ' ')) : 'Working';
}

export function percent(v) {
  const n = Math.round(Number(v));
  return Number.isFinite(n) ? Math.min(100, Math.max(0, n)) : 0;
}

export function plural(n, one, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}

// Versions are fixed-width YYYYMMDD.HHMMSS: string order is version order.
export function compareVersions(a, b) {
  a = String(a || '');
  b = String(b || '');
  return a === b ? 0 : a < b ? -1 : 1;
}

export function hostLabel(system = {}, fallback = 'vapor') {
  if (system && system.mdns) return system.mdns;
  const h = (system && system.hostname) || fallback;
  return h.endsWith('.local') ? h : `${h}.local`;
}
