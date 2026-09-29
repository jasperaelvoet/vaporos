// Whether the site may move, in one place.
//
// Motion is allowed unless the system asks for reduced motion or the visitor
// pressed Pause motion in the footer (saved per browser in localStorage and
// reflected as html[data-motion="paused"], which the inline chrome script
// restores before first paint). Every animated leaf asks here:
//
//   const allowed = useMotionAllowed();   // false on the server and in the
//                                         // first client render: render the
//                                         // final frame, then animate if true
//   if (motionAllowed()) …                // outside React
//   subscribeMotion(() => …)              // react to either switch changing
//
// "Desktop" means a fine pointer on a screen at least 1024 px wide: only
// there does the site run Lenis smooth scrolling and scroll friction.
import { useSyncExternalStore } from 'react';
import { MOTION_KEY } from '../chrome-script';

export const REDUCED_QUERY = '(prefers-reduced-motion: reduce)';
export const DESKTOP_QUERY = '(pointer: fine) and (min-width: 1024px)';

const EVENT = 'vaporos:motion';
const hasWindow = () => typeof window !== 'undefined' && typeof window.matchMedia === 'function';

export function prefersReducedMotion(): boolean {
  return hasWindow() && window.matchMedia(REDUCED_QUERY).matches;
}

export function isMotionPaused(): boolean {
  return typeof document !== 'undefined' && document.documentElement.dataset.motion === 'paused';
}

/** True when neither the system nor the visitor turned motion off. */
export function motionAllowed(): boolean {
  return hasWindow() && !prefersReducedMotion() && !isMotionPaused();
}

/** A fine pointer on a wide screen: smooth scrolling and friction heat run only here. */
export function isDesktopFine(): boolean {
  return hasWindow() && window.matchMedia(DESKTOP_QUERY).matches;
}

/** The footer switch: pause or resume the site's motion, and remember it in this browser. */
export function setMotionPaused(paused: boolean): void {
  const html = document.documentElement;
  if (paused) html.dataset.motion = 'paused';
  else delete html.dataset.motion;
  try {
    if (paused) localStorage.setItem(MOTION_KEY, 'paused');
    else localStorage.removeItem(MOTION_KEY);
  } catch {}
  window.dispatchEvent(new Event(EVENT));
}

/** Calls `cb` whenever reduced motion, Pause motion or the desktop query changes. */
export function subscribeMotion(cb: () => void): () => void {
  if (!hasWindow()) return () => {};
  const queries = [window.matchMedia(REDUCED_QUERY), window.matchMedia(DESKTOP_QUERY)];
  queries.forEach((q) => q.addEventListener('change', cb));
  window.addEventListener(EVENT, cb);
  return () => {
    queries.forEach((q) => q.removeEventListener('change', cb));
    window.removeEventListener(EVENT, cb);
  };
}

const serverFalse = () => false;

/** Motion allowed, as React state. False on the server, so the static HTML is always the final frame. */
export function useMotionAllowed(): boolean {
  return useSyncExternalStore(subscribeMotion, motionAllowed, serverFalse);
}

/** The system's reduced-motion setting, as React state (false on the server). */
export function useReducedMotion(): boolean {
  return useSyncExternalStore(subscribeMotion, prefersReducedMotion, serverFalse);
}

/** The footer's Pause motion switch, as React state (false on the server). */
export function useMotionPaused(): boolean {
  return useSyncExternalStore(subscribeMotion, isMotionPaused, serverFalse);
}

/** Desktop fine pointer, as React state (false on the server). */
export function useDesktopFine(): boolean {
  return useSyncExternalStore(subscribeMotion, isDesktopFine, serverFalse);
}
