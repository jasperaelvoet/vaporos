// /download/ : get the right ISO in seconds, with a way to check it.
//
//   the head        "Get VaporOS." in the hot cut
//   the card        white-hot, the one hot thing on the page: the release
//                   (build time, upgraded live), its files, and what the PC needs
//   channels        where other builds live, and the way on to the guide
//   #requirements   the spec sheet
//   #verify         the two checks, verbatim, and the release key
import { PageHead, RequirementList, SectionTitle } from '@/components/docs';
import { VerifySteps } from '@/components/docs/verify-steps';
import { DownloadCard, RequirementSummary } from '@/components/release';
import { Notice, Rich, TextLink } from '@/components/ui';
import { anchors, downloadPage, pageMeta, requirements, requirementsSection, verifySection } from '@/content';
import { HEAT } from '@/direction';
import { pageMetadata } from '@/lib/metadata';
import { getLatestRelease } from '@/lib/release';

export const metadata = pageMetadata(pageMeta.download);

export default async function DownloadPage() {
  const release = await getLatestRelease();
  const ready = release.state === 'ready';
  return (
    <>
      <PageHead cut="hot" lines={downloadPage.headline} lead={downloadPage.lead} heat={HEAT.ready} heatLabel="ready" />

      <section
        aria-labelledby="release-title"
        className="wrap"
        data-heat={ready ? HEAT.whiteHot : HEAT.coldBoot}
        data-heat-label={ready ? 'white-hot' : 'cold boot'}
      >
        <DownloadCard initial={release} headingId="release-title">
          <RequirementSummary href={`#${anchors.requirements}`} />
        </DownloadCard>

        <div className="mt-6 grid items-start gap-x-12 gap-y-6 desk:grid-cols-[minmax(0,7fr)_minmax(0,5fr)]">
          <Notice icon={downloadPage.channels.icon}>
            <Rich text={downloadPage.channels.text} />
          </Notice>
          <nav aria-label={downloadPage.jumpLabel} className="grid gap-3 desk:pt-4">
            <p>
              <TextLink href={downloadPage.next.href} className="text-[1.0625rem]">
                {downloadPage.next.label}
              </TextLink>
            </p>
            <p className="flex flex-wrap gap-x-6 gap-y-2 text-fine">
              {downloadPage.jump.map((l) => (
                <TextLink key={l.href} href={l.href}>
                  {l.label}
                </TextLink>
              ))}
            </p>
          </nav>
        </div>
      </section>

      <section
        id={requirementsSection.id}
        aria-labelledby="requirements-title"
        className="wrap mt-[clamp(5rem,10vw,8.5rem)] scroll-mt-(--nav-h)"
        data-heat={HEAT.asleep}
        data-heat-label="asleep"
      >
        {/* The Astro site's anchor, kept for old links. */}
        <span id="req-title" aria-hidden />
        <div className="grid gap-x-16 gap-y-8 desk:grid-cols-[minmax(0,4fr)_minmax(0,8fr)]">
          <SectionTitle id="requirements-title" className="desk:sticky desk:top-[calc(var(--nav-h)+1.5rem)] desk:self-start">
            {requirementsSection.title.text}
          </SectionTitle>
          <RequirementList items={requirements} />
        </div>
      </section>

      <section
        id={verifySection.id}
        aria-labelledby="verify-title"
        className="wrap mt-[clamp(5rem,10vw,8.5rem)] mb-[clamp(5rem,10vw,8rem)] scroll-mt-(--nav-h)"
        data-heat={HEAT.coldBoot}
        data-heat-label="cold boot"
      >
        <div className="mb-10 grid items-end gap-x-16 gap-y-5 desk:mb-14 desk:grid-cols-[minmax(0,7fr)_minmax(0,5fr)]">
          <SectionTitle id="verify-title">{verifySection.title.text}</SectionTitle>
          <p className="max-w-read text-smoke">
            <Rich text={verifySection.lead} />
          </p>
        </div>
        <div className="max-w-[56rem]">
          <VerifySteps />
        </div>
      </section>
    </>
  );
}
