// "Check your download": the two checks, what they prove, and the key.
// A server component: it reads the release key at build time.
import { releaseKeyCard, verifyProof, verifySteps } from '@/content/verify';
import { RELEASE_KEY } from '@/lib/key';
import { CodeBlock } from './code-block';
import { Icon } from './icon';
import { Callout, IconTile } from './plain';
import { Rich } from './rich';
import { SmartLink } from './smart-link';

export function VerifySteps() {
  const steps = verifySteps(RELEASE_KEY);
  const key = releaseKeyCard(RELEASE_KEY);
  return (
    <div className="grid gap-8">
      <ol className="grid gap-8">
        {steps.map((s) => (
          <li key={s.num} className="grid min-w-0 gap-4">
            <div className="flex gap-4">
              <span className="grid size-8 shrink-0 place-items-center rounded-full bg-brand font-bold text-brand-ink">{s.num}</span>
              <div>
                <h3 className="text-xl font-semibold">{s.title}</h3>
                <p className="text-fg-2">
                  <Rich text={s.body} />
                </p>
              </div>
            </div>
            <CodeBlock {...s.code} />
          </li>
        ))}
      </ol>

      <Callout callout={verifyProof} />

      <div className="grid min-w-0 gap-4 rounded-2xl border border-line-2 bg-surface p-6">
        <div className="flex items-center gap-4">
          <IconTile name={key.icon} />
          <div>
            <h3 className="text-lg font-semibold">{key.title}</h3>
            <p className="text-sm text-fg-2">{key.detail}</p>
          </div>
        </div>
        <CodeBlock {...key.code} />
        <p className="flex flex-wrap gap-x-7 gap-y-3">
          <SmartLink href={key.download.href} download className="inline-flex items-center gap-1.5 font-semibold text-brand hover:text-fg">
            {key.download.label}
            <Icon name="download" className="size-4" />
          </SmartLink>
          <a href={key.compare.href} className="inline-flex items-center gap-1.5 font-semibold text-brand hover:text-fg">
            {key.compare.label}
            <Icon name="external" className="size-4" />
          </a>
        </p>
      </div>
    </div>
  );
}
