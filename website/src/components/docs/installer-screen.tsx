'use client';
// The welcome screen while the installer waits, drawn like the PC draws it
// (internal/display/welcome, REDLINE): the words on the cool side, the setup
// code on a white-hot rim, and the QR card in its own heat, bracketed like a
// spot meter. Laid out on the renderer's 1920 × 1080 reference and scaled
// with the frame (container units), so it reads as a screen at any width.
//
// An illustration: the QR is a decorative pattern (it doesn't scan), the IP
// address and the code are examples, and the whole picture is one image
// with a text alternative. Once the site ships real renders of the screen
// (pages.yml, TvStill), those replace it. The field is painted once, lazily,
// by the shared thermal painter; it is a still, so reduced motion changes
// nothing.
import { useRef } from 'react';
import { LogoMark, Wordmark } from '@/components/ui';
import { around, paintIsotherms, smoothstep, useThermalCanvas } from '@/direction';
import { TvHandshake } from '@/components/story/tv-handshake';
import { installerScreen as s } from '@/content/install';
import { tvInstaller } from '@/content/story';

// A QR-like grid: three finder squares and pseudo-random cells. Deterministic,
// so the server and the browser draw the same thing. Not a real code.
function pattern(size = 25, seed = 11): boolean[] {
  const out: boolean[] = [];
  const far = size - 7;
  let r = seed;
  for (let i = 0; i < size * size; i++) {
    r = (r * 1103515245 + 12345) & 0x7fffffff;
    const x = i % size;
    const y = Math.floor(i / size);
    const finder = (x < 7 && y < 7) || (x >= far && y < 7) || (x < 7 && y >= far);
    if (finder) {
      const fx = x >= far ? x - far : x;
      const fy = y >= far ? y - far : y;
      out.push(fx === 0 || fx === 6 || fy === 0 || fy === 6 || (fx > 1 && fx < 5 && fy > 1 && fy < 5));
    } else {
      const sep = (x === 7 || y === 7 || x === far - 1) && (y < 8 || x < 8);
      out.push(!sep && (((r >> 16) & 3) === 0 || ((r >> 12) & 1) === 1));
    }
  }
  return out;
}

const QR = 25;
const CELLS = pattern(QR);
const QR_PATH = CELLS.map((on, i) => (on ? `M${i % QR} ${Math.floor(i / QR)}h1v1h-1z` : '')).join('');

// The card's heat, in the 1920 × 1080 layout: it rises, the air warps it,
// and it flattens toward the text column (the cool side), as on the PC.
const SOURCE = { cx: 1512, cy: 552, hw: 292, hh: 292, r: 26, reach: 150, peak: 0.5, rise: 620, nf: 0.0042, warp: 70, ambient: 0.1, h: 1080 };

export function InstallerScreen({ className = '' }: { className?: string }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  useThermalCanvas(
    canvas,
    (c, { dpr }) => {
      const scale = c.width / 1920;
      const field = around(SOURCE, scale);
      paintIsotherms(
        c,
        (x, y) => {
          const u = x / c.width;
          return field(x, y) * (0.22 + 0.78 * smoothstep(0.34, 0.66, u)) + 0.035;
        },
        { bands: 12, line: 0.4, px: dpr },
      );
    },
    [],
    { maxDpr: 1.5 },
  );

  const label = `The screen on the PC: “${s.status}”, “${s.detail}”, the address ${s.url} (${s.or} ${s.ip}), a ${s.codeLabel.toLowerCase()} and a QR code.`;

  return (
    <div
      role="img"
      aria-label={label}
      className={`rounded-[1.125rem] bg-char p-[clamp(5px,0.9vw,10px)] inset-ring-1 inset-ring-line ${className}`}
    >
      <div className="@container relative aspect-video overflow-hidden rounded-[0.625rem] bg-h0 select-none" aria-hidden>
        <canvas ref={canvas} className="absolute inset-0 size-full" />

        {/* brand row */}
        <div className="absolute top-[9%] left-[6.8%] flex items-center gap-[1.1cqw]">
          <LogoMark className="size-[2.5cqw]" />
          <Wordmark className="h-[2cqw] w-auto" />
        </div>

        {/* the words, on the cool side */}
        <p className="cut-cold absolute top-[25%] left-[6.6%] text-[6.5cqw] leading-[0.95] whitespace-nowrap text-bone">{s.status}</p>
        <p className="absolute top-[41%] left-[6.7%] w-[33%] text-[2.2cqw] leading-[1.28] text-smoke">{s.detail}</p>
        <div className="absolute top-[57.5%] left-[6.7%] grid gap-[0.6cqw]">
          <p className="telemetry text-[2.75cqw] leading-none whitespace-nowrap text-bone">
            <span className="text-smoke">http://</span>
            {s.url.replace(/^https?:\/\//, '')}
          </p>
          <p className="text-[1.95cqw] leading-none whitespace-nowrap text-smoke">
            {s.or} <span className="telemetry font-semibold text-bone">{s.ip}</span>
          </p>
        </div>

        {/* the setup code on a white-hot rim */}
        <div className="absolute top-[73.5%] left-[6.7%] grid w-[37.6%] gap-[0.5cqw] rounded-[0.7cqw] bg-ash px-[1.9cqw] pt-[1.3cqw] pb-[1.1cqw] ring-[0.13cqw] ring-h9">
          <span className="text-[1.5cqw] leading-none text-smoke">{s.codeLabel}</span>
          <span className="telemetry text-[5.2cqw] leading-none font-semibold tracking-[0.02em] text-bone">{s.code}</span>
        </div>

        {/* the QR card, bracketed like a spot meter */}
        <div className="absolute top-[25.4%] left-[64.3%] aspect-square w-[28.9%]">
          <span className="absolute -top-[5.5%] -left-[5.5%] size-[16%] border-t-[0.3cqw] border-l-[0.3cqw] border-h9" />
          <span className="absolute -top-[5.5%] -right-[5.5%] size-[16%] border-t-[0.3cqw] border-r-[0.3cqw] border-h9" />
          <span className="absolute -bottom-[5.5%] -left-[5.5%] size-[16%] border-b-[0.3cqw] border-l-[0.3cqw] border-h9" />
          <span className="absolute -right-[5.5%] -bottom-[5.5%] size-[16%] border-r-[0.3cqw] border-b-[0.3cqw] border-h9" />
          <div className="absolute inset-0 grid place-items-center rounded-[1.1cqw] bg-h9">
            <svg viewBox={`0 0 ${QR} ${QR}`} className="size-[78%]" shapeRendering="crispEdges" focusable="false">
              <path d={QR_PATH} fill="var(--color-ash)" />
            </svg>
          </div>
        </div>
        <p className="absolute top-[82%] left-[78.75%] flex -translate-x-1/2 items-center gap-[0.8cqw] rounded-full bg-h0/85 px-[1.3cqw] py-[0.55cqw] text-[1.55cqw] leading-none whitespace-nowrap text-bone">
          {/* the hostname's mark, which the TV draws before the caption */}
          <TvHandshake mark={tvInstaller.handshake} className="size-[2.1cqw]" />
          {s.qrLabel}
        </p>
      </div>
    </div>
  );
}
