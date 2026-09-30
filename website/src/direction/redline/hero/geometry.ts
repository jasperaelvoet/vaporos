// Where the hero's PC stands, shared by the WebGL view (thermal-canvas.tsx)
// and the CSS poster (poster.tsx, hero.css): both draw the same mid-tower in
// the same place, so the live view fades in over the poster without a jump.
//
// The PC is a box half as wide as it is tall, in "case units" q: x 0..0.5,
// y 0..1 from the bottom, seen from the other side (the shader mirrors x),
// so its rear exhaust faces the words.
//
//   desktop (≥ 901 px): 96% of the hero's height, its left edge at 65.5% of
//                       the width, its bottom 8% below the hero's
//   phones:             half the hero's height, centred, its bottom at 47%
//
// hero.css repeats these numbers for the poster (.hero-rig); keep them equal.

export const NARROW_MAX = 900;

export interface CaseBox {
  /** CSS pixels: the case's height, its left edge, and its bottom above the hero's bottom. */
  ch: number;
  x0: number;
  y0: number;
  narrow: boolean;
}

export function caseBox(cssW: number, cssH: number): CaseBox {
  const narrow = cssW <= NARROW_MAX;
  const ch = narrow ? cssH * 0.5 : cssH * 0.96;
  const x0 = narrow ? cssW * 0.5 - ch * 0.25 : cssW * 0.655;
  const y0 = narrow ? cssH * 0.47 : -cssH * 0.08;
  return { ch, x0, y0, narrow };
}

/** A point in case units → the canvas's uv (0..1, y up). Case x is mirrored on screen. */
export function caseToUv(box: CaseBox, cssW: number, cssH: number, qx: number, qy: number): [number, number] {
  return [(box.x0 + (0.5 - qx) * box.ch) / cssW, (box.y0 + qy * box.ch) / cssH];
}

/**
 * The exhausts the fluid is fed from, in case units, with their spread (as a
 * fraction of the screen, squared) and the way they blow (velocity, in sim
 * texels per second): the rear fan and the GPU bracket push toward the
 * words, the top of the case breathes up.
 */
export const EXHAUSTS: { q: [number, number]; spread: number; dir: [number, number] }[] = [
  { q: [0.5, 0.83], spread: 0.0022, dir: [-150, 60] },
  { q: [0.5, 0.515], spread: 0.0018, dir: [-180, 34] },
  { q: [0.3, 1.0], spread: 0.005, dir: [0, 70] },
  { q: [0.12, 1.0], spread: 0.0035, dir: [0, 50] },
];

/** The GPU's centre in case units: where the spot meter sits (hero.css .hero-spot). */
export const GPU_Q: [number, number] = [0.253, 0.515];

/** The load (0..1) at rest, and where the scroll takes it over the first 62% of the hero. */
export const LOAD_REST = 0.34;
export const LOAD_SPAN = 0.62;
/** The scroll progress at which the spot meter says a game started. */
export const HOT_AT = 0.3;
