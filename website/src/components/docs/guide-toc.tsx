'use client';
// The install guide's table of contents. The guide warms the PC up section
// by section, so each entry carries its section's temperature as a short
// bar on a dark track (a cold section shows no heat), and the section you
// are reading lights white-hot.
//
// From 901 px it is a sticky list beside the guide; on phones the same list
// sits in a <details> that starts closed. Only one of the two is ever
// displayed, so there is one "On this page" landmark at a time. Without
// JavaScript both work as plain anchor lists; the script only marks the
// current section (aria-current="location").
import { useEffect, useState, type CSSProperties } from 'react';

export interface TocItem {
  id: string;
  num: string;
  label: string;
  /** The section's CSS colour on the heat ramp (or '' for no heat). */
  heat: string;
}

function List({ items, active }: { items: readonly TocItem[]; active: string }) {
  return (
    <ol className="grid">
      {items.map((t) => {
        const on = t.id === active;
        return (
          <li key={t.id}>
            <a
              href={`#${t.id}`}
              aria-current={on ? 'location' : undefined}
              className={`group grid min-h-11 grid-cols-[0.1875rem_2rem_minmax(0,1fr)] items-center gap-x-3 py-1 text-[0.9375rem] leading-snug no-underline transition-colors ${
                on ? 'text-bone' : 'text-smoke hover:text-bone'
              }`}
            >
              <span aria-hidden className="relative h-full min-h-7 overflow-hidden rounded-full bg-line">
                <span
                  className={`absolute inset-x-0 bottom-0 h-full transition-[background-color] duration-(--vos-dur-heat) ${on ? 'bg-h9' : 'bg-(--toc-heat)'}`}
                  style={{ '--toc-heat': t.heat || 'transparent' } as CSSProperties}
                />
              </span>
              <span aria-hidden className={`telemetry text-meta ${on ? 'text-bone' : 'text-dim'}`}>
                {t.num}
              </span>
              <span className={on ? 'font-semibold' : undefined}>{t.label}</span>
            </a>
          </li>
        );
      })}
    </ol>
  );
}

export function GuideToc({ items, label, unit }: { items: readonly TocItem[]; label: string; unit: string }) {
  const [active, setActive] = useState('');

  useEffect(() => {
    const sections = items.map((t) => document.getElementById(t.id)).filter((el): el is HTMLElement => !!el);
    if (!sections.length) return;
    // The section crossing a line a third of the way down the viewport.
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) if (e.isIntersecting) setActive(e.target.id);
      },
      { rootMargin: '-32% 0px -67% 0px' },
    );
    sections.forEach((s) => io.observe(s));
    return () => io.disconnect();
  }, [items]);

  return (
    <>
      <nav aria-label={label} className="sticky top-[calc(var(--nav-h)+1.5rem)] hidden self-start desk:block">
        <p className="mb-3 text-fine font-semibold text-bone">{label}</p>
        <List items={items} active={active} />
      </nav>
      <details className="group rounded-xl bg-soot inset-ring-1 inset-ring-line desk:hidden">
        <summary className="flex min-h-13 cursor-pointer list-none items-center justify-between gap-4 px-4 font-semibold text-bone [&::-webkit-details-marker]:hidden">
          <span>
            {label} <span className="telemetry text-meta font-medium text-smoke">· {items.length} {unit}</span>
          </span>
          <span
            aria-hidden
            className="size-2.5 shrink-0 rotate-45 border-r-2 border-b-2 border-smoke transition-[rotate] duration-300 group-open:-rotate-[135deg]"
          />
        </summary>
        <nav aria-label={label} className="border-t border-line px-4 pt-2 pb-3">
          <List items={items} active={active} />
        </nav>
      </details>
    </>
  );
}
