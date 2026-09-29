// The three Anybody cuts as numbers, from the tokens (design/tokens.json):
// cold (condensed), warm and hot (expanded). "Width is temperature": cold
// states and chapters are condensed, hot ones expanded; a fault is off the
// scale and takes the warm cut. CSS: cut-cold, cut-warm, cut-hot (type.css).
import { tokens } from '../tokens.gen';

export type CutName = keyof typeof tokens.font.display.faces;

export const CUTS: Record<CutName, { w: number; g: number }> = {
  cold: { w: tokens.font.display.faces.cold.wdth, g: tokens.font.display.faces.cold.wght },
  warm: { w: tokens.font.display.faces.warm.wdth, g: tokens.font.display.faces.warm.wght },
  hot: { w: tokens.font.display.faces.hot.wdth, g: tokens.font.display.faces.hot.wght },
};

/**
 * The Tailwind class that sets a cut. Spelled out, because Tailwind only
 * generates classes it finds written whole in the source.
 */
export const CUT_CLASS = { cold: 'cut-cold', warm: 'cut-warm', hot: 'cut-hot' } as const satisfies Record<CutName, string>;
export const cutClass = (cut: CutName) => CUT_CLASS[cut];
