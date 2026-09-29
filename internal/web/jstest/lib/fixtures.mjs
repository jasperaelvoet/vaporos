// The dev server's fixtures (internal/web/fixtures) as snapshots for the pure
// modules: base documents, a preset's RFC 7386 patch, relative times and the
// simulated services, as devserver_fixtures_test.go reads them. Test code
// only; nothing here ships.
import { readdirSync, readFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'fixtures');

const read = (p) => JSON.parse(readFileSync(p, 'utf8'));

export function mergePatch(target, patch) {
  if (patch === null || typeof patch !== 'object' || Array.isArray(patch)) return structuredClone(patch);
  const out = target && typeof target === 'object' && !Array.isArray(target) ? { ...target } : {};
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) delete out[k];
    else out[k] = mergePatch(out[k], v);
  }
  return out;
}

const UNIT = { s: 1e3, m: 60e3, h: 3600e3, d: 86400e3 };

export function resolveTimes(v, now) {
  if (Array.isArray(v)) return v.map((x) => resolveTimes(x, now));
  if (v && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, resolveTimes(x, now)]));
  if (typeof v === 'string') {
    const m = /^@(now|([+-])(\d+)([smhd]))$/.exec(v);
    if (m) {
      const t = m[1] === 'now' ? now : now + (m[2] === '-' ? -1 : 1) * Number(m[3]) * UNIT[m[4]];
      return new Date(Math.floor(t / 1000) * 1000).toISOString().replace('.000Z', 'Z');
    }
  }
  return v;
}

export function baseDocs() {
  const docs = {};
  for (const f of readdirSync(join(FIXTURES, 'base'))) {
    if (f.endsWith('.json')) docs[basename(f, '.json')] = read(join(FIXTURES, 'base', f));
  }
  return docs;
}

// presets lists every preset with the snapshot a page would build from its
// GET answers: {name, preset, snap}.
export function presets(now = Date.now()) {
  const base = baseDocs();
  const out = [];
  for (const f of readdirSync(join(FIXTURES, 'presets')).sort()) {
    if (!f.endsWith('.json')) continue;
    const preset = read(join(FIXTURES, 'presets', f));
    const docs = resolveTimes(structuredClone(base), now);
    for (const [res, patch] of Object.entries(preset.patch ?? {})) docs[res] = mergePatch(docs[res], resolveTimes(patch, now));
    const stage = preset.sim?.stage;
    if (stage) {
      docs.update.busy = true;
      docs.update.progress = { phase: stage.phase, percent: stage.percent, bytes: 0, total: 0, version: docs.update.available?.version ?? '' };
    }
    const err = preset.errors?.['GET /sunshine'];
    out.push({
      name: basename(f, '.json'),
      preset,
      snap: {
        system: docs.system,
        sunshine: err ? null : docs.sunshine,
        sunshineError: err ? { status: err[0], message: err[1] } : null,
        display: docs.display,
        update: docs.update,
        power: docs.power,
      },
    });
  }
  return out;
}
