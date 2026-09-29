// Finds and launches Chromium, headless only. There is no way to show a
// window from this harness: no --headed, no show, no attach (AGENTS.md).

import { existsSync, readdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { delimiter, join } from 'node:path';

import { chromium } from 'playwright-core';

// Pages load from http://vapor.local:<port>: an insecure context like the
// box (no async clipboard, no service worker), mapped to the dev server.
export const HOST_RULES = '--host-resolver-rules=MAP *.local 127.0.0.1';

function playwrightCaches(env) {
  const dirs = [];
  if (env.PLAYWRIGHT_BROWSERS_PATH) dirs.push(env.PLAYWRIGHT_BROWSERS_PATH);
  const home = env.HOME || homedir();
  dirs.push(join(home, 'Library', 'Caches', 'ms-playwright'), join(home, '.cache', 'ms-playwright'));
  return dirs;
}

// findChrome returns the browser to use, in this order: $CHROME_PATH,
// $VOS_CHROME, the newest chrome-headless-shell in the Playwright cache,
// then google-chrome or chromium on $PATH. It never downloads anything.
export function findChrome(env = process.env) {
  for (const v of ['CHROME_PATH', 'VOS_CHROME']) {
    if (env[v]) {
      if (!existsSync(env[v])) throw new Error(`${v}=${env[v]} does not exist`);
      return env[v];
    }
  }
  for (const dir of playwrightCaches(env)) {
    if (!existsSync(dir)) continue;
    const shells = readdirSync(dir)
      .filter((d) => d.startsWith('chromium_headless_shell-'))
      .sort((a, b) => Number(b.split('-').pop()) - Number(a.split('-').pop()));
    for (const s of shells) {
      for (const sub of ['chrome-headless-shell-mac-arm64', 'chrome-headless-shell-mac-x64', 'chrome-headless-shell-linux64', 'chrome-linux']) {
        for (const exe of ['chrome-headless-shell', 'headless_shell']) {
          const p = join(dir, s, sub, exe);
          if (existsSync(p)) return p;
        }
      }
    }
  }
  for (const dir of (env.PATH ?? '').split(delimiter)) {
    for (const exe of ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser']) {
      const p = join(dir, exe);
      if (dir && existsSync(p)) return p;
    }
  }
  throw new Error('no Chromium found: set CHROME_PATH to a chrome-headless-shell or Chrome binary');
}

export async function launch(executablePath = findChrome()) {
  const browser = await chromium.launch({ headless: true, executablePath, args: [HOST_RULES] });
  return { browser, executablePath, version: browser.version() };
}
