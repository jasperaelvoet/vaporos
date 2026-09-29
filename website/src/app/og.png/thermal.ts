// The share cards' thermal image, drawn in Node at build time (no canvas):
// a hot V (the mark's glyph) as the heat source, rising and warped by air,
// posterised into Inferno isotherm bands with hairline contours. It is the
// algorithm of the site's 2D painter (src/direction/redline/painter.ts) and
// the Go welcome renderer: heat on a coarse grid, interpolated per pixel,
// bands and contour lines per pixel. Returns a PNG as a data: URL, for an
// <img> in the ImageResponse.
import { crc32, deflateSync } from 'node:zlib';
import { bandPalette, hexToRgb, INFERNO } from '@/direction/redline/heat';
import { smoothstep, valueNoise } from '@/direction/redline/painter';

export interface VSource {
  /** The glyph's centre line as a polyline, in px (see samplePath). */
  line: [number, number][];
  /** The white-hot core's radius around the line. */
  core: number;
  /** Heat just outside the core, 0..1, and its exponential falloff length. */
  peak: number;
  reach: number;
  /** Heat rises above `riseY`: the distance shrinks over `rise` px. */
  riseY: number;
  rise: number;
  /** Air: noise frequency and warp length. */
  nf: number;
  warp: number;
  /** A faint floor fading toward the top. */
  ambient: number;
  /** The cool side: heat drops left of this x (the text column), over `coolWidth`. */
  coolX: number;
  coolWidth: number;
}

export interface ThermalOptions {
  width: number;
  height: number;
  bands?: number;
  /** 'field': Inferno bands on the page's ash. 'rings': white-hot ground with faint h8 contour rings only. */
  mode?: 'field' | 'rings';
  /** Grid step for sampling the field (default 4). */
  grid?: number;
}

function distToSegment(px: number, py: number, ax: number, ay: number, bx: number, by: number): number {
  const vx = bx - ax;
  const vy = by - ay;
  const t = Math.max(0, Math.min(1, ((px - ax) * vx + (py - ay) * vy) / (vx * vx + vy * vy || 1)));
  return Math.hypot(px - (ax + vx * t), py - (ay + vy * t));
}

/** Absolute M/L/C path data (implicit repeats allowed) mapped by x·k+tx, y·k+ty. */
export function transformPath(d: string, k: number, tx: number, ty: number): string {
  let i = 0;
  return d.replace(/-?\d*\.?\d+(?:e-?\d+)?/gi, (num) => {
    const v = Number(num) * k + (i++ % 2 === 0 ? tx : ty);
    return String(Math.round(v * 100) / 100);
  });
}

/** Samples absolute M/L/C path data into a polyline (`steps` points per curve). */
export function samplePath(d: string, steps = 12): [number, number][] {
  const tokens = d.match(/[MLC]|-?\d*\.?\d+(?:e-?\d+)?/gi) ?? [];
  const pts: [number, number][] = [];
  let cmd = 'M';
  let x = 0;
  let y = 0;
  for (let i = 0; i < tokens.length; ) {
    if (/[MLC]/i.test(tokens[i])) cmd = tokens[i++].toUpperCase();
    const n = (j: number) => Number(tokens[i + j]);
    if (cmd === 'C') {
      const [x1, y1, x2, y2, x3, y3] = [n(0), n(1), n(2), n(3), n(4), n(5)];
      for (let s = 1; s <= steps; s++) {
        const t = s / steps;
        const u = 1 - t;
        pts.push([u * u * u * x + 3 * u * u * t * x1 + 3 * u * t * t * x2 + t * t * t * x3, u * u * u * y + 3 * u * u * t * y1 + 3 * u * t * t * y2 + t * t * t * y3]);
      }
      [x, y] = [x3, y3];
      i += 6;
    } else {
      [x, y] = [n(0), n(1)];
      pts.push([x, y]);
      i += 2;
      if (cmd === 'M') cmd = 'L';
    }
  }
  return pts;
}

