// B8: a few questions, answered in place (native <details>, so they open
// without JavaScript), and the way to all of them.
import { Disclosure } from '@/components/ui/disclosure';
import { Rich } from '@/components/ui/rich';
import { TextLink } from '@/components/ui/text-link';
import { faqByIds } from '@/content/faq';
import { faqTeaser } from '@/content/home';
import { Beat, BeatHead } from './beat';

export function FaqTeaser() {
  const items = faqByIds(faqTeaser.ids);
  return (
    <Beat head={faqTeaser} className="beat-faq">
      <div className="faq-cols">
        <BeatHead head={faqTeaser} cut="warm" friction={0} />
        <div>
          {items.map((q) => (
            <Disclosure key={q.id} summary={q.q}>
              {q.a.map((p, i) => (
                <p key={i} className={i ? 'mt-3' : undefined}>
                  <Rich text={p} />
                </p>
              ))}
            </Disclosure>
          ))}
          <p className="mt-8">
            <TextLink href={faqTeaser.more.href}>{faqTeaser.more.label}</TextLink>
          </p>
        </div>
      </div>
    </Beat>
  );
}
