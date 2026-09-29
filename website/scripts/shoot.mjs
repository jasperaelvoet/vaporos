#!/usr/bin/env node
// Headless screenshots of the VaporOS site, with WebGL that really renders.
//
//   node scripts/shoot.mjs <baseUrl> <outDir> [options]
//   node scripts/shoot.mjs --probe            only test which chromium + flags render WebGL
//
//   baseUrl            e.g. http://127.0.0.1:4342/vaporos/ (scripts/serve.mjs)
//   outDir             screenshots and report.json; keep it outside the repo
//   --pages=/,/download/,/install/,/faq/,/nope/   paths relative to baseUrl (default: those)
//   --widths=1440,390  viewport widths (default 1440,390)
//   --height=N         viewport height (default: 900 wide, 1024 tablet, 844 phone)
//   --shots=10         max viewport screenshots per page, one per scroll step
//   --full=0           skip the full-page screenshot (pages taller than the GPU texture
//                      limit, 8192px with SwiftShader, come as -full-1.png, -full-2.png, …)
//   --dpr=1            device scale factor
//   --wait=350         extra ms after animations settle, per scroll step
//   --mobile=auto      phone emulation (touch, isMobile): auto (< 600px), on, off
//   --reduced-motion   prefers-reduced-motion: reduce
//   --release=none     GitHub API in the page: none (stub: no releases, the default,
//                      keeps runs deterministic and spares the 60/h anonymous limit),
//                      live (real API), or a path to a release JSON (served as the latest)
//   --gl=auto          auto: first chromium/flags combination that passes the WebGL probe;
//                      or a candidate name from --probe (e.g. shell-swiftshader)
//   --strict           exit 1 on console errors, page errors or horizontal overflow
//
// Per page and width: <outDir>/<page>-<width>-00.png … (viewport, scrolled
// step by step), <page>-<width>-full.png, and <outDir>/report.json with
// console errors, failed requests, overflow and the WebGL probe.
//
// Browsers (scripts/chrome.mjs): CHROME_PATH is tried first (the headless
// shell or any Chrome), then Playwright's cached headless shell, then its full
// Chromium (CHROME_FULL). Always headless.
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { chromium } from 'playwright-core';
import { fullChromium, headlessShell } from './chrome.mjs';

// ---------------------------------------------------------------- options
const argv = process.argv.slice(2);
const opt = Object.fromEntries(
  argv.filter((a) => a.startsWith('--')).map((a) => {
    const i = a.indexOf('=');
    return i < 0 ? [a.slice(2), true] : [a.slice(2, i), a.slice(i + 1)];
  }),
);
const [baseArg, outArg] = argv.filter((a) => !a.startsWith('--'));
const PROBE_ONLY = !!opt.probe;
if (!PROBE_ONLY && (!baseArg || !outArg)) {
  console.error('usage: node shoot.mjs <baseUrl> <outDir> [--pages=/,/download/] [--widths=1440,390] [--probe] …');
  process.exit(2);
}
const list = (v, d) => (typeof v === 'string' && v ? v.split(',').map((s) => s.trim()).filter(Boolean) : d);
const PAGES = list(opt.pages, ['/', '/download/', '/install/', '/faq/', '/nope/']);
const WIDTHS = list(opt.widths, ['1440', '390']).map(Number);
const MAX_SHOTS = Number(opt.shots ?? 10);
const FULL = opt.full !== '0' && opt.full !== 'false';
const DPR = Number(opt.dpr ?? 1);
const EXTRA_WAIT = Number(opt.wait ?? 350);
const MOBILE = String(opt.mobile ?? 'auto');
const RELEASE = String(opt.release ?? 'none');
const GL = String(opt.gl ?? 'auto');

