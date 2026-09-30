// The REDLINE share cards (1200×630 PNG, built by `next build` from the
// og.png route handlers): the page's words on the cool side, the mark's V on
// the other as a thermal camera sees it, with spot-meter brackets and the
// heat scale. Each card sits at its page's temperature:
//
//   home      streaming heat; the hero's kicker and headline in the hot cut
//   download  the white-hot block: ash words on h9 with faint h8 rings
//   install   the installer's heat; "Install guide" in the cold cut
//   faq       ready's heat; "FAQ" in the warm cut
//
// Words come from src/content only (the hero, the page meta, the site). The
// fonts are the TV's static cuts, copied into src/fonts/ttf at prebuild.
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { ImageResponse } from 'next/og';
import { downloadPage } from '@/content/download';
import { faqPage } from '@/content/faq';
import { hero } from '@/content/home';
import { installGuide, installStrip } from '@/content/install';
import { site } from '@/content/site';
import { logo, type LogoDrawing } from '@/lib/logo.gen';
import { plain } from '@/lib/rich-text';
import { tokens } from '@/lib/tokens.gen';
import { fitSize, fontMetrics, textWidth, type FontMetrics } from './measure';
import { samplePath, thermalDataURL, transformPath, type VSource } from './thermal';

export const OG_SIZE = { width: site.ogImage.width, height: site.ogImage.height } as const;
const W = OG_SIZE.width;
const H = OG_SIZE.height;
const P = tokens.palette;
const R = tokens.ramp.heat;
const EDGE = 64;
const COLUMN = 600;
const TRACKING = -0.01;

export type CardName = 'home' | 'download' | 'install' | 'faq';
export const CARDS: readonly CardName[] = ['home', 'download', 'install', 'faq'];
type Cut = 'cold' | 'warm' | 'hot';

// ---------------------------------------------------------------- fonts
const FILES = {
  cold: 'anybody-cold.ttf',
  warm: 'anybody-warm.ttf',
  hot: 'anybody-hot.ttf',
  ui: 'monasans-regular.ttf',
  mono: 'martianmono-medium.ttf',
} as const;
type FontKey = keyof typeof FILES;
let loaded: Promise<Record<FontKey, Buffer>> | null = null;
function loadFonts() {
  const dir = join(process.cwd(), 'src/fonts/ttf');
  loaded ??= Promise.all(Object.entries(FILES).map(async ([k, f]) => [k, await readFile(join(dir, f))] as const)).then(
    (e) => Object.fromEntries(e) as Record<FontKey, Buffer>,
  );
  return loaded;
}
const CUT_FONT: Record<Cut, { fontFamily: string; fontWeight: 600 | 700 | 800 }> = {
  cold: { fontFamily: 'Anybody Cold', fontWeight: 600 },
  warm: { fontFamily: 'Anybody Warm', fontWeight: 700 },
  hot: { fontFamily: 'Anybody Hot', fontWeight: 800 },
};

// ---------------------------------------------------------------- cards
interface Card {
  ground: 'dark' | 'light';
  /** The page's name in mono above the headline, with the heat's colour. */
  eyebrow?: string;
  /** The plain line above the headline. */
  kicker?: string;
  lines: string[];
  cut: Cut;
  /** The largest headline size, and the column it fits (default COLUMN). */
  max: number;
  column?: number;
  lead?: string;
  /** A row of short facts, or numbered steps one per line. */
  facts?: string[];
  steps?: string[];
  /** The page's address after the site's. */
  path: string;
  /** The field around the V, and the heat scale's reading and colour. */
  heat: { peak: number; reach: number; label: string; at: number; color: string };
}

/** Headline lines: the content's own breaks when it has them, else text and accent. */
function headlineLines(title: { text: string; accent?: string; lines?: readonly string[] }): string[] {
  if (title.lines?.length) return [...title.lines];
  return [title.text, title.accent].filter((s): s is string => !!s);
}

