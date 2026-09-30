'use client';
// A TV still's image, fetched when it comes within 900 px of the viewport,
// as the drawings' canvases paint (use-thermal-canvas.ts). The browser's own
// lazy loading starts up to 3000 px ahead on a connection it can't rate, which
// put the home page's two stills and the install guide's into the first
// view's image budget (scripts/budget.mjs). Until then the frame shows its
// dark ground. Without JavaScript the <noscript> copy loads, natively lazy.
//
// It fills its frame (a positioned 16:9 box) and is decorative: the frame
// carries the still's words (TvStill).
import { useEffect, useRef, useState } from 'react';

const FILL = 'absolute inset-0 size-full';

export function TvStillImage({ src, width, height }: { src: string; width: number; height: number }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [near, setNear] = useState(false);
  useEffect(() => {
    const box = ref.current;
    if (!box) return;
    const io = new IntersectionObserver(
      (entries) => {
        if (!entries.some((e) => e.isIntersecting)) return;
        io.disconnect();
        setNear(true);
      },
      { rootMargin: '900px 0px' },
    );
    io.observe(box);
    return () => io.disconnect();
  }, []);
  return (
    <span ref={ref} className={FILL}>
      {near && (
        // eslint-disable-next-line @next/next/no-img-element -- a static export: the still is already sized and compressed
        <img src={src} width={width} height={height} alt="" decoding="async" className={FILL} />
      )}
      <noscript>
        {/* eslint-disable-next-line @next/next/no-img-element -- as above */}
        <img src={src} width={width} height={height} alt="" loading="lazy" decoding="async" className={FILL} />
      </noscript>
    </span>
  );
}
