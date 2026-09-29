'use client';
// Page links that mark the current page (aria-current="page").
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import type { LinkItem } from '@/content/types';
import { isRoute } from '@/lib/base-path';
import { Icon } from '../ui/icon';

const trim = (p: string) => p.replace(/\/+$/, '') || '/';

/** usePathname() is the route without the base path ('/download/' or '/download'). */
export function useIsCurrent() {
  const path = trim(usePathname() ?? '/');
  return (href: string) => isRoute(href) && trim(href.split('#')[0]) === path;
}

export function NavLinks({
  items,
  className,
  linkClassName,
  onNavigate,
}: {
  items: readonly LinkItem[];
  className?: string;
  linkClassName?: string;
  onNavigate?: () => void;
}) {
  const isCurrent = useIsCurrent();
  return (
    <ul className={className}>
      {items.map((n) => (
        <li key={n.href}>
          {isRoute(n.href) ? (
            <Link href={n.href} aria-current={isCurrent(n.href) ? 'page' : undefined} className={linkClassName} onClick={onNavigate}>
              {n.icon && <Icon name={n.icon} className="size-4 shrink-0" />}
              {n.label}
            </Link>
          ) : (
            <a href={n.href} className={linkClassName} onClick={onNavigate}>
              {n.icon && <Icon name={n.icon} className="size-4 shrink-0" />}
              {n.label}
            </a>
          )}
        </li>
      ))}
    </ul>
  );
}
