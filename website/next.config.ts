import type { NextConfig } from 'next';

// Served from GitHub Pages at https://jasperaelvoet.github.io/vaporos/.
// `next build` writes a fully static site to out/; GitHub Pages serves it.
const basePath = '/vaporos';

const nextConfig: NextConfig = {
  output: 'export',
  basePath,
  // /download/ is written as out/download/index.html, the layout GitHub Pages
  // serves without redirects.
  trailingSlash: true,
  // A static host has no image optimizer.
  images: { unoptimized: true },
  // next/link and the metadata API add the base path themselves; plain <a>,
  // <img>, CSS url() and fetch() don't. src/lib/base-path.ts reads this
  // (inlined at build time, so it works in server and client code).
  env: { NEXT_PUBLIC_BASE_PATH: basePath },
};

export default nextConfig;