function cardFor(name: CardName): Card {
  const s = tokens.states;
  switch (name) {
    case 'home':
      return {
        ground: 'dark',
        kicker: hero.kicker,
        lines: headlineLines(hero.title as { text: string; accent?: string; lines?: readonly string[] }),
        cut: 'hot',
        max: 92,
        column: 610,
        facts: hero.facts.map((f) => f.label),
        path: '',
        heat: { peak: s.streaming.peak, reach: 135, label: s.streaming.signage, at: s.streaming.heat, color: s.streaming.color },
      };
    case 'download':
      return {
        ground: 'light',
        eyebrow: downloadPage.eyebrow,
        lines: [...downloadPage.headline],
        cut: 'hot',
        max: 120,
        lead: downloadPage.lead,
        path: 'download/',
        heat: { peak: 1, reach: 150, label: 'white-hot', at: 1, color: R[6] },
      };
    case 'install':
      return {
        ground: 'dark',
        eyebrow: installGuide.eyebrow,
        lines: [...installGuide.headline],
        cut: 'cold',
        max: 116,
        steps: installStrip.steps.map((st) => st.title),
        path: 'install/',
        heat: { peak: s.installing.peak, reach: s.installing.reach, label: s.installing.signage, at: s.installing.heat, color: s.installing.color },
      };
    case 'faq':
      return {
        ground: 'dark',
        eyebrow: faqPage.eyebrow,
        // The H1's two halves, one per line: a poster, not a heading.
        lines: headlineLines(faqPage.title),
        cut: 'warm',
        max: 120,
        lead: faqPage.lead,
        path: 'faq/',
        heat: { peak: s.ready.peak, reach: s.ready.reach, label: s.ready.signage, at: s.ready.heat, color: s.ready.color },
      };
  }
}

// ---------------------------------------------------------------- pieces
function Drawing({ d, width, height, ink, os }: { d: LogoDrawing; width: number; height: number; ink: string; os?: string }) {
  return (
    <svg width={width} height={height} viewBox={d.viewBox}>
      {d.paths.map((p, i) => {
        const fill = p.className === 'os' ? os : p.fill === 'currentColor' || (!p.fill && !p.stroke) ? ink : (p.fill ?? 'none');
        const stroke = p.stroke === 'currentColor' ? ink : p.stroke;
        return <path key={i} d={p.d} fill={fill} stroke={stroke} strokeWidth={p.strokeWidth} strokeLinecap="round" strokeLinejoin="round" />;
      })}
    </svg>
  );
}

function Lockup({ light }: { light: boolean }) {
  const [, , ww, wh] = logo.wordmark.viewBox.split(' ').map(Number);
  const mark = 46;
  const h = Math.round(mark * 0.65);
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 15 }}>
      <Drawing d={light ? logo.markLight : logo.mark} width={mark} height={mark} ink={P.ash} />
      <Drawing d={logo.wordmark} width={Math.round((h * ww) / wh)} height={h} ink={light ? P.ash : P.bone} os={light ? P['hot-ink-2'] : P.smoke} />
    </div>
  );
}

/** A mono label on a plate, as the site's HUD. */
function Plate({ children, light, size = 16 }: { children: string; light: boolean; size?: number }) {
  return (
    <div
      style={{
        display: 'flex',
        padding: '5px 10px 6px',
        fontFamily: 'Martian Mono',
        fontSize: size,
        color: light ? P['hot-ink'] : P.bone,
        backgroundColor: light ? 'rgba(252,255,164,0.92)' : 'rgba(11,10,12,0.86)',
        border: `1px solid ${light ? 'rgba(74,63,79,0.35)' : P.line}`,
      }}
    >
      {children}
    </div>
  );
}

// ---------------------------------------------------------------- the card
// Text sizes the card sets (px): the kicker, the lead, the facts.
const KICKER = 28;
const LEAD = 25;
const FACTS = 14;
const STEPS = 19;
const EYEBROW = 17;
const BOTTOM = 62;
/** The V's tip, low and right of centre, and its scale over the 512 icon. */
const TIP = { x: 884, y: 452 } as const;
const V_SCALE = 1.45;

