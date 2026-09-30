// The hero's poster: the same PC as the WebGL view, drawn once as flat
// isotherms in SVG. It is the whole picture for phones, save-data, small-
// memory devices, reduced motion and browsers without WebGL2, and the first
// frame for everyone (the live view fades in over it). Server-rendered and
// aria-hidden; an inline SVG is never a Largest Contentful Paint candidate,
// so the headline stays the LCP.
//
// Geometry: the case's box in case units (geometry.ts), 500 × 1000 SVG units,
// mirrored in x like the shader. Each part is a stack of rounded rectangles,
// one per isotherm band (outer and cooler first), filled with the band's
// Inferno colour and outlined with the band's contour line; a turbulence
// filter warps the edges, as air warps a thermal image. The bands follow the
// display pass (shaders.ts): heat h lands in band floor(14 · (1 − e^(−1.5h))).
import { bandPalette } from '../heat';

const BANDS = 14;
const PAL = bandPalette(BANDS).map(([r, g, b]) => ({
  fill: `rgb(${Math.round(r)} ${Math.round(g)} ${Math.round(b)})`,
  line: `rgb(${Math.round(r * 0.58)} ${Math.round(g * 0.58)} ${Math.round(b * 0.58)})`,
}));

/** A part: a box in case units (centre, half size, radius) and its bands from the outside in. */
interface Part {
  c: [number, number];
  h: [number, number];
  r: number;
  /** [how far the band reaches beyond the box (case units), band]. */
  bands: [number, number][];
}

// At rest (boot 1, load 0.34): GPU ≈ 0.64 in the middle (band 8), CPU ≈ 0.5
// (band 7), case walls ≈ 0.17 (band 3), board 0.04-0.11 (bands 0-2).
const PARTS: Part[] = [
  // The board, warmer toward the CPU.
  { c: [0.305, 0.63], h: [0.162, 0.318], r: 0.01, bands: [[0, 1]] },
  { c: [0.3, 0.74], h: [0.11, 0.12], r: 0.1, bands: [[0.02, 2]] },
  // PSU and a drive at the bottom.
  { c: [0.17, 0.095], h: [0.12, 0.064], r: 0.01, bands: [[0.02, 2], [0, 3], [-0.022, 4]] },
  { c: [0.395, 0.09], h: [0.058, 0.04], r: 0.006, bands: [[0.008, 1], [-0.004, 2]] },
  // CPU tower cooler: hot at the base, cooler up the fins; its top plate.
  { c: [0.315, 0.785], h: [0.058, 0.082], r: 0.012, bands: [[0.07, 2], [0.045, 3], [0.022, 4], [0.004, 6], [-0.014, 8], [-0.032, 9]] },
  { c: [0.315, 0.905], h: [0.075, 0.011], r: 0.004, bands: [[0.008, 3], [0, 4]] },
  // DIMMs.
  ...[0, 1, 2, 3].map((i): Part => ({ c: [0.405 + i * 0.0135, 0.79], h: [0.004, 0.108], r: 0.002, bands: [[0, 4]] })),
  // GPU: a halo, the card, hottest along its middle; the bracket.
  {
    c: [0.255, 0.515],
    h: [0.205, 0.052],
    r: 0.014,
    bands: [[0.075, 2], [0.05, 3], [0.028, 4], [0.01, 6], [-0.008, 8], [-0.022, 9], [-0.034, 10]],
  },
  { c: [0.47, 0.515], h: [0.008, 0.046], r: 0.003, bands: [[0.006, 6], [0, 8]] },
];

/** GPU fans read as cooler discs in the card; the rear exhaust fan as a ring. */
const FANS: { c: [number, number]; r: number; band: number; ring?: number }[] = [
  ...[0, 1, 2].map((i) => ({ c: [0.132 + i * 0.121, 0.515] as [number, number], r: 0.03, band: 7 })),
  { c: [0.468, 0.83], r: 0.034, band: 4, ring: 0.012 },
];

