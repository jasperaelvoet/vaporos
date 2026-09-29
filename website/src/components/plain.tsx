// Plain building blocks for the placeholder pages. A design replaces these.
import type { ReactNode } from 'react';
import type { ButtonItem, Callout as CalloutData, Headline, LinkItem, Rich as RichText } from '@/content/types';
import { Icon } from './icon';
import { Rich } from './rich';
import { SmartLink } from './smart-link';

export function Container({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`mx-auto w-full max-w-5xl px-4 sm:px-6 ${className}`}>{children}</div>;
}

export function Section({ id, children, className = '' }: { id?: string; children: ReactNode; className?: string }) {
  return (
    <section id={id} className={`scroll-mt-8 py-14 ${className}`}>
      <Container>{children}</Container>
    </section>
  );
}

/** Eyebrow, two-part heading (the accent in the brand gradient), optional lead. */
export function SectionHead({
  eyebrow,
  title,
  lead,
  as: H = 'h2',
}: {
  eyebrow: string;
  title: Headline;
  lead?: RichText;
  as?: 'h1' | 'h2';
}) {
  return (
    <div className="mb-8 grid max-w-3xl gap-3">
      <p className="text-xs font-semibold tracking-widest text-brand uppercase">{eyebrow}</p>
      <H className={H === 'h1' ? 'text-4xl font-bold tracking-tight sm:text-5xl' : 'text-3xl font-bold tracking-tight'}>
        {title.text}
        {title.accent && (
          <>
            {' '}
            <span className="text-vapor">{title.accent}</span>
          </>
        )}
      </H>
      {lead && (
        <p className="text-lg text-fg-2">
          <Rich text={lead} />
        </p>
      )}
    </div>
  );
}

export function ArrowLink({ link }: { link: LinkItem }) {
  return (
    <SmartLink href={link.href} className="inline-flex items-center gap-1.5 font-semibold text-brand hover:text-fg">
      {link.label}
      <Icon name={link.icon ?? 'arrow'} className="size-4" />
    </SmartLink>
  );
}

export function Button({ item }: { item: ButtonItem }) {
  return (
    <SmartLink
      href={item.href}
      className={`inline-flex items-center gap-2 rounded-xl px-5 py-3 font-semibold ${item.primary ? 'bg-brand text-brand-ink' : 'border border-line-2 text-fg'}`}
    >
      {item.icon && item.icon !== 'arrow' && <Icon name={item.icon} />}
      {item.label}
      {item.icon === 'arrow' && <Icon name="arrow" />}
    </SmartLink>
  );
}

export function Callout({ callout }: { callout: CalloutData }) {
  const warn = callout.tone === 'warn';
  return (
    <div className={`flex gap-3 rounded-xl border p-4 ${warn ? 'border-warn/40 bg-warn/5' : 'border-line-2 bg-white/3'}`}>
      <Icon name={callout.icon} className={`mt-0.5 size-5 shrink-0 ${warn ? 'text-warn' : 'text-brand'}`} />
      <p className="text-fg-2">
        <Rich text={callout.text} />
      </p>
    </div>
  );
}

export function IconTile({ name }: { name: Parameters<typeof Icon>[0]['name'] }) {
  return (
    <span className="grid size-10 shrink-0 place-items-center rounded-xl border border-brand/30 bg-brand/10 text-brand">
      <Icon name={name} />
    </span>
  );
}
