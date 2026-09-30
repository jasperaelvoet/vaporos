// The head of a content page (download, install guide, FAQ): the H1 in its
// cut, and the lead beside it from 901 px (under it on phones). It clears the
// fixed nav itself and joins the page's heat scale with `heat`.
//
//   <PageHead cut="hot" lines={downloadPage.headline} lead={downloadPage.lead} heat={HEAT.whiteHot} heatLabel="white-hot" />
//
// Width is temperature: a page takes the cut of what it is about (the
// download is hot, the install starts from a cold boot, the FAQ is the calm
// ready state).
import type { ReactNode } from 'react';
import { Headline, Rich } from '@/components/ui';
import type { Rich as RichText } from '@/content/types';
import type { CutName } from '@/lib/type/cuts';

export function PageHead({
  lines,
  narrow,
  cut,
  fitK,
  lead,
  heat,
  heatLabel,
  className = '',
  children,
}: {
  lines: readonly string[];
  narrow?: readonly string[];
  cut: CutName;
  /** The measured fit of `lines` (the H1's data-fit-k), so the first paint is already fitted. */
  fitK?: number;
  lead?: RichText;
  heat?: number;
  heatLabel?: string;
  className?: string;
  /** Under the lead, in its column: a readout, jump links. */
  children?: ReactNode;
}) {
  return (
    <header
      className={`wrap grid items-end gap-x-16 gap-y-6 pt-[calc(var(--nav-h)+clamp(2.75rem,7vw,6.5rem))] pb-[clamp(2.25rem,4.5vw,4rem)] desk:grid-cols-[minmax(0,7fr)_minmax(0,5fr)] ${className}`}
      data-heat={heat}
      data-heat-label={heatLabel}
    >
      <Headline as="h1" cut={cut} size="page" lines={lines} narrow={narrow} fitK={fitK} />
      {(lead || children) && (
        <div className="grid gap-5 desk:pb-2">
          {lead && (
            <p className="max-w-read text-lead text-smoke">
              <Rich text={lead} />
            </p>
          )}
          {children}
        </div>
      )}
    </header>
  );
}

/**
 * A content page's section heading: the cold cut for the calm reference
 * sections (a spec sheet, the checks), sized for reading rather than for a
 * story beat.
 */
export function SectionTitle({
  id,
  cut = 'cold',
  as: Tag = 'h2',
  className = '',
  children,
}: {
  id?: string;
  cut?: CutName;
  as?: 'h2' | 'h3';
  className?: string;
  children: ReactNode;
}) {
  const cutClass = cut === 'hot' ? 'cut-hot' : cut === 'warm' ? 'cut-warm' : 'cut-cold';
  return (
    <Tag id={id} className={`${cutClass} text-[clamp(2.375rem,4.4vw,3.75rem)] leading-[0.95] tracking-[-0.012em] text-bone ${className}`}>
      {children}
    </Tag>
  );
}
