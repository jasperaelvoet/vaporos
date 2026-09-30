// B6: three steps, in the cold cut: the PC boots cold from a USB stick, the
// screen on it only says where to go, and the phone does the rest. The step
// numbers heat to white-hot as you reach them.
import { LightUp } from '@/components/motion/light-up';
import { Rich } from '@/components/ui/rich';
import { TextLink } from '@/components/ui/text-link';
import { setupBeat } from '@/content/home';
import { installStrip } from '@/content/install';
import { setupStory } from '@/content/story';
import { Beat, BeatHead } from './beat';
import { InstallerPhone } from './installer-phone';
import { TvScreen } from './tv-screen';

export function SetupBeat() {
  return (
    <Beat head={setupBeat} className="beat-setup">
      <div className="beat-cols setup-cols">
        <div className="setup-text">
          <BeatHead head={setupBeat} cut="cold" />
          <ol className="steps">
            {installStrip.steps.map((s, i) => (
              <li key={s.title}>
                <span className="step-n cut-cold" aria-hidden="true">
                  {i + 1}
                </span>
                <h3>{s.title}</h3>
                <p>
                  <Rich text={s.body} />
                </p>
              </li>
            ))}
            <LightUp selector=":scope > li" />
          </ol>
          <p className="beat-more">
            <TextLink href={installStrip.more.href}>{installStrip.more.label}</TextLink>
          </p>
        </div>
        <figure className="duo" aria-label={setupStory.tvLabel}>
          <TvScreen state="installer" />
          <InstallerPhone />
          <figcaption className="duo-cap">
            {setupStory.caption} <Rich text={setupStory.headless} />
          </figcaption>
        </figure>
      </div>
    </Beat>
  );
}