// ---------------------------------------------------------------- browsers
// undefined lets playwright-core use its own download (probe reports it).
const SHELL = headlessShell();
const FULL_CHROMIUM = fullChromium();
const SWIFTSHADER = ['--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist', '--enable-webgl'];
const METAL = ['--use-angle=metal', '--enable-gpu', '--ignore-gpu-blocklist', '--enable-webgl'];
const COMMON = ['--force-color-profile=srgb', '--hide-scrollbars'];

// Tried in this order; the first to pass the probe is used (--gl=auto).
const CANDIDATES = [
  { name: 'shell-swiftshader', exe: SHELL, args: SWIFTSHADER },
  { name: 'shell-metal', exe: SHELL, args: METAL },
  { name: 'shell-default', exe: SHELL, args: [] },
  { name: 'chromium-swiftshader', exe: FULL_CHROMIUM, args: SWIFTSHADER },
  { name: 'chromium-metal', exe: FULL_CHROMIUM, args: METAL },
  { name: 'chromium-default', exe: FULL_CHROMIUM, args: [] },
];

// Runs in the page: WebGL 1 and 2 must (a) clear to a color and read it back,
// (b) compile a shader and draw a triangle, read back its pixel.
function glTest() {
  function test(kind) {
    const c = document.createElement('canvas');
    c.width = c.height = 64;
    const gl = c.getContext(kind, { preserveDrawingBuffer: true, antialias: false });
    if (!gl) return { ok: false, error: 'no context' };
    const dbg = gl.getExtension('WEBGL_debug_renderer_info');
    const renderer = String(gl.getParameter(dbg ? dbg.UNMASKED_RENDERER_WEBGL : gl.RENDERER));
    gl.clearColor(1, 0, 0.5, 1);
    gl.clear(gl.COLOR_BUFFER_BIT);
    const a = new Uint8Array(4);
    gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, a);
    const clearOk = a[0] === 255 && a[1] === 0 && Math.abs(a[2] - 128) <= 1 && a[3] === 255;
    const v2 = kind === 'webgl2';
    const vs = v2
      ? '#version 300 es\nin vec2 p;void main(){gl_Position=vec4(p,0.,1.);}'
      : 'attribute vec2 p;void main(){gl_Position=vec4(p,0.,1.);}';
    const fs = v2
      ? '#version 300 es\nprecision mediump float;out vec4 o;void main(){o=vec4(0.,1.,0.,1.);}'
      : 'precision mediump float;void main(){gl_FragColor=vec4(0.,1.,0.,1.);}';
    const sh = (type, src) => {
      const s = gl.createShader(type);
      gl.shaderSource(s, src);
      gl.compileShader(s);
      if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s) || 'compile failed');
      return s;
    };
    const prog = gl.createProgram();
    gl.attachShader(prog, sh(gl.VERTEX_SHADER, vs));
    gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, fs));
    gl.bindAttribLocation(prog, 0, 'p');
    gl.linkProgram(prog);
    gl.useProgram(prog);
    gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 0, 0);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    const b = new Uint8Array(4);
    gl.readPixels(32, 32, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, b);
    const drawOk = b[0] === 0 && b[1] === 255 && b[2] === 0 && b[3] === 255;
    return { ok: clearOk && drawOk, clearOk, drawOk, clear: [...a], draw: [...b], renderer, version: String(gl.getParameter(gl.VERSION)), maxTexture: gl.getParameter(gl.MAX_TEXTURE_SIZE) };
  }
  const out = {};
  for (const k of ['webgl', 'webgl2']) {
    try {
      out[k] = test(k);
    } catch (e) {
      out[k] = { ok: false, error: String(e) };
    }
  }
  return out;
}

