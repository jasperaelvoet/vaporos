// The web installer's steps, as the installer itself counts them: a bar of
// six segments that fills one step at a time, and "2 of 6". An ordered list,
// so the order is in the markup; the bar and the count are decorative.
import { Rich } from '@/components/ui';
import type { Item } from '@/content/types';

export function WizardSteps({ steps, of }: { steps: readonly Item[]; of: string }) {
  const n = steps.length;
  return (
    <ol className="grid border-t border-line desk:grid-cols-2 desk:gap-x-12">
      {steps.map((s, i) => (
        <li key={s.title} className="grid content-start gap-2.5 border-b border-line py-6">
          <div aria-hidden className="flex items-center gap-3">
            <span className="flex gap-1">
              {steps.map((_, k) => (
                <span key={k} className={`h-[3px] w-4.5 rounded-full ${k <= i ? 'bg-h7' : 'bg-line'}`} />
              ))}
            </span>
            <span className="telemetry text-meta text-smoke">
              {i + 1} {of} {n}
            </span>
          </div>
          <h3 className="text-[1.1875rem] leading-snug font-semibold text-bone">{s.title}</h3>
          <p className="text-smoke">
            <Rich text={s.body} />
          </p>
        </li>
      ))}
    </ol>
  );
}
