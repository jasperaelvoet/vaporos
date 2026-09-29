'use client';
// Opens the <details> a link points at (/faq/#rollback), on load and
// whenever the hash changes, and brings it into view: the browser scrolls
// to the anchor before the answer is open, so the scroll happens again once
// it is. Renders nothing.
import { useEffect } from 'react';

export function OpenFromHash() {
  useEffect(() => {
    const open = () => {
      const id = decodeURIComponent(location.hash.slice(1));
      const el = id ? document.getElementById(id) : null;
      if (!(el instanceof HTMLDetailsElement)) return;
      el.open = true;
      requestAnimationFrame(() => el.scrollIntoView({ block: 'start' }));
    };
    open();
    window.addEventListener('hashchange', open);
    return () => window.removeEventListener('hashchange', open);
  }, []);
  return null;
}
