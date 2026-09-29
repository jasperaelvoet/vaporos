// The release public key, read at build time, for server components.
// First hit wins:
//   1. VAPOROS_KEY_FILE          an explicit path (env or .env.local), for a
//                                checkout outside the repo
//   2. ../keys/release.pub       the repo's key: the site is built from website/
//   3. public/release.pub        the copy scripts/copy-assets.mjs makes before
//                                every build and dev start
import 'server-only';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

function candidates(): string[] {
  const root = process.cwd();
  const list = [resolve(root, '../keys/release.pub'), resolve(root, 'public/release.pub')];
  if (process.env.VAPOROS_KEY_FILE) list.unshift(resolve(root, process.env.VAPOROS_KEY_FILE));
  return list;
}

function read(): string {
  const tried = candidates();
  for (const p of tried) {
    let k: string;
    try {
      k = readFileSync(p, 'utf8').trim();
    } catch {
      continue; // try the next one
    }
    // An ed25519 public key: base64 of the raw 32 bytes.
    if (!/^[A-Za-z0-9+/]{43}=$/.test(k) || Buffer.from(k, 'base64').length !== 32) {
      throw new Error(`${p} is not a base64 ed25519 public key`);
    }
    return k;
  }
  throw new Error(`release.pub not found (tried ${tried.join(', ')}): build from website/ or set VAPOROS_KEY_FILE`);
}

/** base64 of the raw 32-byte ed25519 key, exactly as in keys/release.pub. */
export const RELEASE_KEY = read();
