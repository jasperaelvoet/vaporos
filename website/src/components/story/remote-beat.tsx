// B3 and B4: your phone is the remote, and a monitor only says where to go.
// Warm, at the ready temperature: what the control center does, one line
// each, beside the picture of it: the control center's home page on a phone
// (as it is today), in front of the welcome screen a connected monitor shows
// once VaporOS is installed. The live demo is off until the next release
// (content/demo.ts), so a line under the picture says when it arrives.
import { remoteBeat } from '@/content/home';
import { remoteStory } from '@/content/story';
import { Beat, BeatHead } from './beat';
import { RemotePhone } from './remote-phone';
import { TvScreen } from './tv-screen';
import { TvStill } from './tv-still';

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
        </div>
        <figure className="remote-fig" aria-label={remoteStory.figureLabel}>
          <TvStill name="ready" fallback={<TvScreen state="ready" />} />
          <RemotePhone />
          <figcaption className="beat-note">
            {remoteStory.tvCaption} <span className="remote-demo">{remoteStory.demoLater}</span>
          </figcaption>
        </figure>
      </div>
    </Beat>
  );
}
