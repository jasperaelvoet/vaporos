'use client';
// The one client part of <Headline>: once the fonts are in, it measures the
// headline it sits in at its hottest width and writes the fit (--fit-k,
// data-fitted; src/lib/type/fit.ts). It renders an empty, hidden span.
import { useEffect, useRef } from 'react';
import { fitHeadline, type Hottest } from '@/lib/type/fit';

export function HeadlineFitter({ hot, hotNarrow }: { hot: Hottest; hotNarrow: Hottest }) {
  const ref = useRef<HTMLSpanElement>(null);
  const { w, g } = hot;
  const { w: nw, g: ng } = hotNarrow;
  useEffect(() => {
    const el = ref.current?.closest<HTMLElement>('.headline');
    if (!el) return;
    const run = () => fitHeadline(el, { w, g }, { w: nw, g: ng });
    run();
    let alive = true;
    const fonts = document.fonts;
    fonts?.ready.then(() => alive && run());
    fonts?.addEventListener('loadingdone', run);
    return () => {
      alive = false;
      fonts?.removeEventListener('loadingdone', run);
    };
  }, [w, g, nw, ng]);
  return <span ref={ref} hidden />;
}
