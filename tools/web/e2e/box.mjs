#!/usr/bin/env node
// Read-only HTTP checks against a real box (VM window A, MASTER-PLAN §6.6),
// or against a dev server. A skeleton: it covers every page path and old
// URL, the headers and CSP, gzip and caching of each asset the pages name
// (stylesheets, scripts, modulepreload links, icons, fonts). It only sends
// GETs and never signs in.
//
//   node tools/web/e2e/box.mjs --base=http://192.168.1.50 [--ui=legacy|next]
//        [--host=vapor.local] [--out=DIR]
//
// --host sends another Host header, for a dev server reached by address.
//
// Still to come (C0b): the signed-in walk at 390 and 1440 in both schemes,
// the 6-minute idle with only /events and /ping, and safe writes reverted.

import { request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import { gunzipSync } from 'node:zlib';

import { ROUTES } from './lib/presets.mjs';
import { outDir, parseArgs, writeReport } from './lib/run-helpers.mjs';
import { activeUI } from './lib/server.mjs';

const args = parseArgs(process.argv.slice(2), { base: '', ui: '', host: '', out: '' });
if (!args.base) {
  console.error('box: --base=http://<box address> is required');
  process.exit(2);
}
const base = new URL(args.base);
const uiName = args.ui || activeUI();
const out = outDir(args.out, 'box');

// The new UI's old URLs and where they land (ARCH §8.2).
const OLD_URLS = {
  '/pair': '/devices',
  '/streaming': '/screen',
  '/display': '/screen',
  '/storage': '/system/storage',
  '/updates': '/system/updates',
  '/power': '/system/power',
  '/advanced': '/system/settings',
};

function get(path, headers = {}) {
  const url = new URL(path, base);
  const req = url.protocol === 'https:' ? httpsRequest : httpRequest;
  return new Promise((resolve, reject) => {
    const r = req(url, { method: 'GET', headers: { ...(args.host ? { Host: args.host } : {}), ...headers }, timeout: 10_000 }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => {
        let body = Buffer.concat(chunks);
        if (res.headers['content-encoding'] === 'gzip') body = gunzipSync(body);
        resolve({ status: res.statusCode, headers: res.headers, body: body.toString('utf8'), raw: Buffer.concat(chunks).length });
      });
    });
    r.on('timeout', () => r.destroy(new Error(`GET ${path} timed out`)));
    r.on('error', reject);
    r.end();
  });
}

const results = [];
function check(what, ok, detail = '') {
  results.push({ what, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${what}${detail ? `: ${detail}` : ''}`);
}

const assetRe = /<(?:link|script)\b[^>]*\b(?:href|src)="(\/static\/[^"]+)"/g;
const pages = [...new Set([...Object.values(ROUTES[uiName]), '/login', '/setup'])];
const assets = new Set();

try {
  const ping = await get('/api/v1/ping');
  check('GET /api/v1/ping', ping.status === 200, `${ping.status} ${ping.body.slice(0, 80)}`);
  const installer = /"mode":"installer"/.test(ping.body);

  for (const p of installer ? ['/setup'] : pages) {
    const r = await get(p, { 'Accept-Encoding': 'gzip' });
    const csp = r.headers['content-security-policy'] ?? '';
    check(`GET ${p}`, r.status === 200, `${r.status}`);
    check(`${p} is HTML, revalidated`, r.headers['content-type'] === 'text/html; charset=utf-8' && r.headers['cache-control'] === 'no-cache',
      `${r.headers['content-type']}; ${r.headers['cache-control']}`);
    check(`${p} has the CSP`, csp.includes("default-src 'self'") && csp.includes("frame-ancestors 'none'"), csp);
    check(`${p} has nosniff`, r.headers['x-content-type-options'] === 'nosniff');
    if (uiName === 'next') check(`${p} is gzipped with an ETag`, r.headers['content-encoding'] === 'gzip' && !!r.headers.etag);
    for (const m of r.body.matchAll(assetRe)) assets.add(m[1]);
  }

  for (const a of [...assets].sort()) {
    const r = await get(a, { 'Accept-Encoding': 'gzip' });
    check(`GET ${a}`, r.status === 200, `${r.status}`);
    check(`${a} is immutable`, r.headers['cache-control'] === 'public, max-age=31536000, immutable', r.headers['cache-control']);
    if (/\.(css|js|svg|webmanifest)$/.test(a) && r.raw > 1024) check(`${a} is gzipped`, r.headers['content-encoding'] === 'gzip');
    if (r.headers.etag) {
      const again = await get(a, { 'If-None-Match': r.headers.etag, 'Accept-Encoding': 'gzip' });
      check(`${a} revalidates`, again.status === 304, `${again.status}`);
    }
  }

  if (uiName === 'next' && !installer) {
    for (const [from, to] of Object.entries(OLD_URLS)) {
      const r = await get(`${from}?x=1`);
      const loc = r.headers.location ?? '';
      check(`old URL ${from}`, r.status === 303 && loc.startsWith(to) && loc.includes('x=1') && r.headers['cache-control'] === 'no-store',
        `${r.status} → ${loc}; ${r.headers['cache-control']}`);
    }
  }
  for (const p of ['/nope', '/index.html']) {
    const r = await get(p);
    check(`GET ${p} is 404`, r.status === 404, `${r.status}`);
  }
} catch (err) {
  check('the box answers', false, String(err?.message ?? err));
}

const failed = results.filter((r) => !r.ok);
writeReport(out, { base: String(base), ui: uiName, results }, [
  `${results.length} checks, ${failed.length} failed against ${base} (${uiName} UI)`,
  ...failed.map((r) => `FAIL ${r.what}: ${r.detail}`),
]);
console.log(`box: ${results.length} checks, ${failed.length} failed; report in ${out}`);
process.exit(failed.length ? 1 : 0);
