// Shared shapes for the content modules in src/content/.
//
// FACTS: every sentence in src/content was fact-checked against the VaporOS
// code (it is ported word for word from the Astro site). Rephrase for rhythm
// if you like, but never add or change a claim. Where a `short` variant sits
// next to the full text, it says the same thing with fewer words; prefer it
// over inventing new copy.

import type { IconName } from '@/lib/icons';

export type { IconName };

/**
 * Inline text with a tiny markup, rendered by <Rich> (src/components/ui/rich.tsx)
 * and flattened to plain text by plain() (for metadata, aria labels, alt text):
 *
 *   `code`          → <code>
 *   **strong**      → <strong>
 *   [[UI label]]    → <strong data-ui>: a label in the control center, word for
 *                     word (tests check it against the control center's strings)
 *   [label](href)   → a link. '/…' is a site route or file (base path added),
 *                     anything else is external.
 *   ++Key++         → <kbd>
 *
 * Nothing else is special.
 */
export type Rich = string;

/**
 * A two-part heading. The Astro site drew `accent` in the brand gradient,
 * sometimes on its own line. Read whole: `${text} ${accent}`.
 */
export interface Headline {
  text: string;
  accent?: string;
}

export interface LinkItem {
  label: string;
  /** A site route ('/faq/'), a site file ('/release.pub'), a hash ('#how') or an external URL. */
  href: string;
  icon?: IconName;
}

export interface ButtonItem extends LinkItem {
  primary?: boolean;
}

/** A small titled thing: a feature card, a step, a device. */
export interface Item {
  title: string;
  body: Rich;
  /** Same facts, fewer words. */
  short?: Rich;
  icon?: IconName;
}

export interface SectionHead {
  /** In-page anchor, when the section is linked to. */
  id?: string;
  eyebrow: string;
  title: Headline;
  lead?: Rich;
}

export interface Code {
  /** The window title of the code block ('Linux', 'Checksum'). */
  title: string;
  /** 'sh' for commands, 'text' for data such as the key. */
  lang: 'sh' | 'text';
  /** Exact text: what the copy button copies. */
  code: string;
}

export interface Callout {
  icon: IconName;
  tone?: 'info' | 'warn';
  text: Rich;
}

/** A page's <title>/description/canonical path, used by src/lib/metadata.ts. */
export interface PageMeta {
  /** The route, without the base path. */
  path: string;
  /** Shown as `${title} · VaporOS`; null for the home page's own title. */
  title: string | null;
  description: string;
  noindex?: boolean;
}

/** `${text} ${accent}`: a Headline as one plain string. */
export function headlineText(h: Headline): string {
  return h.accent ? `${h.text} ${h.accent}` : h.text;
}
