'use client';
// The heat behind the phone's state word: the ready field (violet, peaking at
// magenta, as the TV's), painted once into a small canvas when it nears the
// viewport, with the shared painter.
import { useRef } from 'react';
import { around, paintIsotherms } from '@/direction/redline/painter';
import { useThermalCanvas } from '@/direction/redline/use-thermal-canvas';

export function PhoneField() {
  const ref = useRef<HTMLCanvasElement>(null);
  useThermalCanvas(
    ref,
    (cv) =>
      paintIsotherms(
        cv,
        around({ cx: cv.width * 0.62, cy: cv.height * 0.38, hw: 20, hh: 26, r: 20, rise: 170, nf: 0.012, warp: 34, reach: 58, peak: 0.45, ambient: 0.05, h: cv.height }),
        { bands: 12 },
      ),
    [],
    { size: { width: 264, height: 240 } },
  );
  return <canvas ref={ref} className="phone-field" aria-hidden="true" />;
}
