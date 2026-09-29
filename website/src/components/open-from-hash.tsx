'use client';
// Opens the <details> a link points at (for example /faq/#rollback), on load
// and whenever the hash changes.
import { useEffect } from 'react';

export function OpenFromHash() {
  useEffect(() => {
    const open = () => {
      const id = decodeURIComponent(location.hash.slice(1));
      const el = id && document.getElementById(id);
      if (el instanceof HTMLDetailsElement) el.open = true;
    };
    open();
    window.addEventListener('hashchange', open);
    return () => window.removeEventListener('hashchange', open);
  }, []);
  return null;
}