// A WebGL canvas in the DOM (no preserveDrawingBuffer) must show up in a
// screenshot: the compositor path real pages use.
async function screenshotTest(page) {
  await page.setContent(
    '<body style="margin:0;background:#000"><canvas id="c" width="120" height="120" style="width:120px;height:120px"></canvas></body>',
  );
  const drew = await page.evaluate(
    () =>
      new Promise((done) => {
        const gl = document.getElementById('c').getContext('webgl2') || document.getElementById('c').getContext('webgl');
        if (!gl) return done(false);
        let n = 0;
        const frame = () => {
          gl.clearColor(1, 0, 0.5, 1);
          gl.clear(gl.COLOR_BUFFER_BIT);
          if (++n < 3) requestAnimationFrame(frame);
          else done(true);
        };
        requestAnimationFrame(frame);
      }),
  );
  if (!drew) return { ok: false, error: 'no context' };
  const png = await page.screenshot({ clip: { x: 40, y: 40, width: 20, height: 20 } });
  const px = await page.evaluate(async (b64) => {
    const img = new Image();
    img.src = `data:image/png;base64,${b64}`;
    await img.decode();
    const c = document.createElement('canvas');
    c.width = img.width;
    c.height = img.height;
    const g = c.getContext('2d');
    g.drawImage(img, 0, 0);
    return [...g.getImageData(Math.floor(img.width / 2), Math.floor(img.height / 2), 1, 1).data];
  }, png.toString('base64'));
  const ok = px[0] >= 250 && px[1] <= 5 && Math.abs(px[2] - 128) <= 3;
  return { ok, pixel: px };
}

async function probe(c) {
  if (c.exe && !existsSync(c.exe)) return { ...c, ok: false, error: `binary missing: ${c.exe}` };
  let browser;
  try {
    browser = await chromium.launch({ executablePath: c.exe, headless: true, args: [...COMMON, ...c.args] });
    const page = await browser.newPage({ viewport: { width: 200, height: 200 } });
    const gl = await page.evaluate(glTest);
    const shot = await screenshotTest(page);
    const ok = !!(gl.webgl?.ok && gl.webgl2?.ok && shot.ok);
    return { ...c, ok, version: browser.version(), webgl: gl.webgl, webgl2: gl.webgl2, screenshot: shot };
  } catch (e) {
    return { ...c, ok: false, error: String(e).split('\n')[0] };
  } finally {
    await browser?.close();
  }
}

function describeProbe(r) {
  const gl = (x) => (!x ? '-' : x.ok ? 'ok' : `FAIL(${x.error ?? `clear ${x.clear} draw ${x.draw}`})`);
  const renderer = r.webgl2?.renderer ?? r.webgl?.renderer ?? '';
  return r.error
    ? `${r.name.padEnd(22)} ERROR ${r.error}`
    : `${r.name.padEnd(22)} webgl ${gl(r.webgl)}  webgl2 ${gl(r.webgl2)}  screenshot ${r.screenshot?.ok ? 'ok' : `FAIL ${r.screenshot?.pixel ?? r.screenshot?.error}`}  ${renderer}`;
}

async function pickBrowser() {
  if (PROBE_ONLY) {
    const results = [];
    for (const c of CANDIDATES) {
      const r = await probe(c);
      results.push(r);
      console.log(describeProbe(r));
    }
    const win = results.find((r) => r.ok);
    console.log(win ? `\nfirst working: ${win.name}\n  exe:   ${win.exe ?? '(playwright default)'}\n  flags: ${win.args.join(' ') || '(none)'}` : '\nno candidate renders WebGL');
    return { results, win };
  }
  const pool = GL === 'auto' ? CANDIDATES : CANDIDATES.filter((c) => c.name === GL);
  if (!pool.length) throw new Error(`--gl=${GL}: no such candidate (${CANDIDATES.map((c) => c.name).join(', ')})`);
  for (const c of pool) {
    const r = await probe(c);
    console.log(`webgl probe: ${describeProbe(r)}`);
    if (r.ok) return { win: r };
  }
  console.warn('webgl probe: nothing rendered WebGL; falling back to headless-shell without GPU flags');
  return { win: { ...CANDIDATES[2], ok: false } };
}

