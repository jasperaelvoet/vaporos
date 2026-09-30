'use client';
// The footer's Pause motion switch (WCAG 2.2.2): stops the site's ambient
// motion (the hero's heat, auto-cycling scenes, smooth scrolling, friction
// heat) and keeps every final frame. Saved in this browser. When the system
// already asks for reduced motion, it says so instead of offering a switch.
import { setMotionPaused, useMotionPaused, useReducedMotion } from '@/lib/motion/prefs';

export function MotionToggle({ className = '' }: { className?: string }) {
  const paused = useMotionPaused();
  const reduced = useReducedMotion();
  if (reduced) {
    return <p className={`text-fine text-dim ${className}`}>Motion is off: your system asks for reduced motion.</p>;
  }
  return (
    <button
      type="button"
      aria-pressed={paused}
      onClick={() => setMotionPaused(!paused)}
      className={`motion-switch group inline-flex min-h-11 items-center gap-2.5 text-fine font-semibold text-smoke transition-colors hover:text-bone ${className}`}
    >
      <span
        aria-hidden
        className="motion-track relative h-5 w-9 rounded-pill bg-char inset-ring-1 inset-ring-line transition-colors group-aria-pressed:bg-soot"
      >
        <span className="motion-knob absolute top-1 left-1 size-3 rounded-pill bg-h7 transition-[translate,background-color] duration-300 group-aria-pressed:translate-x-4 group-aria-pressed:bg-dim" />
      </span>
      Pause motion
    </button>
  );
}
