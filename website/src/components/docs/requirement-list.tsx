// The requirements as a spec sheet: one ruled row per requirement, what it
// is on the left and why on the right (a <dl>, so screen readers pair them).
// Not numbered and not cards: it is a checklist to scan, not a sequence.
import { Icon, Rich } from '@/components/ui';
import type { Requirement } from '@/content/requirements';

export function RequirementList({ items, className = '' }: { items: readonly Requirement[]; className?: string }) {
  return (
    <dl className={`border-t border-line ${className}`}>
      {items.map((r) => (
        <div
          key={r.id}
          id={`req-${r.id}`}
          className="grid gap-x-10 gap-y-1.5 border-b border-line py-5 desk:grid-cols-[minmax(0,5fr)_minmax(0,7fr)] desk:py-6"
        >
          <dt className="flex items-start gap-3.5 text-[1.125rem] leading-snug font-semibold text-bone">
            <Icon name={r.icon} className="mt-0.5 size-5 shrink-0 text-smoke" />
            <span>{r.title}</span>
          </dt>
          <dd className="max-w-read pl-[2.125rem] text-smoke desk:pl-0">
            <Rich text={r.body} />
          </dd>
        </div>
      ))}
    </dl>
  );
}
