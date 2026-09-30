// The footer: the lockup ("Vapor" hot in bone, "OS" cold in smoke), the
// blurb, the Product and Project links, the fine print, the Pause motion
// switch, the version this page describes, and the ember where the page's
// heat cycle ends (slot D7).
import Link from 'next/link';
import { FooterEmber } from '@/direction';
import { footer, nav } from '@/content/site';
import type { ReleaseLookup } from '@/lib/release-shape';
import { Logo } from '../ui/logo';
import { SmartLink } from '../ui/smart-link';
import { MotionToggle } from './motion-toggle';
import { ReleaseChip } from './release-chip';

export function SiteFooter({ release }: { release: ReleaseLookup }) {
  return (
    <footer className="relative mt-auto border-t border-line text-fine text-smoke">
      <div className="wrap grid gap-10 pt-14 pb-11 desk:grid-cols-[minmax(0,5fr)_repeat(2,minmax(0,2fr))] max-desk:grid-cols-2">
        <div className="grid content-start gap-4 max-desk:col-span-2">
          <Link href="/" aria-label={nav.homeLabel} className="inline-flex min-h-11 items-center self-start justify-self-start rounded-md">
            <Logo size={34} />
          </Link>
          <p className="max-w-[30em]">{footer.blurb}</p>
          <MotionToggle className="justify-self-start" />
        </div>
        {footer.groups.map((g) => (
          <nav key={g.title} aria-labelledby={`footer-${g.title.toLowerCase()}`}>
            <h2 id={`footer-${g.title.toLowerCase()}`} className="mb-3 text-[0.875rem] font-semibold text-bone">
              {g.title}
            </h2>
            <ul className="grid gap-1">
              {g.links.map((l) => (
                <li key={l.href}>
                  <SmartLink href={l.href} className="inline-flex min-h-8 items-center no-underline transition-colors hover:text-bone">
                    {l.label}
                  </SmartLink>
                </li>
              ))}
            </ul>
          </nav>
        ))}
        <div className="col-span-full flex flex-wrap items-baseline justify-between gap-x-8 gap-y-3 border-t border-line pt-7 text-[0.8125rem] text-dim">
          <p>
            {footer.disclaimer}
            {footer.license && ` ${footer.license}`}
          </p>
          <ReleaseChip initial={release} />
        </div>
      </div>
      <FooterEmber />
    </footer>
  );
}
