'use client';
// The white-hot block's download (B7): the release the build found, upgraded
// live by useLatestRelease (never downgraded). The one ISO button on the
// home page: ash on white-hot, its text names the type and the size.
//
// Keeps the download card's contract for the site's checks: the root carries
// data-download-card, data-state (ready | none | unknown) and data-origin
// (build | live); a ready card links the ISO (a[href$=".iso"]), an unknown
// one GitHub's latest release, and a none card never links an ISO.
import { Icon } from '@/components/ui/icon';
import { LinkButton } from '@/components/ui/button';
import { Rich } from '@/components/ui/rich';
import { downloadCard as copy } from '@/content/download';
import { getStory } from '@/content/story';
import { formatDate, formatSize, type ReleaseLookup } from '@/lib/release-shape';
import { useLatestRelease } from '@/lib/use-latest-release';

export function DownloadGet({ initial }: { initial: ReleaseLookup }) {
  const { lookup, origin } = useLatestRelease(initial);
  return (
    <div className="get" data-download-card data-state={lookup.state} data-origin={origin}>
      {lookup.state === 'ready' ? (
        <>
          <a href={lookup.release.iso.url} className="get-iso">
            <Icon name="download" className="size-6 shrink-0" />
            <span className="get-iso-label">{getStory.button}</span>
            <span className="get-iso-meta telemetry">
              {[getStory.fileType, formatSize(lookup.release.iso.size)].filter(Boolean).join(' · ')}
            </span>
          </a>
          <p className="get-meta telemetry">
            <span className="get-file">{lookup.release.iso.name}</span>
            <br />
            {getStory.released} {formatDate(lookup.release.published)} ·{' '}
            <a href={getStory.allReleases.href}>{getStory.allReleases.label}</a>
          </p>
        </>
      ) : lookup.state === 'none' ? (
        <>
          <p className="get-badge telemetry">{copy.none.badge}</p>
          <p className="get-note">
            <strong>{copy.none.heading}.</strong> <Rich text={copy.none.textShort} />
          </p>
          <div className="get-actions">
            {copy.none.actions.map((a) => (
              <LinkButton key={a.href} href={a.href} variant={a.primary ? 'ash' : 'ghost-ash'} icon={a.icon}>
                {a.label}
              </LinkButton>
            ))}
          </div>
        </>
      ) : (
        <>
          <p className="get-note">
            <Rich text={copy.unknown.text} />
          </p>
          <div className="get-actions">
            {copy.unknown.actions.map((a) => (
              <LinkButton key={a.href} href={a.href} variant={a.primary ? 'ash' : 'ghost-ash'} icon={a.icon} size={a.primary ? 'lg' : 'md'}>
                {a.label}
              </LinkButton>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
