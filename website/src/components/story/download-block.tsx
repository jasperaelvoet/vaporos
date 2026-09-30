// B7: the white-hot block, where the page's heat cycle peaks and you
// download. The loudest line on the site, the one ISO button, and what the
// PC needs, in one line, with the way to check the download right under it.
// .surface-hot turns the fixed nav into a hard ash bar while it's under it.
import { Headline } from '@/components/ui/headline';
import { TextLink } from '@/components/ui/text-link';
import { Rich } from '@/components/ui/rich';
import { downloadCard } from '@/content/download';
import { getBeat } from '@/content/home';
import { requirements } from '@/content/requirements';
import type { ReleaseLookup } from '@/lib/release-shape';
import { DownloadGet } from './download-get';
import './story.css';

export function DownloadBlock({ release }: { release: ReleaseLookup }) {
  return (
    <section
      id={getBeat.id}
      className="dl surface-hot isotherm-rings"
      data-heat={getBeat.heat}
      data-heat-label={getBeat.heatLabel}
      aria-labelledby={`${getBeat.id}-title`}
    >
      <div className="wrap dl-grid">
        <Headline
          as="h2"
          id={`${getBeat.id}-title`}
          cut="hot"
          size="block"
          friction={12}
          lines={getBeat.title.lines}
          narrow={getBeat.title.narrow}
          fitK={getBeat.title.fitK}
          fitKNarrow={getBeat.title.fitKNarrow}
          className="dl-title"
        />
        <p className="dl-lead">
          <Rich text={getBeat.lead} />
        </p>
        <DownloadGet initial={release}>
          <div className="dl-spec">
            <ul className="dl-req telemetry" aria-label={getBeat.requirementsLabel}>
              <li className="dl-req-k" aria-hidden="true">
                {getBeat.needs}
              </li>
              {requirements.map((r) => (
                <li key={r.id}>{r.label}</li>
              ))}
            </ul>
            <p className="dl-links">
              {downloadCard.ready.compactLinks.map((l) => (
                <TextLink key={l.href} href={l.href}>
                  {l.label}
                </TextLink>
              ))}
            </p>
          </div>
        </DownloadGet>
      </div>
    </section>
  );
}
