// ui/thermal.js: the heat fields (span.heat-field[data-field]): a CSS
// poster, then canvases a Worker paints once per level, faded in by opacity
// alone, so no frame repaints the art and nothing runs at rest.

import { reducedMotion } from '../core/dom.js';

// Each field's viewBox (slice) and sources [key, cx, cy, rx, ry], core last.
const FIELDS = {
  home: [400, 360, [['f', 200, 420, 330, 150], ['p', 232, 64, 126, 178], ['a', 96, 110, 104, 104], ['c', 214, 150, 168, 168]]],
  pinpad: [400, 860, [['p', 200, 250, 440, 250], ['c', 200, 352, 330, 190]]],
  scene: [400, 860, [['f', 200, 980, 420, 240], ['p', 200, 360, 200, 300], ['c', 200, 520, 260, 260]]],
  devices: [400, 300, [['p', 390, 150, 300, 250], ['c', 380, 215, 215, 175]]],
  power: [400, 150, [['p', 270, 30, 170, 120], ['c', 250, 110, 150, 150]]],
  screen: [320, 200, [['p', 186, 34, 150, 110], ['c', 170, 120, 150, 150]]],
  entry: [400, 400, [['f', 210, 430, 330, 120], ['p', 288, 150, 200, 150], ['c', 276, 212, 128, 104]]],
};

// The levels: per source [opacity, scale], changes from the field's base.
const BASE = { c: [0.43, 1], p: [0.2, 1], a: [0.14, 1], f: [0.06, 1] };
const DOWN = { c: [0.2, 0.72], p: [0.04], f: [0] };
const COLD = { map: 'cold' };
const LEVELS = {
  home: {
    '': { c: [0.3, 0.9], p: [0.1], a: [0.02], f: [0.02] },
    asleep: { c: [0.2, 0.8], p: [0], a: [0], f: [0.02] },
    updating: { c: [0.57, 1.04], p: [0.3] },
    'restart-needed': { c: [0.66, 1.06], p: [0.3] },
    pair: { c: [0.6, 1.03], p: [0.28] },
    streaming: { c: [0.98, 1.2], p: [0.62, 1.15], a: [0.42], f: [0.26] },
    fault: { c: [0.7], map: 'cold' },
    idle: { c: [0.24, 0.86], p: [0.06], a: [0.06] },
  },
  pinpad: { base: { c: [0.3, 0.94], p: [0.12] }, 1: { c: [0.45] }, 2: { c: [0.6, 1], p: [0.2] }, 3: { c: [0.76, 1.08], p: [0.3] }, 4: { c: [0.98, 1.22], p: [0.5] } },
  scene: { down: DOWN, restarting: DOWN, updating: DOWN, timeout: COLD, unreachable: COLD, back: { c: [0.62, 1.06], p: [0.28] }, asleep: { c: [0.2, 0.8], p: [0], f: [0] } },
  devices: { base: { c: [0.08, 0.6], p: [0] }, waiting: { c: [0.6, 1], p: [0.2] } },
  screen: {
    base: { c: [0.1, 0.72], p: [0] },
    '': { c: [0.3, 0.84], p: [0.08] },
    ready: { c: [0.46, 0.96], p: [0.16] },
    streaming: { c: [0.72, 1.12], p: [0.32] },
    fault: { c: [0.42, 0.9], map: 'cold' },
  },
};
const CONT = {
  power: (h) => ({ c: [0.1 + 0.88 * h, 0.6 + 0.62 * h], p: [0.6 * h, 0.7 + 0.5 * h] }),
  entry: (h, g) => ({ c: [0.18 + 0.8 * h, (0.8 + 0.46 * h) * g], p: [0.62 * h, (0.86 + 0.34 * h) * g], f: [Math.max(0, h - 0.45) * 0.6] }),
};

const off = (a, b) => Math.abs(a - b) >= 2;

let T = null;
function tokens() {
  if (T) return T;
  const cs = getComputedStyle(document.documentElement);
  const v = (n) => cs.getPropertyValue(n).trim();
  const nums = (n) => v(n).split(/\s+/).map(Number);
  const pal = (p) => ({ stops: [...Array(10).keys()].map((i) => v(`--color-${p}${i}`)), bands: nums('--vos-heat-bands') });
  // The heat curve, evaluated by a paused animation that targets nothing.
  const fx = new Animation(new KeyframeEffect(null, null, { duration: 1000, easing: v('--ease-out'), fill: 'both' }));
  const ease = (x) => {
    fx.currentTime = x * 1000;
    return fx.effect.getComputedTiming().progress;
  };
  return (T = { air: nums('--vos-heat-air'), pal: { heat: pal('h'), cold: pal('c') }, ms: (n) => parseFloat(v(n)) || 0, ease });
}

