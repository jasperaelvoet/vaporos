// B2: the screen follows you. The hottest beat before the download: the
// words in the hot cut, the virtual display taking each device's shape, and
// how a frame gets from the PC to the screen in front of you. The hero's
// "How it works" lands here.
import { TextLink } from '@/components/ui/text-link';
import { screenBeat } from '@/content/home';
import { howItWorks } from '@/content/how-it-works';
import { screenStory } from '@/content/story';
import { Beat, BeatHead } from './beat';
import { ScreenFollows } from './screen-follows';

export function ScreenBeat() {
  const path = [
    ...howItWorks.pc.pipeline.map((n) => ({ title: n.title, detail: n.detail })),
    { title: howItWorks.network.label, detail: '' },
    { title: howItWorks.client.title, detail: howItWorks.client.detail },
  ];
  return (
    <Beat head={screenBeat} className="beat-screen">
      <BeatHead head={screenBeat} cut="hot" split />
      <ScreenFollows />
      <div className="screen-foot">
        {/* Five stages, each its name over its role, on one thin amber line:
            across on desktops, down on phones. */}
        <ol className="pipeline" aria-label={screenStory.pipelineLabel}>
          {path.map((n) => (
            <li key={n.title}>
              <b>{n.title}</b>
              {n.detail && <span className="telemetry">{n.detail}</span>}
            </li>
          ))}
        </ol>
        <p className="beat-more">
          <TextLink href={screenStory.switching.href}>{screenStory.switching.label}</TextLink>
        </p>
      </div>
    </Beat>
  );
}
