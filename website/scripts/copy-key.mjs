// Copies the release signing key into public/ so the site serves the exact
// file the build trusts (keys/release.pub is the single source).
import { copyFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const src = fileURLToPath(new URL('../../keys/release.pub', import.meta.url));
const dst = fileURLToPath(new URL('../public/release.pub', import.meta.url));
if (!existsSync(src)) {
  console.error(`copy-key: ${src} is missing`);
  process.exit(1);
}
copyFileSync(src, dst);
console.log('copy-key: public/release.pub updated');
