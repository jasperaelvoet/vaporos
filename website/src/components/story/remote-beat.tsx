// B3 and B4: your phone is the remote, and a monitor only says where to go.
// Warm, at the ready temperature: what the control center does, one line
// each, beside the welcome screen a connected monitor shows once VaporOS is
// installed. The live demo is off until the next release (content/demo.ts),
// so the beat says when it arrives instead of linking it.
import { remoteBeat } from '@/content/home';
import { remoteStory } from '@/content/story';
import { Beat, BeatHead } from './beat';
import { TvScreen } from './tv-screen';

export function RemoteBeat() {
  return (
    <Beat head={remoteBeat} className="beat-remote">
      <div className="beat-cols remote-cols">
        <div className="remote-text">
          <BeatHead head={remoteBeat} cut="warm" />
          <dl className="remote-list" aria-label={remoteStory.itemsLabel}>
            {remoteStory.items.map((it) => (
              <div key={it.label}>
                <dt className="telemetry">{it.label}</dt>
                <dd>{it.text}</dd>
              </div>
            ))}
          </dl>
          <p className="remote-demo telemetry">{remoteStory.demoLater}</p>
        </div>
        <figure className="remote-tv">
          <TvScreen state="ready" />
          <figcaption className="beat-note">{remoteStory.tvCaption}</figcaption>
        </figure>
      </div>
    </Beat>
  );
}
