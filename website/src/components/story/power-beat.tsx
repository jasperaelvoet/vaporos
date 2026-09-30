// B5, second chapter: off when nobody plays, on when you do. The coldest
// beat, in the cold cut: one evening as a heat ribbon, then the three things
// to know (Moonlight wakes it, never mid-game, stay awake). The Wake-on-LAN
// condition sits in the lead, next to the promise.
import { Rich } from '@/components/ui/rich';
import { TextLink } from '@/components/ui/text-link';
import { powerBeat } from '@/content/home';
import { powerFacts, powerMore } from '@/content/story';
import { Beat, BeatHead } from './beat';
import { EveningRibbon } from './evening-ribbon';

export function PowerBeat() {
  return (
    <Beat head={powerBeat} className="beat-power">
      <BeatHead head={powerBeat} cut="cold" split />
      <EveningRibbon />
      <ul className="power-facts">
        {powerFacts.map((f) => (
          <li key={f.title}>
            <h3>{f.title}</h3>
            <p>
              <Rich text={f.body} />
            </p>
          </li>
        ))}
      </ul>
      <p className="beat-more">
        <TextLink href={powerMore.href}>{powerMore.label}</TextLink>
      </p>
    </Beat>
  );
}
