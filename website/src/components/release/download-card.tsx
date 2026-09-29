'use client';
// The download card: white-hot, because it is the thing you touch. It shows
// what the build's release lookup found (`initial`, from getLatestRelease()
// in a server component), then re-reads the GitHub API in the browser
// (useLatestRelease) and upgrades itself when a newer release is out; it
// never downgrades.
//
//   ready    the release: date, version, the ISO button with its size, the
//            file name, and (full card) the checksum and signature files
//   none     GitHub has no release: never a download link, only ways to
//            follow the project. Nothing to touch, so the card stays cold.
//   unknown  the lookup failed: never "no release", a link to GitHub's
//            latest release instead
//
// Test hooks, kept on the root: data-download-card, data-state
// (ready|none|unknown), data-origin (build|live), and the ISO as an <a>
// whose href ends in .iso. `children` (a RequirementSummary, say) sits at
// the card's foot in every state.
import type { ReactNode } from 'react';
import { buttonClass, Chip, Icon, LinkButton, Rich, TextLink } from '@/components/ui';
import { downloadCard as copy } from '@/content/download';
import { formatDate, formatSize, type Release, type ReleaseLookup } from '@/lib/release-shape';
import { useLatestRelease } from '@/lib/use-latest-release';

type H = 'h2' | 'h3';

interface Props {
  initial: ReleaseLookup;
  /** The home page's short card: no file list; links to verify and the requirements. */
  compact?: boolean;
  /** h3 when the card sits under a section heading. */
  headingLevel?: H;
  /** The id of the card's heading, for aria-labelledby on a surrounding section. */
  headingId?: string;
  className?: string;
  children?: ReactNode;
}

export function DownloadCard({ initial, compact = false, headingLevel = 'h2', headingId, className = '', children }: Props) {
  const { lookup, origin } = useLatestRelease(initial);
  const ready = lookup.state === 'ready';
  return (
    <div
      className={`dl-card relative isolate overflow-hidden rounded-[1.75rem] ${
        ready ? 'surface-hot isotherm-rings' : 'bg-soot text-bone inset-ring-1 inset-ring-line'
      } ${compact ? 'p-6 desk:p-9' : 'p-6 desk:p-12'} ${className}`}
      data-download-card
      data-state={lookup.state}
      data-origin={origin}
    >
      {lookup.state === 'ready' ? (
        <Ready r={lookup.release} compact={compact} H={headingLevel} id={headingId} />
      ) : lookup.state === 'none' ? (
        <None H={headingLevel} id={headingId} />
      ) : (
        <Unknown H={headingLevel} id={headingId} />
      )}
      {children && <div className="mt-8 desk:mt-10">{children}</div>}
    </div>
  );
}

