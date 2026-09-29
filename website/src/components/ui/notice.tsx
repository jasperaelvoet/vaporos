// A note that needs a second look.
//
//   <Notice icon="info">…</Notice>                       info: ink-ready marker
//   <Notice tone="warn" icon="alert">…</Notice>          warn: a hot (h8) marker, a nudge
//   <Notice tone="fault" icon="alert" title="…">…</Notice>
//                                                        fault: cold, with hazard tape;
//                                                        the only cold thing on the site
//
// Content callouts map straight on: <Notice tone={c.tone} icon={c.icon}><Rich text={c.text} /></Notice>.
// The words carry the meaning; colour and tape only repeat it.
import type { ReactNode } from 'react';
import type { IconName } from '@/lib/icons';
import { Icon } from './icon';

export type NoticeTone = 'info' | 'warn' | 'fault';

const marker: Record<NoticeTone, string> = {
  info: 'bg-ink-ready',
  warn: 'bg-h8',
  fault: 'hazard',
};
const iconTone: Record<NoticeTone, string> = {
  info: 'text-ink-ready',
  warn: 'text-h8',
  fault: 'text-cold',
};

export function Notice({
  tone = 'info',
  icon,
  title,
  className = '',
  children,
}: {
  tone?: NoticeTone;
  icon?: IconName;
  title?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={`relative flex gap-3 overflow-hidden rounded-lg bg-soot py-4 pr-4 pl-5 text-smoke inset-ring-1 inset-ring-line ${className}`}
      data-tone={tone}
    >
      <span aria-hidden className={`absolute inset-y-0 left-0 w-[3px] ${marker[tone]}`} />
      {icon && <Icon name={icon} className={`mt-0.5 size-5 shrink-0 ${iconTone[tone]}`} />}
      <div className="min-w-0 space-y-1">
        {title && <p className="font-semibold text-bone">{title}</p>}
        <div>{children}</div>
      </div>
    </div>
  );
}
