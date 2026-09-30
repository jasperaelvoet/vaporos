// One requestAnimationFrame for everything that follows the scroll.
//
// A frame that has work runs, in this order:
//   1. Lenis, when it runs (smooth-scroll.ts): it moves the page;
//   2. every job's read(): scrollY and cached geometry, nothing that
//      makes the browser lay out again;
//   3. every job's write(): style and text writes;
// so no write forces a layout that a later read, or Lenis's scrollTo, would
// pay for, and the browser lays out once, after the frame.
//
// A frame is asked for by a scroll or resize, by input Lenis turns into a
// smooth scroll (wheel, touch, keys), by requestFrame() (a Lenis scrollTo,
// a kick of friction heat), or by a job whose write() returns true because
// it is still easing. When nothing moves, no frame runs at all.
//
//   const stop = whileNear(section, {
//     read: () => { p = progress(box, window.scrollY); },
//     write: () => { el.style.setProperty('--p', p.toFixed(3)); },
//   });
//   const box = trackBox(section);   // { top, height }, page coordinates, kept fresh
//
// whileNear() keeps a job registered only while its element is within a
// screen of the viewport, so off-screen scenes cost nothing per frame.
import type Lenis from 'lenis';

export interface FrameJob {
  /** Reads only: scrollY, innerHeight, a trackBox. */
  read?: () => void;
  /** Writes only. Return true to get another frame (still easing). */
  write?: () => boolean | void;
}

const jobs = new Set<FrameJob>();
let raf = 0;
let lenis: Lenis | null = null;
let lenisIdle = true;
let listening = false;

function frame(t: number) {
  raf = 0;
  let again = false;
  if (lenis) {
    // After a pause Lenis would see the whole pause as one step and jump.
    if (lenisIdle) lenis.time = t;
    lenis.raf(t);
    lenisIdle = lenis.isScrolling !== 'smooth';
    if (!lenisIdle) again = true;
  }
  for (const j of jobs) j.read?.();
  for (const j of jobs) if (j.write?.()) again = true;
  if (again) requestFrame();
}

/** Asks for one frame (several calls before it runs share it). */
export function requestFrame(): void {
  if (!raf && typeof window !== 'undefined') raf = requestAnimationFrame(frame);
}

function listen() {
  if (listening || typeof window === 'undefined') return;
  listening = true;
  const opts = { passive: true } as const;
  window.addEventListener('scroll', requestFrame, opts);
  window.addEventListener('resize', requestFrame, opts);
  // Input Lenis turns into a smooth scroll; it moves only when ticked.
  window.addEventListener('wheel', requestFrame, opts);
  window.addEventListener('touchmove', requestFrame, opts);
  window.addEventListener('keydown', requestFrame, opts);
  // An in-page link Lenis eases to (its anchors option).
  window.addEventListener('click', requestFrame, opts);
}

/** Registers a job for every frame from now on; returns the function that removes it. */
export function addFrameJob(job: FrameJob): () => void {
  listen();
  jobs.add(job);
  requestFrame();
  return () => {
    jobs.delete(job);
  };
}

/** The Lenis instance the frame ticks first (smooth-scroll.ts), or null for native scrolling. */
export function setFrameLenis(l: Lenis | null): void {
  lenis = l;
  lenisIdle = true;
  if (l) {
    listen();
    requestFrame();
  }
}

// ------------------------------------------------------------ geometry

export interface Box {
  /** The element's top and height in page coordinates (CSS px), as of its last change. */
  top: number;
  height: number;
}

const boxes = new Map<Element, Box>();
let bodyObserver: ResizeObserver | null = null;

function measure(el: Element, box: Box) {
  const r = el.getBoundingClientRect();
  box.top = r.top + window.scrollY;
  box.height = r.height;
}

function remeasureAll() {
  for (const [el, box] of boxes) measure(el, box);
  requestFrame();
}

/**
 * The element's page position, measured now and again whenever the page's
 * size changes (fonts, images, a fitted headline: anything above it moving
 * it), never during a frame. dispose() stops tracking it.
 */
export function trackBox(el: Element): Box & { dispose: () => void } {
  const box: Box = { top: 0, height: 0 };
  measure(el, box);
  boxes.set(el, box);
  if (!bodyObserver && typeof ResizeObserver === 'function') {
    bodyObserver = new ResizeObserver(remeasureAll);
    bodyObserver.observe(document.body);
    window.addEventListener('resize', remeasureAll, { passive: true });
  }
  bodyObserver?.observe(el);
  return Object.assign(box, {
    dispose: () => {
      boxes.delete(el);
      bodyObserver?.unobserve(el);
    },
  });
}

/**
 * Runs `job` every frame while `el` is within `margin` of the viewport
 * (default: one screen above and below), and not at all otherwise. `onLeave`
 * runs as it goes away, to settle a final state. Returns the stop function.
 */
export function whileNear(el: Element, job: FrameJob, margin = '100% 0px 100% 0px', onLeave?: () => void): () => void {
  let remove: (() => void) | null = null;
  const io = new IntersectionObserver(
    (es) => {
      const near = es[es.length - 1]?.isIntersecting ?? false;
      if (near && !remove) remove = addFrameJob(job);
      else if (!near && remove) {
        remove();
        remove = null;
        onLeave?.();
      }
    },
    { rootMargin: margin },
  );
  io.observe(el);
  return () => {
    io.disconnect();
    remove?.();
    remove = null;
  };
}