/** Heat 0..1 at a pixel; ≥ 1 inside the white-hot core. */
export function vField(s: VSource, height: number): (x: number, y: number) => number {
  const L = s.line;
  return (x, y) => {
    let raw = Infinity;
    for (let i = 1; i < L.length; i++) raw = Math.min(raw, distToSegment(x, y, L[i - 1][0], L[i - 1][1], L[i][0], L[i][1]));
    if (raw <= s.core) return 1;
    let d = raw - s.core;
    const up = (s.riseY - y) / s.rise;
    d *= up > 0 ? 1 / (1 + 0.55 * up) : 1 - 0.3 * up;
    const n = (valueNoise(x * s.nf, y * s.nf) * 0.62 + valueNoise(x * s.nf * 2.03 + 17.1, y * s.nf * 2.03 + 9.2) * 0.38) * 2 - 1;
    d = Math.max(0, d + n * s.warp * Math.min(1, d / 120 + 0.35));
    if (x < s.coolX) d *= 1 + ((s.coolX - x) / s.coolWidth) ** 1.6;
    return s.peak * Math.exp(-d / s.reach) + s.ambient * (1 - y / height) * (0.6 + 0.4 * n) * smoothstep(s.coolX - s.coolWidth, s.coolX, x);
  };
}

/** Paints `field` into RGBA pixels as posterised isotherms. */
export function isotherms(field: (x: number, y: number) => number, o: ThermalOptions): Uint8Array {
  const { width: W, height: H, bands: N = 12, mode = 'field', grid: G = 4 } = o;
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
  const pal = bandPalette(N);
  const whiteHot = hexToRgb(INFERNO[9]);
  const ring = hexToRgb(INFERNO[8]);
  const px = new Uint8Array(W * H * 4);
  for (let y = 0; y < H; y++) {
    for (let x = 0; x < W; x++) {
      const i = y * W + x;
      const t = heat[i] * N;
      const b = Math.min(N - 1, t | 0);
      const gx = (heat[i + (x < W - 1 ? 1 : 0)] - heat[i - (x > 0 ? 1 : 0)]) * N * 0.5;
      const gy = (heat[i + (y < H - 1 ? W : 0)] - heat[i - (y > 0 ? W : 0)]) * N * 0.5;
      const f = t - (t | 0);
      const m = f < 0.5 ? f : 1 - f;
      const edge = m / (Math.sqrt(gx * gx + gy * gy) + 1e-6);
      // ~1.5 px contour at each band edge; none on the floor band or in the core
      const line = b > 0 && t < N - 0.06 ? Math.max(0, 1 - Math.max(0, edge - 0.45) / 0.9) : 0;
      let r: number, g: number, bl: number;
      if (mode === 'rings') {
        const a = line * 0.55;
        r = whiteHot[0] + (ring[0] - whiteHot[0]) * a;
        g = whiteHot[1] + (ring[1] - whiteHot[1]) * a;
        bl = whiteHot[2] + (ring[2] - whiteHot[2]) * a;
      } else {
        const c = pal[b];
        const k = 1 - 0.42 * line;
        r = c[0] * k;
        g = c[1] * k;
        bl = c[2] * k;
      }
      px[i * 4] = r;
      px[i * 4 + 1] = g;
      px[i * 4 + 2] = bl;
      px[i * 4 + 3] = 255;
    }
  }
  return px;
}

/** RGBA pixels → PNG bytes (8-bit truecolour with alpha, no filtering). */
export function encodePNG(rgba: Uint8Array, width: number, height: number): Buffer {
  const chunk = (type: string, data: Buffer) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type, 'ascii'), data]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(td));
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  const stride = width * 4;
  const raw = Buffer.alloc((stride + 1) * height);
  for (let y = 0; y < height; y++) {
    raw[y * (stride + 1)] = 0;
    raw.set(rgba.subarray(y * stride, (y + 1) * stride), y * (stride + 1) + 1);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

export function thermalDataURL(source: VSource, o: ThermalOptions): string {
  const png = encodePNG(isotherms(vField(source, o.height), o), o.width, o.height);
  return `data:image/png;base64,${png.toString('base64')}`;
}
