// heatmap.js: the heat fields' pixels (pure), the old SVG filter's model:
// grey sources screened, displaced by the spec's fractalNoise, posterised
// by brand's tables (design/heat-vectors.json), sampled every 4 CSS px.

// g-heat: a source's grey by radius.
const GS = [0, 0.18, 0.38, 0.58, 0.78, 1];
const GV = [255, 232, 166, 92, 38, 0].map((v) => v / 255);

function falloff(r) {
  let k = 1;
  while (r > GS[k]) k++;
  return GV[k - 1] + ((GV[k] - GV[k - 1]) * (r - GS[k - 1])) / (GS[k] - GS[k - 1]);
}

// lattice is feTurbulence's setup for a seed (the spec's code).
const lattices = {};
export function lattice(seed) {
  if (lattices[seed]) return lattices[seed];
  const M = 2147483647;
  let s = seed <= 0 ? -(seed % (M - 1)) + 1 : Math.min(seed, M - 1);
  const rnd = () => {
    s = 16807 * (s % 127773) - 2836 * Math.floor(s / 127773);
    if (s <= 0) s += M;
    return s;
  };
  const L = new Int32Array(514);
  const G = [0, 1, 2, 3].map(() => {
    const g = new Float64Array(1028);
    for (let i = 0; i < 256; i++) {
      L[i] = i;
      const x = ((rnd() % 512) - 256) / 256, y = ((rnd() % 512) - 256) / 256, m = Math.sqrt(x * x + y * y) || 1;
      g[i * 2] = x / m;
      g[i * 2 + 1] = y / m;
    }
    return g;
  });
  for (let i = 255; i > 0; i--) {
    const k = L[i], j = rnd() % 256;
    L[i] = L[j];
    L[j] = k;
  }
  for (let i = 0; i < 258; i++) {
    L[256 + i] = L[i];
    for (const g of G) g.copyWithin(512 + i * 2, i * 2, i * 2 + 2);
  }
  return (lattices[seed] = { L, G });
}

function noise(L, g, x, y) {
  const tx = x + 4096, ty = y + 4096, ix = tx | 0, iy = ty | 0, bx = ix & 255, by = iy & 255;
  const rx0 = tx - ix, ry0 = ty - iy, rx1 = rx0 - 1, ry1 = ry0 - 1, i = L[bx], j = L[(bx + 1) & 255];
  const b00 = L[i + by] * 2, b10 = L[j + by] * 2, b01 = L[i + ((by + 1) & 255)] * 2, b11 = L[j + ((by + 1) & 255)] * 2;
  const sx = rx0 * rx0 * (3 - 2 * rx0), sy = ry0 * ry0 * (3 - 2 * ry0);
  const u = rx0 * g[b00] + ry0 * g[b00 + 1], a = u + sx * (rx1 * g[b10] + ry0 * g[b10 + 1] - u);
  const w = rx0 * g[b01] + ry1 * g[b01 + 1], b = w + sx * (rx1 * g[b11] + ry1 * g[b11 + 1] - w);
  return a + sy * (b - a);
}

// turbulence is one fractalNoise channel at (x, y), 0-1 in 8-bit steps.
// air: [fx, fy, octaves, seed, scale].
export function turbulence(air, ch, x, y) {
  const { L, G } = lattice(air[3]);
  let sum = 0, X = x * air[0], Y = y * air[1];
  for (let o = 0, r = 1; o < air[2]; o++, r *= 2, X *= 2, Y *= 2) sum += noise(L, G[ch], X, Y) / r;
  return Math.round(Math.max(0, Math.min(1, (sum + 1) / 2)) * 255) / 255;
}

function rampAt(stops, t) {
  const f = Math.max(0, Math.min(1, t)) * (stops.length - 1), k = Math.min(stops.length - 2, Math.floor(f)), u = f - k;
  const c = (h, i) => parseInt(h.slice(1 + i * 2, 3 + i * 2), 16) / 255;
  return [0, 1, 2].map((i) => c(stops[k], i) + (c(stops[k + 1], i) - c(stops[k], i)) * u);
}

// table: bands isotherms of sub entries, the first of each but the coolest
// darkened by line (the contour); [r, g, b], 3 decimals, as brand's.
export function table(stops, [bands, sub, line]) {
  const out = [];
  for (let b = 0; b < bands; b++) {
    const c = rampAt(stops, (b + 0.5) / bands);
    for (let s = 0; s < sub; s++) out.push(c.map((v) => Number((v * (b > 0 && s === 0 ? line : 1)).toFixed(3))));
  }
  return out;
}

const luts = {};
export function lut(pal) {
  const key = pal.stops.join() + pal.bands.join();
  if (luts[key]) return luts[key];
  const t = table(pal.stops, pal.bands);
  const out = new Uint32Array(256);
  for (let i = 0; i < 256; i++) {
    const [r, g, b] = t[Math.min(t.length - 1, Math.floor((i * t.length) / 255))].map((v) => Math.round(v * 255));
    out[i] = (255 << 24) | (b << 16) | (g << 8) | r;
  }
  return (luts[key] = out);
}

// paint: W×H px at res per CSS px of a w×h viewBox (slice); src: per
// source [cx, cy, rx, ry, opacity, scale, dx, dy].
export function paint({ W, H, res, w, h, src, air, pal }) {
  const colour = lut(pal);
  const k = Math.max(W / res / w, H / res / h), ox = (W / res - w * k) / 2, oy = (H / res - h * k) / 2;
  const on = src.filter((q) => q[4] > 0 && q[5] > 0).map(([cx, cy, rx, ry, a, s, dx, dy]) => [cx + dx, cy + dy, 1 / (rx * s), 1 / (ry * s), a]);
  const G = Math.max(2, Math.round(4 * res)), gw = Math.ceil(W / G) + 2, gh = Math.ceil(H / G) + 2;
  const at = new Float32Array(gw * gh);
  for (let j = 0; j < gh; j++) {
    const v0 = ((j * G) / res - oy) / k;
    for (let i = 0; i < gw; i++) {
      const u0 = ((i * G) / res - ox) / k;
      const u = u0 + air[4] * (turbulence(air, 0, u0, v0) - 0.5), v = v0 + air[4] * (turbulence(air, 1, u0, v0) - 0.5);
      let p = 1;
      for (const [cx, cy, ix, iy, a] of on) {
        const x = (u - cx) * ix, y = (v - cy) * iy, r2 = x * x + y * y;
        if (r2 < 1) p *= 1 - a * falloff(Math.sqrt(r2));
      }
      at[j * gw + i] = (1 - p) * 255;
    }
  }
  const px = new Uint32Array(W * H), row = new Float32Array(gw);
  for (let y = 0, o = 0; y < H; y++) {
    const j = Math.floor(y / G), f = (y - j * G) / G;
    for (let i = 0; i < gw; i++) row[i] = at[j * gw + i] + (at[(j + 1) * gw + i] - at[j * gw + i]) * f;
    for (let i = 0, x = 0; x < W; i++) {
      let v = row[i] + 0.5;
      const d = (row[i + 1] - row[i]) / G;
      for (const end = Math.min(W, x + G); x < end; x++, v += d) px[o++] = colour[v | 0];
    }
  }
  return px;
}
