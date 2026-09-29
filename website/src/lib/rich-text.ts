// Parser for the Rich mini-markup of src/content (see src/content/types.ts):
//   `code`  **strong**  [label](href)  ++Key++
// Pure: no React, no Node APIs. src/components/rich.tsx renders the nodes.
import type { Rich } from '@/content/types';

export type RichNode =
  | { type: 'text'; text: string }
  | { type: 'code'; text: string }
  | { type: 'kbd'; text: string }
  | { type: 'strong'; children: RichNode[] }
  | { type: 'link'; href: string; children: RichNode[] };

const TOKEN = /`([^`]+)`|\*\*(.+?)\*\*|\[([^\]]+)\]\(([^)\s]+)\)|\+\+([^+\s][^+]*?)\+\+/g;

export function parseRich(text: Rich): RichNode[] {
  const out: RichNode[] = [];
  let last = 0;
  for (const m of text.matchAll(TOKEN)) {
    const at = m.index ?? 0;
    if (at > last) out.push({ type: 'text', text: text.slice(last, at) });
    if (m[1] !== undefined) out.push({ type: 'code', text: m[1] });
    else if (m[2] !== undefined) out.push({ type: 'strong', children: parseRich(m[2]) });
    else if (m[3] !== undefined) out.push({ type: 'link', href: m[4], children: parseRich(m[3]) });
    else if (m[5] !== undefined) out.push({ type: 'kbd', text: m[5] });
    last = at + m[0].length;
  }
  if (last < text.length) out.push({ type: 'text', text: text.slice(last) });
  return out;
}

/** The text without markup: for <title>, meta descriptions, aria-labels, alt text. */
export function plain(text: Rich): string {
  const walk = (nodes: RichNode[]): string =>
    nodes.map((n) => ('children' in n ? walk(n.children) : n.text)).join('');
  return walk(parseRich(text));
}
