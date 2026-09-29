// The dev server for the harness: internal/web's TestDevServer, built once
// into a test binary and run once per preset on a free port. One process per
// server, never `go test` itself: killing go test can orphan its child.

import { spawn, spawnSync } from 'node:child_process';
import { createWriteStream, mkdirSync, readFileSync } from 'node:fs';
import { request } from 'node:http';
import { createServer } from 'node:net';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { presetEnv } from './presets.mjs';

export const REPO = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', '..');
const PKG = join(REPO, 'internal', 'web');

// The UI set internal/web serves in production, read from web.go.
export function activeUI() {
  const src = readFileSync(join(PKG, 'web.go'), 'utf8');
  const m = src.match(/^var activeSet = (\w+)Set$/m);
  return m ? m[1] : 'legacy';
}

// buildTestBinary compiles internal/web's tests once into dir/web.test.
export function buildTestBinary(dir) {
  mkdirSync(dir, { recursive: true });
  const bin = join(dir, 'web.test');
  const r = spawnSync('go', ['test', '-c', '-o', bin, './internal/web'], { cwd: REPO, encoding: 'utf8' });
  if (r.status !== 0) throw new Error(`go test -c ./internal/web failed:\n${r.stderr || r.stdout || r.error}`);
  return bin;
}

export function freePort() {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.unref();
    s.on('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });
}

// get sends one GET to 127.0.0.1:port with the given Host header and
// resolves to { status, headers, body }.
export function get(port, path, host = `127.0.0.1:${port}`, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = request({ host: '127.0.0.1', port, path, method: 'GET', headers: { Host: host, ...headers }, timeout: 5000 }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks).toString('utf8') }));
    });
    req.on('timeout', () => req.destroy(new Error(`GET ${path} timed out`)));
    req.on('error', reject);
    req.end();
  });
}

const running = new Set();

function stopAllNow() {
  for (const child of running) child.kill('SIGKILL');
  running.clear();
}
process.on('exit', stopAllNow);
for (const sig of ['SIGINT', 'SIGTERM']) {
  process.on(sig, () => {
    stopAllNow();
    process.exit(130);
  });
}

// startServer runs the dev server in preset p and waits until it answers.
// It resolves to { port, origin, host, features, stop() }.
//   origin:   http://vapor.local:<port>, the box's insecure origin; the
//             browser maps *.local to 127.0.0.1. A dev server that does not
//             accept that host name (before v2 it has no hostname file) is
//             reached as vaporos-setup.local, which vosd always accepts.
//   features: { presets } is true when the server knows VOS_WEB_PRESET.
export async function startServer({ bin, p, ui, logDir, stateDir, extraEnv = {} }) {
  const port = await freePort();
  mkdirSync(logDir, { recursive: true });
  mkdirSync(stateDir, { recursive: true });
  const log = join(logDir, `${p.name}-${port}.log`);
  const out = createWriteStream(log);
  const child = spawn(bin, ['-test.run', '^TestDevServer$', '-test.timeout', '0', '-test.count', '1', '-test.v'], {
    cwd: PKG,
    env: {
      ...process.env,
      ...presetEnv(p),
      ...extraEnv,
      VOS_WEB_DEV: `127.0.0.1:${port}`,
      VOS_WEB_UI: ui,
      VOS_WEB_STATE: stateDir,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  running.add(child);
  child.stdout.pipe(out);
  child.stderr.pipe(out);
  let exited = null;
  child.on('exit', (code, signal) => {
    exited = { code, signal };
    running.delete(child);
  });
  const stop = () =>
    new Promise((resolve) => {
      if (exited) return resolve();
      child.once('exit', () => resolve());
      child.kill('SIGTERM');
      setTimeout(() => child.kill('SIGKILL'), 3000).unref();
    });

  const deadline = Date.now() + 30_000;
  for (;;) {
    if (exited) {
      const tail = readFileSync(log, 'utf8').split('\n').slice(-30).join('\n');
      throw new Error(`dev server for preset ${p.name} exited (${JSON.stringify(exited)}):\n${tail}`);
    }
    try {
      const r = await get(port, '/api/v1/ping');
      if (r.status === 200) break;
    } catch {
      // not listening yet
    }
    if (Date.now() > deadline) {
      await stop();
      throw new Error(`dev server for preset ${p.name} did not answer /api/v1/ping within 30 s (log: ${log})`);
    }
    await new Promise((r) => setTimeout(r, 100));
  }

  const features = { presets: (await get(port, '/__dev/state')).status === 200 };
  let host = 'vapor.local';
  if ((await get(port, '/setup', `${host}:${port}`)).status === 421) host = 'vaporos-setup.local';
  // /setup renders in every mode (first run or installer), so its
  // stylesheet link tells which UI set the server really serves.
  const page = await get(port, '/setup', `${host}:${port}`);
  const served = /\/legacy\/app\.css"/.test(page.body) ? 'legacy' : 'next';
  return { port, host, origin: `http://${host}:${port}`, features, served, log, stop };
}
