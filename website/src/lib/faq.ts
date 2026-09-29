// Frequently asked questions. Every answer follows from the code; see
// docs/CONTRACTS.md for the underlying behaviour.
import { href } from './paths';

export interface QA {
  id: string;
  q: string;
  a: string; // trusted HTML, written here
}

export const faq: QA[] = [
  {
    id: 'what',
    q: 'What is VaporOS?',
    a: `<p>An immutable, CachyOS-based operating system that turns a PC with an AMD GPU into a headless Steam streaming box. Steam runs on a virtual display on the PC, Sunshine streams it, and you play with <a href="https://moonlight-stream.org/">Moonlight</a> on a TV, phone, tablet or laptop. You install and manage it from a web page on your phone.</p>`,
  },
  {
    id: 'gpu',
    q: 'Does it work with NVIDIA or Intel graphics?',
    a: `<p>Not yet. VaporOS streams with AMD Radeon GPUs on the <code>amdgpu</code> driver. On other graphics it still installs and runs, but the dashboard and the welcome screen say there's no supported graphics card, and it won't stream. Very old AMD cards that use the legacy <code>radeon</code> driver aren't supported either.</p>`,
  },
  {
    id: 'monitor',
    q: 'Do I need a monitor? Can I use one?',
    a: `<p>Only while you install. The installer's setup code appears on the PC's screen, so connect a monitor or TV for the install.</p><p>After that you don't need one. VaporOS makes its own virtual display at the resolution, frame rate and HDR setting of the device you stream to, so you don't need a dummy plug either.</p><p>If a monitor is connected, it only ever shows the welcome screen: the address of the PC, a QR code, and a status line such as “Ready to stream”. It never shows a terminal or a desktop.</p>`,
  },
  {
    id: 'dual-boot',
    q: 'Can I dual boot it with Windows?',
    a: `<p>No. VaporOS installs to a whole drive and erases it. Other drives in the PC are left alone, and if they hold Steam libraries you can pick them during setup (or later, under Storage) and keep playing those games. Nothing on them is changed.</p>`,
  },
  {
    id: 'secure-boot',
    q: 'Why does Secure Boot have to be off?',
    a: `<p>The VaporOS kernel isn't signed for Secure Boot, so firmware with Secure Boot on refuses to start it. Turn Secure Boot off and boot in UEFI mode. Legacy (CSM/BIOS) boot isn't supported.</p>`,
  },
  {
    id: 'address',
    q: 'Where do I find the web page?',
    a: `<p>After installing, open <code>http://vapor.local</code> on a phone or computer on the same network. If you picked another name during setup, it's that name plus <code>.local</code>. A connected monitor also shows the address, the PC's IP address and a QR code.</p><p>While you install from the USB stick, the address is <code>http://vaporos-setup.local</code>.</p>`,
  },
  {
    id: 'updates',
    q: 'Where do updates come from?',
    a: `<p>From this project's GitHub container registry, <code>ghcr.io/jasperaelvoet/vaporos</code>. By default VaporOS downloads new versions in the background and prepares them; they start on the next restart. You can turn that off and update by hand under Updates.</p><p>Every update has to carry a valid ed25519 signature from the release key, before VaporOS writes anything, and every file has to match its size and SHA-256 before the new version can start. The <code>main</code> channel is the stable one; other channels are test builds.</p>`,
  },
  {
    id: 'rollback',
    q: 'What if an update breaks something?',
    a: `<p>VaporOS keeps two copies of the system. An update goes to the one you aren't running, and the old one stays as it was. If the new version doesn't start cleanly, the PC goes back to the previous version by itself and won't install that version again.</p><p>You can also go back by hand: open Updates, choose <strong>Roll back</strong>, and restart.</p>`,
  },
  {
    id: 'power',
    q: 'Does it stay on all the time?',
    a: `<p>By default it powers off after 15 minutes of nobody playing, but never while it's streaming, downloading or updating. You can change the time or turn this off under Power, or keep it awake for an hour or four. Moonlight wakes a paired PC over Wake-on-LAN, which needs a wired connection.</p>`,
  },
  {
    id: 'internet',
    q: 'Can other people on the internet reach it?',
    a: `<p>The web page only answers devices on private networks, such as your home network, and asks for your admin password. Sunshine's own admin page is never reachable from other devices. SSH is off unless you turn it on under Advanced, and then only with keys you add.</p>`,
  },
  {
    id: 'apps',
    q: 'Can I install other software on it?',
    a: `<p>No. The system is one read-only image with no package manager, which is what makes updates and rollback reliable. Games come from Steam, as usual.</p>`,
  },
  {
    id: 'verify',
    q: 'How do I know the ISO is genuine?',
    a: `<p>Every release comes with <code>SHA256SUMS</code> and a signed <code>manifest.json</code>. The <a href="${href('/download/#verify')}">download page</a> shows how to check both. They prove different things: <code>SHA256SUMS</code> catches a damaged download, and the signature proves that <code>manifest.json</code>, the list of system image files, came from the release key. Neither signs the ISO file itself, because <code>SHA256SUMS</code> is not signed.</p><p>The installer on the ISO only installs a system image whose manifest carries a valid signature. Download from this site or the project's GitHub releases.</p>`,
  },
];
