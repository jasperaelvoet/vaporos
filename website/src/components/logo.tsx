import { useId } from 'react';
import { site } from '@/content/site';

/**
 * The VaporOS mark (public/icon.svg, from internal/web/static/icon.svg):
 * a rounded tile with three rising steam waves. Inline, so it can be styled
 * and animated; each instance gets its own gradient id.
 */
export function LogoMark({ size = 32, className }: { size?: number; className?: string }) {
  const id = `vapor-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      aria-hidden="true"
      focusable="false"
      className={className}
    >
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#8ccaff" />
          <stop offset=".55" stopColor="#529ce9" />
          <stop offset="1" stopColor="#2a5ea6" />
        </linearGradient>
      </defs>
      <rect x="2" y="2" width="60" height="60" rx="17" fill={`url(#${id})`} />
      <g fill="none" stroke="#fff" strokeLinecap="round" strokeWidth="5.5">
        <path d="M20 47c-5-4 5-8.5 0-12.5s5-8.5 0-12.5" opacity=".78" />
        <path d="M32 52c-6-5 6-11.5 0-17s6-11.5 0-17" />
        <path d="M44 47c-5-4 5-8.5 0-12.5s5-8.5 0-12.5" opacity=".78" />
      </g>
    </svg>
  );
}

/** Vapor**OS**: "OS" in the accent color, as the product draws it. */
export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={className}>
      {site.wordmark.text}
      <b className="font-[inherit] text-brand">{site.wordmark.accent}</b>
    </span>
  );
}

export function Logo({ size = 32, className }: { size?: number; className?: string }) {
  return (
    <span className={`inline-flex items-center gap-2.5 font-semibold tracking-tight ${className ?? ''}`}>
      <LogoMark size={size} />
      <Wordmark />
    </span>
  );
}
