'use client';
// The latest release's version next to the nav's Download button (from
// 901 px) and in the footer: baked in at build time, then upgraded live by
// useLatestRelease, never downgraded. It links to that release's notes on
// GitHub. With no known release it renders nothing, so neither place ever
// claims one. `tone="nav"` matches the nav's links, which stay readable over
// a hero's heat before the scrim appears.
import { useLatestRelease } from '@/lib/use-latest-release';
import type { ReleaseLookup } from '@/lib/release-shape';

export function ReleaseChip({
  initial,
  tone = 'quiet',
  className = '',
}: {
  initial: ReleaseLookup;
  tone?: 'nav' | 'quiet';
  className?: string;
}) {
  const { lookup } = useLatestRelease(initial);
  if (lookup.state !== 'ready') return null;
  const r = lookup.release;
  // The name starts with the visible text (WCAG 2.5.3, label in name).
  const label = r.tag || `v${r.version}`;
  const color = tone === 'nav' ? 'text-bone/78 hover:text-bone' : 'text-smoke hover:text-bone';
  return (
    <a
      href={r.url}
      className={`telemetry inline-flex min-h-11 items-center gap-2 rounded-md text-[0.75rem] no-underline transition-colors ${color} ${className}`}
      aria-label={`${label} release notes`}
    >
      <span aria-hidden className="size-1.5 rounded-[1px] bg-h7" />
      {label}
    </a>
  );
}
