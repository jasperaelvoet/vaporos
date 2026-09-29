// The heat scale: one Inferno ramp does all of REDLINE's signalling.
//
// A temperature t runs from 0 (asleep, the field's floor, h0) to 1
// (white-hot, h9, the thing you touch). The ten stops are Matplotlib's
// Inferno (perceptually uniform, monotonic in lightness, readable in
// greyscale); the cold ramp is the fault map below the scale. Both come
// from design/tokens.json through src/lib/tokens.gen.ts.
//
//   heatRGB(0.87)          → [r, g, b] on the ramp (linear between stops)
//   heatCSS(0.42)          → 'rgb(…)'
//   infernoLUT()           → 256×1 RGBA bytes, for a WebGL LUT texture
//   HEAT.streaming         → 0.87: where each state and beat sits
//   stateHeat('ready')     → the device state's temperature (tokens)
import { tokens, type StateName } from '@/lib/tokens.gen';

export type RGB = readonly [number, number, number];
export type Ramp = 'heat' | 'cold';

export function hexToRgb(hex: string): RGB {
  const h = hex.replace('#', '');
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
}

/** The ten Inferno stops, h0…h9, as hex and as RGB. */
export const INFERNO: readonly string[] = tokens.ramp.heat;
export const COLD: readonly string[] = tokens.ramp.cold;
const RGB_RAMPS: Record<Ramp, readonly RGB[]> = {
  heat: INFERNO.map(hexToRgb),
  cold: COLD.map(hexToRgb),
};

/** The colour at temperature t (0..1, clamped), interpolated between the ten stops. */
export function heatRGB(t: number, ramp: Ramp = 'heat'): [number, number, number] {
  const stops = RGB_RAMPS[ramp];
  const f = Math.max(0, Math.min(1, t)) * (stops.length - 1);
  const k = Math.min(stops.length - 2, Math.floor(f));
  const u = f - k;
  const a = stops[k];
  const b = stops[k + 1];
  return [a[0] + (b[0] - a[0]) * u, a[1] + (b[1] - a[1]) * u, a[2] + (b[2] - a[2]) * u];
}

export function heatCSS(t: number, ramp: Ramp = 'heat'): string {
  const [r, g, b] = heatRGB(t, ramp);
  return `rgb(${Math.round(r)} ${Math.round(g)} ${Math.round(b)})`;
}

/**
 * The Inferno LUT: `size` RGBA texels (default 256), linear along the ramp.
 * Upload it as a size×1 RGBA/UNSIGNED_BYTE texture with NEAREST filtering and
 * sample it at ((band + 0.5) / bands, 0.5) to posterise into isotherms.
 */
export function infernoLUT(size = 256, ramp: Ramp = 'heat'): Uint8Array {
  const out = new Uint8Array(size * 4);
  for (let i = 0; i < size; i++) {
    const [r, g, b] = heatRGB(i / (size - 1), ramp);
    out[i * 4] = Math.round(r);
    out[i * 4 + 1] = Math.round(g);
    out[i * 4 + 2] = Math.round(b);
    out[i * 4 + 3] = 255;
  }
  return out;
}

/** The colour of each of `bands` isotherm bands (the band's middle), as RGB. */
export function bandPalette(bands: number, ramp: Ramp = 'heat'): [number, number, number][] {
  return Array.from({ length: bands }, (_, b) => heatRGB((b + 0.5) / bands, ramp));
}

/**
 * Where things sit on the scale. The device states come from the tokens;
 * the rest are the website's beats (the page's scale follows them).
 */
export const HEAT = {
  asleep: tokens.states.asleep.heat,
  coldBoot: 0.18,
  installing: tokens.states.installing.heat,
  ready: tokens.states.ready.heat,
  pairing: tokens.attention.pair.heat,
  updating: tokens.states.updating.heat,
  restartNeeded: tokens.states['restart-needed'].heat,
  streaming: tokens.states.streaming.heat,
  whiteHot: 1,
} as const;

/** A device state's temperature (a fault is off the scale: 0, on the cold map). */
export function stateHeat(state: StateName): number {
  return tokens.states[state].heat;
}

/** A device state's map: 'heat', or 'cold' for a fault. */
export function stateRamp(state: StateName): Ramp {
  return tokens.states[state].map === 'cold' ? 'cold' : 'heat';
}
