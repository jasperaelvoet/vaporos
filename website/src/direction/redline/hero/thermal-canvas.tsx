'use client';
// The hero's live thermal view: a WebGL2 canvas over the poster. Loaded
// with next/dynamic (ssr: false) by HeroStage, after the page is idle, on
// desktops only (hero-stage.tsx says which). It:
//
// - boots the PC (PSU, board, CPU, GPU) over 2.2 s and breathes one trail of
//   steam across the headline, to show that the pointer does this too;
// - lets the pointer (mouse and pen, never touch) stir heat and air;
// - follows the scroll's load (scroll-load.ts), heating the CPU and GPU;
// - runs at 60 fps while the pointer stirs the air and 30 fps otherwise
//   (always 30 on a coarse pointer), at a capped device pixel ratio;
// - stops when the hero is off screen, the tab is hidden, or motion is
//   turned off (reduced motion or Pause motion), keeping its last frame;
// - falls back to the poster when WebGL is missing or the context is lost.
//
// When the first frame is drawn it sets data-gl="on" on the hero, and the
// CSS fades the canvas in over the poster.
import { useEffect, useRef } from 'react';
import { motionAllowed, subscribeMotion } from '@/lib/motion/prefs';
import { createThermal, type Thermal } from './engine';
import { EXHAUSTS, caseBox, caseToUv, NARROW_MAX } from './geometry';
import { heroLoad } from './scroll-load';

const BOOT_S = 2.2;
const BREATH_FROM = 0.5;
const BREATH_S = 1.6;

// power1.inOut
const inOut = (x: number) => (x <= 0 ? 0 : x >= 1 ? 1 : x < 0.5 ? 2 * x * x : 1 - 2 * (1 - x) * (1 - x));

