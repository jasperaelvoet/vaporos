'use client';
// The heat behind a small welcome screen: the Go renderer's field around the
// QR card (internal/display/welcome), on its 1920 × 1080 layout, painted once
// into a 480 × 270 canvas when it nears the viewport. `peak` is the state's
// heat: the installer runs cooler than a PC that is ready to stream.
import { useRef } from 'react';
import { around, paintIsotherms } from '@/direction/redline/painter';
import { useThermalCanvas } from '@/direction/redline/use-thermal-canvas';

export function TvField({ peak }: { peak: number }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useThermalCanvas(
    ref,
    (cv) =>
      paintIsotherms(
        cv,
        around(
          { cx: 1512, cy: 552, hw: 300, hh: 300, r: 16, rise: 540, nf: 0.0032, warp: 70, reach: 118, peak, ambient: 0.05, h: 1080 },
          cv.width / 1920,
        ),
        { bands: 12, px: 0.5 },
      ),
    [peak],
    { size: { width: 480, height: 270 } },
  );
  return <canvas ref={ref} className="tv-field" aria-hidden="true" />;
}
