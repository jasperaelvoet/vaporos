import type { QA } from '@/content/faq';
import { OpenFromHash } from './open-from-hash';
import { Rich } from './rich';

/** FAQ entries as <details>, each with its id as the anchor (/faq/#rollback opens it). */
export function FaqList({ items }: { items: QA[] }) {
  return (
    <div className="divide-y divide-line border-y border-line">
      {items.map((item) => (
        <details key={item.id} id={item.id} className="group scroll-mt-24 py-4">
          <summary className="flex cursor-pointer list-none items-center justify-between gap-4 [&::-webkit-details-marker]:hidden">
            <h3 className="text-lg font-semibold">{item.q}</h3>
            <span aria-hidden="true" className="text-xl text-brand transition-transform group-open:rotate-45">
              +
            </span>
          </summary>
          <div className="mt-3 grid gap-3 text-fg-2">
            {item.a.map((p, i) => (
              <p key={i}>
                <Rich text={p} />
              </p>
            ))}
          </div>
        </details>
      ))}
      <OpenFromHash />
    </div>
  );
}
