// Lets Node load the site's .ts and .tsx modules (JSX included) outside Next,
// transpiled by the project's own typescript: '@/x' resolves to src/x, and an
// extensionless './x' to './x.ts' or './x.tsx', as in tsconfig.json.
//
//   import { registerTsx } from './tests/unit/tsx-hooks.mjs'; registerTsx();
//
// For scripts/og.mjs and the share-card test. Run with --conditions=react-server
// when a module imports 'server-only'.
import { existsSync, readFileSync } from 'node:fs';
import { registerHooks } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import ts from 'typescript';

const SRC = new URL('../../src/', import.meta.url);
const EXT = ['.ts', '.tsx', '/index.ts', '/index.tsx'];
let registered = false;

export function registerTsx() {
  if (registered) return;
  registered = true;
  registerHooks({
    resolve(spec, ctx, next) {
      if (spec.startsWith('@/')) spec = new URL(spec.slice(2), SRC).href;
      // next has no exports map: its entry points (next/og, next/link) are files.
      if (/^next\/[\w-]+$/.test(spec)) spec = `${spec}.js`;
      const local = spec.startsWith('./') || spec.startsWith('../') || spec.startsWith('file:');
      if (local && !/\.[cm]?[jt]sx?$/.test(spec.replace(/[?#].*$/, ''))) {
        const base = new URL(spec, ctx.parentURL ?? pathToFileURL(process.cwd() + '/'));
        for (const ext of EXT) {
          const u = new URL(base.href.replace(/[?#].*$/, '') + ext);
          if (existsSync(fileURLToPath(u))) return next(u.href, ctx);
        }
      }
      return next(spec, ctx);
    },
    load(url, ctx, next) {
      if (!/\.tsx?$/.test(url.replace(/[?#].*$/, '')) || !url.startsWith('file:')) return next(url, ctx);
      const file = fileURLToPath(url.replace(/[?#].*$/, ''));
      const out = ts.transpileModule(readFileSync(file, 'utf8'), {
        fileName: file,
        compilerOptions: {
          module: ts.ModuleKind.ESNext,
          target: ts.ScriptTarget.ES2022,
          jsx: ts.JsxEmit.ReactJSX,
          verbatimModuleSyntax: false,
          isolatedModules: true,
        },
      });
      return { format: 'module', source: out.outputText, shortCircuit: true };
    },
  });
}
