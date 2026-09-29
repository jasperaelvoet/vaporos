// Which Chromium the site's headless tools launch (shoot, og, the e2e checks).
//
//   CHROME_PATH   an explicit binary, used first (CI: $(command -v google-chrome))
//   CHROME_FULL   an explicit full Chromium, for the WebGL candidates in shoot.mjs
//   otherwise     the newest Playwright download in its cache
//                 (npx playwright-core install chromium-headless-shell)
//   otherwise     undefined: playwright-core looks for its own revision
//
// Every tool launches with headless: true. Nothing here opens a window.
import { existsSync, readdirSync } from 'node:fs';
import { homedir, platform } from 'node:os';
import { join } from 'node:path';

export function playwrightCache() {
  const env = process.env.PLAYWRIGHT_BROWSERS_PATH;
  if (env && env !== '0') return env;
  if (platform() === 'darwin') return join(homedir(), 'Library/Caches/ms-playwright');
  if (platform() === 'win32') return join(process.env.LOCALAPPDATA ?? homedir(), 'ms-playwright');
  return join(process.env.XDG_CACHE_HOME ?? join(homedir(), '.cache'), 'ms-playwright');
}

// The newest <prefix>-<revision> directory in the cache holding one of `names`
// one level down (chrome-headless-shell-mac-arm64/…, chrome-linux64/…).
function newest(prefix, names) {
  const cache = playwrightCache();
  let dirs;
  try {
    dirs = readdirSync(cache).filter((d) => d.startsWith(`${prefix}-`) && /^\d+$/.test(d.slice(prefix.length + 1)));
  } catch {
    return undefined;
  }
  dirs.sort((a, b) => Number(b.slice(prefix.length + 1)) - Number(a.slice(prefix.length + 1)));
  for (const d of dirs) {
    let subs = [];
    try {
      subs = readdirSync(join(cache, d));
    } catch {}
    for (const s of subs) {
      for (const n of names) {
        const exe = join(cache, d, s, n);
        if (existsSync(exe)) return exe;
      }
    }
  }
  return undefined;
}

/** The headless shell: CHROME_PATH, else the cached one, else undefined. */
export function headlessShell() {
  return process.env.CHROME_PATH || newest('chromium_headless_shell', ['chrome-headless-shell']);
}

/** A full Chromium (WebGL through ANGLE): CHROME_FULL, else the cached one, else undefined. */
export function fullChromium() {
  return (
    process.env.CHROME_FULL ||
    newest('chromium', ['Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing', 'chrome'])
  );
}