// ---------------------------------------------------------------- page helpers
// Runs in the page: wait for fonts, images in view, and finite CSS/WAAPI
// animations (infinite loops are ignored), then two frames.
async function settle(page, maxMs = 3000) {
  await page
    .evaluate(async (maxMs) => {
      const deadline = performance.now() + maxMs;
      const timeout = (ms) => new Promise((r) => setTimeout(r, ms));
      try {
        await Promise.race([document.fonts?.ready, timeout(maxMs)]);
      } catch {}
      const pendingImages = [...document.images].filter((img) => {
        if (img.complete) return false;
        const r = img.getBoundingClientRect();
        return r.bottom > 0 && r.top < innerHeight;
      });
      await Promise.race([Promise.all(pendingImages.map((i) => i.decode().catch(() => {}))), timeout(maxMs)]);
      const finite = () =>
        document.getAnimations().filter((a) => {
          if (a.playState !== 'running') return false;
          const t = a.effect?.getComputedTiming?.();
          return t && Number.isFinite(t.endTime);
        });
      while (finite().length && performance.now() < deadline) {
        await Promise.race([Promise.all(finite().map((a) => a.finished.catch(() => {}))), timeout(200)]);
      }
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    }, maxMs)
    .catch(() => {});
}

// Runs in the page: the thing that scrolls (the document, or a full-screen
// scroll container), its height, and a way to scroll it instantly.
const SCROLLER = `(() => {
  const doc = document.scrollingElement || document.documentElement;
  if (doc.scrollHeight > innerHeight + 2) return doc;
  let best = null;
  for (const el of document.querySelectorAll('body *')) {
    const s = getComputedStyle(el);
    if (!/(auto|scroll)/.test(s.overflowY) || el.scrollHeight <= el.clientHeight + 2) continue;
    if (el.clientHeight < innerHeight * 0.6) continue;
    if (!best || el.scrollHeight > best.scrollHeight) best = el;
  }
  return best || doc;
})()`;

async function scrollInfo(page) {
  return page.evaluate(`(() => { const s = ${SCROLLER}; return { height: s.scrollHeight, view: s === (document.scrollingElement || document.documentElement) ? innerHeight : s.clientHeight }; })()`);
}

async function scrollTo(page, y) {
  await page.evaluate(`(() => {
    const y = ${Number(y)};
    if (window.lenis?.scrollTo) window.lenis.scrollTo(y, { immediate: true, force: true });
    const s = ${SCROLLER};
    if (s === (document.scrollingElement || document.documentElement)) window.scrollTo({ top: y, behavior: 'instant' });
    else s.scrollTo({ top: y, behavior: 'instant' });
  })()`);
}

async function overflow(page) {
  return page.evaluate(() => {
    const de = document.documentElement;
    const vw = de.clientWidth;
    const sw = Math.max(de.scrollWidth, document.body?.scrollWidth ?? 0);
    const out = { viewport: vw, scrollWidth: sw, overflow: sw > vw + 1, offenders: [] };
    if (!out.overflow) return out;
    const name = (el) =>
      el.tagName.toLowerCase() +
      (el.id ? `#${el.id}` : '') +
      (typeof el.className === 'string' && el.className.trim() ? `.${el.className.trim().split(/\s+/).slice(0, 3).join('.')}` : '');
    for (const el of document.querySelectorAll('body *')) {
      const r = el.getBoundingClientRect();
      if (!r.width || (r.right <= vw + 1 && r.left >= -1)) continue;
      if (getComputedStyle(el).position === 'fixed') continue;
      out.offenders.push({ el: name(el), left: Math.round(r.left), right: Math.round(r.right) });
      if (out.offenders.length >= 8) break;
    }
    return out;
  });
}

