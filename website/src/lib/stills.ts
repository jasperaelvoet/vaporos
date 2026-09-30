// The TV stills: real renders of the welcome screen, for server components.
// pages.yml writes them into public/tv before the build, with stills.json
// beside them (internal/display/welcome/stills_test.go):
//
//   VOS_WELCOME_STILLS="$PWD/website/public/tv" go test ./internal/display/welcome -run 'TestExportStills$'
//
// They are not committed, so a local build or `next dev` usually has none:
// getStill() then answers null and the page draws its own picture (TvStill's
// fallback). On a still, the QR code opens this website: the installed
// system's stills open the site's root, or its live demo when that is on
// (stills.json `qr`; pages.yml sets VOS_WELCOME_STILLS_QR to the site's /demo/
// when src/content/demo.ts has `enabled: true`), and the installer's open the
// install guide, since the demo leaves the installer out (each still's `qr`).
//
//   VAPOROS_STILLS_REQUIRED=1   no stills.json fails the build (pages.yml), so
//                               the site never deploys the drawings by mistake
//
// A stills.json that is there but broken (a name or file missing, a field of
// the wrong kind) always fails the build.
import 'server-only';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { site } from '@/content/site';
import { withBase } from './base-path';

/** The screens the site has stills of: the fixtures TestExportStills renders. */
export const STILL_NAMES = ['installer-ready', 'ready', 'streaming', 'pairing', 'installer-running', 'no-gpu'] as const;
export type StillName = (typeof STILL_NAMES)[number];

export interface Still {
  name: StillName;
  /** The image's base-aware URL, with its hash as a cache buster. */
  src: string;
  width: number;
  height: number;
  /** What the screen shows, in words: its text alternative. */
  alt: string;
  /** The screen's status line ("Ready to stream") and its tone ("ready"). */
  status: string;
  tone: string;
  /** What its QR code opens. */
  qr: string;
}

export interface StillSet {
  /** What the installed system's stills' QR codes open. */
  qr: string;
  stills: Record<StillName, Still>;
}

/** Where the stills are, from the project directory (the build's cwd). */
export const STILLS_DIR = 'public/tv';

/** What the installed system's stills' QR codes may open: this site, or its live demo. */
const QR_TARGETS = [site.url, `${site.url}demo/`];
/** What the installer's stills' QR codes open: the install guide. */
const QR_INSTALL = `${site.url}install/`;

const FILE = /^[a-z0-9-]+\.(?:jpg|png)$/;
const HASH = /^[0-9a-f]{8,64}$/;

function fail(msg: string): never {
  throw new Error(`stills: ${msg}`);
}

const text = (o: Record<string, unknown>, key: string, where: string, empty = false): string => {
  const v = o[key];
  if (typeof v !== 'string' || (!empty && !v.trim())) fail(`${where}: "${key}" is not a string${empty ? '' : ' with text'}`);
  return v;
};

const size = (o: Record<string, unknown>, key: string, where: string): number => {
  const v = o[key];
  if (typeof v !== 'number' || !Number.isInteger(v) || v <= 0) fail(`${where}: "${key}" is not a positive whole number`);
  return v;
};

/**
 * stills.json, checked: every name in STILL_NAMES with a file that `exists`
 * (a name relative to the stills' directory). Throws on anything else.
 */
export function parseStills(json: unknown, exists: (file: string) => boolean): StillSet {
  if (!json || typeof json !== 'object' || Array.isArray(json)) fail('stills.json is not an object');
  const top = json as Record<string, unknown>;
  const qr = text(top, 'qr', 'stills.json');
  if (!QR_TARGETS.includes(qr)) fail(`stills.json: "qr" ${JSON.stringify(qr)} is not this site (${QR_TARGETS.join(' or ')})`);
  const list = top.stills;
  if (!list || typeof list !== 'object' || Array.isArray(list)) fail('stills.json: "stills" is not an object');
  const stills = {} as Record<StillName, Still>;
  for (const name of STILL_NAMES) {
    const where = `stills.json "${name}"`;
    const s = (list as Record<string, unknown>)[name];
    if (!s || typeof s !== 'object' || Array.isArray(s)) fail(`${where} is missing`);
    const o = s as Record<string, unknown>;
    const file = text(o, 'file', where);
    if (!FILE.test(file)) fail(`${where}: "file" ${JSON.stringify(file)} is not a plain .jpg or .png name`);
    if (!exists(file)) fail(`${where}: ${file} is missing`);
    const v = text(o, 'v', where);
    if (!HASH.test(v)) fail(`${where}: "v" ${JSON.stringify(v)} is not a hex hash`);
    const stillQr = text(o, 'qr', where);
    const wantQr = name.startsWith('installer-') ? QR_INSTALL : qr;
    if (stillQr !== wantQr) fail(`${where}: "qr" ${JSON.stringify(stillQr)} is not ${JSON.stringify(wantQr)}`);
    stills[name] = {
      name,
      src: withBase(`/tv/${file}?v=${v}`),
      width: size(o, 'w', where),
      height: size(o, 'h', where),
      alt: text(o, 'alt', where),
      status: text(o, 'status', where),
      tone: text(o, 'tone', where, true),
      qr: stillQr,
    };
  }
  return { qr, stills };
}

function load(): StillSet | null {
  const dir = resolve(process.cwd(), STILLS_DIR);
  const manifest = resolve(dir, 'stills.json');
  if (!existsSync(manifest)) {
    if (process.env.VAPOROS_STILLS_REQUIRED === '1') {
      fail(`${manifest} is missing: VOS_WELCOME_STILLS="$PWD/website/public/tv" go test ./internal/display/welcome -run 'TestExportStills$' writes it`);
    }
    return null;
  }
  let json: unknown;
  try {
    json = JSON.parse(readFileSync(manifest, 'utf8'));
  } catch (e) {
    fail(`${manifest}: ${e instanceof Error ? e.message : String(e)}`);
  }
  return parseStills(json, (file) => existsSync(resolve(dir, file)));
}

let cached: StillSet | null | undefined;

/** Every still, or null when this build has none. Read once per build. */
export function getStills(): StillSet | null {
  if (cached === undefined) cached = load();
  return cached;
}

/** One still, or null when this build has none (draw the screen instead). */
export function getStill(name: StillName): Still | null {
  return getStills()?.stills[name] ?? null;
}
