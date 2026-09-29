// One line of what the PC needs, under the download button: "Needs" and
// every requirement's spec-sheet label (content/requirements.ts), linking to
// the full list. The separators hang at the end of a line, never start one.
// Server-safe (no hooks), so a client card can take it as children. It sets
// itself in bone on dark grounds and in ash inside a .surface-hot block, so
// it reads on the download card in every state.
import { TextLink } from '@/components/ui';
import { downloadCard as copy } from '@/content/download';
import { requirements } from '@/content/requirements';

export function RequirementSummary({
  href = copy.ready.needsLink.href,
  className = '',
}: {
  /** Where "Requirements" goes: '#requirements' on the download page, the route elsewhere. */
  href?: string;
  className?: string;
}) {
  return (
    <div
      className={`flex flex-wrap items-baseline justify-between gap-x-8 gap-y-3 border-t-[1.5px] pt-5 border-line in-[.surface-hot]:border-ash/80 ${className}`}
    >
      <div className="flex min-w-0 flex-wrap items-baseline gap-y-1.5">
        <span
          aria-hidden
          className={`telemetry mr-3 rounded-sm px-2 py-1 text-[0.8125rem] leading-none font-semibold bg-char text-bone in-[.surface-hot]:bg-ash in-[.surface-hot]:text-h9`}
        >
          {copy.ready.needs}
        </span>
        <ul
          aria-label={copy.ready.needs}
          className={`telemetry flex flex-wrap items-baseline gap-y-1.5 text-[0.875rem] leading-normal font-semibold text-bone in-[.surface-hot]:text-ash`}
        >
          {requirements.map((r) => (
            <li
              key={r.id}
              className="whitespace-nowrap after:mx-[0.6em] after:content-['·'] last:after:content-none"
            >
              {r.label}
            </li>
          ))}
        </ul>
      </div>
      <TextLink href={href} className="whitespace-nowrap">
        {copy.ready.needsLink.label}
      </TextLink>
    </div>
  );
}
