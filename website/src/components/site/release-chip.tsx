'use client';
// The latest release's version next to the nav's Download button (from
// 901 px): baked in at build time, then upgraded live by useLatestRelease,
// never downgraded. It links to that release's notes on GitHub. With no
// known release it renders nothing, so the header never claims one.
import { useLatestRelease } from '@/lib/use-latest-release';
import type { ReleaseLookup } from '@/lib/release-shape';

export function ReleaseChip({ initial, className = '' }: { initial: ReleaseLookup; className?: string }) {
  const { lookup } = useLatestRelease(initial);
  if (lookup.state !== 'ready') return null;
  const r = lookup.release;
  return (
    <a
      href={r.url}
      className={`telemetry inline-flex min-h-11 items-center gap-2 rounded-md px-1 text-[0.75rem] text-smoke no-underline transition-colors hover:text-bone ${className}`}
      aria-label={`Release notes for VaporOS ${r.version}`}
    >
      <span aria-hidden className="size-1.5 rounded-[1px] bg-h7" />
      {r.tag || `v${r.version}`}
    </a>
  );
}
