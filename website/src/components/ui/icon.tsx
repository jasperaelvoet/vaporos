import type { SVGProps } from 'react';
import { iconPaths, type IconName } from '@/lib/icons';

type Props = Omit<SVGProps<SVGSVGElement>, 'children' | 'dangerouslySetInnerHTML'> & {
  name: IconName;
  /** Give the icon a meaning of its own; without it the icon is decorative (aria-hidden). */
  label?: string;
};

/** A stroke icon from src/lib/icons.ts. Size it with className (default size-5); it takes currentColor. */
export function Icon({ name, label, className = 'size-5 shrink-0', ...rest }: Props) {
  const a11y = label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': true as const };
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.9}
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      className={className}
      {...a11y}
      {...rest}
      dangerouslySetInnerHTML={{ __html: iconPaths[name] }}
    />
  );
}
