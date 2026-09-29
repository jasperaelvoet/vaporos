'use client';
// The page's thermal scale on the right edge (from 901 px): the Inferno ramp
// with a white-hot needle that follows the section in view. A section joins
// with data-heat (0..1) and data-heat-label ("streaming", "cold boot"…):
//
//   <section data-heat={HEAT.streaming} data-heat-label="streaming">…</section>
//
// An IntersectionObserver picks the section across the middle of the
// viewport, so it needs no GSAP and works under reduced motion (the needle
// then jumps instead of swinging on the tachometer spring). On pages with
// no such section the scale hides. Decorative: aria-hidden. It writes the
// DOM directly: the needle moves without re-rendering anything.
import { usePathname } from 'next/navigation';
import { useEffect, useRef, type CSSProperties } from 'react';
import { HEAT } from './heat';

const TICKS = [HEAT.asleep, HEAT.ready, HEAT.streaming];

export function HeatScale() {
  const path = usePathname();
  const root = useRef<HTMLElement>(null);
  const needle = useRef<HTMLSpanElement>(null);
  const label = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    const aside = root.current;
    if (!aside || !needle.current || !label.current) return;
    const sections = Array.from(document.querySelectorAll<HTMLElement>('main [data-heat]'));
    aside.hidden = sections.length === 0;
    if (!sections.length) return;
    let current: Element | null = null;
    const read = (el: HTMLElement) => {
      const t = Number.parseFloat(el.dataset.heat ?? '');
      if (!Number.isFinite(t)) return;
      current = el;
      needle.current?.style.setProperty('--heat', String(Math.max(0, Math.min(1, t))));
      if (label.current) label.current.textContent = el.dataset.heatLabel ?? '';
    };
    read(sections[0]);
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) if (e.isIntersecting && e.target !== current) read(e.target as HTMLElement);
      },
      { rootMargin: '-45% 0px -54% 0px' },
    );
    sections.forEach((s) => io.observe(s));
    return () => io.disconnect();
  }, [path]);

  return (
    <aside ref={root} className="heat-scale" aria-hidden hidden>
      <span className="heat-scale-bar" />
      {TICKS.map((t) => (
        <span key={t} className="heat-scale-tick" style={{ '--t': t } as CSSProperties} />
      ))}
      <span ref={needle} className="heat-scale-needle" style={{ '--heat': HEAT.ready } as CSSProperties}>
        <span ref={label} className="heat-scale-label">
          ready
        </span>
      </span>
    </aside>
  );
}
