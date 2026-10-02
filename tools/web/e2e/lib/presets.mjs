// Presets, routes and matrices for the control center harness. Pure data
// and functions: tools/web/test/e2e-lib.test.mjs checks them without a
// browser or a server.
//
// A preset is a named state of the dev server's fake API
// (internal/web/fixtures/presets/<name>.json, MASTER-PLAN Appendix B). The
// harness asks for one with VOS_WEB_PRESET=<name>; the dev server from before
// v2 knows only the VOS_WEB_* switches, so presets with an alias also set it,
// and presets without one are skipped against that server.

// Topics name what a page is about; ROUTES turns them into each UI's paths.
export const ROUTES = {
  legacy: {
    home: '/',
    devices: '/pair',
    stream: '/streaming',
    screen: '/display',
    system: '/advanced',
    storage: '/storage',
    updates: '/updates',
    power: '/power',
    settings: '/advanced',
    logs: '/advanced',
    about: '/advanced',
    login: '/login',
    setup: '/setup',
  },
  next: {
    home: '/',
    devices: '/devices',
    stream: '/screen',
    screen: '/screen',
    system: '/system',
    storage: '/system/storage',
    updates: '/system/updates',
    power: '/system/power',
    settings: '/system/settings',
    logs: '/system/logs',
    about: '/system/about',
    login: '/login',
    setup: '/setup',
  },
};

// Every signed-in page of a UI, in navigation order.
export const APP_TOPICS = {
  legacy: ['home', 'devices', 'stream', 'screen', 'storage', 'updates', 'power', 'system'],
  next: ['home', 'devices', 'screen', 'system', 'updates', 'power', 'storage', 'settings', 'logs', 'about'],
};

const ALL = '*';

// Fault answers some presets are designed to give, as the real handlers do
// (internal/sunshine/handlers.go): the browser logs every one of them.
// The console line carries no URL, so the request line pins it down.
const SUNSHINE_STARTING = [/status of 503/, /503 GET .*\/api\/v1\/sunshine$/];
const LOGS_UNREADABLE = [/status of 502/, /502 GET .*\/api\/v1\/sunshine\/logs$/];

