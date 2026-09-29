// Questions and answers as native <details> (they open without JavaScript,
// and every answer is in the HTML), each with its id as the anchor:
// /faq/#rollback opens that one.
//
//   <FaqList items={faqByIds(faqTeaser.ids)} />     a short list (home page)
//   <FaqTopics groups={faqGroups} items={faq} />    the FAQ page, by topic
import { Disclosure, Rich } from '@/components/ui';
import type { FaqGroup, QA } from '@/content/faq';
import { OpenFromHash } from './open-from-hash';

function Answer({ item }: { item: QA }) {
  return (
    <div className="grid gap-3">
      {item.a.map((p, i) => (
        <p key={i}>
          <Rich text={p} />
        </p>
      ))}
    </div>
  );
}

export function FaqList({ items, className = '' }: { items: readonly QA[]; className?: string }) {
  return (
    <div className={className}>
      {items.map((item) => (
        <Disclosure key={item.id} id={item.id} summary={item.q}>
          <Answer item={item} />
        </Disclosure>
      ))}
      <OpenFromHash />
    </div>
  );
}

/**
 * The FAQ page: one section per topic, its title on the left (sticky from
 * 901 px) and its questions on the right.
 */
export function FaqTopics({ groups, items }: { groups: readonly FaqGroup[]; items: readonly QA[] }) {
  const byId = new Map(items.map((q) => [q.id, q]));
  return (
    <div className="grid gap-[clamp(3.5rem,7vw,6rem)]">
      {groups.map((g) => {
        const qs = g.ids.map((id) => byId.get(id)).filter((q): q is QA => !!q);
        return (
          <section
            key={g.id}
            id={g.id}
            aria-labelledby={`${g.id}-title`}
            className="grid scroll-mt-(--nav-h) gap-x-16 gap-y-6 desk:grid-cols-[minmax(0,4fr)_minmax(0,8fr)]"
          >
            <div className="desk:sticky desk:top-[calc(var(--nav-h)+1.5rem)] desk:self-start">
              <h2 id={`${g.id}-title`} className="cut-warm text-[clamp(2rem,3.4vw,2.875rem)] leading-[0.98] tracking-[-0.012em] text-bone">
                {g.title}
              </h2>
            </div>
            <div>
              {qs.map((item) => (
                <Disclosure key={item.id} id={item.id} summary={item.q}>
                  <Answer item={item} />
                </Disclosure>
              ))}
            </div>
          </section>
        );
      })}
      <OpenFromHash />
    </div>
  );
}
