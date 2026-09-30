// A welcome screen's hostname mark (content/story.ts TvHandshake), as the TV
// draws it before "Scan to open": the 5×5 cells on a tile of its ground, one
// cell of margin. Size it with a class; it is part of a decorative picture.
import type { TvHandshake as Mark } from '@/content/story';

export function TvHandshake({ mark, className = '' }: { mark: Mark; className?: string }) {
  const d = mark.rows.flatMap((row, y) => [...row].map((c, x) => (c === '#' ? `M${x + 1} ${y + 1}h1v1h-1z` : ''))).join('');
  return (
    <svg className={className} viewBox="0 0 7 7" shapeRendering="crispEdges" aria-hidden="true" focusable="false">
      <rect width={7} height={7} rx={1} style={{ fill: `var(--color-${mark.ground})` }} />
      <path d={d} style={{ fill: `var(--color-${mark.on})` }} />
    </svg>
  );
}
