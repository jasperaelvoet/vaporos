// Renders the Rich mini-markup of src/content (see src/content/types.ts):
//   `code`  **strong**  [[UI label]]  [label](href)  ++Key++
// Works in server and client components and on any ground: emphasis and
// links take --strong (bone on dark grounds, ash on white-hot). Swap any
// element's look with `components`, e.g.
//   <Rich text={t} components={{ code: (c) => <code className="…">{c}</code> }} />
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
  ui?: (children: ReactNode) => ReactNode;
  a?: (href: string, children: ReactNode) => ReactNode;
}

const defaults: Required<RichComponents> = {
  code: (c) => <code className="rounded-xs bg-char px-1 py-0.5 text-[0.88em] text-bone">{c}</code>,
  kbd: (c) => <kbd className="rounded-xs border border-line bg-soot px-1.5 text-[0.85em] text-bone">{c}</kbd>,
  strong: (c) => <strong className="font-semibold text-(--strong)">{c}</strong>,
  ui: (c) => (
    <strong data-ui className="font-semibold text-(--strong)">
      {c}
    </strong>
  ),
  a: (href, c) => (
    <SmartLink
      href={href}
      className="text-(--strong) underline decoration-[1.5px] underline-offset-[0.22em] hover:decoration-[2.5px]"
    >
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
      case 'ui':
        el = c.ui(n.text);
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
