// The download page and the download card's three states (ready, none, unknown).
// Ported from pages/download.astro and components/DownloadCard.astro.
// The card's data (version, date, size, URLs) comes from the release:
// src/lib/release.ts at build time, src/lib/use-latest-release.ts in the browser.
import { links, routes } from './site';
import type { ButtonItem, Callout, Headline, IconName, LinkItem, Rich } from './types';

export const downloadPage = {
  eyebrow: 'Download',
  title: { text: 'Get', accent: 'VaporOS' } satisfies Headline,
  lead: 'One ISO. Write it to a USB stick, boot the PC from it, and finish the setup on your phone.' as Rich,
  /** Under the card. */
  channels: {
    icon: 'info',
    text: `**Channels.** Releases from \`main\` are the stable channel shown above. Builds from other branches are published as [prereleases](${links.releases}) for testing. An installed PC follows its channel by itself, so you only download an ISO once.`,
  } satisfies Callout,
  channelsShort: 'An installed PC follows its channel by itself, so you only download an ISO once.' as Rich,
  next: { label: 'Next: the install guide', href: routes.install, icon: 'arrow' } satisfies LinkItem,
};

export type AssetKey = 'sums' | 'manifest' | 'sig' | 'notes';

export const downloadCard = {
  /** A release with an ISO exists. */
  ready: {
    kicker: 'Latest release',
    /** The card title is `${heading} ${formatDate(release.published)}`: "VaporOS Sep 28, 2026". */
    heading: 'VaporOS',
    /** The chips under the title. `version` shows release.version. */
    meta: [
      { icon: 'tag', kind: 'version' },
      { icon: 'cpu', label: 'x86-64-v3 · UEFI' },
      { icon: 'hdr', label: 'AMD Radeon' },
    ] satisfies ({ icon: IconName; kind: 'version' } | { icon: IconName; label: string })[],
    /** The main button: `${label} (${formatSize(release.iso.size)})` when the size is known. Below it, release.iso.name. */
    button: { label: 'Download ISO', icon: 'download' as IconName },
    /** The full card lists these; a missing asset is left out (notes always has a URL). */
    assetsLabel: 'Checksums and signature',
    assets: [
      { key: 'sums', label: 'SHA256SUMS', icon: 'check' },
      { key: 'manifest', label: 'manifest.json', icon: 'file' },
      { key: 'sig', label: 'manifest.json.sig', icon: 'key' },
      { key: 'notes', label: 'Release notes', icon: 'book' },
    ] satisfies { key: AssetKey; label: string; icon: IconName }[],
    /** Footer links: the compact card (home page) and the full card (download page). */
    compactLinks: [
      { label: 'Verify this download', href: routes.verify, icon: 'arrow' },
      { label: 'Requirements', href: routes.requirements, icon: 'arrow' },
    ] satisfies LinkItem[],
    fullLinks: [{ label: 'All releases', href: links.releases, icon: 'arrow' }] satisfies LinkItem[],
  },
  /**
   * GitHub says there is no release (404 on /releases/latest, or an empty
   * list): never a download link, only ways to follow the project.
   */
  none: {
    badge: 'In development',
    heading: 'First release coming soon',
    text: "VaporOS hasn't published a release yet. Watch the repository on GitHub (**Watch → Custom → Releases**) to hear the moment the first ISO is out, or build it from source today." as Rich,
    textShort: "VaporOS hasn't published a release yet. Watch the repository on GitHub, or build it from source today." as Rich,
    actions: [
      { label: 'Watch releases on GitHub', href: links.repo, icon: 'eye', primary: true },
      { label: 'Build from source', href: links.buildFromSource, icon: 'code' },
    ] satisfies ButtonItem[],
  },
  /**
   * The lookup failed (offline build, rate limit, outage): the card can't say
   * which release is the latest, so it sends people to GitHub, whose latest
   * release link is always valid.
   */
  unknown: {
    kicker: 'Latest release',
    heading: 'Get VaporOS on GitHub',
    text: "The latest release couldn't be loaded here. Get it from the releases page on GitHub." as Rich,
    actions: [
      { label: 'Open the latest release', href: links.latestRelease, icon: 'download', primary: true },
      { label: 'All releases', href: links.releases, icon: 'arrow' },
    ] satisfies ButtonItem[],
  },
};