// One image when the page fits the compositor's texture limit (8192px with
// SwiftShader, 16384px with Metal), else <name>-full-1.png, -2.png, … top to bottom.
async function fullPage(page, name, width, docH) {
  const maxTex = Math.min(win.webgl2?.maxTexture ?? win.webgl?.maxTexture ?? 8192, 16384);
  let limit = Math.floor(maxTex / DPR) - 64;
  if (docH <= limit) {
    try {
      await page.screenshot({ path: join(OUT, `${name}-full.png`), fullPage: true });
      return [`${name}-full.png`];
    } catch {
      limit = Math.floor(limit / 2);
    }
  }
  const files = [];
  for (let y = 0, i = 1; y < docH; y += limit, i++) {
    const file = `${name}-full-${i}.png`;
    await page.screenshot({ path: join(OUT, file), fullPage: true, clip: { x: 0, y, width, height: Math.min(limit, docH - y) } });
    files.push(file);
  }
  return files;
}

const slug = (p) => p.replace(/^\/+|\/+$/g, '').replace(/[^a-z0-9]+/gi, '-') || 'home';

// The GitHub API as the page sees it.
async function stubGitHub(context) {
  if (RELEASE === 'live') return;
  let latest = null;
  if (RELEASE !== 'none') {
    const json = JSON.parse(readFileSync(resolve(RELEASE), 'utf8'));
    latest = Array.isArray(json) ? json[0] : json;
  }
  await context.route('https://api.github.com/**', (route) => {
    const url = route.request().url();
    const cors = { 'access-control-allow-origin': '*', 'content-type': 'application/json; charset=utf-8' };
    if (/\/releases\/latest(\?|$)/.test(url)) {
      return latest
        ? route.fulfill({ status: 200, headers: cors, body: JSON.stringify(latest) })
        : route.fulfill({ status: 404, headers: cors, body: '{"message":"Not Found"}' });
    }
    if (/\/releases(\?|$)/.test(url)) return route.fulfill({ status: 200, headers: cors, body: JSON.stringify(latest ? [latest] : []) });
    return route.fulfill({ status: 404, headers: cors, body: '{"message":"Not Found"}' });
  });
}

// ---------------------------------------------------------------- main
const { win } = await pickBrowser();
if (PROBE_ONLY) process.exit(win ? 0 : 1);

const OUT = resolve(outArg);
mkdirSync(OUT, { recursive: true });
const base = baseArg.endsWith('/') ? baseArg : `${baseArg}/`;
const browser = await chromium.launch({ executablePath: win.exe, headless: true, args: [...COMMON, ...win.args] });
console.log(`browser: ${win.name} (${browser.version()})  ${win.exe ?? '(playwright default)'}\nflags: ${[...COMMON, ...win.args].join(' ')}\n`);

const report = {
  baseUrl: base,
  browser: { name: win.name, exe: win.exe, flags: [...COMMON, ...win.args], version: browser.version(), webgl: win.webgl, webgl2: win.webgl2, screenshot: win.screenshot },
  release: RELEASE,
  pages: [],
};
let problems = 0;

