// /faq/ : every question, by topic; /faq/#<id> opens one. A calm page: it
// sits at "ready" on the heat scale and nothing on it is hot.
import { FaqTopics, PageHead } from '@/components/docs';
import { Notice, Rich, TextLink } from '@/components/ui';
import { faq, faqGroups, faqPage, pageMeta } from '@/content';
import { HEAT } from '@/direction';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata(pageMeta.faq);

export default function FaqPage() {
  return (
    <>
      <PageHead cut="warm" lines={faqPage.headline} lead={faqPage.lead} heat={HEAT.ready} heatLabel="ready">
        <nav aria-label={faqPage.topicsLabel}>
          <ul className="flex flex-wrap gap-x-6 gap-y-2 text-fine">
            {faqGroups.map((g) => (
              <li key={g.id}>
                <TextLink href={`#${g.id}`}>{g.title}</TextLink>
              </li>
            ))}
          </ul>
        </nav>
      </PageHead>

      <div className="wrap pb-[clamp(5rem,10vw,8rem)]" data-heat={HEAT.ready} data-heat-label="ready">
        <FaqTopics groups={faqGroups} items={faq} />
        <Notice icon={faqPage.ask.icon} className="mt-[clamp(3.5rem,7vw,6rem)] desk:ml-[calc(33.333%+2.667rem)]">
          <Rich text={faqPage.ask.text} />
        </Notice>
      </div>
    </>
  );
}
