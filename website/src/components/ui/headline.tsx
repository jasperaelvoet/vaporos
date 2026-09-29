// A headline set at its temperature: width is temperature.
//
//   <Headline as="h2" cut="hot" size="section" friction={12}
//             lines={['Every screen gets', 'its own picture.']} />
//
//   <Headline as="h1" id="hero-title" cut="warm" size="hero" stretch={24} stretchNarrow={8} stretchG={100} friction={8}
//             lines={['Leave the heat', 'in the other room.']}
//             narrow={['Leave', 'the heat in', 'the other room.']} fitK={6.93} fitKNarrow={5.62} />
//
// - `cut` picks the Anybody cut (cold, warm, hot); a scroll scene may stretch
//   the headline further by writing --stretch/--stretch-g (percent points and
//   weight; see src/lib/type/scroll-stretch.ts or a ScrollTrigger scrub), and
//   friction heat adds up to `friction` points on desktop.
// - Every line is kept on one line (`lines`, and `narrow` below 901 px), and
//   the headline is fitted at the hottest width it can reach (the cut plus
//   the larger of stretch and friction, where type.css caps it), so it never
//   reflows while it stretches.
// - `fitK`/`fitKNarrow`: the measured fit (the element's data-fit-k after it
//   runs once). With them the server HTML already has the right size, so
//   the page doesn't shift when the script measures; without them long
//   lines wrap until the script has measured.
// - With no `narrow` set, the lines run together and wrap on phones at the
//   preset's phone size.
// The whole text is one accessible string; the visual lines are aria-hidden
// when there are two sets.
import { Fragment, type CSSProperties, type ReactNode } from 'react';
import { CUTS, CUT_CLASS, type CutName } from '@/lib/type/cuts';
import { hottestOf } from '@/lib/type/fit';
import { HeadlineFitter } from './headline-fitter';

export type HeadlineSize = 'hero' | 'section' | 'block' | 'page';

interface Preset {
  /** The size at rest; a fit only ever shrinks it. */
  size: string;
  /** The size below 901 px: one size, or one per cut. */
  narrow: string | Record<CutName, string>;
  leading: number;
  tracking: string;
}

// From REDLINE's scale: H1 up to 112 px (phones 68), section heads 44-92 px
// (phones by cut: cold 58, warm 50, hot 40), the download block 48-132 px
// (phones 46), content-page titles 44-80 px. A fitted line set shrinks below
// these only as far as its hottest width needs.
const PRESETS: Record<HeadlineSize, Preset> = {
  hero: { size: 'clamp(3.5rem, 7.8vw, 7rem)', narrow: '4.25rem', leading: 0.9, tracking: '-0.022em' },
  section: {
    size: 'clamp(2.75rem, 6.6vw, 5.75rem)',
    narrow: { cold: '3.625rem', warm: '3.125rem', hot: '2.5rem' },
    leading: 0.92,
    tracking: '-0.018em',
  },
  block: { size: 'clamp(3rem, 8.6vw, 8.25rem)', narrow: '2.875rem', leading: 0.92, tracking: '-0.018em' },
  page: {
    size: 'clamp(2.75rem, 6vw, 5rem)',
    narrow: { cold: '3.25rem', warm: '2.75rem', hot: '2.375rem' },
    leading: 0.94,
    tracking: '-0.018em',
  },
};

type Tag = 'h1' | 'h2' | 'h3' | 'p';

export interface HeadlineProps {
  as?: Tag;
  id?: string;
  cut: CutName;
  /** The lines from 901 px up, each kept on one line. */
  lines: readonly string[];
  /** The lines below 901 px; omit to let `lines` run together and wrap there. */
  narrow?: readonly string[];
  size?: HeadlineSize;
  /** Width a scroll scene adds at most, in percent points (warm 100 → hot 124 is 24). */
  stretch?: number;
  /** The same below 901 px, where a phone's short lines have less room (default: `stretch`). */
  stretchNarrow?: number;
  /** Weight a scroll scene adds at most. */
  stretchG?: number;
  /** Width full friction heat adds, in percent points (8 on the H1, 12 on section heads). Desktop only. */
  friction?: number;
  /** The measured fit of `lines` (data-fit-k), so the first paint is already fitted. */
  fitK?: number;
  /** The measured fit of `narrow` (data-fit-k-narrow). */
  fitKNarrow?: number;
  className?: string;
  /** Extra content after the lines (rare: a visually hidden note). */
  children?: ReactNode;
}

function Lines({ lines }: { lines: readonly string[] }) {
  return lines.map((l, i) => (
    <Fragment key={i}>
      {i > 0 && ' '}
      <span className="ln">{l}</span>
    </Fragment>
  ));
}

export function Headline({
  as: Tag = 'h2',
  id,
  cut,
  lines,
  narrow,
  size = 'section',
  stretch = 0,
  stretchNarrow,
  stretchG = 0,
  friction = 0,
  fitK,
  fitKNarrow,
  className = '',
  children,
}: HeadlineProps) {
  const p = PRESETS[size];
  // Phones never get friction heat, so the narrow set is fitted without it.
  const hot = hottestOf(cut, stretch, stretchG, friction);
  const hotNarrow = hottestOf(cut, stretchNarrow ?? stretch, stretchG, 0);
  const narrowSize = typeof p.narrow === 'string' ? p.narrow : p.narrow[cut];
  const style = {
    '--headline-size': p.size,
    '--headline-size-narrow': narrowSize,
    '--headline-leading': p.leading,
    '--headline-tracking': p.tracking,
    ...(friction ? { '--friction-gain': `${friction}%` } : {}),
    // The hottest width, where type.css caps stretch plus friction; phones get no friction.
    ...(stretch || friction ? { '--stretch-max-wide': `${hot.w}%` } : {}),
    ...(stretch || friction ? { '--stretch-max-narrow': `${hotNarrow.w}%` } : {}),
    ...(fitK ? { '--fit-k': fitK } : {}),
    ...(fitKNarrow ? { '--fit-k-narrow': fitKNarrow } : {}),
  } as CSSProperties;
  const text = lines.join(' ');
  const two = !!narrow?.length;
  return (
    <Tag
      id={id}
      className={`headline ${CUT_CLASS[cut]} ${className}`}
      style={style}
      data-friction={friction ? '' : undefined}
      data-narrow={two ? '' : undefined}
      data-fitted={fitK ? '' : undefined}
      data-fitted-narrow={fitKNarrow ? '' : undefined}
      data-cut={cut}
    >
      {two && <span className="sr-only">{text}</span>}
      <span className="headline-fit headline-wide" aria-hidden={two || undefined}>
        <Lines lines={lines} />
      </span>
      {two && (
        <span className="headline-fit headline-narrow" aria-hidden>
          <Lines lines={narrow} />
        </span>
      )}
      {children}
      <HeadlineFitter hot={hot} hotNarrow={hotNarrow} />
    </Tag>
  );
}

/** The cut's numbers, for scenes that animate between cuts by hand. */
export { CUTS };
