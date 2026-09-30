// A beat of the home page's story: a section set at its temperature. The
// page's heat scale reads data-heat and data-heat-label as the beat crosses
// the middle of the screen (direction/redline/heat-scale.tsx).
//
//   <Beat head={screenBeat} cut="hot">…</Beat>
//
// BeatHead sets the title in the beat's cut (width is temperature), fitted
// at its hottest width so friction heat never reflows it, with the lead
// beside it (split, from 901 px) or under it.
import type { ReactNode } from 'react';
import { Headline } from '@/components/ui/headline';
import { Rich } from '@/components/ui/rich';
import type { BeatHead as BeatHeadData } from '@/content/home';
import type { CutName } from '@/lib/type/cuts';
import './story.css';

export function Beat({
  head,
  className = '',
  children,
}: {
  head: Pick<BeatHeadData, 'id' | 'heat' | 'heatLabel'>;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section
      id={head.id}
      className={`beat ${className}`}
      data-heat={head.heat}
      data-heat-label={head.heatLabel}
      aria-labelledby={`${head.id}-title`}
    >
      <div className="wrap">{children}</div>
    </section>
  );
}

export function BeatHead({
  head,
  cut,
  split = false,
  friction = 12,
  size = 'section',
  children,
}: {
  head: BeatHeadData;
  cut: CutName;
  /** Title left, lead bottom right (from 901 px). */
  split?: boolean;
  friction?: number;
  size?: 'section' | 'block';
  /** Extra content under the lead. */
  children?: ReactNode;
}) {
  return (
    <div className={`beat-head ${split ? 'beat-head-split' : ''}`}>
      <Headline
        as="h2"
        id={`${head.id}-title`}
        cut={cut}
        size={size}
        friction={friction}
        lines={head.title.lines}
        narrow={head.title.narrow}
        fitK={head.title.fitK}
        fitKNarrow={head.title.fitKNarrow}
      />
      {(head.lead || children) && (
        <div className="beat-lead">
          {head.lead && (
            <p>
              <Rich text={head.lead} />
            </p>
          )}
          {children}
        </div>
      )}
    </div>
  );
}
