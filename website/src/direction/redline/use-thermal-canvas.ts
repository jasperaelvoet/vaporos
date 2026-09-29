'use client';
// Paint a thermal canvas lazily: when it comes within 900 px of the
// viewport, from idle time (at once if it is already on screen, as after a
// jump from the nav), and again when its width changes.
//
//   const ref = useRef<HTMLCanvasElement>(null);
//   useThermalCanvas(ref, (canvas, { dpr }) => paintIsotherms(canvas, field, { px: dpr }), [load]);
//   <canvas ref={ref} aria-hidden className="h-50 w-full" />
//
// The canvas's backing store is sized to its CSS box × devicePixelRatio
// (capped at `maxDpr`, default 2) before each paint; pass `size` to paint a
// fixed resolution instead (the CSS scales it). Painting is a still, so it
// runs under reduced motion too.
import { useEffect, useRef, type DependencyList, type RefObject } from 'react';

export interface ThermalPaintInfo {
  /** The device pixel ratio the canvas was sized for. */
  dpr: number;
  /** The CSS box. */
  width: number;
  height: number;
}

export interface ThermalCanvasOptions {
  maxDpr?: number;
  /** A fixed backing-store size in canvas pixels (skips the CSS-box sizing). */
  size?: { width: number; height: number };
  /** Repaint when the element's width changes by more than this many CSS px (default 8; 0 turns it off). */
  repaintOnResize?: number;
}

// Safari has no requestIdleCallback before 18.
const idle = (fn: () => void) =>
  typeof window.requestIdleCallback === 'function' ? window.requestIdleCallback(fn, { timeout: 900 }) : setTimeout(fn, 200);

export function useThermalCanvas(
  ref: RefObject<HTMLCanvasElement | null>,
  paint: (canvas: HTMLCanvasElement, info: ThermalPaintInfo) => void,
  deps: DependencyList = [],
  opts: ThermalCanvasOptions = {},
): void {
  const paintRef = useRef(paint);
  const optsRef = useRef(opts);
  useEffect(() => {
    paintRef.current = paint;
    optsRef.current = opts;
  });

  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) return;
    let painted = false;
    let lastW = 0;

    const draw = () => {
      const o = optsRef.current;
      const box = canvas.getBoundingClientRect();
      const dpr = Math.min(o.maxDpr ?? 2, window.devicePixelRatio || 1);
      if (o.size) {
        canvas.width = o.size.width;
        canvas.height = o.size.height;
      } else {
        if (!box.width || !box.height) return;
        canvas.width = Math.round(box.width * dpr);
        canvas.height = Math.round(box.height * dpr);
      }
      lastW = box.width;
      painted = true;
      paintRef.current(canvas, { dpr: o.size ? 1 : dpr, width: box.width, height: box.height });
    };

    const io = new IntersectionObserver(
      (entries) => {
        if (!entries[0]?.isIntersecting) return;
        io.disconnect();
        const r = canvas.getBoundingClientRect();
        if (r.bottom > 0 && r.top < window.innerHeight) draw();
        else idle(draw);
      },
      { rootMargin: '900px 0px' },
    );
    io.observe(canvas);

    const step = optsRef.current.repaintOnResize ?? 8;
    let timer = 0;
    const ro =
      step > 0 && !optsRef.current.size
        ? new ResizeObserver(() => {
            if (!painted) return;
            window.clearTimeout(timer);
            timer = window.setTimeout(() => {
              if (Math.abs(canvas.getBoundingClientRect().width - lastW) > step) draw();
            }, 150);
          })
        : null;
    ro?.observe(canvas);

    return () => {
      io.disconnect();
      ro?.disconnect();
      window.clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
}