// Exhaust drifting from the rear fan and the GPU bracket toward the words,
// and rising off the top of the case: broad, soft clouds in the coolest
// bands, warped harder than the parts. SVG units; negative x is left of the
// case, negative y above it.
const PLUMES: { d: string; band: number }[] = [
  { d: 'M60 260C-40 300-190 330-330 250S-560 60-640 -120-720 -330-620 -300-460 -120-330 -60-120 -20 60 60 120 180 60 260Z', band: 1 },
  { d: 'M40 560C-60 600-220 620-360 560S-560 420-520 380-360 470-220 480 20 440 40 560Z', band: 1 },
  { d: 'M60 190C-20 220-130 230-230 180S-380 60-400 -40-300 -20-230 60-60 110 60 190Z', band: 2 },
  { d: 'M30 520C-40 540-140 545-220 510S-300 450-260 440-150 470 30 480Z', band: 2 },
  { d: 'M120 40C80 -60 120 -170 60 -290S-40 -470 40 -560 190 -420 230 -300 280 -80 250 40Z', band: 1 },
  { d: 'M160 30C140 -40 170 -110 140 -190S120 -300 180 -280 230 -140 220 30Z', band: 2 },
];

const box = (p: Part, grow: number) => {
  const hx = p.h[0] + grow;
  const hy = p.h[1] + grow;
  const r = Math.max(0, p.r + grow);
  // Mirrored in x, y down: case units → SVG units.
  return {
    x: +(1000 * (0.5 - p.c[0] - hx)).toFixed(1),
    y: +(1000 * (1 - p.c[1] - hy)).toFixed(1),
    width: +(2000 * hx).toFixed(1),
    height: +(2000 * hy).toFixed(1),
    rx: +(1000 * r).toFixed(1),
  };
};
const pt = (q: [number, number]) => ({ cx: +(1000 * (0.5 - q[0])).toFixed(1), cy: +(1000 * (1 - q[1])).toFixed(1) });

export function HeroPoster({ id = 'hero-poster' }: { id?: string }) {
  const warp = `${id}-warp`;
  const air = `${id}-air`;
  return (
    <svg className="hero-poster" viewBox="0 0 500 1000" preserveAspectRatio="xMidYMid meet" aria-hidden="true" focusable="false">
      <defs>
        <filter id={air} x="-200%" y="-80%" width="400%" height="260%" colorInterpolationFilters="sRGB">
          <feTurbulence type="fractalNoise" baseFrequency="0.006 0.009" numOctaves={3} seed={3} result="n" />
          <feDisplacementMap in="SourceGraphic" in2="n" scale={90} xChannelSelector="R" yChannelSelector="G" />
        </filter>
        <filter id={warp} x="-10%" y="-10%" width="120%" height="120%" colorInterpolationFilters="sRGB">
          <feTurbulence type="fractalNoise" baseFrequency="0.024 0.03" numOctaves={2} seed={7} result="n" />
          <feDisplacementMap in="SourceGraphic" in2="n" scale={9} xChannelSelector="R" yChannelSelector="G" />
        </filter>
      </defs>
      <g filter={`url(#${air})`} strokeWidth={1.2}>
        {PLUMES.map((p, i) => (
          <path key={i} d={p.d} fill={PAL[p.band].fill} stroke={PAL[p.band].line} vectorEffect="non-scaling-stroke" />
        ))}
      </g>
      <g filter={`url(#${warp})`} strokeWidth={1.2}>
        {/* The case: steel walls a few steps above the room, and the glass inside them. */}
        <rect x={0} y={0} width={500} height={1000} rx={18} fill={PAL[1].fill} stroke={PAL[3].fill} strokeWidth={11} />
        <rect x={15} y={15} width={470} height={970} rx={12} fill="none" stroke={PAL[2].fill} strokeWidth={3} />
        {PARTS.flatMap((p, i) =>
          p.bands.map(([grow, band], j) => (
            <rect key={`${i}-${j}`} {...box(p, grow)} fill={PAL[band].fill} stroke={PAL[band].line} vectorEffect="non-scaling-stroke" />
          )),
        )}
        {FANS.map((f, i) =>
          f.ring ? (
            <circle key={i} {...pt(f.c)} r={1000 * f.r} fill="none" stroke={PAL[f.band + 1].fill} strokeWidth={1000 * f.ring} />
          ) : (
            <circle key={i} {...pt(f.c)} r={1000 * f.r} fill={PAL[f.band].fill} stroke={PAL[f.band].line} vectorEffect="non-scaling-stroke" />
          ),
        )}
      </g>
    </svg>
  );
}