// PRESETS follows MASTER-PLAN Appendix B. topics: the pages the preset is
// about ("*" = every signed-in page); env: the pre-v2 alias switches.
// pages, when given, replaces topics: each entry a topic, or an object
// { topic or path, allow, note } where allow lists the console and request
// lines that page is designed to produce.
export const PRESETS = [
  { name: 'idle', group: 'os', topics: ALL, env: {} },
  { name: 'headless', group: 'os', topics: ['home', 'screen'] },
  { name: 'streaming', group: 'os', topics: ALL, env: { VOS_WEB_STREAMING: '1' } },
  { name: 'pairing-1', group: 'os', topics: ['home', 'devices'] },
  { name: 'pairing-2', group: 'os', topics: ['home', 'devices'], env: { VOS_WEB_PAIRING: '1' } },
  { name: 'keep-awake', group: 'os', topics: ['home', 'power'] },
  { name: 'busy-web', group: 'os', topics: ['home', 'power'] },
  { name: 'idle-countdown', group: 'os', topics: ['home', 'power'] },
  { name: 'no-wol', group: 'os', topics: ['home', 'power'] },
  { name: 'empty', group: 'os', topics: ALL },
  { name: 'ssh-on', group: 'os', topics: ['settings'] },
  // Signed out, every page goes to sign-in without a 401 of its own: the
  // pages that read before boot (Updates, Power, Screen) wait for it in a
  // tab that never signed in.
  {
    name: 'signed-out',
    group: 'entry',
    pages: ['login', ...['updates', 'power', 'screen', 'home'].map((topic) => ({ topic, note: 'redirects to sign-in' }))],
    env: { VOS_WEB_SIGNED_OUT: '1' },
  },
  { name: 'first-run', group: 'entry', pages: ['setup', { topic: 'home', note: 'redirects to setup' }], env: { VOS_WEB_SETUP: '1' } },
  { name: 'update-available', group: 'updates', topics: ['home', 'updates'] },
  { name: 'update-staging', group: 'updates', topics: ['home', 'updates'] },
  { name: 'update-staged', group: 'updates', topics: ['home', 'updates'] },
  { name: 'update-stale-check', group: 'updates', topics: ['home', 'updates'] },
  { name: 'update-error', group: 'updates', topics: ['home', 'updates'] },
  { name: 'update-check-failed', group: 'updates', topics: ['home', 'updates'] },
  { name: 'update-failed-newer', group: 'updates', topics: ['home', 'updates'] },
  { name: 'update-held', group: 'updates', topics: ['home', 'updates'], env: { VOS_WEB_HELD: '1' } },
  { name: 'update-trial', group: 'updates', topics: ['home', 'updates'], env: { VOS_WEB_TRIAL: '1' } },
  { name: 'rollback-pending', group: 'updates', topics: ['home', 'updates'] },
  { name: 'rollback-forward', group: 'updates', topics: ['home', 'updates', 'system'] },
  { name: 'no-gpu', group: 'faults', topics: ['home', 'screen'] },
  // /sunshine answers 503 while Sunshine is still being set up.
  { name: 'sunshine-starting', group: 'faults', pages: [{ topic: 'home', allow: SUNSHINE_STARTING }, { topic: 'stream', allow: SUNSHINE_STARTING }] },
  { name: 'sunshine-stopped', group: 'faults', topics: ['home', 'stream'] },
  { name: 'sunshine-unreachable', group: 'faults', topics: ['home', 'stream'] },
  { name: 'reboot-needed', group: 'faults', topics: ['home', 'screen'] },
  { name: 'disk-low', group: 'storage', topics: ['home', 'storage'] },
  { name: 'storage-missing', group: 'storage', topics: ['storage'] },
  { name: 'storage-pending', group: 'storage', topics: ['storage'] },
  { name: 'logs-empty', group: 'storage', topics: ['logs'] },
  // /sunshine/logs answers 502 when neither Sunshine, its log file nor the
  // journal has a log.
  { name: 'logs-error', group: 'storage', pages: [{ topic: 'logs', allow: LOGS_UNREADABLE }] },
  {
    name: 'installer-code',
    group: 'installer',
    installer: true,
    pages: [
      { path: '/setup?code=ABCD-EFGH' },
      { path: '/?code=ABCD-EFGH', note: 'the TV QR code; redirects to /setup' },
      // Without the code the wizard asks the server, gets 403 and shows its
      // code step: the one error this page is designed to log.
      { path: '/setup', allow: [/status of 403/, /\/api\/v1\/install\/status/] },
    ],
    env: { VOS_WEB_INSTALLER: '1' },
  },
  { name: 'installer-waived', group: 'installer', installer: true, pages: [{ path: '/setup' }, { path: '/' }], env: { VOS_WEB_INSTALLER: '1', VOS_WEB_HEADLESS: '1' } },
  { name: 'installer-one-disk', group: 'installer', installer: true, pages: [{ path: '/setup?code=ABCD-EFGH' }] },
  { name: 'installer-no-disk', group: 'installer', installer: true, pages: [{ path: '/setup?code=ABCD-EFGH' }] },
  { name: 'installer-source-error', group: 'installer', installer: true, pages: [{ path: '/setup?code=ABCD-EFGH' }] },
  { name: 'installer-two-vaporos', group: 'installer', installer: true, pages: [{ path: '/setup?code=ABCD-EFGH' }] },
  { name: 'installer-failed', group: 'installer', installer: true, pages: [{ path: '/setup?code=ABCD-EFGH' }] },
  // GET /extensions (internal/web/fixtures/base/extensions.json): an image
  // downloading, a change waiting for a restart (GET /status lists the
  // restart kind extensions), and extensions that need attention.
  { name: 'extensions-installing', group: 'extensions', topics: ['home', 'system'] },
  { name: 'extensions-restart', group: 'extensions', topics: ['home', 'system'] },
  { name: 'extensions-attention', group: 'extensions', topics: ['home', 'system'] },
];