// The painter: a module Worker, else here.
let worker = null;
let broken = false;
let seq = 0;
const jobs = new Map();

async function here(msg) {
  const { paint } = await import('../heatmap.js');
  return createImageBitmap(new ImageData(new Uint8ClampedArray(paint(msg).buffer), msg.W, msg.H));
}

function broke(e) {
  e?.preventDefault?.();
  if (!broken) console.warn('heat fields: no module Worker, painting on the main thread');
  broken = true;
  worker?.terminate();
  for (const [id, j] of jobs) {
    jobs.delete(id);
    here(j.msg).then(j.ok, j.no);
  }
}

function work(msg) {
  if (!worker && !broken) {
    try {
      worker = new Worker(new URL('./thermal-worker.js', import.meta.url), { type: 'module' });
      worker.onmessage = ({ data }) => {
        const j = jobs.get(data.id);
        jobs.delete(data.id);
        if (data.bmp) j?.ok(data.bmp);
        else j?.no();
      };
      worker.onerror = broke;
    } catch {
      broke();
    }
  }
  if (broken) return here(msg);
  return new Promise((ok, no) => {
    jobs.set(++seq, { ok, no, msg });
    worker.postMessage({ ...msg, id: seq });
  });
}

const hosts = new Map();
let observer = null;

function hold(host) {
  let s = hosts.get(host);
  if (s) return s;
  s = { host, field: host.dataset.field, opts: {}, run: 0 };
  hosts.set(host, s);
  // A field that appears paints at once; a new size, once still for 200 ms.
  observer ||= new ResizeObserver((list) => list.forEach(({ target: el, contentRect: r }) => {
    const f = hosts.get(el);
    const was = f.w && f.h;
    f.w = r.width;
    f.h = r.height;
    clearTimeout(f.tm);
    if (f.w && f.h && !was) update(f, 'appear');
    else if (f.w && f.h) f.tm = setTimeout(() => update(f, 'size'), 200);
  }));
  observer.observe(host);
  return s;
}

// target: per source [opacity, scale, dx, dy] (--heat-* from CSS), the map.
function target(s) {
  const tab = LEVELS[s.field] || {};
  const cs = getComputedStyle(s.host);
  const num = (n, d) => {
    const x = parseFloat(cs.getPropertyValue(n));
    return Number.isNaN(x) ? d : x;
  };
  const L = { ...BASE };
  const put = (o = {}) => {
    for (const k in o) if (L[k]) L[k] = [o[k][0], o[k][1] ?? L[k][1]];
  };
  put(tab.base);
  const lvl = CONT[s.field] ? CONT[s.field](Number(s.level) || 0, num('--heat-grow', 1)) : tab[s.level];
  put(lvl);
  if (s.opts.idle) put(tab.idle);
  const d = [num('--heat-dx', 0), num('--heat-dy', 0)];
  return { map: s.opts.map || lvl?.map || 'heat', lv: FIELDS[s.field][2].map(([k]) => [...L[k], ...('cp'.includes(k) ? d : [0, 0])]) };
}

// At most 2 device px per CSS px, 2 Mpx in all: 13 ms in the Worker.
function dims(w, h) {
  const res = Math.min(devicePixelRatio || 1, 2, Math.sqrt(2e6 / (w * h)));
  return { res, W: Math.max(1, Math.round(w * res)), H: Math.max(1, Math.round(h * res)) };
}

function job(s, lv, map, d) {
  const [w, h, src] = FIELDS[s.field];
  const K = tokens();
  return work({ ...d, w, h, src: src.map(([, ...q], i) => [...q, ...lv[i]]), air: K.air, pal: K.pal[map] });
}

const mix = (a, b, k) => a.map((q, i) => q.map((v, j) => v + (b[i][j] - v) * k));
const core = (lv) => lv[lv.length - 1][0];

function update(s, why) {
  if (s.level == null || !s.w || !s.h) return;
  const t = target(s);
  const sig = JSON.stringify(t);
  if (sig === s.sig && !off(s.w, s.pw) && !off(s.h, s.ph)) return;
  s.sig = sig;
  play(s, t, sig === s.shown?.sig ? 'size' : why);
}

