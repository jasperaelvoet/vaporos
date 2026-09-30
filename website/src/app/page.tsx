// / : the story, one heat cycle from top to bottom. The hero is a thermal
// view of the PC; each beat is set at its temperature (the page's heat scale
// follows [data-heat]); the white-hot block is where you download.
//
//   B1 hero            ready → streaming as you scroll (direction/redline/hero)
//   B2 the screen      streaming: the virtual display takes each device's mode
//   B3 the phone       ready: what you do from your phone; a monitor only says where to go
//   B4 live demo       ready: the real control center on made-up data (content/demo.ts)
//   B6 three steps     cold boot: install from a USB stick, finish on the phone
//   B5 updates         updating: two slots, an update and a bad one
//   B5 power           asleep: one evening of idle power-off and Wake-on-LAN
//   B7 download        white-hot: the ISO, what it needs, how to verify it
//   B8 questions       ready
import { DemoBeat } from '@/components/demo/demo-beat';
import { DownloadBlock } from '@/components/story/download-block';
import { FaqTeaser } from '@/components/story/faq-teaser';
import { PowerBeat } from '@/components/story/power-beat';
import { RemoteBeat } from '@/components/story/remote-beat';
import { ScreenBeat } from '@/components/story/screen-beat';
import { SetupBeat } from '@/components/story/setup-beat';
import { UpdatesBeat } from '@/components/story/updates-beat';
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
      <RemoteBeat />
      <DemoBeat />
      <SetupBeat />
      <UpdatesBeat />
      <PowerBeat />
      <DownloadBlock release={release} />
      <FaqTeaser />
    </>
  );
}
