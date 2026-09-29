// What the product's own screens say, for illustrations: the welcome screen
// on a monitor connected to the PC, and the web UI dashboard on a phone.
// Ported from components/DeviceMock.astro, a static recreation of
// internal/web (the repo has no screenshots yet). The IP and the setup code
// are placeholders. src/lib/qr-pattern.ts draws a decorative QR-like grid.

export const welcomeScreen = {
  /** While the installer runs from the USB stick. */
  installer: {
    status: 'Ready to install',
    detail: 'Open this address on a phone or computer to install VaporOS',
    url: 'http://vaporos-setup.local',
    code: 'ABCD-EFGH',
  },
  /** Once installed. */
  os: {
    status: 'Ready to stream',
    detail: 'Open this address on a phone or computer to pair Moonlight',
    url: 'http://vapor.local',
    code: '',
  },
  ip: 'http://192.168.1.50',
  codeLabel: 'Setup code',
  qrLabel: 'Scan to open',
};

export const dashboard = {
  live: 'Live',
  /** The PC's network name, shown above its status. */
  host: 'vapor',
  status: 'Ready to stream',
  detail: 'Open Moonlight on any device and pick this PC.',
  updates: { title: 'Updates', status: 'Up to date.' },
  thisPc: {
    title: 'This PC',
    rows: [
      { key: 'Address', value: 'vapor.local' },
      { key: 'IP', value: '192.168.1.50' },
      { key: 'Graphics', value: 'AMD Radeon' },
    ],
  },
  quickActions: {
    title: 'Quick actions',
    actions: [
      { label: 'Pair a device' },
      { label: 'Stay awake 1 h' },
      { label: 'Restart' },
      { label: 'Power off', danger: true },
    ],
  },
};
