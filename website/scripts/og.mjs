#!/usr/bin/env node
// Renders an HTML file to a PNG, headless: the share card (public/og.png) is
// drawn as a 1200×630 page and photographed.
//
//   node scripts/og.mjs <card.html> <out.png> [--width=1200] [--height=630]
//
// Fonts must be loadable from the HTML (file:// URLs or data: URIs); the
// screenshot waits for document.fonts.ready. The browser comes from
// scripts/chrome.mjs (CHROME_PATH first).
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { chromium } from 'playwright-core';
import { headlessShell } from './chrome.mjs';

const argv = process.argv.slice(2);
const opt = Object.fromEntries(
  argv.filter((a) => a.startsWith('--')).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.slice(2), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
const [html, out] = argv.filter((a) => !a.startsWith('--'));
if (!html || !out) {
  console.error('usage: node scripts/og.mjs <card.html> <out.png> [--width=1200] [--height=630]');
  process.exit(2);
}
const width = Number(opt.width ?? 1200);
const height = Number(opt.height ?? 630);

const browser = await chromium.launch({ executablePath: headlessShell(), headless: true });
try {
  const page = await browser.newPage({ viewport: { width, height } });
  await page.goto(pathToFileURL(resolve(html)).href, { waitUntil: 'load' });
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: resolve(out) });
  console.log(`og: ${resolve(out)} (${width}×${height})`);
} finally {
  await browser.close();
}
