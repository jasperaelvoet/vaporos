// The 2D thermal painter shared by the site's small canvases (the device
// frame, the mini TV, the evening ribbon, the footer's ember): heat per
// pixel, posterised into isotherm bands on the Inferno ramp, with ~1.5 px
// contour lines at the band edges. It is the Go welcome renderer's algorithm
// (internal/display/welcome): heat sampled on a coarse grid (4 px here, 8 px
// there) and interpolated bilinearly; bands, contour lines and colour per
// pixel. No WebGL: it paints once into a CPU-backed canvas.
//
//   paintIsotherms(canvas, (x, y) => heat 0..1, { bands: 12 })
//   paintIsotherms(canvas, around({ cx, cy, hw, hh, r, reach, peak, … }))
//
// Canvas pixels, not CSS pixels: size the canvas first (useThermalCanvas does,
// with the device pixel ratio capped at 2).
import { bandPalette, type Ramp } from './heat';

/** Heat at a canvas pixel, 0..1 (values ≥ 1 are the white-hot core). */
export type HeatField = (x: number, y: number) => number;

export interface PaintOptions {
  /** Isotherm bands (default 12, as the TV). */
  bands?: number;
  /** How dark a contour line gets, 0..1 (default 0.42). */
  line?: number;
  /** Device pixels per CSS pixel, so lines stay ~1.5 CSS px wide (default 1). */
  px?: number;
  /** Grid step for sampling the field, in canvas pixels (default 4). */
  grid?: number;
  /** 'heat' (Inferno) or 'cold' (the fault map). */
  ramp?: Ramp;
}

/** Integer hash to 0..1, the same as the Go renderer's. */
export function hash2(x: number, y: number): number {
  let h = (Math.imul(x, 374761393) + Math.imul(y, 668265263)) | 0;
  h = Math.imul(h ^ (h >>> 13), 1274126177);
  return ((h ^ (h >>> 16)) >>> 0) / 4294967295;
}

/** Smooth value noise, 0..1. */
export function valueNoise(x: number, y: number): number {
  const ix = Math.floor(x);
  const iy = Math.floor(y);
  const fx = x - ix;
  const fy = y - iy;
  const sx = fx * fx * (3 - 2 * fx);
  const sy = fy * fy * (3 - 2 * fy);
  const a = hash2(ix, iy);
  const b = hash2(ix + 1, iy);
  const c = hash2(ix, iy + 1);
  const d = hash2(ix + 1, iy + 1);
  return a + (b - a) * sx + (c - a) * sy + (a - b - c + d) * sx * sy;
}

/** Paints `field` into `canvas` as posterised isotherms. */
export function paintIsotherms(canvas: HTMLCanvasElement, field: HeatField, opts: PaintOptions = {}): void {
  const { bands: N = 12, line = 0.42, px = 1, grid: G = 4, ramp = 'heat' } = opts;
  const W = canvas.width;
  const H = canvas.height;
  if (!W || !H) return;
  // CPU-backed: one upload, and no wait behind a WebGL hero in the GPU queue.
  const ctx = canvas.getContext('2d', { willReadFrequently: true, alpha: false });
  if (!ctx) return;
  const gw = Math.ceil(W / G) + 2;
  const gh = Math.ceil(H / G) + 2;
  const grid = new Float32Array(gw * gh);
  for (let j = 0; j < gh; j++) {
    for (let i = 0; i < gw; i++) grid[j * gw + i] = Math.min(0.999, field(Math.min(W - 1, i * G), Math.min(H - 1, j * G)));
  }
  const heat = new Float32Array(W * H);
  for (let y = 0; y < H; y++) {
    const j = (y / G) | 0;
    const v = y / G - j;
    for (let x = 0; x < W; x++) {
      const i = (x / G) | 0;
      const u = x / G - i;
      const k = j * gw + i;
      heat[y * W + x] = (grid[k] * (1 - u) + grid[k + 1] * u) * (1 - v) + (grid[k + gw] * (1 - u) + grid[k + gw + 1] * u) * v;
    }
  }
  const pal = bandPalette(N, ramp);
  const img = ctx.createImageData(W, H);
  const p = img.data;
  for (let y = 0; y < H; y++) {
    for (let x = 0; x < W; x++) {
      const i = y * W + x;
      const t = heat[i] * N;
      const b = Math.min(N - 1, t | 0);
      const c = pal[b];
      const gx = (heat[i + (x < W - 1 ? 1 : 0)] - heat[i - (x > 0 ? 1 : 0)]) * N * 0.5;
      const gy = (heat[i + (y < H - 1 ? W : 0)] - heat[i - (y > 0 ? W : 0)]) * N * 0.5;
      const f = t - (t | 0);
      const m = f < 0.5 ? f : 1 - f;
      const edge = m / (Math.sqrt(gx * gx + gy * gy) + 1e-6);
      // no contour on the floor band, nor inside a clamped white-hot core
      const k = b > 0 && t < N - 0.06 ? 1 - line * Math.max(0, 1 - Math.max(0, edge - 0.45 * px) / (0.9 * px)) : 1;
      p[i * 4] = c[0] * k;
      p[i * 4 + 1] = c[1] * k;
      p[i * 4 + 2] = c[2] * k;
      p[i * 4 + 3] = 255;
    }
  }
  ctx.putImageData(img, 0, 0);
}

/** A heat source shaped like a rounded rectangle (a GPU, the TV's QR card, a point). */
export interface Source {
  /** Centre and half-size, in field units (canvas pixels ÷ `scale`). */
  cx: number;
  cy: number;
  hw: number;
  hh: number;
  /** Corner radius. */
  r: number;
  /** How far the heat reaches (exponential falloff length). */
  reach: number;
  /** Heat at the source's edge, 0..1. */
  peak: number;
  /** Heat rises: the distance above the source shrinks over this length. */
  rise: number;
  /** Air: noise frequency and how far it warps the isotherms. */
  nf: number;
  warp: number;
  /** A faint floor that fades toward the top, and the field's height for it. */
  ambient: number;
  h: number;
}

/**
 * The heat around a source, rising and warped by air: the Go renderer's
 * field. `scale` maps canvas pixels to field units (e.g. a 480 px canvas of a
 * 1920 px TV layout: 480 / 1920).
 */
export function around(o: Source, scale = 1): HeatField {
  return (x, y) => {
    const X = x / scale;
    const Y = y / scale;
    const qx = Math.abs(X - o.cx) - (o.hw - o.r);
    const qy = Math.abs(Y - o.cy) - (o.hh - o.r);
    let d = Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) + Math.min(Math.max(qx, qy), 0) - o.r;
    const up = (o.cy - Y) / o.rise;
    d *= up > 0 ? 1 / (1 + 0.55 * up) : 1 - 0.3 * up;
    const n = (valueNoise(X * o.nf, Y * o.nf) * 0.62 + valueNoise(X * o.nf * 2.03 + 17.1, Y * o.nf * 2.03 + 9.2) * 0.38) * 2 - 1;
    d = Math.max(0, d + n * o.warp * Math.min(1, d / 120 + 0.35));
    return o.peak * Math.exp(-d / o.reach) + o.ambient * (1 - Y / o.h) * (0.6 + 0.4 * n);
  };
}

/** Hermite step between a and b. */
export function smoothstep(a: number, b: number, x: number): number {
  const t = Math.max(0, Math.min(1, (x - a) / (b - a)));
  return t * t * (3 - 2 * t);
}
