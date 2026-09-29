// The fixed nav on every page. Transparent over a page's first frame; the
// scrim, and the hard ash bar over a white-hot block, come from the inline
// chrome script and src/styles/chrome.css. From 901 px: logo, page links,
// the release chip and Download (always the last, primary item). Below:
// logo, Download and the menu. Download is always one tap away.
import Link from 'next/link';
import { nav } from '@/content/site';
import type { ReleaseLookup } from '@/lib/release-shape';
import { LinkButton } from '../ui/button';
import { Logo } from '../ui/logo';
import { MobileMenu } from './mobile-menu';
import { NavLinks } from './nav-links';
import { NavSync } from './nav-sync';
import { ReleaseChip } from './release-chip';

export function SiteHeader({ release }: { release: ReleaseLookup }) {
  return (
    <header className="site-nav">
      <div className="site-nav-bar">
        <Link href="/" aria-label={nav.homeLabel} className="mr-auto inline-flex min-h-11 items-center rounded-md">
          <Logo className="[--logo:30px] desk:[--logo:34px]" />
        </Link>
        <nav aria-label={nav.mainLabel} className="max-desk:hidden">
          <NavLinks items={nav.items} className="site-nav-links flex items-center gap-7.5" />
        </nav>
        <ReleaseChip initial={release} className="max-desk:hidden" />
        <LinkButton href={nav.cta.href} size="sm" icon={nav.cta.icon}>
          {nav.cta.label}
        </LinkButton>
        <div className="desk:hidden">
          <MobileMenu
            items={nav.menu}
            label={nav.menuLabel}
            navLabel={nav.mobileLabel}
            footer={<ReleaseChip initial={release} className="mt-3" />}
          />
        </div>
      </div>
      <NavSync />
    </header>
  );
}
