// A small telemetry tag in Martian Mono: a version, a mode, a file size.
//
//   <Chip>v20260929.172712</Chip>            soot, smoke text (on dark)
//   <Chip tone="hot">HDR</Chip>              white-hot, ash text
//   <Chip tone="ash">Needs</Chip>            ash, white-hot text (on white-hot)
//   <Chip tone="ready">Ready</Chip>          ink-ready text: "ready" as text
import type { HTMLAttributes, ReactNode } from 'react';

export type ChipTone = 'plain' | 'hot' | 'ash' | 'ready';

const tones: Record<ChipTone, string> = {
  plain: 'bg-soot text-smoke inset-ring-1 inset-ring-line',
  hot: 'bg-h9 text-ash',
  ash: 'bg-ash text-h9',
  ready: 'bg-soot text-ink-ready inset-ring-1 inset-ring-line',
};

export function Chip({
  tone = 'plain',
  className = '',
  children,
  ...rest
}: { tone?: ChipTone; children: ReactNode } & HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      className={`telemetry inline-flex items-center rounded-sm px-2 py-1 text-[0.75rem] leading-none font-semibold whitespace-nowrap ${tones[tone]} ${className}`}
      {...rest}
    >
      {children}
    </span>
  );
}
