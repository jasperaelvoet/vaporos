// The live demo's export, for server components: public/demo/ui, written by
// TestExportDemo before the build (pages.yml, when src/content/demo.ts has
// `enabled: true`):
//
//   VOS_WEB_EXPORT="$PWD/website/public/demo/ui" go test ./internal/web -run 'TestExportDemo$'
//
// It is not committed. getDemo() reads its demo-manifest.json once per build
// and answers null without it (a local build, `next dev`): the stage then
// says the demo is missing instead of framing a page that isn't there.
//
//   VAPOROS_DEMO_REQUIRED=1   no manifest fails the build (pages.yml sets it
//                             when the demo is on), so the site never
//                             deploys a demo page without its demo
//
// A manifest that is there but doesn't fit (another base path, a scenario
// or page the site names that the demo lacks) always fails the build.
import 'server-only';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { DEMO_OPEN, demo } from '@/content/demo';
import { BASE_PATH } from '@/lib/base-path';
import { SCENARIO_IDS } from '@/lib/demo-protocol';

export interface DemoExport {
  /** The export's hash: the iframe's ?v=, and what its ready message names. */
  hash: string;
  /** The scripts the parent may run. */
  scenarios: string[];
}

export const DEMO_DIR = 'public/demo/ui';

function fail(msg: string): never {
  throw new Error(`demo: ${msg}`);
}

/** demo-manifest.json, checked against what the site asks of it. */
export function parseDemoManifest(json: unknown): DemoExport {
  if (!json || typeof json !== 'object' || Array.isArray(json)) fail('demo-manifest.json is not an object');
  const m = json as Record<string, unknown>;
  const want = `${BASE_PATH}/demo/ui`;
  if (m.base !== want) fail(`demo-manifest.json: "base" ${JSON.stringify(m.base)} is not ${want}`);
  if (typeof m.hash !== 'string' || !/^[0-9a-f]{8,64}$/.test(m.hash)) fail('demo-manifest.json: "hash" is not a hex hash');
  const list = (key: string) => {
    const v = m[key];
    if (!Array.isArray(v) || !v.every((x) => typeof x === 'string')) fail(`demo-manifest.json: "${key}" is not a list of strings`);
    return v as string[];
  };
  const scenarios = list('scenarios');
  const lacking = SCENARIO_IDS.filter((s) => !scenarios.includes(s));
  if (lacking.length) fail(`demo-manifest.json: no script for the scenario(s) ${lacking.join(', ')} (src/lib/demo-protocol.ts)`);
  const pages = list('pages');
  const missing = DEMO_OPEN.filter((p) => !pages.includes(`/${p}`));
  if (missing.length) fail(`demo-manifest.json: no page for ${missing.map((p) => `/${p}`).join(', ')} (src/content/demo.ts DEMO_OPEN)`);
  return { hash: m.hash, scenarios };
}

function load(): DemoExport | null {
  const file = resolve(process.cwd(), DEMO_DIR, 'demo-manifest.json');
  if (!existsSync(file)) {
    if (process.env.VAPOROS_DEMO_REQUIRED === '1') {
      fail(`${file} is missing: VOS_WEB_EXPORT="$PWD/website/public/demo/ui" go test ./internal/web -run 'TestExportDemo$' writes it`);
    }
    return null;
  }
  let json: unknown;
  try {
    json = JSON.parse(readFileSync(file, 'utf8'));
  } catch (e) {
    fail(`${file}: ${e instanceof Error ? e.message : String(e)}`);
  }
  return parseDemoManifest(json);
}

let cached: DemoExport | null | undefined;

/** The demo's export, or null when the demo is off or this build has none. */
export function getDemo(): DemoExport | null {
  if (!demo.enabled) return null;
  if (cached === undefined) cached = load();
  return cached;
}
