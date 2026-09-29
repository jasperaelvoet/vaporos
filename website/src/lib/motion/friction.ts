// Friction heat: scroll speed warms the headlines a little.
//
// Every element with [data-friction] gets --friction (0..1) while the page
// moves; type.css adds --friction × --friction-gain to its width (<Headline
// friction={8}> sets both). The loop runs only while there is speed to
// spend, then writes 0 and stops. Desktop fine pointers only, never under
// reduced motion or Pause motion: MotionRuntime (./runtime.tsx) starts and
// stops it. Speed comes from Lenis when it runs, else from native scroll.
import { getLenis, onLenis } from './smooth-scroll';

let v = 0;
let target = 0;
let raf = 0;
let els: HTMLElement[] = [];

function write(value: string) {
  for (const el of els) el.style.setProperty('--friction', value);
}

function loop() {
  v += (target - v) * 0.12;
  target *= 0.9;
  if (v > 0.002 || target > 0.002) {
    write(Math.min(1, v).toFixed(3));
    raf = requestAnimationFrame(loop);
  } else {
    write('0');
    raf = 0;
    v = target = 0;
  }
}

/** Adds speed (0..1). Starts the loop from rest, picking up the page's [data-friction] elements. */
function kick(amount: number) {
  target = Math.max(target, Math.min(1, amount));
  if (!raf) {
    els = Array.from(document.querySelectorAll<HTMLElement>('[data-friction]'));
    if (!els.length) return;
    raf = requestAnimationFrame(loop);
  }
}

/** Starts listening; returns the stop function (which also cools every headline at once). */
export function startFriction(): () => void {
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
    if (raf) cancelAnimationFrame(raf);
    raf = 0;
    v = target = 0;
    els = Array.from(document.querySelectorAll<HTMLElement>('[data-friction]'));
    write('0');
  };
}
