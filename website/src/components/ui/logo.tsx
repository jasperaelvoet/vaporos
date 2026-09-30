// The VaporOS mark and wordmark, drawn from src/lib/logo.gen.ts (generated
// from design/logo.svg, the same drawings the TV and the app icons use).
//
//   <Logo />                 the lockup: compact mark + wordmark, on dark
//   <Logo ground="light" />  the same on white-hot (ash V in an orange band,
//                            "OS" in hot-ink-2)
//   <LogoMark size={34} />   the compact mark alone (all chrome, favicons)
//   <Wordmark />             "Vapor" in the hot cut in bone (currentColor),
//                            "OS" in the cold cut in smoke: the logo is a
//                            temperature contrast
//
// All of them are decorative (aria-hidden): the link or heading around them
// carries the name ("VaporOS home").
import type { CSSProperties } from 'react';
import { logo, type LogoDrawing } from '@/lib/logo.gen';

function Paths({ drawing, osClass }: { drawing: LogoDrawing; osClass?: string }) {
  return drawing.paths.map((p, i) => {
    const os = p.className === 'os' && osClass;
    return (
      <path
        key={i}
        d={p.d}
        fill={os ? undefined : (p.fill ?? 'none')}
        className={os ? osClass : undefined}
        stroke={p.stroke}
        strokeWidth={p.strokeWidth}
        strokeLinecap={p.stroke ? 'round' : undefined}
        strokeLinejoin={p.stroke ? 'round' : undefined}
      />
    );
  });
}

export type Ground = 'dark' | 'light';

export function LogoMark({ size = 32, ground = 'dark', className }: { size?: number; ground?: Ground; className?: string }) {
  const d = ground === 'light' ? logo.markLight : logo.mark;
  return (
    <svg width={size} height={size} viewBox={d.viewBox} aria-hidden="true" focusable="false" className={className}>
      <Paths drawing={d} />
    </svg>
  );
}

/** The wordmark at a given height (its width follows the 227:36 drawing). */
export function Wordmark({ height = 22, ground = 'dark', className = '' }: { height?: number; ground?: Ground; className?: string }) {
  const d = logo.wordmark;
  const [, , w, h] = d.viewBox.split(' ').map(Number);
  return (
    <svg
      width={Math.round((height * w) / h)}
      height={height}
      viewBox={d.viewBox}
      aria-hidden="true"
      focusable="false"
      className={`logo-word ${ground === 'light' ? 'text-ash' : 'text-bone'} ${className}`}
    >
      <Paths drawing={d} osClass={ground === 'light' ? 'fill-hot-ink-2' : 'fill-smoke'} />
    </svg>
  );
}

/**
 * The lockup: mark and wordmark side by side, as in the nav and the footer.
 * Its size is the mark's (the wordmark is 0.65 of it): `size` in px, or set
 * --logo from a class for responsive sizes (className="[--logo:30px] desk:[--logo:34px]").
 */
export function Logo({ size, ground = 'dark', className = '' }: { size?: number; ground?: Ground; className?: string }) {
  const d = ground === 'light' ? logo.markLight : logo.mark;
  const w = logo.wordmark;
  return (
    <span
      className={`inline-flex items-center gap-[calc(var(--logo,34px)*0.32)] ${className}`}
      style={size ? ({ '--logo': `${size}px` } as CSSProperties) : undefined}
    >
      <svg viewBox={d.viewBox} aria-hidden="true" focusable="false" className="size-[var(--logo,34px)] shrink-0">
        <Paths drawing={d} />
      </svg>
      <svg
        viewBox={w.viewBox}
        aria-hidden="true"
        focusable="false"
        className={`logo-word h-[calc(var(--logo,34px)*0.65)] w-auto ${ground === 'light' ? 'text-ash' : 'text-bone'}`}
      >
        <Paths drawing={w} osClass={ground === 'light' ? 'fill-hot-ink-2' : 'fill-smoke'} />
      </svg>
    </span>
  );
}
