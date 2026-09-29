'use client';
// The 404's picture (slot D8): a cold, dead screen. The last of the heat
// sinks out of it, a violet ember low in the frame, and the screen reads
// the code and "No signal" in the cold cut. Painted once, lazily, by the
// shared thermal painter; decorative (the page's heading says it in words).
import { useRef } from 'react';
import { paintIsotherms, smoothstep, useThermalCanvas, valueNoise } from '@/direction';

export function DeadScreen({ code, line, className = '' }: { code: string; line: string; className?: string }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  useThermalCanvas(
    canvas,
    (c, { dpr }) => {
      const W = c.width;
      const H = c.height;
      const s = 1 / dpr;
      paintIsotherms(
        c,
        (x, y) => {
          const u = x / W;
          const v = y / H;
          const n = valueNoise(x * s * 0.011, y * s * 0.014) - 0.5;
          // one ember below the middle, sinking and cooling
          const d = Math.hypot((u - 0.5) * 1.7, (v - 0.86 + n * 0.12) * 1.15);
          const ember = 0.21 * Math.exp(-((d / 0.34) ** 2));
          return ember * (0.55 + 0.45 * smoothstep(0.1, 1, v)) + 0.02;
        },
        { bands: 12, line: 0.36, px: dpr },
      );
    },
    [],
    { maxDpr: 1.5 },
  );
  return (
    <div aria-hidden className={`rounded-[1.25rem] bg-char p-[clamp(6px,1vw,12px)] inset-ring-1 inset-ring-line ${className}`}>
      <div className="@container relative aspect-[16/10] overflow-hidden rounded-[0.75rem] bg-h0">
        <canvas ref={canvas} className="absolute inset-0 size-full" />
        <div className="absolute inset-0 grid place-content-center justify-items-center gap-[1.5cqw]">
          <span className="cut-cold text-[30cqw] leading-[0.8] tracking-[-0.02em] text-bone/92">{code}</span>
          <span className="telemetry rounded-full bg-h0/80 px-[2cqw] py-[0.9cqw] text-[3.1cqw] leading-none text-smoke">{line}</span>
        </div>
      </div>
    </div>
  );
}
