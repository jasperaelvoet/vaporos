// Renders one block of the install guide (content/install.ts → GuideBlock).
import type { GuideBlock as Block } from '@/content/install';
import { CodeBlock } from './code-block';
import { Icon } from './icon';
import { Callout } from './plain';
import { RequirementList } from './requirement-list';
import { Rich } from './rich';
import { WelcomeScreen } from './welcome-screen';

export function GuideBlock({ block }: { block: Block }) {
  switch (block.type) {
    case 'p':
      return (
        <p className={block.muted ? 'text-fg-2' : undefined}>
          <Rich text={block.text} />
        </p>
      );
    case 'ul':
    case 'ol': {
      const L = block.type;
      return (
        <L className={`grid gap-2 pl-5 ${L === 'ul' ? 'list-disc' : 'list-decimal'} marker:text-brand`}>
          {block.items.map((t, i) => (
            <li key={i}>
              <Rich text={t} />
            </li>
          ))}
        </L>
      );
    }
    case 'callout':
      return <Callout callout={block.callout} />;
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
          <WelcomeScreen mode={block.mock} />
          <figcaption className="text-center text-sm text-fg-3">{block.caption}</figcaption>
        </figure>
      );
    case 'wizard':
      return (
        <ol className="grid gap-5">
          {block.steps.map((s, i) => (
            <li key={s.title} className="flex gap-4">
              <span className="grid size-9 shrink-0 place-items-center rounded-full border border-brand/40 bg-brand/10 font-bold text-brand">
                {i + 1}
              </span>
              <div>
                <h3 className="font-semibold">{s.title}</h3>
                <p className="text-fg-2">
                  <Rich text={s.body} />
                </p>
              </div>
            </li>
          ))}
        </ol>
      );
    case 'stores':
      return (
        <ul className="grid gap-3 sm:grid-cols-3">
          {block.stores.map((s) => (
            <li key={s.url}>
              <a href={s.url} className="flex h-full items-center gap-3 rounded-xl border border-line-2 p-4 hover:border-brand/50">
                <Icon name={s.icon} className="size-5 text-brand" />
                <span className="mr-auto grid">
                  <strong className="text-sm">{s.name}</strong>
                  <small className="text-fg-3">{s.where}</small>
                </span>
                <Icon name="external" className="size-4 text-fg-3" />
              </a>
            </li>
          ))}
        </ul>
      );
    case 'done':
      return (
        <div className="flex items-center gap-4 rounded-2xl border border-line-2 bg-surface p-6">
          <Icon name={block.icon} className="size-9 shrink-0 text-brand" />
          <div>
            <h3 className="text-lg font-semibold">{block.title}</h3>
            <p className="text-fg-2">
              <Rich text={block.text} />
            </p>
          </div>
        </div>
      );
  }
}
