// A scroll scene without GSAP: stretch a headline as a section scrolls by.
//
//   const stop = scrollStretch(h1, { trigger: hero, to: 24, toG: 100, span: 0.62 });
//
// Writes --stretch (0% → `to`%) and --stretch-g (0 → `toG`) on `el` as
// `trigger` scrolls from its top at the viewport's top to `span` of its own
// height above it. Fit the headline for the result first (<Headline
// stretch={to} stretchG={toG}>), so the stretch never reflows it. Under
// reduced motion or Pause motion nothing is written; the caller should not
// start it then (useMotionAllowed()). A ScrollTrigger scrub on the same two
// properties does the same with GSAP's easing (./motion/gsap.ts).
export interface ScrollStretchOptions {
  /** The element whose scroll drives it (default: `el`). */
  trigger?: HTMLElement;
  /** Width added at the end, in percent points (default 24: warm 100% → hot 124%). */
  to?: number;
  /** Weight added at the end (default 100: 700 → 800). */
  toG?: number;
  /** How much of the trigger's height the stretch takes (default 0.62). */
  span?: number;
  /** Called with the progress 0..1 on every change. */
  onProgress?: (p: number) => void;
}

export function scrollStretch(el: HTMLElement, opts: ScrollStretchOptions = {}): () => void {
  const trigger = opts.trigger ?? el;
  const to = opts.to ?? 24;
  const toG = opts.toG ?? 100;
  const span = opts.span ?? 0.62;
  let raf = 0;
  let last = -1;
  const run = () => {
    raf = 0;
    const r = trigger.getBoundingClientRect();
    const p = Math.min(1, Math.max(0, -r.top / Math.max(1, r.height * span)));
    if (Math.abs(p - last) < 0.001) return;
    last = p;
    el.style.setProperty('--stretch', `${(to * p).toFixed(2)}%`);
    el.style.setProperty('--stretch-g', (toG * p).toFixed(1));
    opts.onProgress?.(p);
  };
  const queue = () => {
    if (!raf) raf = requestAnimationFrame(run);
  };
  window.addEventListener('scroll', queue, { passive: true });
  window.addEventListener('resize', queue);
  queue();
  return () => {
    window.removeEventListener('scroll', queue);
    window.removeEventListener('resize', queue);
    if (raf) cancelAnimationFrame(raf);
    el.style.removeProperty('--stretch');
    el.style.removeProperty('--stretch-g');
  };
}
