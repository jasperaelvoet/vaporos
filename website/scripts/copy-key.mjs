// Copies the release signing key into public/ so the site serves the exact
// file the build trusts (keys/release.pub is the single source). Runs before
// every build and dev start (package.json prebuild/predev).
//
// Source: VAPOROS_KEY_FILE (environment or .env*.local, loaded the way Next
// loads them), else ../keys/release.pub: the repo's key when the site lives
// in website/.
import { copyFileSync, existsSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import nextEnv from '@next/env';

const root = fileURLToPath(new URL('..', import.meta.url));
nextEnv.loadEnvConfig(root, process.env.npm_lifecycle_event === 'predev', { info() {}, error: console.error });

const src = resolve(root, process.env.VAPOROS_KEY_FILE || '../keys/release.pub');
const dst = resolve(root, 'public/release.pub');
if (!existsSync(src)) {
  console.error(`copy-key: ${src} is missing (build from website/ in the repo, or set VAPOROS_KEY_FILE)`);
  process.exit(1);
}
copyFileSync(src, dst);
console.log(`copy-key: public/release.pub updated from ${src}`);
