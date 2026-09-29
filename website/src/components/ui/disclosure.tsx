// A question and its answer, open or closed: a native <details>, so it works
// without JavaScript and the answer is always in the HTML. Give it an `id`
// to deep-link it (/faq/#rollback opens it; see OpenFromHash on the FAQ).
import type { ReactNode } from 'react';

export function Disclosure({
  id,
  summary,
  open,
  className = '',
  children,
}: {
  id?: string;
  summary: ReactNode;
  open?: boolean;
  className?: string;
  children: ReactNode;
}) {
  return (
    <details id={id} open={open} className={`group scroll-mt-(--nav-h) border-t border-line last:border-b ${className}`}>
      <summary className="flex min-h-11 list-none items-start justify-between gap-5 py-5.5 text-[1.1875rem] leading-snug font-semibold text-bone [&::-webkit-details-marker]:hidden">
        <span>{summary}</span>
        <span
          aria-hidden
          className="mt-1.5 size-3 shrink-0 rotate-45 border-r-2 border-b-2 border-smoke transition-[rotate] duration-300 ease-out group-open:-rotate-[135deg]"
        />
      </summary>
      <div className="max-w-read pb-6 text-smoke">{children}</div>
    </details>
  );
}
