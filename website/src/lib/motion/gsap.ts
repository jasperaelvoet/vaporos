// GSAP, loaded after first paint and only where a page animates with it.
//
//   const { gsap, ScrollTrigger } = await loadGsap();
//
// The first call imports gsap and ScrollTrigger (about 46 kB gz together),
// registers the plugin once, and ties ScrollTrigger to Lenis whenever Lenis
// runs (./smooth-scroll.ts), so scrubbed scenes follow the eased scroll.
// Later calls return the same promise.
//
// In a component, prefer useGSAP from @gsap/react inside a client leaf that
// is itself loaded lazily; call loadGsap() (or import 'gsap' there) so the
// plugin registration and the Lenis bridge happen once. Wrap every scene in
// gsap.matchMedia() with the reduced-motion condition, or check
// useMotionAllowed() (./prefs.ts): under reduced motion or Pause motion the
// final frame must already be on screen.
import type { gsap as GsapNS } from 'gsap';
import type { ScrollTrigger as ScrollTriggerNS } from 'gsap/ScrollTrigger';
import { onLenis } from './smooth-scroll';

export interface Gsap {
  gsap: typeof GsapNS;
  ScrollTrigger: typeof ScrollTriggerNS;
}

let loading: Promise<Gsap> | null = null;

export function loadGsap(): Promise<Gsap> {
  loading ??= Promise.all([import('gsap'), import('gsap/ScrollTrigger')]).then(([g, st]) => {
    const gsap = g.gsap;
    const ScrollTrigger = st.ScrollTrigger;
    gsap.registerPlugin(ScrollTrigger);
    // Lenis moves the page on its own frame loop; tell ScrollTrigger each time.
    let off: (() => void) | null = null;
    onLenis((lenis) => {
      off?.();
      off = null;
      if (lenis) {
        const update = () => ScrollTrigger.update();
        lenis.on('scroll', update);
        off = () => lenis.off('scroll', update);
      }
      ScrollTrigger.refresh();
    });
    return { gsap, ScrollTrigger };
  });
  return loading;
}
