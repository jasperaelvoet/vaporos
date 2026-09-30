// parity.json is the list GATE-2b checks: every behaviour of today's
// control center (SCN §20) plus MASTER-PLAN §6.7's additions, each with the
// agent that owns its spec. Spec flows may only use IDs from it.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import { build, ID_RE, owner, PARITY_FILE, parseAdditions, parseScreens, passing, specFlowIDs } from '../e2e/parity.mjs';

const parity = JSON.parse(readFileSync(PARITY_FILE, 'utf8'));

test('parity.json holds the 129 table rows and the 16 additions, each ID once', () => {
  const ids = parity.flows.map((f) => f.id);
  assert.equal(new Set(ids).size, ids.length);
  assert.equal(parity.flows.filter((f) => f.source === 'SCN §20').length, 129);
  assert.equal(parity.flows.filter((f) => f.source === 'MASTER-PLAN §6.7').length, 16);
  assert.deepEqual(parity.flows.filter((f) => f.source === 'SCN §20').map((f) => f.row), Array.from({ length: 129 }, (_, i) => i + 1));
  for (const f of parity.flows) {
    assert.match(f.id, ID_RE);
    assert.equal(f.owner, owner(f.id), f.id);
  }
});

test('the generator reads both tables', () => {
  const screens = [
    '## 20. Parity table: every item',
    '| # | Today | New home | e2e flow |',
    '|---|---|---|---|',
    '| **S1 Home** | | | |',
    '| 1 | Hero: streaming, with a \\| pipe | Home hero #1 | `HOME-hero-streaming` |',
    '| **Global elements and runtime** (inventory §1.2) | | | |',
    '| 2 | G1 boot gate | §2.1 | `SHELL-boot` |',
    '## 21. Next',
    '| 3 | not in the table | x | `HOME-nope` |',
  ].join('\n');
  const plan = '### 6.7 Flow registry additions\n\n`ENTRY-login-cold`, `SYS-set-theme` (only if ⟦theme⟧ ships a switch). More `in-code`.\n\n## 7. Next\n`SHELL-later`';
  assert.deepEqual(parseScreens(screens).map((r) => [r.row, r.id, r.group, r.today]), [
    [1, 'HOME-hero-streaming', 'S1 Home', 'Hero: streaming, with a | pipe'],
    [2, 'SHELL-boot', 'Global elements and runtime', 'G1 boot gate'],
  ]);
  assert.deepEqual(parseAdditions(plan), [{ id: 'ENTRY-login-cold', conditional: undefined }, { id: 'SYS-set-theme', conditional: 'only if ⟦theme⟧ ships a switch' }]);
  assert.equal(build(screens, plan).flows.length, 4);
});

test('every spec flow of the new UI names a parity ID', async () => {
  const known = new Set(parity.flows.map((f) => f.id));
  for (const [id, file] of await specFlowIDs()) {
    if (id.startsWith('LEGACY-')) continue;
    assert.ok(known.has(id), `${file} defines flow ${id}, which parity.json does not list`);
  }
});

test('every parity ID has a spec flow (VOS_WEB_STRICT=1, GATE-2b)', async (t) => {
  const specs = await specFlowIDs();
  const missing = parity.flows.filter((f) => !f.conditional && !specs.has(f.id)).map((f) => f.id);
  if (process.env.VOS_WEB_STRICT !== '1') {
    t.skip(`${missing.length} of ${parity.flows.length} IDs have no spec flow yet; VOS_WEB_STRICT=1 fails on them`);
    return;
  }
  assert.deepEqual(missing, []);
});

test('an ID passes only when every run of it passed', () => {
  const runs = [
    { id: 'SHELL-scene', ok: false, viewport: 'phone' },
    { id: 'SHELL-scene', ok: true, viewport: 'desktop' },
    { id: 'SHELL-tabs', ok: true, viewport: 'phone' },
    { id: 'SHELL-tabs', ok: true, viewport: 'desktop' },
  ];
  assert.deepEqual([...passing(runs)], ['SHELL-tabs']);
});
