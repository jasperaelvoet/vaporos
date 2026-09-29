import Link from 'next/link';
import { nav } from '@/content/site';
import { Icon } from './icon';
import { Logo } from './logo';
import { NavLinks } from './nav-links';

export function SiteHeader() {
  return (
    <header className="border-b border-line">
      <div className="mx-auto flex max-w-5xl flex-wrap items-center gap-x-6 gap-y-2 px-4 py-4 sm:px-6">
        <Link href="/" aria-label={nav.homeLabel} className="mr-auto">
          <Logo size={28} />
        </Link>
        <nav aria-label={nav.mainLabel}>
          <NavLinks
            items={[nav.home, ...nav.items]}
            className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-fg-2"
            linkClassName="hover:text-fg aria-[current=page]:text-fg aria-[current=page]:underline"
          />
        </nav>
        <a href={nav.github.href} className="inline-flex items-center gap-1.5 text-sm text-fg-2 hover:text-fg">
          <Icon name={nav.github.icon} className="size-4" />
          {nav.github.label}
        </a>
      </div>
    </header>
  );
}
