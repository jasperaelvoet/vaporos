'use client';
// Opens the <details> a link points at (/faq/#rollback), on load and
// whenever the hash changes, brings it into view (the browser scrolls to the
// anchor before the answer is open, so the scroll happens again once it is)
// and moves focus to its question, so keyboard and screen reader users start
// there. Renders nothing.
import { useEffect } from 'react';

export function OpenFromHash() {
  useEffect(() => {
    const open = () => {
      const id = decodeURIComponent(location.hash.slice(1));
      const el = id ? document.getElementById(id) : null;
      if (!(el instanceof HTMLDetailsElement)) return;
      el.open = true;
      requestAnimationFrame(() => {
        el.scrollIntoView({ block: 'start' });
        el.querySelector('summary')?.focus({ preventScroll: true });
      });
    };
    open();
    window.addEventListener('hashchange', open);
    return () => window.removeEventListener('hashchange', open);
  }, []);
  return null;
}
