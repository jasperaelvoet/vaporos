// Renders the Rich mini-markup of src/content (see src/content/types.ts):
//   `code`  **strong**  [label](href)  ++Key++
// Works in server and client components. Swap any element's look with
// `components`, e.g. <Rich text={t} components={{ code: (c) => <code className="…">{c}</code> }} />.
// For plain text (metadata, aria-labels) use plain() from '@/lib/rich-text'.
import { Fragment, type ReactNode } from 'react';
import type { Rich as RichText } from '@/content/types';
import { parseRich, type RichNode } from '@/lib/rich-text';
import { SmartLink } from './smart-link';

export { plain } from '@/lib/rich-text';

export interface RichComponents {
  code?: (children: ReactNode) => ReactNode;
  kbd?: (children: ReactNode) => ReactNode;
  strong?: (children: ReactNode) => ReactNode;
  a?: (href: string, children: ReactNode) => ReactNode;
}

const defaults: Required<RichComponents> = {
  code: (c) => <code className="rounded bg-white/8 px-1 py-0.5 font-mono text-[0.88em]">{c}</code>,
  kbd: (c) => <kbd className="rounded border border-line-2 bg-white/5 px-1.5 font-mono text-[0.85em]">{c}</kbd>,
  strong: (c) => <strong className="font-semibold text-fg">{c}</strong>,
  a: (href, c) => (
    <SmartLink href={href} className="text-brand underline underline-offset-2 hover:text-fg">
      {c}
    </SmartLink>
  ),
};

function render(nodes: RichNode[], c: Required<RichComponents>): ReactNode[] {
  return nodes.map((n, i) => {
    let el: ReactNode;
    switch (n.type) {
      case 'text':
        el = n.text;
        break;
      case 'code':
        el = c.code(n.text);
        break;
      case 'kbd':
        el = c.kbd(n.text);
        break;
      case 'strong':
        el = c.strong(render(n.children, c));
        break;
      case 'link':
        el = c.a(n.href, render(n.children, c));
        break;
    }
    return <Fragment key={i}>{el}</Fragment>;
  });
}

/** Inline Rich text. Wrap it in your own <p>, <li> or <span>. */
export function Rich({ text, components }: { text: RichText; components?: RichComponents }) {
  return <>{render(parseRich(text), { ...defaults, ...components })}</>;
}