// play: a first field fades in (unless painted ahead), a returning one or
// reduced motion swaps, a new size fades fast, a new level plays 2-8 keys
// on the heat curve, the Worker painting key k+1 while k fades in.
async function play(s, t, why) {
  const run = ++s.run;
  const { host, sig } = s;
  s.busy = true;
  delete host.dataset.settled;
  s.anim?.finish();
  const K = tokens();
  const d = dims(s.w, s.h);
  [s.pw, s.ph] = [s.w, s.h];
  const from = s.shown;
  const early = s.ahead?.sig === sig && s.ahead;
  if (early) s.ahead = null;
  if (early && (off(early.w, s.w) || off(early.h, s.h))) [s.pw, s.ph] = [early.w, early.h]; // repainted after
  let keys = [t.lv];
  let ms = 0;
  if (reducedMotion() || (why === 'appear' && from)) ms = 0;
  else if (!from) ms = early ? 0 : K.ms('--vos-dur-heat');
  else if (why === 'size') ms = K.ms('--vos-dur-fast');
  else {
    const heat = from.map === t.map && core(t.lv) > core(from.lv);
    ms = K.ms({ pinpad: '--vos-dur-step', scene: `--vos-scene-${s.level === 'back' ? 'heat' : 'cool'}-ms` }[s.field] || `--vos-dur-${heat ? 'heat' : 'cool'}`);
    const n = Math.max(2, Math.min(8, Math.round(ms / 150)));
    keys = [...Array(n).keys()].map((i) => (i + 1 < n ? mix(from.lv, t.lv, K.ease((i + 1) / n)) : t.lv));
  }
  const pick = (k) => (k + 1 === keys.length && early ? early.bmp : job(s, keys[k], t.map, d));
  let next = pick(0);
  try {
    for (let k = 0; k < keys.length; k++) {
      const bmp = await next;
      next = null;
      if (run !== s.run) return bmp.close();
      if (k + 1 < keys.length) next = pick(k + 1);
      await show(s, bmp, ms / keys.length, { lv: keys[k], map: t.map, sig: k + 1 === keys.length ? sig : '' });
      if (run !== s.run) return;
    }
  } catch {
    if (run === s.run) s.sig = '';
    return settle(s, run);
  } finally {
    next?.then((b) => b.close(), () => {});
  }
  settle(s, run);
  update(s, 'size');
}

function settle(s, run) {
  if (run !== s.run) return;
  s.busy = false;
  s.host.dataset.settled = '';
}

async function show(s, bmp, ms, shown) {
  const cv = document.createElement('canvas');
  cv.width = bmp.width;
  cv.height = bmp.height;
  cv.getContext('bitmaprenderer').transferFromImageBitmap(bmp);
  s.host.append(cv);
  s.shown = shown;
  if (ms > 0) {
    const a = (s.anim = cv.animate([{ opacity: 0 }, { opacity: 1 }], { duration: ms, easing: 'linear' }));
    await a.finished.catch(() => {});
    if (s.anim === a) s.anim = null;
  }
  for (let p = cv.previousElementSibling; p; p = cv.previousElementSibling) {
    p.getContext('bitmaprenderer').transferFromImageBitmap(null);
    p.remove();
  }
}

// heat sets a field's level: a key of its table, 0-1 (power, entry), or
// null for nothing to paint. opts: map 'cold', idle (Home).
export function heat(host, level, opts = {}) {
  if (!host) return;
  const s = hold(host);
  if (s.sig && s.level === level && JSON.stringify(s.opts) === JSON.stringify(opts)) return;
  s.level = level;
  s.opts = opts;
  if (level == null && !s.busy) host.dataset.settled = '';
  update(s, 'set');
}

// prime paints one level ahead (a closed full-screen field at the
// viewport's size), so showing it waits for nothing.
export function prime(host, level, opts = {}) {
  const s = hold(host);
  const [w, h] = [s.w || innerWidth, s.h || innerHeight];
  const t = target({ ...s, level, opts });
  const sig = JSON.stringify(t);
  if (sig === s.sig || s.ahead?.sig === sig) return;
  s.ahead?.bmp.then((b) => b.close(), () => {});
  s.ahead = { sig, w, h, bmp: job(s, t.lv, t.map, dims(w, h)) };
  s.ahead.bmp.catch(() => {});
}
