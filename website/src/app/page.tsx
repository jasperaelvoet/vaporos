// / : the story, one heat cycle from top to bottom. The hero is a thermal
// view of the PC; each beat is set at its temperature (the page's heat scale
// follows [data-heat]); the white-hot block is where you download.
//
//   B1 hero            ready → streaming as you scroll (direction/redline/hero)
//   B2 the screen      streaming: the virtual display takes each device's mode
//   B6 three steps     cold boot: install from a USB stick, finish on the phone
//   B7 download        white-hot: the ISO, what it needs, how to verify it
//   B8 questions       ready
import { DownloadBlock } from '@/components/story/download-block';
import { FaqTeaser } from '@/components/story/faq-teaser';
import { ScreenBeat } from '@/components/story/screen-beat';
import { SetupBeat } from '@/components/story/setup-beat';
import { pageMeta } from '@/content';
import { ThermalHero } from '@/direction/redline/hero';
import { getLatestRelease } from '@/lib/release';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata(pageMeta.home);

export default async function Home() {
  const release = await getLatestRelease();
  return (
    <>
      <ThermalHero release={release} />
      <ScreenBeat />
      <SetupBeat />
      <DownloadBlock release={release} />
      <FaqTeaser />
    </>
  );
}