function Ready({ r, compact, H, id }: { r: Release; compact: boolean; H: H; id?: string }) {
  const size = formatSize(r.iso.size);
  const date = formatDate(r.published);
  const urls: Record<string, string | undefined> = {
    sums: r.sums?.url,
    manifest: r.manifest?.url,
    sig: r.sig?.url,
    notes: r.url,
  };
  const assets = copy.ready.assets.filter((a) => urls[a.key]);
  return (
    <div className={`grid gap-x-12 gap-y-9 ${compact ? '' : 'desk:grid-cols-[minmax(0,7fr)_minmax(0,5fr)]'}`}>
      <div className="grid min-w-0 content-start justify-items-start gap-5">
        <div className="grid gap-3">
          <p className="telemetry text-meta text-hot-ink-2">{copy.ready.kicker}</p>
          <H id={id} className="cut-hot text-[clamp(2.125rem,4.6vw,3.75rem)] leading-[0.95] tracking-[-0.018em] text-ash">
            {copy.ready.heading}
            {date && (
              <>
                {' '}
                <span className="whitespace-nowrap">{date}</span>
              </>
            )}
          </H>
        </div>
        <ul className="flex flex-wrap gap-2">
          {copy.ready.meta.map((m) => (
            <li key={m.icon}>
              {'kind' in m ? (
                <Chip tone="ash" className="gap-1.5">
                  <Icon name={m.icon} className="size-3.5 shrink-0" />
                  {r.tag || `v${r.version}`}
                </Chip>
              ) : (
                <Chip tone="hot" className="gap-1.5 inset-ring-1 inset-ring-ash/35">
                  <Icon name={m.icon} className="size-3.5 shrink-0" />
                  {m.label}
                </Chip>
              )}
            </li>
          ))}
        </ul>
        <div className="mt-2 grid w-full justify-items-start gap-3">
          <a href={r.iso.url} className={buttonClass('ash', 'lg', 'max-desk:w-full')}>
            <Icon name={copy.ready.button.icon} className="size-6 shrink-0" />
            {copy.ready.button.label}
            {size ? ` (${size})` : ''}
          </a>
          <p className="telemetry text-meta break-all text-hot-ink-2">{r.iso.name}</p>
        </div>
      </div>

      {compact ? (
        <p className="flex flex-wrap gap-x-7 gap-y-2">
          {copy.ready.compactLinks.map((l) => (
            <TextLink key={l.href} href={l.href}>
              {l.label}
            </TextLink>
          ))}
        </p>
      ) : (
        <div className="grid min-w-0 content-start gap-4 desk:pt-1">
          <p className="font-semibold text-ash">{copy.ready.assetsLabel}</p>
          <ul className="border-t border-ash/25" aria-label={copy.ready.assetsLabel}>
            {assets.map((a) => (
              <li key={a.key} className="border-b border-ash/25">
                <a
                  href={urls[a.key]}
                  className="group flex min-h-13 items-center gap-3 text-hot-ink no-underline transition-colors hover:text-ash"
                >
                  <Icon name={a.icon} className="size-[1.125rem] shrink-0 text-hot-ink-2" />
                  <span className={`mr-auto min-w-0 truncate ${a.key === 'notes' ? 'font-semibold' : 'telemetry text-[0.875rem]'}`}>
                    {a.label}
                  </span>
                  <Icon name="download" className={`size-4 shrink-0 opacity-60 transition-opacity group-hover:opacity-100 ${a.key === 'notes' ? 'hidden' : ''}`} />
                  <Icon name="external" className={`size-4 shrink-0 opacity-60 transition-opacity group-hover:opacity-100 ${a.key === 'notes' ? '' : 'hidden'}`} />
                </a>
              </li>
            ))}
          </ul>
          <p className="pt-1">
            {copy.ready.fullLinks.map((l) => (
              <TextLink key={l.href} href={l.href}>
                {l.label}
              </TextLink>
            ))}
          </p>
        </div>
      )}
    </div>
  );
}

function None({ H, id }: { H: H; id?: string }) {
  return (
    <div className="grid justify-items-start gap-5">
      <p className="telemetry inline-flex items-center gap-2 text-meta text-smoke">
        <span aria-hidden className="size-1.5 rounded-[1px] bg-h3" />
        {copy.none.badge}
      </p>
      <H id={id} className="cut-cold text-[clamp(2.25rem,4.6vw,3.75rem)] leading-[0.95] tracking-[-0.018em]">
        {copy.none.heading}
      </H>
      <p className="max-w-read text-lead text-smoke">
        <Rich text={copy.none.text} />
      </p>
      <Actions actions={copy.none.actions} />
    </div>
  );
}

function Unknown({ H, id }: { H: H; id?: string }) {
  return (
    <div className="grid justify-items-start gap-5">
      <p className="telemetry text-meta text-smoke">{copy.unknown.kicker}</p>
      <H id={id} className="cut-warm text-[clamp(2.25rem,4.6vw,3.75rem)] leading-[0.95] tracking-[-0.018em]">
        {copy.unknown.heading}
      </H>
      <p className="max-w-read text-lead text-smoke">
        <Rich text={copy.unknown.text} />
      </p>
      <Actions actions={copy.unknown.actions} />
    </div>
  );
}

function Actions({ actions }: { actions: readonly { label: string; href: string; icon?: Parameters<typeof Icon>[0]['name']; primary?: boolean }[] }) {
  return (
    <div className="mt-2 flex flex-wrap gap-3">
      {actions.map((a) => (
        <LinkButton key={a.href} href={a.href} icon={a.icon} variant={a.primary ? 'hot' : 'ghost'} size="md">
          {a.label}
        </LinkButton>
      ))}
    </div>
  );
}
