// Fitting a stretching headline at its hottest width, so nothing reflows.
//
// A headline that stretches (with scroll, or with friction heat) is sized for
// the widest it can ever get. We measure the widest line once, at 100 px and
// at the hottest cut it can reach, and store K = width / 100 px (the line's
// width in em) as --fit-k. type.css then sets the size to min(the preset's
// size, 100cqi / K) inside the headline's own inline-size container:
// resizing needs no script, and stretching never overflows because the fit
// already assumed the hottest width.
//
// <Headline> (src/components/ui/headline.tsx) does all of this; these are
// the parts, for anyone building a headline by hand.
import type { CutName } from './cuts';
import { CUTS } from './cuts';

/** The widest a headline gets: its font-stretch (percent) and weight. */
export interface Hottest {
  w: number;
  g: number;
}

/** Anybody's variable file covers wdth 56-136. */
export const WDTH_MIN = 56;
export const WDTH_MAX = 136;

/**
 * The hottest width a headline reaches: its cut plus the larger of the most
 * a scroll scene stretches it (`stretch`, percent points) and full friction
 * (`friction`, percent points): type.css caps their sum there
 * (--stretch-max). `stretchG` is the weight the scroll scene adds.
 */
export function hottestOf(cut: CutName, stretch = 0, stretchG = 0, friction = 0): Hottest {
  const c = CUTS[cut];
  return { w: Math.min(WDTH_MAX, c.w + Math.max(stretch, friction)), g: Math.min(860, c.g + stretchG) };
}

/**
 * K for one line set: the widest `.ln` inside `set`, in em, set at `hot`.
 * The set is shown for the measurement if CSS hides it, then restored.
 */
export function measureK(set: HTMLElement, hot: Hottest): number {
  return measure(set, hot, (range, ln) => {
    range.selectNodeContents(ln);
    return range.getBoundingClientRect().width;
  });
}

/**
 * K for the longest word in `set` (in em, at `hot`): what a headline with no
 * narrow line set is fitted to on phones, where its lines run together and
 * wrap, so no single word is ever wider than the column.
 */
export function measureWordK(set: HTMLElement, hot: Hottest): number {
  return measure(set, hot, (range, ln) => {
    let widest = 0;
    const walker = document.createTreeWalker(ln, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      for (const m of (node.textContent ?? '').matchAll(/\S+/g)) {
        range.setStart(node, m.index);
        range.setEnd(node, m.index + m[0].length);
        widest = Math.max(widest, range.getBoundingClientRect().width);
      }
    }
    return widest;
  });
}

/** Sets `set` at 100 px and the hottest cut, and returns the widest `.ln` as `width` measures it, in em plus 1%. */
function measure(set: HTMLElement, hot: Hottest, width: (range: Range, ln: HTMLElement) => number): number {
  const before = set.getAttribute('style');
  set.style.setProperty('display', 'block', 'important');
  set.style.setProperty('position', 'absolute');
  set.style.setProperty('visibility', 'hidden');
  set.style.setProperty('white-space', 'nowrap', 'important');
  set.style.setProperty('font-size', '100px', 'important');
  set.style.setProperty('font-stretch', `${hot.w}%`, 'important');
  set.style.setProperty('font-weight', String(hot.g), 'important');
  const range = document.createRange();
  let widest = 0;
  for (const ln of set.querySelectorAll<HTMLElement>('.ln')) widest = Math.max(widest, width(range, ln));
  range.detach?.();
  if (before === null) set.removeAttribute('style');
  else set.setAttribute('style', before);
  // 1% for sub-pixel rounding and hinting differences between sizes.
  return (widest / 100) * 1.01;
}

/**
 * Measures the headline's wide set (and its narrow set, when it has one) and
 * writes --fit-k / --fit-k-narrow and data-fitted / data-fitted-narrow; with
 * no narrow set, --fit-k-word (its longest word, for phones).
 * Also records the measured value in data-fit-k, so an author can copy it
 * into <Headline fitK> and render the right size before any script runs.
 */
export function fitHeadline(el: HTMLElement, hot: Hottest, hotNarrow: Hottest = hot): void {
  const wide = el.querySelector<HTMLElement>('.headline-wide');
  const narrow = el.querySelector<HTMLElement>('.headline-narrow');
  if (wide) {
    const k = measureK(wide, hot);
    if (k > 0) {
      el.style.setProperty('--fit-k', k.toFixed(4));
      el.dataset.fitted = '';
      el.dataset.fitK = k.toFixed(3);
    }
  }
  if (narrow) {
    const k = measureK(narrow, hotNarrow);
    if (k > 0) {
      el.style.setProperty('--fit-k-narrow', k.toFixed(4));
      el.dataset.fittedNarrow = '';
      el.dataset.fitKNarrow = k.toFixed(3);
    }
  } else if (wide) {
    const k = measureWordK(wide, hotNarrow);
    if (k > 0) el.style.setProperty('--fit-k-word', k.toFixed(4));
  }
}
