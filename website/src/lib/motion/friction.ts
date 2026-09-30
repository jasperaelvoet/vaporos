// Friction heat: scroll speed warms the headlines a little.
//
// Every element with [data-friction] gets --friction (0..1) while the page
// moves; type.css adds --friction × --friction-gain to its width (<Headline
// friction={8}> sets both). Desktop fine pointers only, never under reduced
// motion or Pause motion: MotionRuntime (./runtime.tsx) starts and stops it.
// Speed comes from Lenis when it runs, else from native scroll.
//
// It is one job in the page's scroll frame (./scroll-frame.ts), writing after
// Lenis has moved the page, only to the headlines on screen (an
// IntersectionObserver keeps that list), and only when the value changes by a
// visible step. When the speed is spent it writes 0 and asks for no frames.
import { addFrameJob, requestFrame } from './scroll-frame';
import { getLenis, onLenis } from './smooth-scroll';

/** The smallest change worth a style write (and a relayout of the headline). */
const STEP = 0.01;

let v = 0;
let target = 0;

/** Adds speed (0..1). */
function kick(amount: number) {
  target = Math.max(target, Math.min(1, amount));
  requestFrame();
}

/** Starts listening; returns the stop function (which also cools every headline at once). */
export function startFriction(): () => void {
  const visible = new Set<HTMLElement>();
  const written = new Map<HTMLElement, number>();
  const put = (el: HTMLElement, value: number) => {
    if (written.get(el) === value) return;
    written.set(el, value);
    el.style.setProperty('--friction', value ? value.toFixed(2) : '0');
  };
  const io = new IntersectionObserver((es) => {
    for (const e of es) {
      const el = e.target as HTMLElement;
      if (e.isIntersecting) visible.add(el);
      else {
        visible.delete(el);
        put(el, 0);
      }
    }
  });
  const els = Array.from(document.querySelectorAll<HTMLElement>('[data-friction]'));
  els.forEach((el) => io.observe(el));

  const removeJob = addFrameJob({
    write: () => {
      v += (target - v) * 0.12;
      target *= 0.9;
      const live = v > 0.002 || target > 0.002;
      if (!live) v = target = 0;
      const value = Math.round(Math.min(1, v) / STEP) * STEP;
      for (const el of visible) put(el, value);
      return live;
    },
  });

  let lastY = window.scrollY;
  let lastT = performance.now();
  const onNative = () => {
    if (getLenis()) return; // Lenis reports its own velocity
    const now = performance.now();
    const dy = Math.abs(window.scrollY - lastY);
    const dt = Math.max(8, now - lastT);
    lastY = window.scrollY;
    lastT = now;
    // px per frame at 60 fps, on the same scale as Lenis's velocity
    kick(((dy / dt) * 16.7) / 40);
  };
  let offLenisScroll: (() => void) | null = null;
  const offLenis = onLenis((lenis) => {
    offLenisScroll?.();
    offLenisScroll = null;
    if (lenis) {
      const onScroll = () => kick(Math.abs(lenis.velocity) / 40);
      lenis.on('scroll', onScroll);
      offLenisScroll = () => lenis.off('scroll', onScroll);
    }
  });
  window.addEventListener('scroll', onNative, { passive: true });
  return () => {
    window.removeEventListener('scroll', onNative);
    offLenis();
    offLenisScroll?.();
    removeJob();
    io.disconnect();
    v = target = 0;
    for (const el of els) put(el, 0);
  };
}
