// B4 on the home page (#demo): the live demo, once src/content/demo.ts turns
// it on. Wide screens with a fine pointer get the stage (a phone frame
// beside the scenario panel), loaded as it nears the viewport; phones and
// touch screens get a card that opens /demo/, where the demo fills the
// screen. The hero's "Try the live demo" lands here.
import { Beat, BeatHead } from '@/components/story/beat';
import { LinkButton } from '@/components/ui/button';
import { demo, demoBeat, demoStage } from '@/content/demo';
import { routes } from '@/content/site';
import { DemoStage } from './demo-stage';
import { getDemo } from './manifest';

export function DemoBeat() {
  if (!demo.enabled) return null;
  const d = getDemo();
  return (
    <Beat head={demoBeat} className="beat-demo">
      <BeatHead head={demoBeat} cut="warm" split />
      {d ? (
        <>
          <DemoStage variant="home" hash={d.hash} />
          <noscript>
            <p className="demo-missing">{demoStage.noscript}</p>
          </noscript>
        </>
      ) : (
        <p className="demo-missing">{demoStage.missing}</p>
      )}
      <div className="demo-card">
        <p className="demo-card-title">{demoStage.card.title}</p>
        <p className="demo-card-text">{demoStage.card.text}</p>
        <LinkButton href={routes.demo} icon="phone">
          {demoStage.card.cta}
        </LinkButton>
      </div>
    </Beat>
  );
}
