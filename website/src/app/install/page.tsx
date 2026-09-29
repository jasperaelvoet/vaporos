// /install/ : the full guide, section by section, block by block.
import { GuideBlock } from '@/components/guide-block';
import { Container, SectionHead } from '@/components/plain';
import { installGuide, installToc, pageMeta } from '@/content';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata(pageMeta.install);

export default function InstallPage() {
  return (
    <Container className="py-14">
      <SectionHead as="h1" eyebrow={installGuide.eyebrow} title={installGuide.title} lead={installGuide.lead} />
      <div className="grid gap-12 lg:grid-cols-[200px_minmax(0,1fr)]">
        <nav aria-label={installGuide.tocLabel} className="lg:sticky lg:top-6 lg:self-start">
          <p className="mb-2 text-xs font-semibold tracking-widest text-fg-3 uppercase">{installGuide.tocLabel}</p>
          <ol className="grid gap-1 text-sm">
            {installToc.map((t) => (
              <li key={t.id}>
                <a href={`#${t.id}`} className="flex gap-2 text-fg-2 hover:text-fg">
                  <span className="font-mono text-fg-3">{t.num}</span>
                  {t.label}
                </a>
              </li>
            ))}
          </ol>
        </nav>
        <article className="grid max-w-3xl min-w-0 gap-16">
          {installGuide.sections.map((s) => (
            <section key={s.id} id={s.id} className="grid min-w-0 scroll-mt-8 gap-5">
              <h2 className="flex items-baseline gap-3 text-2xl font-bold">
                <span className="font-mono text-sm text-brand">{s.num}</span>
                {s.title}
              </h2>
              {s.blocks.map((b, i) => (
                <GuideBlock key={i} block={b} />
              ))}
            </section>
          ))}
        </article>
      </div>
    </Container>
  );
}
