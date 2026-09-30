// "Check your download": the two checks, their exact commands, what a good
// run prints, what they prove, and the release key. A server component: the
// signature command embeds the release key, which only the build can read.
// The steps are a real sequence, so they are numbered (in ink-ready, the
// colour REDLINE gives step numbers).
import 'server-only';
import { CodeBlock, Icon, LinkButton, Notice, Rich } from '@/components/ui';
import { releaseKeyCard, verifyProof, verifySection, verifySteps } from '@/content/verify';
import { RELEASE_KEY } from '@/lib/key';

export function VerifySteps() {
  const steps = verifySteps(RELEASE_KEY);
  const key = releaseKeyCard(RELEASE_KEY);
  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-12 desk:gap-16">
      <ol className="grid grid-cols-[minmax(0,1fr)] gap-12 desk:gap-14">
        {steps.map((s) => (
          <li key={s.num} className="grid grid-cols-[minmax(0,1fr)] gap-x-8 gap-y-4 desk:grid-cols-[4.5rem_minmax(0,1fr)]">
            <span aria-hidden className="cut-cold text-[4.25rem] leading-[0.78] text-ink-ready desk:text-[5.25rem]">
              {s.num}
            </span>
            <div className="grid min-w-0 gap-4">
              <h3 className="text-[1.375rem] leading-snug font-semibold text-bone">{s.title}</h3>
              <p className="max-w-read text-smoke">
                <Rich text={s.body} />
              </p>
              <CodeBlock {...s.code} />
              <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-fine text-smoke">
                <Icon name="check" className="size-4 shrink-0 text-ink-ready" />
                {verifySection.expectLabel}
                <code className="telemetry rounded-xs bg-char px-1.5 py-0.5 text-meta font-semibold text-bone">{s.expect}</code>
              </p>
            </div>
          </li>
        ))}
      </ol>

      <Notice icon={verifyProof.icon} className="desk:ml-[6.5rem]">
        <Rich text={verifyProof.text} />
      </Notice>

      <section
        aria-labelledby="release-key-title"
        className="grid grid-cols-[minmax(0,1fr)] gap-5 rounded-2xl bg-soot p-5 inset-ring-1 inset-ring-line desk:ml-[6.5rem] desk:p-7"
      >
        <div className="flex items-start gap-3.5">
          <Icon name={key.icon} className="mt-0.5 size-5 shrink-0 text-smoke" />
          <div className="grid gap-0.5">
            <h3 id="release-key-title" className="text-[1.125rem] font-semibold text-bone">
              {key.title}
            </h3>
            <p className="text-fine text-smoke">{key.detail}</p>
          </div>
        </div>
        <CodeBlock {...key.code} />
        <div className="flex flex-wrap gap-3">
          <LinkButton href={key.download.href} download variant="ghost" size="sm" icon="download">
            {key.download.label}
          </LinkButton>
          <LinkButton href={key.compare.href} variant="ghost" size="sm" iconAfter="external">
            {key.compare.label}
          </LinkButton>
        </div>
      </section>
    </div>
  );
}
