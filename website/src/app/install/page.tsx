// /install/ : from USB stick to first game, section by section.
//
// The guide warms the PC up as it goes: each section sits on the heat scale
// where the PC is at that point (off while you prepare, a cold boot, the
// installer, ready, pairing, an update, asleep again), the contents show
// that heat per entry, and the guide ends on the streaming panel.
import { GuideBlock, GuideToc, PageHead, type TocItem } from '@/components/docs';
import { installGuide, pageMeta } from '@/content';
import { HEAT, heatCSS } from '@/direction';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata(pageMeta.install);

// Where the PC is, per section: [temperature, the heat scale's label].
// Width is temperature: a section's title takes the cut of its heat.
const SECTION_HEAT: Record<string, [number, string]> = {
  before: [HEAT.asleep, 'asleep'],
  flash: [HEAT.asleep, 'asleep'],
  firmware: [HEAT.coldBoot, 'cold boot'],
  boot: [HEAT.coldBoot, 'cold boot'],
  installer: [HEAT.installing, 'installing'],
  'first-run': [HEAT.ready, 'ready'],
  pair: [HEAT.pairing, 'pairing'],
  updates: [HEAT.updating, 'updating'],
  power: [HEAT.asleep, 'asleep'],
};

export default function InstallPage() {
  const toc: TocItem[] = installGuide.sections.map((s) => {
    const [t] = SECTION_HEAT[s.id] ?? [0, ''];
    return { id: s.id, num: s.num, label: s.title, heat: t > 0.1 ? heatCSS(t) : '' };
  });
  return (
    <>
      <PageHead cut="cold" lines={installGuide.headline} fitK={5.109} lead={installGuide.lead} heat={HEAT.asleep} heatLabel="asleep" />

      <div className="wrap grid gap-x-16 gap-y-8 pb-[clamp(5rem,10vw,8rem)] desk:grid-cols-[15.5rem_minmax(0,1fr)]">
        <GuideToc items={toc} label={installGuide.tocLabel} unit={installGuide.tocUnit} />

        <article className="grid min-w-0 gap-[clamp(4rem,8vw,6.5rem)]">
          {installGuide.sections.map((s) => {
            const [heat, label] = SECTION_HEAT[s.id] ?? [HEAT.ready, 'ready'];
            const cut = heat < HEAT.installing + 0.05 ? 'cut-cold' : heat < HEAT.streaming ? 'cut-warm' : 'cut-hot';
            return (
              <section
                key={s.id}
                id={s.id}
                aria-labelledby={`${s.id}-title`}
                className="grid min-w-0 content-start gap-6 border-t border-line pt-8"
                data-heat={heat}
                data-heat-label={label}
              >
                <header className="grid gap-3">
                  <h2 id={`${s.id}-title`} className="flex items-baseline gap-4">
                    <span className="telemetry text-meta font-semibold text-ink-ready">{s.num}</span>
                    <span className={`${cut} text-[clamp(2rem,3.6vw,2.875rem)] leading-[0.98] tracking-[-0.012em] text-bone`}>{s.title}</span>
                  </h2>
                  <p className="max-w-read pl-[calc(1.5rem+1ch)] text-lead text-smoke max-desk:pl-0">{s.summary}</p>
                </header>
                <div className="grid min-w-0 gap-6">
                  {s.blocks.map((b, i) => (
                    <GuideBlock key={i} block={b} />
                  ))}
                </div>
              </section>
            );
          })}
        </article>
      </div>
    </>
  );
}