// The presets smoke runs at 1440 light as well as at 390 dark.
export const KEY_PRESETS = ['idle', 'streaming', 'pairing-1', 'update-staged', 'sunshine-stopped'];

export const VIEWPORTS = {
  phone: { width: 390, height: 844, isMobile: true, hasTouch: true, deviceScaleFactor: 2 },
  small: { width: 320, height: 568, isMobile: true, hasTouch: true, deviceScaleFactor: 2 },
  landscape: { width: 844, height: 390, isMobile: true, hasTouch: true, deviceScaleFactor: 2 },
  tablet: { width: 768, height: 1024, hasTouch: true, deviceScaleFactor: 1 },
  desktop: { width: 1440, height: 900, deviceScaleFactor: 1 },
};

export function preset(name) {
  const p = PRESETS.find((x) => x.name === name);
  if (!p) throw new Error(`unknown preset ${name}; see tools/web/e2e/lib/presets.mjs`);
  return p;
}

// env is what the dev server needs to start in preset p.
export function presetEnv(p) {
  return { VOS_WEB_PRESET: p.name, ...(p.env ?? {}) };
}

// hasAlias reports whether a dev server without presets can show p.
export function hasAlias(p) {
  return p.env !== undefined;
}

// pagesFor lists the pages preset p covers in ui, each as
// { path, allow, note }, without duplicates.
export function pagesFor(p, ui) {
  const routes = ROUTES[ui];
  if (!routes) throw new Error(`unknown UI ${ui}; use legacy or next`);
  const entries = p.pages ?? (p.topics === ALL ? APP_TOPICS[ui] : p.topics);
  const out = [];
  for (const e of entries) {
    const entry = typeof e === 'string' ? { topic: e } : e;
    const path = entry.path ?? routes[entry.topic];
    if (!out.some((x) => x.path === path)) out.push({ path, allow: entry.allow ?? [], note: entry.note ?? '' });
  }
  return out;
}

// A load is one page in one preset at one viewport, scheme and motion.
function load(p, page, viewport, scheme, extra = {}) {
  return { preset: p.name, path: page.path, allow: page.allow, note: page.note, viewport, scheme, motion: 'no-preference', ...extra };
}

// loads expands a matrix (MASTER-PLAN §6.3) into its page loads.
//   smoke: every preset at phone/dark; the key presets at desktop/light;
//          the idle preset's pages at 320 px (small) for overflow.
//   full:  every preset at every viewport in both schemes; reduced motion
//          at phone/dark; forced colours on the idle preset at phone/dark.
export function loads(ui, matrix, presets = PRESETS) {
  const out = [];
  for (const p of presets) {
    const pages = pagesFor(p, ui);
    if (matrix === 'smoke') {
      for (const pg of pages) out.push(load(p, pg, 'phone', 'dark'));
      if (KEY_PRESETS.includes(p.name)) for (const pg of pages) out.push(load(p, pg, 'desktop', 'light'));
      if (p.name === 'idle') for (const pg of pages) out.push(load(p, pg, 'small', 'dark'));
    } else if (matrix === 'full') {
      for (const pg of pages) {
        for (const viewport of Object.keys(VIEWPORTS)) {
          for (const scheme of ['dark', 'light']) out.push(load(p, pg, viewport, scheme));
        }
        out.push(load(p, pg, 'phone', 'dark', { motion: 'reduce' }));
        if (p.name === 'idle') out.push(load(p, pg, 'phone', 'dark', { forcedColors: 'active' }));
      }
    } else {
      throw new Error(`unknown matrix ${matrix}; use smoke or full`);
    }
  }
  return out;
}

// flowRuns lists where each flow runs: smoke at phone/dark, full also at
// desktop/light.
export function flowRuns(matrix) {
  return matrix === 'full'
    ? [{ viewport: 'phone', scheme: 'dark' }, { viewport: 'desktop', scheme: 'light' }]
    : [{ viewport: 'phone', scheme: 'dark' }];
}
