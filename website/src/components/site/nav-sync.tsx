'use client';
// After a client-side navigation, ask the inline chrome script
// (src/lib/chrome-script.ts) to re-check the nav's scrim: the new page may
// start scrolled, or with a white-hot block under the nav.
import { usePathname } from 'next/navigation';
import { useEffect } from 'react';

export function NavSync() {
  const path = usePathname();
  useEffect(() => {
    window.__vosNav?.();
  }, [path]);
  return null;
}
