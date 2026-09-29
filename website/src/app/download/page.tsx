// /download/ : the card (build-time release, upgraded live), channels, requirements, verify.
import { DownloadCard } from '@/components/download-card';
import { ArrowLink, Callout, Section, SectionHead } from '@/components/plain';
import { RequirementList } from '@/components/requirement-list';
import { VerifySteps } from '@/components/verify-steps';
import { downloadPage, pageMeta, requirements, requirementsSection, verifySection } from '@/content';
import { getLatestRelease } from '@/lib/release';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata(pageMeta.download);

export default async function DownloadPage() {
  const release = await getLatestRelease();
  return (
    <>
      <Section>
        <SectionHead as="h1" eyebrow={downloadPage.eyebrow} title={downloadPage.title} lead={downloadPage.lead} />
        <DownloadCard initial={release} />
        <div className="mt-6 grid gap-5">
          <Callout callout={downloadPage.channels} />
          <p>
            <ArrowLink link={downloadPage.next} />
          </p>
        </div>
      </Section>

      <Section id={requirementsSection.id}>
        <SectionHead eyebrow={requirementsSection.eyebrow} title={requirementsSection.title} />
        <RequirementList items={requirements} />
      </Section>

      <Section id={verifySection.id}>
        <SectionHead eyebrow={verifySection.eyebrow} title={verifySection.title} lead={verifySection.lead} />
        <VerifySteps />
      </Section>
    </>
  );
}
