'use client';
// The hero's client half. It renders nothing of its own until the live view
// may start, and does two things:
//
// 1. The scroll: over the first 62% of the hero the load goes 0.34 → 1
//    (scroll-load.ts; the canvas heats the CPU and GPU with it), the spot
//    meter reads "Streaming to Living room TV" past HOT_AT (HOT_AT_NARROW on
//    phones; data-hot on the hero: the CSS swaps the words and heats the
//    poster), and, when motion is allowed, the
//    headline stretches from its warm cut toward the hot one (--stretch,
//    --stretch-g: +24 and +100 on desktop, +8 on phones; the Headline is
//    fitted for it, so nothing reflows).
//
// 2. The live view: on a desktop-sized screen (≥ 1024 px), with motion
//    allowed, WebGL2, no Save-Data and more than 2 GB of memory, it loads
//    the thermal canvas with next/dynamic once the page is idle
//    (requestIdleCallback) and the hero is on screen. Everyone else keeps
//    the poster, which is the same PC drawn in SVG. Loading WebGL on
//    desktops only keeps phones light (U-7).
import dynamic from 'next/dynamic';
import { useEffect, useRef, useState } from 'react';
import { motionAllowed, subscribeMotion } from '@/lib/motion/prefs';
import { requestFrame, trackBox, whileNear } from '@/lib/motion/scroll-frame';
import { HOT_AT, HOT_AT_NARROW, LOAD_REST, LOAD_SPAN, NARROW_MAX } from './geometry';
import { heroLoad } from './scroll-load';

const ThermalCanvas = dynamic(() => import('./thermal-canvas'), { ssr: false, loading: () => null });

/** Where the live view may run. */
export const HERO_GL_QUERY = '(min-width: 1024px)';

interface NetworkInformation {
  saveData?: boolean;
}

function canRunGL(): boolean {
  if (!window.matchMedia(HERO_GL_QUERY).matches || !motionAllowed()) return false;
  const nav = navigator as Navigator & { connection?: NetworkInformation; deviceMemory?: number };
  if (nav.connection?.saveData) return false;
  if (typeof nav.deviceMemory === 'number' && nav.deviceMemory <= 2) return false;
  return typeof WebGL2RenderingContext !== 'undefined';
}

export function HeroStage() {
  const ref = useRef<HTMLSpanElement>(null);
  const [live, setLive] = useState(false);

  // 1. The scroll: a job in the page's scroll frame, only while the hero is
  // within a screen of the viewport; progress from scrollY and the hero's
  // cached box, never a layout read per frame.
  useEffect(() => {
    const hero = ref.current?.closest<HTMLElement>('[data-hero]');
    const h1 = hero?.querySelector<HTMLElement>('.hero-title');
    if (!hero) return;
    const box = trackBox(hero);
    let stretch = motionAllowed();
    let p = 0;
    let lastP = -1;
    const job = {
      read: () => {
        p = Math.min(1, Math.max(0, (window.scrollY - box.top) / Math.max(1, box.height * LOAD_SPAN)));
      },
      write: () => {
        if (Math.abs(p - lastP) < 0.0005) return;
        lastP = p;
        heroLoad.set(LOAD_REST + (1 - LOAD_REST) * p);
        const narrow = window.innerWidth <= NARROW_MAX;
        const hot = p > (narrow ? HOT_AT_NARROW : HOT_AT);
        if (hot !== (hero.dataset.hot === 'on')) {
          if (hot) hero.dataset.hot = 'on';
          else delete hero.dataset.hot;
        }
        if (h1 && stretch) {
          const to = narrow ? 8 : 24;
          h1.style.setProperty('--stretch', `${(to * p).toFixed(2)}%`);
          h1.style.setProperty('--stretch-g', (100 * p).toFixed(1));
        }
      },
    };
    const offMotion = subscribeMotion(() => {
      stretch = motionAllowed();
      if (!stretch && h1) {
        h1.style.removeProperty('--stretch');
        h1.style.removeProperty('--stretch-g');
      }
      lastP = -1;
      requestFrame();
    });
    const stop = whileNear(hero, job);
    return () => {
      stop();
      box.dispose();
      offMotion();
    };
  }, []);

  // 2. The live view, when the page is idle and the hero is on screen.
  useEffect(() => {
    const hero = ref.current?.closest<HTMLElement>('[data-hero]');
    if (!hero) return;
    let idle = 0;
    let done = false;
    const ric = typeof window.requestIdleCallback === 'function';
    const go = () => {
      if (done || !canRunGL()) return;
      done = true;
      setLive(true);
    };
    const io = new IntersectionObserver((es) => {
      if (!es[0]?.isIntersecting || done) return;
      io.disconnect();
      idle = ric ? window.requestIdleCallback(go, { timeout: 2000 }) : window.setTimeout(go, 400);
    });
    const arm = () => {
      if (!done && canRunGL()) io.observe(hero);
    };
    // Motion may be allowed later (Pause motion turned off): arm again then.
    const offMotion = subscribeMotion(arm);
    arm();
    return () => {
      io.disconnect();
      offMotion();
      if (ric) window.cancelIdleCallback(idle);
      else window.clearTimeout(idle);
    };
  }, []);

  return (
    <>
      <span ref={ref} hidden />
      {live && <ThermalCanvas />}
    </>
  );
}
