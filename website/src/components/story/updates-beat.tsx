// B5, first chapter: updates that undo themselves, in the warm cut at the
// updating temperature. The two slots play it; the signature line says why
// an update can be trusted.
import { TextLink } from '@/components/ui/text-link';
import { updatesBeat } from '@/content/home';
import { updatesStory } from '@/content/story';
import { AbSlots } from './ab-slots';
import { Beat, BeatHead } from './beat';

export function UpdatesBeat() {
  return (
    <Beat head={updatesBeat} className="beat-updates">
      <AbSlots>
        <BeatHead head={updatesBeat} cut="warm">
          <p className="beat-note">
            <strong>{updatesStory.signed.strong}</strong> {updatesStory.signed.text}
          </p>
        </BeatHead>
      </AbSlots>
      <p className="beat-more ab-more">
        <TextLink href={updatesStory.more.href}>{updatesStory.more.label}</TextLink>
      </p>
    </Beat>
  );
}
