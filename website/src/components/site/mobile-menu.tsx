'use client';
// The nav below 901 px: a <details> disclosure, so it opens without
// JavaScript too. With it, the menu closes on Escape (focus back on the
// button), on a click outside, when focus moves past it, and after a link is
// followed.
import { usePathname } from 'next/navigation';
import { useEffect, useRef, type ReactNode } from 'react';
import type { LinkItem } from '@/content/types';
import { Icon } from '../ui/icon';
import { NavLinks } from './nav-links';

export function MobileMenu({ items, label, navLabel, footer }: { items: readonly LinkItem[]; label: string; navLabel: string; footer?: ReactNode }) {
  const ref = useRef<HTMLDetailsElement>(null);
  const path = usePathname();

  const close = (focus = false) => {
    const d = ref.current;
    if (!d?.open) return;
    d.open = false;
    if (focus) d.querySelector('summary')?.focus();
  };

  useEffect(() => close(), [path]);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && d.open) {
        e.preventDefault();
        close(true);
      }
    };
    const onDown = (e: PointerEvent) => {
      if (d.open && !d.contains(e.target as Node)) close();
    };
    // Tabbing out of the open panel closes it, so focus never lands on a link
    // the panel covers (WCAG 2.4.11). relatedTarget is null when focus leaves
    // the page (another window), which keeps the menu as it was.
    const onFocusOut = (e: FocusEvent) => {
      const to = e.relatedTarget as Node | null;
      if (d.open && to && !d.contains(to)) close();
    };
    document.addEventListener('keydown', onKey);
    document.addEventListener('pointerdown', onDown);
    d.addEventListener('focusout', onFocusOut);
    return () => {
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('pointerdown', onDown);
      d.removeEventListener('focusout', onFocusOut);
    };
  }, []);

  return (
    <details ref={ref} className="site-menu">
      <summary
        aria-label={label}
        className="grid size-11 place-items-center rounded-lg text-bone inset-ring-[1.5px] inset-ring-bone/35 transition-shadow hover:inset-ring-bone"
      >
        <Icon name="menu" className="site-menu-icon-open size-5" />
        <Icon name="close" className="site-menu-icon-close size-5" />
      </summary>
      <nav aria-label={navLabel} className="site-menu-panel">
        <NavLinks
          items={items}
          onNavigate={() => close()}
          className="grid"
          linkClassName="flex min-h-14 items-center gap-3 border-b border-line text-[1.1875rem] font-semibold text-bone no-underline aria-[current=page]:text-h9"
        />
        {footer}
      </nav>
    </details>
  );
}
