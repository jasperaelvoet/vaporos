// The screen on the PC as the PC really draws it: a still rendered by the
// welcome screen's own code (src/lib/stills.ts), in the frame of the drawing
// it replaces. A build without stills (a local one, `next dev`) shows
// `fallback`, the drawing, instead.
//
//   frame="story"  the home page's small TV (.tv in story.css: the bezel, and
//                  the rim the install beat lights)
//   frame="guide"  the install guide's screen, framed like InstallerScreen
//
// The frame carries the screen's words (role="img", the still's alt text) and
// the image inside it is decorative, so the words are there before the image
// is fetched (TvStillImage fetches it on approach).
import { getStill, type StillName } from '@/lib/stills';
import type { ReactNode } from 'react';
import { TvStillImage } from './tv-still-image';

export function TvStill({
  name,
  fallback,
  frame = 'story',
  className = '',
}: {
  name: StillName;
  fallback: ReactNode;
  frame?: 'story' | 'guide';
  className?: string;
}) {
  const still = getStill(name);
  if (!still) return fallback;
  const image = <TvStillImage src={still.src} width={still.width} height={still.height} />;
  if (frame === 'story') {
    return (
      <div role="img" aria-label={still.alt} className={`tv ${className}`} data-still={name}>
        {image}
      </div>
    );
  }
  return (
    <div
      role="img"
      aria-label={still.alt}
      className={`rounded-[1.125rem] bg-char p-[clamp(5px,0.9vw,10px)] inset-ring-1 inset-ring-line ${className}`}
      data-still={name}
    >
      <div className="relative aspect-video overflow-hidden rounded-[0.625rem] bg-h0">{image}</div>
    </div>
  );
}
