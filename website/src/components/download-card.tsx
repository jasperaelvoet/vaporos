'use client';
// The download card. It renders what the build's release lookup found
// (`initial`, from getLatestRelease() in a server component), then re-reads
// the GitHub API in the browser and upgrades itself if a newer release is out.
// With no release it never shows a download link, only ways to follow the
// project; when the lookup failed it sends people to GitHub's releases page.
//
// data-state="ready|none|unknown" and data-origin="build|live" are on the
// root for tests and screenshots.
import { downloadCard as copy } from '@/content/download';
import { formatDate, formatSize, type Release, type ReleaseLookup } from '@/lib/release-shape';
import { useLatestRelease } from '@/lib/use-latest-release';
import { Icon } from './icon';
import { LogoMark } from './logo';
import { Rich } from './rich';
import { SmartLink } from './smart-link';

interface Props {
  initial: ReleaseLookup;
  /** The home page's short card: no asset list, links to verify and requirements. */
  compact?: boolean;
  /** h3 when the card sits under a section heading. */
  headingLevel?: 'h2' | 'h3';
}

export function DownloadCard({ initial, compact = false, headingLevel = 'h2' }: Props) {
  const { lookup, origin } = useLatestRelease(initial);
  const H = headingLevel;

  return (
    <div
      className="grid gap-5 rounded-2xl border border-line-2 bg-surface p-6 sm:p-8"
      data-download-card
      data-state={lookup.state}
      data-origin={origin}
    >
      {lookup.state === 'ready' ? (
        <Ready r={lookup.release} compact={compact} H={H} />
      ) : lookup.state === 'none' ? (
        <None H={H} />
      ) : (
        <Unknown H={H} />
      )}
    </div>
  );
}

function Ready({ r, compact, H }: { r: Release; compact: boolean; H: 'h2' | 'h3' }) {
  const size = formatSize(r.iso.size);
  const urls: Record<string, string | undefined> = {
    sums: r.sums?.url,
    manifest: r.manifest?.url,
    sig: r.sig?.url,
    notes: r.url,
  };
  return (
    <>
      <div className="flex items-center gap-4">
        <LogoMark size={52} />
        <div className="grid gap-1">
          <p className="text-xs font-semibold tracking-widest text-brand uppercase">{copy.ready.kicker}</p>
          <H className="text-2xl font-bold">
            {copy.ready.heading} <span className="font-medium text-fg-2">{formatDate(r.published)}</span>
          </H>
        </div>
      </div>
      <ul className="flex flex-wrap gap-x-5 gap-y-2 text-sm text-fg-2">
        {copy.ready.meta.map((m) => (
          <li key={m.icon} className="inline-flex items-center gap-2">
            <Icon name={m.icon} className="size-4 text-brand" />
            {'kind' in m ? <span className="font-mono">{r.version}</span> : m.label}
          </li>
        ))}
      </ul>
      <div className="grid gap-1.5">
        <a
          href={r.iso.url}
          className="inline-flex items-center justify-center gap-2 justify-self-start rounded-xl bg-brand px-5 py-3 font-semibold text-brand-ink"
        >
          <Icon name={copy.ready.button.icon} />
          {copy.ready.button.label}
          {size ? ` (${size})` : ''}
        </a>
        <p className="font-mono text-xs break-all text-fg-3">{r.iso.name}</p>
      </div>
      {!compact && (
        <ul className="flex flex-wrap gap-2" aria-label={copy.ready.assetsLabel}>
          {copy.ready.assets
            .filter((a) => urls[a.key])
            .map((a) => (
              <li key={a.key}>
                <a
                  href={urls[a.key]}
                  className="inline-flex items-center gap-2 rounded-lg border border-line-2 px-3 py-2 font-mono text-sm text-fg-2 hover:text-fg"
                >
                  <Icon name={a.icon} className="size-4 text-brand" />
                  {a.label}
                </a>
              </li>
            ))}
        </ul>
      )}
      <p className="flex flex-wrap gap-x-6 gap-y-2">
        {(compact ? copy.ready.compactLinks : copy.ready.fullLinks).map((l) => (
          <SmartLink key={l.href} href={l.href} className="inline-flex items-center gap-1.5 text-brand hover:text-fg">
            {l.label}
            {l.icon && <Icon name={l.icon} className="size-4" />}
          </SmartLink>
        ))}
      </p>
    </>
  );
}

function None({ H }: { H: 'h2' | 'h3' }) {
  return (
    <>
      <div className="flex items-center gap-4">
        <LogoMark size={52} />
        <div className="grid gap-1.5">
          <p>
            <span className="inline-flex items-center gap-2 rounded-full border border-warn/40 px-2.5 py-0.5 text-xs font-semibold text-warn">
              <span className="size-1.5 rounded-full bg-warn" />
              {copy.none.badge}
            </span>
          </p>
          <H className="text-2xl font-bold">{copy.none.heading}</H>
        </div>
      </div>
      <p className="max-w-prose text-fg-2">
        <Rich text={copy.none.text} />
      </p>
      <div className="flex flex-wrap gap-3">
        {copy.none.actions.map((a) => (
          <a
            key={a.href}
            href={a.href}
            className={`inline-flex items-center gap-2 rounded-xl px-5 py-3 font-semibold ${a.primary ? 'bg-brand text-brand-ink' : 'border border-line-2 text-fg'}`}
          >
            <Icon name={a.icon} />
            {a.label}
          </a>
        ))}
      </div>
    </>
  );
}

function Unknown({ H }: { H: 'h2' | 'h3' }) {
  return (
    <>
      <div className="flex items-center gap-4">
        <LogoMark size={52} />
        <div className="grid gap-1">
          <p className="text-xs font-semibold tracking-widest text-brand uppercase">{copy.unknown.kicker}</p>
          <H className="text-2xl font-bold">{copy.unknown.heading}</H>
        </div>
      </div>
      <p className="max-w-prose text-fg-2">
        <Rich text={copy.unknown.text} />
      </p>
      <div className="flex flex-wrap gap-3">
        {copy.unknown.actions.map((a) => (
          <a
            key={a.href}
            href={a.href}
            className={`inline-flex items-center gap-2 rounded-xl px-5 py-3 font-semibold ${a.primary ? 'bg-brand text-brand-ink' : 'border border-line-2 text-fg'}`}
          >
            <Icon name={a.icon} />
            {a.label}
          </a>
        ))}
      </div>
    </>
  );
}