export default function ThermalCanvas() {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const slot = ref.current;
    const hero = slot?.closest<HTMLElement>('[data-hero]');
    if (!slot || !hero) return;
    // A canvas per run: its context is released with it (dispose() loses it).
    const canvas = document.createElement('canvas');
    canvas.className = 'hero-canvas';
    canvas.setAttribute('aria-hidden', 'true');
    slot.append(canvas);
    const coarse = window.matchMedia('(pointer: coarse)').matches;
    let T: Thermal | null = null;
    let alive = true;
    let raf = 0;
    let visible = true;
    let running = false;
    let started = 0;
    let last = 0;
    let lastDraw = 0;
    let lastMove = -1e9;
    let load = heroLoad.get();
    let px = -1;
    let py = -1;
    let breath = -1;
    let bx = -1;
    let by = -1;
    let narrow = false;

    const place = () => {
      if (!T) return;
      T.resize();
      const { w, h, dpr } = T.size();
      const cssW = w / dpr;
      const cssH = h / dpr;
      const box = caseBox(cssW, cssH);
      narrow = cssW <= NARROW_MAX;
      T.state.case = [box.x0 * dpr, box.y0 * dpr, box.ch * dpr];
      T.state.sources = EXHAUSTS.map((e) => [...caseToUv(box, cssW, cssH, e.q[0], e.q[1]), e.spread, 0]);
      T.state.dirs = EXHAUSTS.map((e) => e.dir);
    };

    // The rear fan always breathes a little, so the picture is never still.
    const feed = () => {
      if (!T) return;
      const s = T.state.sources;
      const boot = T.state.boot;
      const base = (0.34 + 1.2 * T.state.load) * boot;
      if (s.length < 4) return;
      s[0][3] = (0.5 + 1.2 * base) * boot;
      s[1][3] = 1.4 * base;
      s[2][3] = 0.8 * base;
      s[3][3] = 0.5 * base;
    };

    const frame = (now: number) => {
      raf = 0;
      if (!T || !running || !visible || document.hidden) return;
      raf = requestAnimationFrame(frame);
      const minFrame = coarse || now - lastMove > 2000 ? 1000 / 30 : 0;
      if (now - lastDraw < minFrame - 2) return;
      const dt = Math.min(0.033, (now - (last || now)) / 1000 || 1 / 60);
      last = now;
      lastDraw = now;
      const t = (now - started) / 1000;
      T.state.boot = inOut(t / BOOT_S);
      // Ease toward the scroll's load (a 0.6 s scrub).
      load += (heroLoad.get() - load) * Math.min(1, dt / 0.2);
      T.state.load = load;
      // One breath of steam across the words.
      if (breath >= 0 && t >= BREATH_FROM) {
        const k = Math.min(1, (t - BREATH_FROM) / BREATH_S);
        const e = inOut(k);
        const x = narrow ? 0.08 + 0.84 * e : 0.04 + 0.5 * e;
        const y = narrow ? 0.34 + 0.12 * Math.sin(e * Math.PI * 1.5) : 0.2 + 0.26 * e + 0.08 * Math.sin(e * Math.PI * 2);
        if (bx >= 0) T.splat(x, y, (x - bx) * 2600, (y - by) * 2600 + 30, 0.3);
        bx = x;
        by = y;
        if (k >= 1) breath = -1;
      }
      feed();
      T.step(dt);
      T.render(t);
    };
    const start = () => {
      running = motionAllowed();
      if (running && !raf && T) {
        last = 0;
        raf = requestAnimationFrame(frame);
      }
    };
    const stop = () => {
      running = false;
      if (raf) cancelAnimationFrame(raf);
      raf = 0;
    };

    const onMove = (e: PointerEvent) => {
      if (!T || e.pointerType === 'touch' || !running) return;
      const r = canvas.getBoundingClientRect();
      const x = (e.clientX - r.left) / r.width;
      const y = 1 - (e.clientY - r.top) / r.height;
      if (px >= 0) T.splat(x, y, (x - px) * 4200, (y - py) * 4200, 0.32);
      px = x;
      py = y;
      lastMove = performance.now();
    };
    const onLeave = () => {
      px = -1;
    };
    const onVisibility = () => (document.hidden ? stop() : start());
    const io = new IntersectionObserver(
      (es) => {
        visible = !!es[0]?.isIntersecting;
        if (visible) start();
      },
      { threshold: 0.02 },
    );
    const ro = new ResizeObserver(() => {
      place();
      if (!raf && T) T.render((performance.now() - started) / 1000);
    });
    const onLost = (e: Event) => {
      e.preventDefault();
      stop();
      T = null;
      delete hero.dataset.gl;
    };

    const size: Parameters<typeof createThermal>[1] = coarse
      ? { maxDpr: 1, sim: 72, dye: 240, iters: 12, power: 'low-power' }
      : { maxDpr: 1.25, sim: 96, dye: 320, iters: 14, power: 'high-performance' };

    createThermal(canvas, size)
      .then((thermal) => {
        if (!thermal || !alive) {
          thermal?.dispose();
          return;
        }
        T = thermal;
        place();
        started = performance.now();
        breath = 0;
        T.state.load = load;
        T.render(0);
        canvas.addEventListener('webglcontextlost', onLost);
        hero.addEventListener('pointermove', onMove, { passive: true });
        hero.addEventListener('pointerleave', onLeave);
        document.addEventListener('visibilitychange', onVisibility);
        io.observe(hero);
        ro.observe(canvas);
        requestAnimationFrame(() => {
          if (alive && T) hero.dataset.gl = 'on';
        });
        start();
      })
      .catch(() => {});

    // Reduced motion or Pause motion stop the loop and keep the last frame.
    const offMotion = subscribeMotion(() => (motionAllowed() ? start() : stop()));

    return () => {
      alive = false;
      stop();
      offMotion();
      io.disconnect();
      ro.disconnect();
      hero.removeEventListener('pointermove', onMove);
      hero.removeEventListener('pointerleave', onLeave);
      document.removeEventListener('visibilitychange', onVisibility);
      canvas.removeEventListener('webglcontextlost', onLost);
      delete hero.dataset.gl;
      T?.dispose();
      T = null;
      canvas.remove();
    };
  }, []);

  return <div ref={ref} className="hero-canvas-slot" aria-hidden="true" />;
}