for (const width of WIDTHS) {
  const phone = MOBILE === 'on' || (MOBILE === 'auto' && width < 600);
  const height = Number(opt.height ?? (width < 600 ? 844 : width < 1024 ? 1024 : 900));
  const context = await browser.newContext({
    viewport: { width, height },
    deviceScaleFactor: DPR,
    isMobile: phone,
    hasTouch: phone,
    reducedMotion: opt['reduced-motion'] ? 'reduce' : 'no-preference',
  });
  await stubGitHub(context);

  for (const path of PAGES) {
    const page = await context.newPage();
    const url = new URL(path.replace(/^\//, ''), base).href;
    const entry = { page: path, width, height, url, consoleErrors: [], pageErrors: [], failedRequests: [], aborted: 0, shots: [] };
    const consoleRaw = [];
    page.on('console', (m) => {
      if (m.type() === 'error') consoleRaw.push({ text: m.text(), from: m.location()?.url ?? '' });
    });
    page.on('pageerror', (e) => entry.pageErrors.push(String(e)));
    page.on('requestfailed', (r) => {
      const why = r.failure()?.errorText ?? 'failed';
      // Cancelled requests (next/link prefetches cut off by the next step or page close) are noise.
      if (why === 'net::ERR_ABORTED') entry.aborted++;
      else entry.failedRequests.push(`${why} ${r.url()}`);
    });
    page.on('response', (r) => {
      if (r.status() >= 400) entry.failedRequests.push(`${r.status()} ${r.url()}`);
    });

    const t0 = Date.now();
    const res = await page.goto(url, { waitUntil: 'load', timeout: 30_000 }).catch((e) => {
      entry.pageErrors.push(`navigation: ${e.message.split('\n')[0]}`);
      return null;
    });
    entry.status = res?.status() ?? 0;
    await page.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => {});
    await settle(page);
    await page.waitForTimeout(EXTRA_WAIT);
    entry.title = await page.title();

    const name = `${slug(path)}-${width}`;
    let { height: docH, view } = await scrollInfo(page);
    // One screen per step; a page longer than MAX_SHOTS screens gets evenly
    // spread positions, so the first and the last screen are always in.
    const need = Math.max(1, Math.ceil(docH / view));
    const steps = Math.min(MAX_SHOTS, need);
    for (let i = 0; i < steps; i++) {
      const bottom = Math.max(0, docH - view);
      const y = need <= MAX_SHOTS || steps < 2 ? Math.min(i * view, bottom) : Math.round((i * bottom) / (steps - 1));
      await scrollTo(page, y);
      await settle(page, 2500);
      await page.waitForTimeout(EXTRA_WAIT);
      const file = `${name}-${String(i).padStart(2, '0')}.png`;
      await page.screenshot({ path: join(OUT, file) });
      entry.shots.push(file);
      ({ height: docH, view } = await scrollInfo(page)); // pages can grow as they reveal
    }
    entry.height = docH;
    entry.overflow = await overflow(page);

    if (FULL) {
      await scrollTo(page, 0);
      await settle(page, 2500);
      entry.full = await fullPage(page, name, width, docH);
    }
    // A missing page answering 404 is the point of shooting it: not an error.
    const own404 = (u) => entry.status === 404 && u === (res?.url() ?? url);
    entry.consoleErrors = consoleRaw.filter((c) => !(own404(c.from) && /status of 404/.test(c.text))).map((c) => c.text);
    entry.failedRequests = entry.failedRequests.filter((f) => !own404(f.replace(/^404 /, '')));
    entry.ms = Date.now() - t0;
    report.pages.push(entry);
    await page.close();

    const errs = entry.consoleErrors.length + entry.pageErrors.length;
    if (errs || entry.overflow.overflow) problems++;
    console.log(
      `${String(width).padStart(5)} ${path.padEnd(14)} ${entry.status} h=${docH} shots=${entry.shots.length}${FULL ? `+full${entry.full.length > 1 ? `x${entry.full.length}` : ''}` : ''}` +
        `  console=${entry.consoleErrors.length} pageerr=${entry.pageErrors.length} failed=${entry.failedRequests.length}` +
        `  overflow=${entry.overflow.overflow ? `YES (${entry.overflow.scrollWidth}>${entry.overflow.viewport}) ${entry.overflow.offenders.map((o) => o.el).join(' ')}` : 'no'}  ${entry.ms}ms`,
    );
    for (const m of [...entry.pageErrors, ...entry.consoleErrors]) console.log(`        ! ${m}`);
    for (const m of entry.failedRequests) console.log(`        ~ ${m}`);
  }
  await context.close();
}

await browser.close();
writeFileSync(join(OUT, 'report.json'), `${JSON.stringify(report, null, 2)}\n`);
console.log(`\n${report.pages.length} page runs, screenshots and report.json in ${OUT}`);
process.exit(opt.strict && problems ? 1 : 0);
