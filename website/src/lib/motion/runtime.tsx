'use client';
// The site's motion runtime, mounted once in the root layout. It renders
// nothing: it starts Lenis and friction heat on desktop fine pointers when
// motion is allowed, and stops both the moment reduced motion or Pause
// motion turns on (or the window shrinks below the desktop query).
import { useEffect } from 'react';
import { startFriction } from './friction';
import { isDesktopFine, motionAllowed, subscribeMotion } from './prefs';
import { startSmoothScroll, stopSmoothScroll } from './smooth-scroll';

export function MotionRuntime() {
  useEffect(() => {
    let stopFriction: (() => void) | null = null;
    const apply = () => {
      const on = motionAllowed() && isDesktopFine();
      if (on) {
        void startSmoothScroll();
        stopFriction ??= startFriction();
      } else {
        stopSmoothScroll();
        stopFriction?.();
        stopFriction = null;
      }
    };
    // After first paint: none of this may compete with the LCP.
    // Safari has no requestIdleCallback before 18.
    const ric = typeof window.requestIdleCallback === 'function';
    const idle = ric ? window.requestIdleCallback(apply, { timeout: 1200 }) : window.setTimeout(apply, 300);
    const off = subscribeMotion(apply);
    return () => {
      if (ric) window.cancelIdleCallback(idle);
      else window.clearTimeout(idle);
      off();
      stopSmoothScroll();
      stopFriction?.();
    };
  }, []);
  return null;
}
