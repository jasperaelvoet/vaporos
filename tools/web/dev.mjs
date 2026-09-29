#!/usr/bin/env node
// The control center's dev loop: the stylesheet watch and the dev server
// with live reload, side by side. It never opens a browser.
//
//   npm --prefix tools/web run dev [-- --port=8081] [-- --preset=streaming] [-- --ui=next]
//
// The dev server is internal/web's TestDevServer (fake services behind the
// real API core), built once into tools/web/.cache/web.test. Ctrl-C stops
// both.

import { spawn } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { list, parseArgs } from './e2e/lib/run-helpers.mjs';
import { buildTestBinary, REPO } from './e2e/lib/server.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const args = parseArgs(process.argv.slice(2), { port: process.env.PORT || '8081', preset: '', ui: '' });
const port = Number(args.port);
const cache = join(here, '.cache');
mkdirSync(cache, { recursive: true });

console.log('dev: building the dev server');
const bin = buildTestBinary(cache);

const children = [];
process.on('exit', () => {
  for (const c of children) if (c.exitCode === null) c.kill('SIGTERM');
});
const stop = (code) => process.exit(code);
process.on('SIGINT', () => stop(130));
process.on('SIGTERM', () => stop(143));

const css = spawn(process.execPath, [join(here, 'css.mjs'), '--watch'], { stdio: 'inherit' });
children.push(css);

const env = { ...process.env, VOS_WEB_DEV: `127.0.0.1:${port}`, VOS_WEB_LIVE: '1' };
if (args.preset) env.VOS_WEB_PRESET = list(args.preset)[0];
if (args.ui) env.VOS_WEB_UI = args.ui;
const server = spawn(bin, ['-test.run', '^TestDevServer$', '-test.timeout', '0', '-test.count', '1', '-test.v'], {
  cwd: join(REPO, 'internal', 'web'),
  env,
  stdio: 'inherit',
});
children.push(server);

console.log(
  `dev: http://127.0.0.1:${port} (a secure context: the clipboard and similar APIs work here but not on the box;\n` +
    `     the harness, npm run e2e, tests the box's insecure origin http://vapor.local:<port>)`,
);
for (const c of children) c.on('exit', (code) => stop(code ?? 1));
