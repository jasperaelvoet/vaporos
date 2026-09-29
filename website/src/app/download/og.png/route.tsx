// The download page's share card, out/download/og.png: 1200×630, built once by next build
// (src/app/og.png/card.tsx draws it).
import { renderCard } from '@/app/og.png/card';

export const dynamic = 'force-static';

export function GET() {
  return renderCard('download');
}
