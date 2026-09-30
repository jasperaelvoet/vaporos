// The site's three families, self-hosted with next/font/local. The files are
// copied into src/fonts/ by scripts/copy-assets.mjs at prebuild/predev, from
// design/fonts (the variable Anybody) and the control center's WOFF2 subsets
// (Mona Sans, Martian Mono), each checked against design/fonts/fonts.json.
//
// Put all three `variable` classes on <html> (fontVariables); the theme in
// src/styles/theme.css maps them to font-display, font-ui and font-mono.
//
// Anybody is the variable cut (wdth 56-136, wght 300-860): the site travels
// between the device's three static cuts (cold 72/560, warm 100/700, hot
// 124/800) with font-stretch and font-weight. Declaring the width range lets
// `font-stretch: 124%` pick the wdth axis directly (src/styles/type.css).
//
// Preloads: Mona Sans, its two weights (19 kB each). Every page's Largest
// Contentful Paint is a paragraph set in it (the hero's lead, a page's
// lead): the headlines are one block per line, so no line outgrows the
// paragraph, and the first screen's kicker and buttons are its semibold.
// Anybody (48.9 kB) and Martian Mono (8.6 kB) swap in with metric-matched
// fallbacks; the headlines are fitted per line, so the swap never reflows
// them (WEB §12: at most two preloads, 110 kB).
import localFont from 'next/font/local';

export const anybody = localFont({
  src: '../fonts/anybody-variable.woff2',
  weight: '300 860',
  style: 'normal',
  display: 'swap',
  preload: false,
  variable: '--font-anybody',
  declarations: [{ prop: 'font-stretch', value: '56% 136%' }],
  adjustFontFallback: 'Arial',
  fallback: ['Arial Narrow', 'Helvetica Neue', 'Arial', 'sans-serif'],
});

export const monaSans = localFont({
  src: [
    { path: '../fonts/monasans-regular.woff2', weight: '400', style: 'normal' },
    { path: '../fonts/monasans-semibold.woff2', weight: '600', style: 'normal' },
  ],
  display: 'swap',
  preload: true,
  variable: '--font-mona',
  adjustFontFallback: 'Arial',
  fallback: ['-apple-system', 'Segoe UI', 'system-ui', 'sans-serif'],
});

export const martianMono = localFont({
  src: [
    { path: '../fonts/martianmono-medium.woff2', weight: '500', style: 'normal' },
    { path: '../fonts/martianmono-semibold.woff2', weight: '600', style: 'normal' },
  ],
  display: 'swap',
  preload: false,
  variable: '--font-martian',
  adjustFontFallback: false,
  fallback: ['ui-monospace', 'SF Mono', 'Menlo', 'Consolas', 'monospace'],
});

/** The classes that define --font-anybody, --font-mona and --font-martian: put them on <html>. */
export const fontVariables = `${anybody.variable} ${monaSans.variable} ${martianMono.variable}`;
