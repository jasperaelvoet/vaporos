// The home page's share card, out/og.png: 1200×630, built once by next build
// (src/app/og.png/card.tsx draws it).
import { renderCard } from './card';

export const dynamic = 'force-static';

export function GET() {
  return renderCard('home');
}
