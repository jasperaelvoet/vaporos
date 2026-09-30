// Lenis smooth scrolling: desktop fine pointers only, never under reduced
// motion or Pause motion. Touch keeps the platform's native scrolling.
//
// MotionRuntime (./runtime.tsx, mounted once in the root layout) starts and
// stops it as the preferences change. Other code reads it here:
//
//   onLenis((lenis) => …)   called now and whenever it starts or stops
//                           (lenis is null when native scrolling is in use)
//   getLenis()              the instance, or null
//   scrollToTarget(el)      Lenis's eased scroll when it runs, else native
//
// Lenis is imported on demand (5.4 kB gz), so pages that never start it
// don't pay for it. It has no loop of its own: the page's one scroll frame
// (./scroll-frame.ts) ticks it first, only while it moves, and runs every
// scroll reader and writer after it.
import type Lenis from 'lenis';
import { isDesktopFine, motionAllowed } from './prefs';
import { requestFrame, setFrameLenis } from './scroll-frame';

let lenis: Lenis | null = null;
let starting: Promise<void> | null = null;
const listeners = new Set<(l: Lenis | null) => void>();

const notify = () => listeners.forEach((cb) => cb(lenis));

export function getLenis(): Lenis | null {
  return lenis;
}

/** Subscribe to Lenis starting and stopping; `cb` runs at once with the current instance. */
export function onLenis(cb: (l: Lenis | null) => void): () => void {
  listeners.add(cb);
  cb(lenis);
  return () => {
    listeners.delete(cb);
  };
}

function navOffset(): number {
  const h = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--nav-h'));
  return -((Number.isFinite(h) ? h : 4.5) * 16 + 16);
}

/** Starts Lenis when this device and the motion preferences allow it. */
export function startSmoothScroll(): Promise<void> {
  if (lenis || !isDesktopFine() || !motionAllowed()) return Promise.resolve();
  starting ??= import('lenis')
    .then(({ default: LenisCtor }) => {
      // The preferences may have changed while the module loaded.
      if (lenis || !isDesktopFine() || !motionAllowed()) return;
      lenis = new LenisCtor({
        lerp: 0.11,
        wheelMultiplier: 0.9,
        autoRaf: false,
        anchors: { offset: navOffset() },
        stopInertiaOnNavigate: true,
      });
      setFrameLenis(lenis);
      notify();
    })
    .catch(() => {})
    .finally(() => {
      starting = null;
    });
  return starting;
}

export function stopSmoothScroll(): void {
  if (!lenis) return;
  lenis.destroy();
  lenis = null;
  setFrameLenis(null);
  notify();
}

/** Scrolls to an element or y offset: eased by Lenis when it runs, native (instant under reduced motion) otherwise. */
export function scrollToTarget(target: HTMLElement | number, opts: { offset?: number } = {}): void {
  const offset = opts.offset ?? (typeof target === 'number' ? 0 : navOffset());
  if (lenis) {
    lenis.scrollTo(target, { offset });
    requestFrame();
    return;
  }
  const y = typeof target === 'number' ? target : target.getBoundingClientRect().top + window.scrollY + offset;
  window.scrollTo({ top: y, behavior: motionAllowed() ? 'smooth' : 'auto' });
}
