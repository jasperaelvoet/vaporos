// Lets Node run the site's TypeScript directly (Node 24 strips the types):
// './x' resolves to './x.ts', and '@/x' to 'src/x.ts', as in tsconfig.json.
//
//   node --conditions=react-server --import ./tests/unit/check-ts-hooks.mjs <script>
//
// react-server makes `import 'server-only'` resolve to its empty module.
import { registerHooks } from 'node:module';
import { pathToFileURL } from 'node:url';

const SRC = pathToFileURL(new URL('../../src/', import.meta.url).pathname).href;
const hasExt = (s) => /\.[cm]?[jt]sx?$/.test(s.replace(/[?#].*$/, ''));
// 'x?v=1' → 'x.ts?v=1' (tests import a fresh module instance with a query).
const withTs = (s) => s.replace(/^([^?#]*)/, '$1.ts');

registerHooks({
  resolve(spec, ctx, next) {
    if (spec.startsWith('@/')) spec = SRC + spec.slice(2);
    if ((spec.startsWith('./') || spec.startsWith('../') || spec.startsWith('file:')) && !hasExt(spec)) {
      try {
        return next(withTs(spec), ctx);
      } catch {}
    }
    return next(spec, ctx);
  },
});
