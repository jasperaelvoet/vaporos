// /faq/ : every question; /faq/#<id> opens one.
import { FaqList } from '@/components/faq-list';
import { Callout, Section, SectionHead } from '@/components/plain';
import { faq, faqPage, pageMeta } from '@/content';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata(pageMeta.faq);

export default function FaqPage() {
  return (
    <Section>
      <SectionHead as="h1" eyebrow={faqPage.eyebrow} title={faqPage.title} lead={faqPage.lead} />
      <FaqList items={faq} />
      <div className="mt-8">
        <Callout callout={{ icon: faqPage.ask.icon, text: faqPage.ask.text }} />
      </div>
    </Section>
  );
}
