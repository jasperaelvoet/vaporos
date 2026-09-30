#!/usr/bin/env node
// The live demo's two sides and its switch (npm test):
//
//   protocol  src/lib/demo-protocol.ts against internal/web/demo/runtime.js,
//             the other end of the postMessage channel: the same envelope,
//             the same events and commands, and the scenario ids are the
//             demo's scripts (internal/web/fixtures/scripts, which
//             TestExportDemo lists in demo-manifest.json `scenarios`).
//   parsing   what the parent accepts: well-formed messages from its own
//             frame and origin, and nothing else.
//   flag      src/content/demo.ts: `enabled` is a boolean, `uiLabels` is
//             legacy or next, and the other readers of the flag
//             (pages.yml, scripts/drop-demo.mjs) import the module itself.
//             (That DEMO_OPEN names real pages of the demo is checked at
//             build time against demo-manifest.json: components/demo/manifest.ts.)
//
//   node --conditions=react-server tests/unit/check-demo.mjs
import { readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { registerTsx } from './tsx-hooks.mjs';

registerTsx();
const SITE = fileURLToPath(new URL('../../', import.meta.url));
const REPO = resolve(SITE, '..');

let failures = 0;
const check = (name, cond, detail = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${!cond && detail ? `  (${detail})` : ''}`);
  if (!cond) failures++;
  return cond;
};
const same = (a, b) => JSON.stringify([...a].sort()) === JSON.stringify([...b].sort());

const P = await import(pathToFileURL(join(SITE, 'src/lib/demo-protocol.ts')).href);
const { demo, ccCopy, DEMO_OPEN } = await import(pathToFileURL(join(SITE, 'src/content/demo.ts')).href);

// ---------------------------------------------------------------- protocol
const runtime = readFileSync(join(REPO, 'internal/web/demo/runtime.js'), 'utf8');
const scripts = readdirSync(join(REPO, 'internal/web/fixtures/scripts'))
  .filter((f) => f.endsWith('.json'))
  .map((f) => f.slice(0, -5));
check('protocol: the scenario ids are the demo scripts', same(P.SCENARIO_IDS, scripts), `site ${P.SCENARIO_IDS.join(' ')} | scripts ${scripts.join(' ')}`);
check(
  "protocol: runtime.js's envelope is { type: 'vos-demo', v: 1 }",
  new RegExp(`const PROTO = \\{ type: '${P.DEMO_TYPE}', v: ${P.DEMO_VERSION} \\}`).test(runtime),
);
const posted = new Set([...runtime.matchAll(/post\(\{ event: '([a-z]+)'/g)].map((m) => m[1]));
check('protocol: runtime.js posts ready, state, paired and notice', same(posted, ['ready', 'state', 'paired', 'notice']), [...posted].join(' '));
const cmds = new Set([...runtime.matchAll(/m\.cmd === '([a-z]+)'/g)].map((m) => m[1]));
check('protocol: runtime.js takes run and preset', same(cmds, ['run', 'preset']), [...cmds].join(' '));
check("protocol: runtime.js starts over on run 'reset'", /m\.scenario === 'reset'\) return reset\(\)/.test(runtime));
check('protocol: runtime.js answers only its parent, on its own origin', /ev\.source !== window\.parent \|\| ev\.origin !== location\.origin/.test(runtime) && /postMessage\(\{ \.\.\.PROTO, \.\.\.msg \}, location\.origin\)/.test(runtime));
const snap = /function snapshot\(\) \{[\s\S]*?return \{\s*([\s\S]*?)\};\s*\}/.exec(runtime)?.[1] ?? '';
const fields = [...snap.matchAll(/^\s*([a-z]+):/gm)].map((m) => m[1]);
check("protocol: runtime.js's state carries device, streaming, pairing and update", same(fields, ['device', 'streaming', 'pairing', 'update']), fields.join(' '));
const states = new Set([...runtime.matchAll(/return '([a-z-]+)';/g)].map((m) => m[1]));
check('protocol: every device state runtime.js reports is in DEVICE_STATES', [...states].every((s) => P.DEVICE_STATES.includes(s)), [...states].join(' '));

// ---------------------------------------------------------------- parsing
const env = { type: 'vos-demo', v: 1 };
const good = [
  { ...env, event: 'ready', path: '/system/updates', manifest: '7d5628482768' },
  { ...env, event: 'state', state: { device: 'ready', streaming: null, pairing: null, update: null } },
  { ...env, event: 'state', state: { device: 'streaming', streaming: 'Living room TV', pairing: { device: 'Steam Deck', pin: '1234' }, update: { phase: 'download', percent: 12.5 } } },
  { ...env, event: 'paired', device: 'Steam Deck' },
  { ...env, event: 'notice', text: 'Signing out is off in the demo.' },
];
for (const m of good) check(`parsing: accepts ${m.event}`, JSON.stringify(P.parseFromDemo(m)) === JSON.stringify(m), JSON.stringify(P.parseFromDemo(m)));
const bad = [
  null,
  'vos-demo',
  { ...env, v: 2, event: 'ready', path: '/', manifest: 'x' },
  { type: 'other', v: 1, event: 'ready', path: '/', manifest: 'x' },
  { ...env, event: 'ready', path: '/' },
  { ...env, event: 'state', state: { device: 'melting', streaming: null, pairing: null, update: null } },
  { ...env, event: 'state', state: { device: 'ready', streaming: 3, pairing: null, update: null } },
  { ...env, event: 'state', state: { device: 'ready', streaming: null, pairing: { device: 'Deck' }, update: null } },
  { ...env, event: 'state', state: { device: 'ready', streaming: null, pairing: null, update: { phase: 'download', percent: 'half' } } },
  { ...env, event: 'paired' },
  { ...env, event: 'boom' },
  { ...env, cmd: 'run', scenario: 'stream' },
];
check(`parsing: rejects ${bad.length} malformed or foreign messages`, bad.every((m) => P.parseFromDemo(m) === null), bad.filter((m) => P.parseFromDemo(m) !== null).map((m) => JSON.stringify(m)).join(' '));
const frame = { name: 'frame' };
const other = { name: 'other' };
const ORIGIN = 'https://jasperaelvoet.github.io';
const ok = { origin: ORIGIN, source: frame, data: good[3] };
check('parsing: takes a message from its frame on its origin', P.acceptFromDemo(ok, frame, ORIGIN)?.event === 'paired');
check('parsing: ignores another window', P.acceptFromDemo({ ...ok, source: other }, frame, ORIGIN) === null);
check('parsing: ignores another origin', P.acceptFromDemo({ ...ok, origin: 'https://example.com' }, frame, ORIGIN) === null);
check('parsing: ignores everything before the frame exists', P.acceptFromDemo(ok, null, ORIGIN) === null);
const run = P.runMessage('pair');
check('parsing: runMessage is what runtime.js takes', run.type === 'vos-demo' && run.v === 1 && run.cmd === 'run' && run.scenario === 'pair');
check('parsing: isScenario knows the ids and nothing else', P.isScenario('update') && !P.isScenario('install') && !P.isScenario(undefined));

// ---------------------------------------------------------------- flag
check('flag: demo.enabled is a boolean', typeof demo.enabled === 'boolean', String(demo.enabled));
check("flag: demo.uiLabels is 'legacy' or 'next'", demo.uiLabels === 'legacy' || demo.uiLabels === 'next', String(demo.uiLabels));
check('flag: ccCopy picks the uiLabels set', ccCopy({ legacy: 'L', next: 'N' }) === (demo.uiLabels === 'next' ? 'N' : 'L'));
check('flag: DEMO_OPEN are page paths without slashes at either end', DEMO_OPEN.every((p) => /^[a-z]+(\/[a-z]+)?$/.test(p)), DEMO_OPEN.join(' '));
const pagesYml = readFileSync(join(REPO, '.github/workflows/pages.yml'), 'utf8');
check('flag: pages.yml reads the flag from src/content/demo.ts', pagesYml.includes("await import('./src/content/demo.ts')"));
check('flag: pages.yml exports the demo when it is on, and requires it in the build', /if: steps\.demo\.outputs\.enabled == 'true'[\s\S]*?TestExportDemo/.test(pagesYml) && /VAPOROS_DEMO_REQUIRED: \$\{\{ steps\.demo\.outputs\.enabled == 'true'/.test(pagesYml));
const drop = readFileSync(join(SITE, 'scripts/drop-demo.mjs'), 'utf8');
check('flag: scripts/drop-demo.mjs reads the flag from src/content/demo.ts', drop.includes("'../src/content/demo.ts'"));
const pkg = JSON.parse(readFileSync(join(SITE, 'package.json'), 'utf8'));
check('flag: npm run build runs scripts/drop-demo.mjs after next build', /scripts\/drop-demo\.mjs/.test(pkg.scripts?.postbuild ?? ''), pkg.scripts?.postbuild);

console.log(failures ? `\n${failures} FAILED` : '\nALL DEMO CHECKS PASSED');
process.exit(failures ? 1 : 0);
