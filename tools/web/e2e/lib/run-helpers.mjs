// Small pieces run.mjs, nopoll.mjs and box.mjs share: flag parsing, the
// output directory, a worker pool and the report.

import { mkdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

// parseArgs reads --name=value, --name value (for flags that take a value,
// i.e. whose default isn't a boolean) and --flag. Unknown flags are an error,
// and so is anything that would show a browser window.
export function parseArgs(argv, known) {
  const out = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const m = a.match(/^--([a-z][a-z-]*)(?:=(.*))?$/);
    if (!m) throw new Error(`unexpected argument ${a}`);
    const [, name] = m;
    let value = m[2];
    if (['headed', 'show', 'attach', 'extension', 'debug'].includes(name)) {
      throw new Error(`--${name} is refused: the harness is headless only and never shows a window`);
    }
    if (!(name in known)) throw new Error(`unknown flag --${name}; known: ${Object.keys(known).map((k) => '--' + k).join(' ')}`);
    if (value === undefined && typeof known[name] !== 'boolean' && i + 1 < argv.length && !argv[i + 1].startsWith('--')) {
      value = argv[++i];
    }
    out[name] = value ?? true;
  }
  return { ...known, ...out };
}

export function list(v) {
  return v ? String(v).split(',').map((s) => s.trim()).filter(Boolean) : [];
}

// outDir is where a run writes: --out, else $SCRATCH/gates/<name>-<time>,
// else the system temp directory. Never the repository.
export function outDir(flag, name) {
  const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..*/, '').replace('T', '-');
  const dir = flag ? resolve(flag) : join(process.env.SCRATCH || process.env.RUNNER_TEMP || tmpdir(), 'gates', `${name}-${stamp}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

// pool runs fn over items with at most n at a time, in order of start.
export async function pool(items, n, fn) {
  const results = new Array(items.length);
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const i = next++;
      results[i] = await fn(items[i], i);
    }
  };
  await Promise.all(Array.from({ length: Math.min(n, items.length) }, worker));
  return results;
}

export function writeReport(dir, report, lines) {
  writeFileSync(join(dir, 'report.json'), JSON.stringify(report, null, 2) + '\n');
  writeFileSync(join(dir, 'summary.txt'), lines.join('\n') + '\n');
}

// slug makes a file name from a path and its matrix cell.
export function slug(...parts) {
  return parts
    .join('-')
    .replace(/^\/+/, '')
    .replace(/[^a-zA-Z0-9.-]+/g, '_')
    .replace(/^_+|_+$/g, '') || 'root';
}