export interface Box {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

export interface Layout {
  card: Card;
  /** The headline's fitted size. */
  size: number;
  /** The V's path in card px, and the heat field around it. */
  glyph: string;
  source: VSource;
  /** Where the words are, line by line (from the fonts' advances), for the cool-side check. */
  words: Box[];
}

/** Everything a card places, before drawing it. */
export async function cardLayout(name: CardName): Promise<Layout> {
  const c = cardFor(name);
  const light = c.ground === 'light';
  const buf = await loadFonts();
  const metrics: Record<Cut, FontMetrics> = { cold: fontMetrics(buf.cold), warm: fontMetrics(buf.warm), hot: fontMetrics(buf.hot) };
  const column = c.column ?? COLUMN;
  // 2% under the exact fit: the advances leave out kerning.
  const size = Math.floor(fitSize(metrics[c.cut], c.lines, column, c.max, TRACKING) * 0.98);

  // The V: the 512 icon's glyph, scaled up and set low on the right.
  const icon = logo.icon.paths.find((p) => p.group === 'glyph' && p.className === 'core')!;
  const k = V_SCALE;
  const tip = TIP;
  const glyph = transformPath(icon.d, k, tip.x - 256 * k, tip.y - 347 * k);
  const source: VSource = {
    line: samplePath(glyph, 16),
    core: (icon.strokeWidth * k) / 2,
    peak: c.heat.peak,
    reach: c.heat.reach,
    riseY: tip.y - 130,
    rise: 220,
    nf: 1 / 150,
    warp: 64,
    ambient: light ? 0 : 0.08,
    coolX: 720,
    coolWidth: 70,
  };

  // The words' boxes, bottom-anchored like the markup below.
  const ui = fontMetrics(buf.ui);
  const mono = fontMetrics(buf.mono);
  const words: Box[] = [];
  let y = H - BOTTOM;
  const push = (w: number, h: number, gapAbove: number) => {
    words.push({ left: EDGE, right: Math.round(EDGE + w), top: Math.round(y - h), bottom: Math.round(y) });
    y -= h + gapAbove;
  };
  if (c.steps?.length) push(Math.max(...c.steps.map((st) => textWidth(mono, st, STEPS))) + STEPS * 2.6, c.steps.length * STEPS * 1.75, 28);
  if (c.facts?.length) push(c.facts.reduce((w, f) => w + textWidth(mono, f, FACTS), 0) + c.facts.length * 17 + (c.facts.length - 1) * 22, FACTS * 1.5, 30);
  if (c.lead) {
    const w = textWidth(ui, plain(c.lead), LEAD);
    push(Math.min(560, w), Math.ceil(w / 560) * LEAD * 1.4, 24);
  }
  for (const l of [...c.lines].reverse()) push(textWidth(metrics[c.cut], l, size, TRACKING), size * 0.96, 0);
  if (c.kicker) {
    y -= 16;
    push(textWidth(ui, c.kicker, KICKER), KICKER * 1.2, 0);
  }
  if (c.eyebrow) {
    y -= 22;
    push(textWidth(mono, c.eyebrow, EYEBROW) + 20, EYEBROW * 1.5, 0);
  }
  return { card: c, size, glyph, source, words };
}

export async function renderCard(name: CardName): Promise<ImageResponse> {
  const { card: c, size, glyph: d, source: v } = await cardLayout(name);
  const light = c.ground === 'light';
  const buf = await loadFonts();
  const core = v.core;
  const tip = TIP;
  const field = thermalDataURL(v, { width: W, height: H, bands: 12, mode: light ? 'rings' : 'field' });
  const ink = light ? P.ash : P.bone;
  const url = site.url.replace(/^https:\/\//, '') + c.path;

  // The heat scale: a vertical ramp on the right edge, white-hot on top.
  const scale = { x: W - 46, top: 170, h: 300 };
  const needle = scale.top + (1 - c.heat.at) * scale.h;

  return new ImageResponse(
    (
      <div style={{ width: W, height: H, display: 'flex', position: 'relative', backgroundColor: light ? R[9] : P.ash, fontFamily: 'Mona Sans', color: ink }}>
        {/* eslint-disable-next-line @next/next/no-img-element -- satori draws <img>, not next/image */}
        <img src={field} width={W} height={H} alt="" style={{ position: 'absolute', left: 0, top: 0 }} />
        <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} style={{ position: 'absolute', left: 0, top: 0 }}>
          <defs>
            <linearGradient id="ramp" x1="0" y1="1" x2="0" y2="0">
              {R.map((hex, i) => (
                <stop key={hex} offset={i / (R.length - 1)} stopColor={hex} />
              ))}
            </linearGradient>
          </defs>
          {/* the glyph over its own field: a hairline ring, then the core */}
          {light ? (
            <g>
              <path d={d} fill="none" stroke={R[6]} strokeWidth={core * 2 + 14} strokeLinecap="round" strokeLinejoin="round" />
              <path d={d} fill="none" stroke={R[9]} strokeWidth={core * 2 + 5} strokeLinecap="round" strokeLinejoin="round" />
              <path d={d} fill="none" stroke={P.ash} strokeWidth={core * 2} strokeLinecap="round" strokeLinejoin="round" />
            </g>
          ) : (
            <g>
              <path d={d} fill="none" stroke={R[0]} strokeWidth={core * 2 + 5} strokeLinecap="round" strokeLinejoin="round" />
              <path d={d} fill="none" stroke={R[9]} strokeWidth={core * 2} strokeLinecap="round" strokeLinejoin="round" />
            </g>
          )}
          {/* the spot meter: brackets in the corners, a crosshair under the tip */}
          {[
            [26, 26, 1, 1],
            [W - 26, 26, -1, 1],
            [26, H - 26, 1, -1],
            [W - 26, H - 26, -1, -1],
          ].map(([x, y, sx, sy]) => (
            <path key={`${x}-${y}`} d={`M${x} ${y + sy * 28} V${y} H${x + sx * 28}`} fill="none" stroke={light ? P['hot-ink-2'] : P.dim} strokeWidth={2} />
          ))}
          <path d={`M${tip.x - 20} ${tip.y + 60} H${tip.x + 20} M${tip.x} ${tip.y + 40} V${tip.y + 80}`} stroke={ink} strokeWidth={2} />
          <rect x={scale.x} y={scale.top} width={8} height={scale.h} fill="url(#ramp)" stroke={light ? P.ash : P.line} strokeWidth={1} />
          {[0, 0.25, 0.5, 0.75, 1].map((t) => (
            <path key={t} d={`M${scale.x - 8} ${scale.top + t * scale.h} h5`} stroke={light ? P['hot-ink-2'] : P.dim} strokeWidth={1.5} />
          ))}
          <path d={`M${scale.x - 4} ${needle} l-11 -7 v14 z`} fill={ink} />
        </svg>

        <div style={{ position: 'absolute', left: 0, top: needle - 17, width: scale.x - 22, display: 'flex', justifyContent: 'flex-end' }}>
          <Plate light={light} size={15}>
            {c.heat.label}
          </Plate>
        </div>

        <div style={{ position: 'absolute', left: EDGE, top: 54, right: EDGE, display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <Lockup light={light} />
          <Plate light={light}>{url}</Plate>
        </div>

        <div style={{ position: 'absolute', left: EDGE, bottom: BOTTOM, width: (c.column ?? COLUMN) + 40, display: 'flex', flexDirection: 'column' }}>
          {c.eyebrow ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 11, marginBottom: 22, fontFamily: 'Martian Mono', fontSize: EYEBROW, lineHeight: 1.5, color: light ? P['hot-ink'] : P.smoke }}>
              <div style={{ width: 9, height: 9, backgroundColor: light ? P.ash : c.heat.color }} />
              {c.eyebrow}
            </div>
          ) : null}
          {c.kicker ? <div style={{ fontSize: KICKER, lineHeight: 1.2, marginBottom: 16, color: ink }}>{c.kicker}</div> : null}
          <div style={{ display: 'flex', flexDirection: 'column', ...CUT_FONT[c.cut], fontSize: size, lineHeight: 0.96, letterSpacing: `${TRACKING}em`, color: ink }}>
            {c.lines.map((l) => (
              <div key={l} style={{ display: 'flex', whiteSpace: 'nowrap' }}>
                {l}
              </div>
            ))}
          </div>
          {c.lead ? <div style={{ marginTop: 24, fontSize: LEAD, lineHeight: 1.4, color: light ? P['hot-ink'] : P.smoke, maxWidth: 560, textWrap: 'balance' }}>{plain(c.lead)}</div> : null}
          {c.steps?.length ? (
            <div style={{ display: 'flex', flexDirection: 'column', marginTop: 28, fontFamily: 'Martian Mono', fontSize: STEPS, lineHeight: 1.75, color: P.bone }}>
              {c.steps.map((st, i) => (
                <div key={st} style={{ display: 'flex', whiteSpace: 'nowrap' }}>
                  <span style={{ color: tokens.palette['ink-ready'], width: STEPS * 2.6 }}>{String(i + 1).padStart(2, '0')}</span>
                  {st}
                </div>
              ))}
            </div>
          ) : null}
          {c.facts?.length ? (
            <div style={{ display: 'flex', gap: 22, marginTop: 30, fontFamily: 'Martian Mono', fontSize: FACTS, lineHeight: 1.5, color: P.smoke }}>
              {c.facts.map((f) => (
                <div key={f} style={{ display: 'flex', alignItems: 'center', gap: 9, whiteSpace: 'nowrap' }}>
                  <div style={{ width: 8, height: 8, backgroundColor: R[7] }} />
                  {f}
                </div>
              ))}
            </div>
          ) : null}
        </div>
      </div>
    ),
    {
      ...OG_SIZE,
      fonts: [
        { name: 'Anybody Cold', data: buf.cold, weight: 600, style: 'normal' },
        { name: 'Anybody Warm', data: buf.warm, weight: 700, style: 'normal' },
        { name: 'Anybody Hot', data: buf.hot, weight: 800, style: 'normal' },
        { name: 'Mona Sans', data: buf.ui, weight: 400, style: 'normal' },
        { name: 'Martian Mono', data: buf.mono, weight: 500, style: 'normal' },
      ],
    },
  );
}
