'use client';
// Page links that mark the current page (aria-current="page").
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import type { LinkItem } from '@/content/types';

const trim = (p: string) => p.replace(/\/+$/, '') || '/';

/** usePathname() is the route without the base path ('/download/' or '/download'). */
export function useIsCurrent() {
  const path = trim(usePathname() ?? '/');
  return (href: string) => trim(href.split('#')[0]) === path;
}

export function NavLinks({ items, className, linkClassName }: { items: LinkItem[]; className?: string; linkClassName?: string }) {
  const isCurrent = useIsCurrent();
  return (
    <ul className={className}>
      {items.map((n) => (
        <li key={n.href}>
          <Link href={n.href} aria-current={isCurrent(n.href) ? 'page' : undefined} className={linkClassName}>
            {n.label}
          </Link>
        </li>
      ))}
    </ul>
  );
}
