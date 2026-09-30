// The 404 page: exported as out/404.html, which GitHub Pages serves for any
// missing path under /vaporos/. Links and assets are absolute
// (/vaporos/...), so it works at any depth.
//
// Slot D8, a cold, dead screen: the heading in the cold cut beside a screen
// whose last heat is sinking out of it. The way on is white-hot: Home first,
// then the pages people come for.
import type { Metadata } from 'next';
import { DeadScreen } from '@/components/docs';
import { Headline, LinkButton, Rich } from '@/components/ui';
import { notFound, pageMeta } from '@/content';
import { HEAT } from '@/direction';
import { pageMetadata } from '@/lib/metadata';

// Next adds <meta name="robots" content="noindex"> to this page by itself.
export const metadata: Metadata = { ...pageMetadata(pageMeta.notFound), robots: undefined };

export default function NotFound() {
  return (
    <div
      className="wrap grid min-h-[min(100dvh,60rem)] items-center gap-x-16 gap-y-10 pt-[calc(var(--nav-h)+clamp(2rem,6vw,5rem))] pb-[clamp(4rem,8vw,6rem)] desk:grid-cols-[minmax(0,6fr)_minmax(0,6fr)]"
      data-heat={HEAT.asleep}
      data-heat-label="no signal"
    >
      <div className="grid gap-6 desk:order-2">
        <Headline as="h1" cut="cold" size="page" lines={notFound.headline} fitK={4.235} />
        <p className="max-w-read text-lead text-smoke">
          <Rich text={notFound.lead} />
        </p>
        <ul className="mt-2 flex flex-wrap gap-3">
          {notFound.actions.map((a) => (
            <li key={a.href}>
              <LinkButton href={a.href} icon={a.icon} variant={a.primary ? 'hot' : 'ghost'}>
                {a.label}
              </LinkButton>
            </li>
          ))}
        </ul>
      </div>
      <DeadScreen code={notFound.code} line={notFound.screen} className="desk:order-1" />
    </div>
  );
}
