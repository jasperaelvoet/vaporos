// /demo/ : the live demo on its own page (spec-website §4.5, §8.6). Wide
// screens: the title, then the phone frame beside the scenario panel.
// Phones, touch screens and short windows: the control center fills the
// screen under a bar, with the scenarios in a bottom sheet. `?open=<page>`
// opens that page of the control center (content/demo.ts DEMO_OPEN).
//
// Built only with the demo on: scripts/drop-demo.mjs removes it from the
// export otherwise, and nothing links here.
import { DemoStage } from '@/components/demo/demo-stage';
import { getDemo } from '@/components/demo/manifest';
import { demoPage, demoStage } from '@/content/demo';
import { pageMeta } from '@/content';
import { HEAT } from '@/direction';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata(pageMeta.demo);

export default function DemoPage() {
  const d = getDemo();
  if (!d) {
    return (
      <div className="wrap pt-[calc(var(--nav-h)+4rem)] pb-24" data-heat={HEAT.ready} data-heat-label="ready">
        <h1 className="cut-warm text-[clamp(3rem,7vw,5.75rem)] leading-[0.95] text-bone">{demoPage.headline.join(' ')}</h1>
        <p className="mt-6 max-w-read text-lead text-smoke">{demoStage.missing}</p>
      </div>
    );
  }
  return (
    <div data-heat={HEAT.ready} data-heat-label="ready">
      <DemoStage variant="page" hash={d.hash} />
      <noscript>
        <p className="wrap pb-16 text-fine text-smoke">{demoStage.noscript}</p>
      </noscript>
    </div>
  );
}
