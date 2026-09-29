'use client';
// The footer's ember (slot D7): where the page's heat cycle ends. A low,
// cooling glow along the bottom edge, painted once by the shared thermal
// painter when the footer comes near (and again if its width changes).
// Violet and magenta only: nothing down here is hot any more. Decorative.
import { useRef } from 'react';
import { paintIsotherms, smoothstep, valueNoise } from './painter';
import { useThermalCanvas } from './use-thermal-canvas';

export function FooterEmber({ className = '' }: { className?: string }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useThermalCanvas(ref, (canvas, { dpr }) => {
    const W = canvas.width;
    const H = canvas.height;
    const s = 1 / dpr;
    paintIsotherms(
      canvas,
      (x, y) => {
        const X = x * s;
        const Y = y * s;
        const u = x / W;
        // two embers, left of centre and far right, on a faint floor
        const glow =
          0.26 * Math.exp(-(((u - 0.3) / 0.22) ** 2)) + 0.2 * Math.exp(-(((u - 0.86) / 0.16) ** 2)) + 0.07;
        const n = valueNoise(X * 0.012, Y * 0.02) - 0.5;
        const rise = 1 - y / H + n * 0.35;
        return glow * smoothstep(0.02, 1.05, 1 - rise * 0.92) * 1.15;
      },
      { bands: 12, line: 0.38, px: dpr },
    );
  });
  return <canvas ref={ref} aria-hidden className={`footer-ember ${className}`} />;
}
