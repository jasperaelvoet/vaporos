// The harness's pure parts: presets, routes, matrices, flags and the
// browser lookup. No browser and no server here.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { findChrome } from '../e2e/lib/browser.mjs';
import { APP_TOPICS, flowRuns, hasAlias, KEY_PRESETS, loads, pagesFor, PRESETS, preset, presetEnv, ROUTES } from '../e2e/lib/presets.mjs';
import { parseArgs, slug } from '../e2e/lib/run-helpers.mjs';

// MASTER-PLAN Appendix B, the one list of preset names, plus rollback-forward
// (a newer next_boot with nothing staged) and the extensions' presets
// (internal/web/contract_test.go).
const APPENDIX_B = [
  'idle', 'headless', 'streaming', 'pairing-1', 'pairing-2', 'keep-awake', 'busy-web', 'idle-countdown', 'no-wol', 'empty', 'ssh-on', 'signed-out', 'first-run',
  'update-available', 'update-staging', 'update-staged', 'update-stale-check', 'update-error', 'update-check-failed', 'update-failed-newer', 'update-held', 'update-trial', 'rollback-pending', 'rollback-forward',
  'no-gpu', 'sunshine-starting', 'sunshine-stopped', 'sunshine-unreachable', 'reboot-needed',
  'disk-low', 'storage-missing', 'storage-pending', 'logs-empty', 'logs-error',
  'installer-code', 'installer-waived', 'installer-one-disk', 'installer-no-disk', 'installer-source-error', 'installer-two-vaporos', 'installer-failed',
  'extensions-installing', 'extensions-restart', 'extensions-attention',
];

test('the presets are exactly Appendix B, each once', () => {
  assert.deepEqual(PRESETS.map((p) => p.name).sort(), [...APPENDIX_B].sort());
  for (const k of KEY_PRESETS) assert.ok(preset(k));
  assert.throws(() => preset('nope'), /unknown preset/);
});

test('the pre-v2 switches are the aliases Appendix B names', () => {
  const aliased = Object.fromEntries(PRESETS.filter(hasAlias).map((p) => [p.name, p.env]));
  assert.deepEqual(aliased, {
    idle: {},
    streaming: { VOS_WEB_STREAMING: '1' },
    'pairing-2': { VOS_WEB_PAIRING: '1' },
    'signed-out': { VOS_WEB_SIGNED_OUT: '1' },
    'first-run': { VOS_WEB_SETUP: '1' },
    'update-held': { VOS_WEB_HELD: '1' },
    'update-trial': { VOS_WEB_TRIAL: '1' },
    'installer-code': { VOS_WEB_INSTALLER: '1' },
    'installer-waived': { VOS_WEB_INSTALLER: '1', VOS_WEB_HEADLESS: '1' },
  });
  assert.deepEqual(presetEnv(preset('streaming')), { VOS_WEB_PRESET: 'streaming', VOS_WEB_STREAMING: '1' });
});

test('every topic has a route in both UIs, and the new UI has its ten app pages', () => {
  assert.deepEqual(Object.keys(ROUTES.legacy).sort(), Object.keys(ROUTES.next).sort());
  assert.deepEqual(APP_TOPICS.next.map((t) => ROUTES.next[t]), [
    '/', '/devices', '/screen', '/system', '/system/updates', '/system/power', '/system/storage', '/system/settings', '/system/logs', '/system/about',
  ]);
  assert.deepEqual(pagesFor(preset('idle'), 'legacy').map((p) => p.path), ['/', '/pair', '/streaming', '/display', '/storage', '/updates', '/power', '/advanced']);
  assert.deepEqual(pagesFor(preset('no-wol'), 'next').map((p) => p.path), ['/', '/system/power']);
  assert.deepEqual(pagesFor(preset('logs-empty'), 'legacy').map((p) => p.path), ['/advanced']);
  const installer = pagesFor(preset('installer-code'), 'next');
  assert.equal(installer.find((p) => p.path === '/setup').allow.length, 2);
});

test('smoke covers every preset at phone/dark and the key presets at desktop/light', () => {
  const smoke = loads('next', 'smoke');
  for (const p of PRESETS) assert.ok(smoke.some((l) => l.preset === p.name && l.viewport === 'phone' && l.scheme === 'dark'), p.name);
  for (const k of KEY_PRESETS) assert.ok(smoke.some((l) => l.preset === k && l.viewport === 'desktop' && l.scheme === 'light'), k);
  assert.ok(smoke.some((l) => l.viewport === 'small'));
  assert.ok(!smoke.some((l) => l.viewport === 'tablet'));
  const full = loads('next', 'full', [preset('idle')]);
  assert.equal(full.length, 10 * (5 * 2 + 2));
  assert.ok(full.some((l) => l.forcedColors === 'active') && full.some((l) => l.motion === 'reduce'));
  assert.deepEqual(flowRuns('smoke'), [{ viewport: 'phone', scheme: 'dark' }]);
  assert.throws(() => loads('next', 'huge'), /unknown matrix/);
});

test('flags: known ones parse, a window is refused', () => {
  assert.deepEqual(parseArgs(['--ui=next', '--list'], { ui: '', list: false }), { ui: 'next', list: true });
  for (const f of ['--headed', '--show', '--attach']) assert.throws(() => parseArgs([f], {}), /headless only/);
  assert.throws(() => parseArgs(['--nope'], { ui: '' }), /unknown flag/);
  assert.equal(slug('idle', '/system/updates', 'phone'), 'idle-_system_updates-phone');
});

test('the browser comes from CHROME_PATH first and is never downloaded', () => {
  const dir = mkdtempSync(join(tmpdir(), 'vos-chrome-'));
  try {
    assert.throws(() => findChrome({ CHROME_PATH: join(dir, 'missing') }), /does not exist/);
    assert.throws(() => findChrome({ PLAYWRIGHT_BROWSERS_PATH: dir, PATH: '', HOME: dir }), /no Chromium found|does not exist/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
