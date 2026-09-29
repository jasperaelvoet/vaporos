// / : a plain rendering of every piece of home page content. A design replaces the markup.
import { DashboardMock } from '@/components/dashboard-mock';
import { DownloadCard } from '@/components/download-card';
import { FaqList } from '@/components/faq-list';
import { Icon } from '@/components/icon';
import { ArrowLink, Button, Container, IconTile, Section, SectionHead } from '@/components/plain';
import { Rich } from '@/components/rich';
import { WelcomeScreen } from '@/components/welcome-screen';
import {
  controlCenter,
  faqByIds,
  faqTeaser,
  features,
  featuresSection,
  getSection,
  hero,
  howItWorks,
  howItWorksSection,
  installStrip,
  pageMeta,
  setupSection,
} from '@/content';
import { getLatestRelease } from '@/lib/release';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata(pageMeta.home);

export default async function Home() {
  const release = await getLatestRelease();
  return (
    <>
      {/* Hero */}
      <section className="py-20">
        <Container className="grid gap-6">
          <p className="flex flex-wrap items-center gap-3 text-sm">
            <span className="rounded-full border border-brand/40 bg-brand/10 px-3 py-1 font-semibold text-brand">{hero.badge}</span>
            <span className="text-fg-2">{hero.kicker}</span>
          </p>
          <h1 className="max-w-3xl text-5xl font-bold tracking-tight sm:text-6xl">
            {hero.title.text} <span className="text-vapor">{hero.title.accent}</span>
          </h1>
          <p className="max-w-2xl text-lg text-fg-2">
            <Rich text={hero.lead} />
          </p>
          <div className="flex flex-wrap gap-3">
            {hero.ctas.map((c) => (
              <Button key={c.href} item={c} />
            ))}
          </div>
          <ul aria-hidden="true" className="flex flex-wrap gap-3 text-sm">
            {hero.chips.map((c) => (
              <li key={c.title} className="inline-flex items-center gap-2.5 rounded-xl border border-line-2 bg-surface px-3 py-2">
                {c.live ? <i className="size-2 rounded-full bg-ok" /> : c.icon && <Icon name={c.icon} className="size-4 text-brand" />}
                <span className="grid">
                  <strong>{c.title}</strong>
                  <small className="text-fg-3">{c.detail}</small>
                </span>
              </li>
            ))}
          </ul>
          <ul aria-label={hero.factsLabel} className="flex flex-wrap gap-x-6 gap-y-2 text-sm text-fg-2">
            {hero.facts.map((f) => (
              <li key={f.label} className="inline-flex items-center gap-2">
                <Icon name={f.icon} className="size-4 text-brand" />
                {f.label}
              </li>
            ))}
          </ul>
        </Container>
      </section>

      {/* What you get */}
      <Section>
        <SectionHead {...featuresSection} />
        <ul className="grid gap-4 sm:grid-cols-2">
          {features.map((f) => (
            <li key={f.id} className={`grid content-start gap-3 rounded-2xl border border-line bg-surface/60 p-6 ${f.wide ? 'sm:col-span-2 lg:col-span-1' : ''}`}>
              <div className="flex items-center gap-3">
                <IconTile name={f.icon} />
                <h3 className="text-lg font-semibold">{f.title}</h3>
              </div>
              <p className="text-fg-2">
                <Rich text={f.body} />
              </p>
              {f.devices && (
                <p aria-hidden="true" className="flex flex-wrap gap-2 text-sm text-fg-3">
                  {f.devices.map((d) => (
                    <span key={d.label} className="inline-flex items-center gap-1.5 rounded-lg border border-line px-2 py-1">
                      <Icon name={d.icon} className="size-4" />
                      {d.label}
                    </span>
                  ))}
                </p>
              )}
              {f.modes && (
                <p aria-hidden="true" className="flex flex-wrap gap-2 font-mono text-xs text-fg-3">
                  {f.modes.map((m) => (
                    <span key={m.size} className={`rounded-lg border px-2 py-1 ${m.active ? 'border-brand/50 text-fg' : 'border-line'}`}>
                      {m.size}
                      <b>{m.rate}</b>
                      {m.hdr && <i className="ml-1 not-italic text-brand">HDR</i>}
                    </span>
                  ))}
                </p>
              )}
            </li>
          ))}
        </ul>
      </Section>

      {/* How it works */}
      <Section id={howItWorksSection.id}>
        <SectionHead {...howItWorksSection} />
        <figure className="grid gap-4 md:grid-cols-[1fr_auto_1fr] md:items-center">
          <figcaption className="sr-only">{howItWorks.diagramCaption}</figcaption>
          <div className="grid gap-4 rounded-2xl border border-line-2 bg-surface p-6">
            <DiagramHead {...howItWorks.pc} />
            <ol className="grid gap-2">
              {howItWorks.pc.pipeline.map((n) => (
                <li key={n.title} className="flex items-center gap-3">
                  <Icon name={n.icon} className="size-5 text-brand" />
                  <span>
                    <strong>{n.title}</strong> <small className="text-fg-3">{n.detail}</small>
                  </span>
                </li>
              ))}
            </ol>
            <p className="flex gap-2 text-sm text-fg-2">
              <Icon name="monitor" className="mt-0.5 size-4 shrink-0" />
              {howItWorks.pc.monitorNote}
            </p>
          </div>
          <p aria-hidden="true" className="inline-flex items-center justify-center gap-2 text-sm text-fg-2">
            <Icon name={howItWorks.network.icon} className="size-4 text-brand" />
            {howItWorks.network.label}
          </p>
          <div className="grid gap-4 rounded-2xl border border-line-2 bg-surface p-6">
            <DiagramHead {...howItWorks.client} />
            <ul className="grid gap-2">
              {howItWorks.client.devices.map((n) => (
                <li key={n.title} className="flex items-center gap-3">
                  <Icon name={n.icon} className="size-5 text-brand" />
                  <span>
                    <strong>{n.title}</strong> <small className="text-fg-3">{n.detail}</small>
                  </span>
                </li>
              ))}
            </ul>
          </div>
        </figure>
        <ol className="mt-10 grid gap-6 md:grid-cols-3">
          {howItWorks.steps.map((s, i) => (
            <li key={s.title} className="grid content-start gap-2">
              <span className="font-mono text-sm text-brand">{String(i + 1).padStart(2, '0')}</span>
              <h3 className="text-lg font-semibold">{s.title}</h3>
              <p className="text-fg-2">
                <Rich text={s.body} />
              </p>
            </li>
          ))}
        </ol>
      </Section>

      {/* Setup */}
      <Section>
        <SectionHead {...setupSection} />
        <ol className="grid gap-4 md:grid-cols-3">
          {installStrip.steps.map((s, i) => (
            <li key={s.title} className="grid content-start gap-3 rounded-2xl border border-line bg-surface/60 p-6">
              <div className="flex items-center gap-3">
                <span className="font-mono text-brand">{i + 1}</span>
                <IconTile name={s.icon} />
              </div>
              <h3 className="text-lg font-semibold">{s.title}</h3>
              <p className="text-fg-2">
                <Rich text={s.body} />
              </p>
            </li>
          ))}
        </ol>
        <p className="mt-6">
          <ArrowLink link={installStrip.more} />
        </p>
      </Section>

      {/* Control center */}
      <Section>
        <SectionHead eyebrow={controlCenter.eyebrow} title={controlCenter.title} lead={controlCenter.lead} />
        <ul className="grid gap-3">
          {controlCenter.items.map((c) => (
            <li key={c.label} className="flex gap-3 text-fg-2">
              <Icon name={c.icon} className="mt-0.5 size-5 text-brand" />
              <span>
                <Rich text={c.text} />
              </span>
            </li>
          ))}
        </ul>
        <figure className="mt-8 grid gap-4 md:grid-cols-[2fr_1fr] md:items-start">
          <WelcomeScreen mode="os" />
          <DashboardMock />
          <figcaption className="text-sm text-fg-3 md:col-span-2">{controlCenter.mockNote}</figcaption>
        </figure>
      </Section>

      {/* Download */}
      <Section id={getSection.id}>
        <SectionHead {...getSection} />
        <DownloadCard initial={release} compact headingLevel="h3" />
      </Section>

      {/* Questions */}
      <Section>
        <SectionHead eyebrow={faqTeaser.eyebrow} title={faqTeaser.title} />
        <FaqList items={faqByIds(faqTeaser.ids)} />
        <p className="mt-6">
          <ArrowLink link={faqTeaser.more} />
        </p>
      </Section>
    </>
  );
}

function DiagramHead({ icon, title, detail }: { icon: Parameters<typeof IconTile>[0]['name']; title: string; detail: string }) {
  return (
    <header className="flex items-center gap-3">
      <IconTile name={icon} />
      <span className="grid">
        <strong>{title}</strong>
        <small className="text-fg-3">{detail}</small>
      </span>
    </header>
  );
}
