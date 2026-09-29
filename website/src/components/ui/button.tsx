// Buttons and button-looking links. White-hot is always the thing you touch.
//
//   <LinkButton href="/download/" icon="download">Download VaporOS</LinkButton>
//   <LinkButton href="#how" variant="ghost">How it works</LinkButton>
//   <Button variant="ash" size="lg" onClick={…}>…</Button>   (on a white-hot block)
//
// Variants: hot (white-hot, the primary action), ghost (outlined, on dark),
// ash (ash on a white-hot block), ghost-ash (outlined, on a white-hot block).
// Sizes: sm (44 px, the nav), md (52 px), lg (76 px, the download button).
// Every size meets the 44 px touch target. The label says exactly what
// happens; icons are decorative (aria-hidden).
import type { ButtonHTMLAttributes, ReactNode } from 'react';
import type { IconName } from '@/lib/icons';
import { Icon } from './icon';
import { SmartLink, type SmartLinkProps } from './smart-link';

export type ButtonVariant = 'hot' | 'ghost' | 'ash' | 'ghost-ash';
export type ButtonSize = 'sm' | 'md' | 'lg';

const base =
  'inline-flex shrink-0 items-center justify-center gap-2.5 font-semibold whitespace-nowrap no-underline select-none transition-[background-color,box-shadow,scale] duration-200 active:scale-[.97] disabled:pointer-events-none disabled:opacity-50';

const variants: Record<ButtonVariant, string> = {
  hot: 'bg-h9 text-ash hover:bg-h8',
  ghost: 'text-bone inset-ring-[1.5px] inset-ring-bone/35 hover:inset-ring-bone',
  ash: 'bg-ash text-h9 hover:bg-char',
  'ghost-ash': 'text-ash inset-ring-[1.5px] inset-ring-ash/55 hover:inset-ring-ash',
};

const sizes: Record<ButtonSize, string> = {
  sm: 'min-h-11 rounded-lg px-4 text-[0.90625rem]',
  md: 'min-h-13 rounded-xl px-5.5 text-base',
  lg: 'min-h-19 rounded-[1.125rem] px-7.5 text-[1.3125rem]',
};

const iconSizes: Record<ButtonSize, string> = {
  sm: 'size-4 shrink-0',
  md: 'size-[1.125rem] shrink-0',
  lg: 'size-6 shrink-0',
};

export function buttonClass(variant: ButtonVariant = 'hot', size: ButtonSize = 'md', extra = '') {
  return `${base} ${variants[variant]} ${sizes[size]} ${extra}`.trim();
}

interface Common {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** A decorative icon before the label. */
  icon?: IconName;
  /** A decorative icon after the label. */
  iconAfter?: IconName;
  children: ReactNode;
}

function Inner({ icon, iconAfter, size = 'md', children }: Pick<Common, 'icon' | 'iconAfter' | 'size' | 'children'>) {
  return (
    <>
      {icon && <Icon name={icon} className={iconSizes[size]} />}
      {children}
      {iconAfter && <Icon name={iconAfter} className={iconSizes[size]} />}
    </>
  );
}

export function Button({
  variant,
  size,
  icon,
  iconAfter,
  className,
  children,
  type = 'button',
  ...rest
}: Common & ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button type={type} className={buttonClass(variant, size, className)} {...rest}>
      <Inner icon={icon} iconAfter={iconAfter} size={size}>
        {children}
      </Inner>
    </button>
  );
}

export function LinkButton({ variant, size, icon, iconAfter, className, children, ...rest }: Common & SmartLinkProps) {
  return (
    <SmartLink className={buttonClass(variant, size, className)} {...rest}>
      <Inner icon={icon} iconAfter={iconAfter} size={size}>
        {children}
      </Inner>
    </SmartLink>
  );
}
