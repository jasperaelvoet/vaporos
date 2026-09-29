#!/usr/bin/env node
// A zero-dependency static server that behaves like GitHub Pages for the
// VaporOS site: the export in <outDir> is served under /vaporos/.
//
//   node scripts/serve.mjs <outDir> [port=4329] [--base=/vaporos] [--quiet]
//
//   /vaporos/x/          → <outDir>/x/index.html
//   /vaporos/x           → 301 /vaporos/x/ when x is a directory; else x.html if it exists
//   /vaporos/missing     → <outDir>/404.html with status 404
//   /                    → 302 /vaporos/ (convenience; GitHub Pages would show another site)
// GET and HEAD only, single byte ranges (for video), no caching, correct MIME
// types (.wasm, .glb, .gltf, .ktx2, .woff2, .txt RSC payloads and more).
import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { createServer } from 'node:http';
import { extname, join, resolve, sep } from 'node:path';

const args = process.argv.slice(2);
const flags = Object.fromEntries(
  args.filter((a) => a.startsWith('--')).map((a) => {
    const [k, v] = a.slice(2).split('=');
    return [k, v ?? true];
  }),
);
const [outArg, portArg] = args.filter((a) => !a.startsWith('--'));
if (!outArg) {
  console.error('usage: node scripts/serve.mjs <outDir> [port=4329] [--base=/vaporos] [--quiet]');
  process.exit(2);
}
const ROOT = resolve(outArg);
const PORT = Number(portArg ?? 4329);
const BASE = String(flags.base ?? '/vaporos').replace(/\/+$/, '');
const QUIET = !!flags.quiet;

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.htm': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.cjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.map': 'application/json; charset=utf-8',
  '.webmanifest': 'application/manifest+json; charset=utf-8',
  '.xml': 'application/xml; charset=utf-8',
  '.txt': 'text/plain; charset=utf-8', // Next's RSC payloads for client navigation
  '.md': 'text/markdown; charset=utf-8',
  '.pub': 'text/plain; charset=utf-8',
  '.csv': 'text/csv; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.avif': 'image/avif',
  '.ico': 'image/x-icon',
  '.bmp': 'image/bmp',
  '.ktx2': 'image/ktx2',
  '.hdr': 'image/vnd.radiance',
  '.exr': 'image/x-exr',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.ttf': 'font/ttf',
  '.otf': 'font/otf',
  '.wasm': 'application/wasm',
  '.glb': 'model/gltf-binary',
  '.gltf': 'model/gltf+json',
  '.obj': 'model/obj',
  '.usdz': 'model/vnd.usdz+zip',
  '.bin': 'application/octet-stream',
  '.drc': 'application/octet-stream',
  '.basis': 'application/octet-stream',
  '.riv': 'application/octet-stream',
  '.lottie': 'application/zip',
  '.glsl': 'text/plain; charset=utf-8',
  '.vert': 'text/plain; charset=utf-8',
  '.frag': 'text/plain; charset=utf-8',
  '.mp4': 'video/mp4',
  '.webm': 'video/webm',
  '.mov': 'video/quicktime',
  '.m4v': 'video/mp4',
  '.mp3': 'audio/mpeg',
  '.ogg': 'audio/ogg',
  '.wav': 'audio/wav',
  '.m4a': 'audio/mp4',
  '.flac': 'audio/flac',
  '.pdf': 'application/pdf',
  '.zip': 'application/zip',
};
const typeOf = (file) => TYPES[extname(file).toLowerCase()] ?? 'application/octet-stream';

async function isFile(p) {
  try {
    return (await stat(p)).isFile();
  } catch {
    return false;
  }
}
async function isDir(p) {
  try {
    return (await stat(p)).isDirectory();
  } catch {
    return false;
  }
}

function log(status, req, note = '') {
  if (!QUIET) console.log(`${status} ${req.method} ${req.url}${note ? `  ${note}` : ''}`);
}

async function send(req, res, file, status = 200) {
  const info = await stat(file);
  const headers = { 'Content-Type': typeOf(file), 'Cache-Control': 'no-store', 'Accept-Ranges': 'bytes' };
  let start = 0;
  let end = info.size - 1;
  const range = status === 200 && /^bytes=(\d*)-(\d*)$/.exec(req.headers.range ?? '');
  if (range && (range[1] || range[2])) {
    if (range[1]) {
      start = Number(range[1]);
      if (range[2]) end = Math.min(Number(range[2]), end);
    } else {
      start = Math.max(0, info.size - Number(range[2]));
    }
    if (start > end || start >= info.size) {
      res.writeHead(416, { 'Content-Range': `bytes */${info.size}` }).end();
      return log(416, req);
    }
    status = 206;
    headers['Content-Range'] = `bytes ${start}-${end}/${info.size}`;
  }
  headers['Content-Length'] = info.size ? end - start + 1 : 0;
  res.writeHead(status, headers);
  log(status, req, status === 404 ? '→ 404.html' : '');
  if (req.method === 'HEAD' || !info.size) return res.end();
  createReadStream(file, { start, end }).pipe(res);
}

async function notFound(req, res) {
  const page = join(ROOT, '404.html');
  if (await isFile(page)) return send(req, res, page, 404);
  res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' }).end('404 Not Found (no 404.html in the export)\n');
  log(404, req);
}

function redirect(req, res, to, code = 301) {
  res.writeHead(code, { Location: to, 'Cache-Control': 'no-store' }).end();
  log(code, req, `→ ${to}`);
}

const server = createServer(async (req, res) => {
  try {
    if (req.method !== 'GET' && req.method !== 'HEAD') {
      res.writeHead(405, { Allow: 'GET, HEAD' }).end();
      return log(405, req);
    }
    const url = new URL(req.url ?? '/', 'http://localhost');
    let path;
    try {
      path = decodeURIComponent(url.pathname);
    } catch {
      res.writeHead(400).end();
      return log(400, req);
    }
    if (path === '/' && BASE) return redirect(req, res, `${BASE}/`, 302);
    if (BASE && path === BASE) return redirect(req, res, `${BASE}/${url.search}`);
    if (BASE && !path.startsWith(`${BASE}/`)) return notFound(req, res);

    const rel = path.slice(BASE.length); // starts with '/'
    const file = resolve(join(ROOT, rel));
    if (file !== ROOT && !file.startsWith(ROOT + sep)) return notFound(req, res); // no escaping the export

    if (await isDir(file)) {
      if (!path.endsWith('/')) return redirect(req, res, `${path}/${url.search}`);
      const index = join(file, 'index.html');
      return (await isFile(index)) ? send(req, res, index) : notFound(req, res);
    }
    if (await isFile(file)) return send(req, res, file);
    if (!extname(file) && (await isFile(`${file}.html`))) return send(req, res, `${file}.html`);
    return notFound(req, res);
  } catch (err) {
    console.error(err);
    if (!res.headersSent) res.writeHead(500).end();
  }
});

server.on('error', (err) => {
  console.error(err.code === 'EADDRINUSE' ? `serve: port ${PORT} is in use` : err);
  process.exit(1);
});
server.listen(PORT, '127.0.0.1', () => {
  console.log(`serve: ${ROOT} at http://127.0.0.1:${PORT}${BASE}/`);
});
