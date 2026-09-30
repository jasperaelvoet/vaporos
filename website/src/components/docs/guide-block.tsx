// One block of the install guide (content/install.ts → GuideBlock). The
// renderer switches on block.type; everything a block needs is inside it.
import { CodeBlock, Icon, Notice, Rich } from '@/components/ui';
import { installGuide, type GuideBlock as Block } from '@/content/install';
import { HEAT } from '@/direction';
import { TvStill } from '@/components/story/tv-still';
import { InstallerScreen } from './installer-screen';
import { RequirementList } from './requirement-list';
import { StoreLinks } from './store-links';
import { WizardSteps } from './wizard-steps';

export function GuideBlock({ block }: { block: Block }) {
  switch (block.type) {
    case 'p':
      return (
        <p className={`max-w-read ${block.muted ? 'text-smoke' : 'text-bone/88'}`}>
          <Rich text={block.text} />
        </p>
      );
    case 'ul':
      return (
        <ul className="grid max-w-read gap-2.5 text-bone/88">
          {block.items.map((t, i) => (
            <li key={i} className="relative pl-6 before:absolute before:top-[0.72em] before:left-0.5 before:h-[2px] before:w-2.5 before:rounded-full before:bg-smoke">
              <Rich text={t} />
            </li>
          ))}
        </ul>
      );
    case 'ol':
      return (
        <ol className="grid max-w-read gap-3.5 text-bone/88">
          {block.items.map((t, i) => (
            <li key={i} className="grid grid-cols-[1.75rem_minmax(0,1fr)] gap-x-2">
              <span aria-hidden className="telemetry pt-[0.2em] text-meta font-semibold text-ink-ready">
                {i + 1}
              </span>
              <span>
                <Rich text={t} />
              </span>
            </li>
          ))}
        </ol>
      );
    case 'callout':
      return (
        <Notice tone={block.callout.tone === 'warn' ? 'warn' : 'info'} icon={block.callout.icon} className="max-w-[44rem]">
          <Rich text={block.callout.text} />
        </Notice>
      );
    case 'code':
      return (
        <div className="grid gap-3">
          {block.blocks.map((c) => (
            <CodeBlock key={c.title} {...c} />
          ))}
        </div>
      );
    case 'requirements':
      return <RequirementList items={block.items} />;
    case 'figure':
      return (
        <figure className="grid gap-3">
          <TvStill name="installer-ready" frame="guide" fallback={<InstallerScreen />} />
          <figcaption className="text-fine text-dim">{block.caption}</figcaption>
        </figure>
      );
    case 'wizard':
      return <WizardSteps steps={block.steps} of={installGuide.of} />;
    case 'stores':
      return <StoreLinks stores={block.stores} />;
    case 'done':
      // Where the guide ends: the PC is streaming, the hottest thing on the page.
      return (
        <div
          className="relative mt-6 grid gap-3 overflow-hidden rounded-2xl bg-soot px-6 pt-8 pb-7 inset-ring-1 inset-ring-line desk:px-9"
          data-heat={HEAT.streaming}
          data-heat-label="streaming"
        >
          <span aria-hidden className="heat-bar absolute inset-x-0 top-0 h-1" />
          <Icon name={block.icon} className="size-7 text-h7" />
          <p className="cut-hot text-[clamp(1.875rem,3.4vw,2.75rem)] leading-[0.98] tracking-[-0.012em] text-bone">{block.title}</p>
          <p className="text-smoke">
            <Rich text={block.text} />
          </p>
        </div>
      );
  }
}
