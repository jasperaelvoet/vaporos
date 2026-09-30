'use client';
// MotionRuntime (./runtime.tsx) as its own chunk, loaded after hydration: its
// code (Lenis's starter, friction heat, the scroll frame) renders nothing and
// starts from idle time anyway, so it has no business in the scripts the
// first paint waits on.
import dynamic from 'next/dynamic';

export const MotionRuntimeLazy = dynamic(() => import('./runtime').then((m) => m.MotionRuntime), { ssr: false });
