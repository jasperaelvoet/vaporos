import Link from 'next/link';
import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { isRoute, withBase } from '@/lib/base-path';

type Props = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> & { href: string; children: ReactNode };

/**
 * One link for every href in src/content:
 *   '/faq/', '/download/#verify'  → next/link (client navigation, base path added)
 *   '/release.pub'                → <a> with the base path (a file, not a route)
 *   '#how', 'https://…'           → plain <a>
 */
export function SmartLink({ href, children, ...rest }: Props) {
  if (isRoute(href)) {
    return (
      <Link href={href} {...rest}>
        {children}
      </Link>
    );
  }
  return (
    <a href={withBase(href)} {...rest}>
      {children}
    </a>
  );
}
