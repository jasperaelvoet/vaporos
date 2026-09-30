'use client';
// Lights items up as the page reaches them: each match of `selector` inside
// the parent element gets data-on once its top passes `at` of the viewport
// (default 70%), and keeps it. Under reduced motion or Pause motion all of
// them light at once. Without JavaScript nothing is lit, and the unlit look
// must read fine on its own. Renders an empty, hidden span in the parent.
//
//   <ol className="steps">… <LightUp selector=":scope > li" /></ol>
import { useEffect, useRef } from 'react';
import { motionAllowed } from '@/lib/motion/prefs';

export function LightUp({ selector, at = 0.7 }: { selector: string; at?: number }) {
  const ref = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    const parent = ref.current?.parentElement;
    if (!parent) return;
    const items = Array.from(parent.querySelectorAll<HTMLElement>(selector));
    if (!motionAllowed()) {
      items.forEach((el) => el.setAttribute('data-on', ''));
      return;
    }
    const io = new IntersectionObserver(
      (es) => {
        for (const e of es) {
          if (!e.isIntersecting) continue;
          e.target.setAttribute('data-on', '');
          io.unobserve(e.target);
        }
      },
      { rootMargin: `0px 0px -${Math.round((1 - at) * 100)}% 0px` },
    );
    items.forEach((el) => io.observe(el));
    return () => io.disconnect();
  }, [selector, at]);
  return <span ref={ref} hidden />;
}
