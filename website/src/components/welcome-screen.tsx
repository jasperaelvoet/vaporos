// A plain rendition of the welcome screen a monitor on the PC shows
// (content/mock.ts). Decorative: the figure's caption says what it is.
import { site } from '@/content/site';
import { welcomeScreen } from '@/content/mock';
import { qrPattern } from '@/lib/qr-pattern';

const cells = qrPattern();

export function WelcomeScreen({ mode = 'os' }: { mode?: 'installer' | 'os' }) {
  const w = welcomeScreen[mode];
  return (
    <div aria-hidden="true" className="flex flex-wrap items-center justify-between gap-6 rounded-2xl border border-line-2 bg-bg p-6 sm:p-8">
      <div className="grid gap-1.5">
        <p className="text-xl font-bold">
          {site.wordmark.text}
          <b className="text-brand">{site.wordmark.accent}</b>
        </p>
        <p className="text-2xl font-semibold">{w.status}</p>
        <p className="text-sm text-fg-2">{w.detail}</p>
        <p className="font-mono text-brand">{w.url}</p>
        <p className="font-mono text-sm text-fg-3">{welcomeScreen.ip}</p>
        {w.code && (
          <p className="mt-2 font-mono text-lg">
            <span className="font-sans text-xs tracking-widest text-fg-3 uppercase">{welcomeScreen.codeLabel}</span> {w.code}
          </p>
        )}
      </div>
      <div className="grid justify-items-center gap-2">
        <div className="grid size-28 grid-cols-[repeat(21,1fr)] rounded-md bg-white p-1.5">
          {cells.map((on, i) => (
            <i key={i} className={on ? 'bg-bg-deep' : undefined} />
          ))}
        </div>
        <span className="text-xs text-fg-3">{welcomeScreen.qrLabel}</span>
      </div>
    </div>
  );
}
