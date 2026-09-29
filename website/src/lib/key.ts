// The release public key, read from the repo at build time. The site is
// built from website/, so keys/ is one level up; public/release.pub (copied
// by scripts/copy-key.mjs) is the fallback.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

function read(): string {
  for (const p of [resolve(process.cwd(), '../keys/release.pub'), resolve(process.cwd(), 'public/release.pub')]) {
    try {
      const k = readFileSync(p, 'utf8').trim();
      if (k) return k;
    } catch {
      // try the next one
    }
  }
  throw new Error('release.pub not found: run the build from website/');
}

export const RELEASE_KEY = read();
