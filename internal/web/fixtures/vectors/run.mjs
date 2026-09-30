// fixtures/vectors/run.mjs plays vectors against the website demo's JS
// engine (demo/engine.js) on a virtual clock and prints one transcript per
// vector. TestFakeEnginesAgree (fake_engines_test.go) sends the vectors on
// stdin ({"vectors": [...]}, as in fixtures/vectors/*.json), plays the same
// vectors against the dev server's Go fake, and compares. By hand:
//
//   node internal/web/fixtures/vectors/run.mjs internal/web/fixtures/vectors/core.json
//
// A transcript is {start, steps}: start is when the preset loaded (ms), and
// each step records what the Go side records (see fake_engines_test.go),
// plus "copy" on errors: the words messages.js shows for it (T5).
import { readdirSync, readFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createEngine, virtualClock } from '../../demo/engine.js';
import { messageFor } from '../../static/js/messages.js';

const here = dirname(fileURLToPath(import.meta.url));
const read = (p) => JSON.parse(readFileSync(p, 'utf8'));
const dir = (d) =>
  Object.fromEntries(
    readdirSync(join(here, '..', d))
      .filter((f) => f.endsWith('.json'))
      .sort()
      .map((f) => [basename(f, '.json'), read(join(here, '..', d, f))]),
  );
const fixtures = { base: dir('base'), presets: dir('presets'), scripts: dir('scripts') };

const SENTINEL = 'system.message';

// matches reports whether got has every key of want, recursively.
function matches(want, got) {
  if (want === null || typeof want !== 'object') return want === got;
  if (Array.isArray(want)) return Array.isArray(got) && want.length === got.length && want.every((w, i) => matches(w, got[i]));
  return got !== null && typeof got === 'object' && Object.entries(want).every(([k, w]) => matches(w, got[k]));
}

function parseBody(res) {
  if (!res.body) return null;
  if ((res.headers['content-type'] || '').includes('json')) return JSON.parse(res.body);
  return res.body;
}

function play(v) {
  const start = Date.now();
  const clock = virtualClock(start);
  const e = createEngine(fixtures, { preset: v.preset, clock });
  const preset = fixtures.presets[v.preset];
  const jar = {};
  let csrf = '';
  if ((v.signed_in ?? true) && preset.auth !== 'signed-out') {
    const s = e.startSession(null);
    jar.vos_session = s.token;
    csrf = s.csrf;
  }
  e.settle();
  e.advance(0);
  let seen = [];
  e.watch((m) => {
    if (m.type === 'event') seen.push([m.topic, m.data]);
  });
  const cookie = () =>
    Object.entries(jar)
      .map(([k, val]) => `${k}=${val}`)
      .join('; ');
  const keep = (list) => {
    for (const c of list || []) {
      const [pair] = c.split(';');
      const i = pair.indexOf('=');
      if (/Max-Age=0/i.test(c)) delete jar[pair.slice(0, i)];
      else jar[pair.slice(0, i)] = pair.slice(i + 1);
    }
  };
  const steps = [];
  for (const st of v.steps) {
    seen = [];
    const rec = {};
    if (st.req) {
      const [method, path] = st.req.split(' ');
      const headers = { ...(st.headers || {}), cookie: cookie() };
      if (method !== 'GET') {
        const token = st.csrf ?? csrf;
        if (token) headers['x-vos-csrf'] = token;
      }
      if (st.setup) headers['x-vos-setup'] = st.setup;
      if (st.passive) headers['x-vos-passive'] = '1';
      let body;
      if (st.raw !== undefined) body = st.raw;
      else if (st.body !== undefined) body = JSON.stringify(st.body);
      if (body !== undefined) headers['content-type'] = 'application/json';
      const res = e.handle({ method, path: '/api/v1' + path, headers, body });
      Object.assign(rec, { kind: 'req', req: st.req });
      if (!res) {
        rec.status = 0;
      } else {
        keep(res.headers['set-cookie']);
        rec.status = res.status;
        rec.type = (res.headers['content-type'] || '').split(';')[0];
        const h = {};
        if (res.headers.allow) h.allow = res.headers.allow;
        if (res.headers['retry-after']) h['retry-after'] = res.headers['retry-after'];
        rec.headers = h;
        rec.body = parseBody(res);
        if (rec.body && typeof rec.body.csrf === 'string' && rec.body.csrf) csrf = rec.body.csrf;
        if (res.status >= 400 && rec.body && typeof rec.body.error === 'string') {
          rec.copy = messageFor({ status: res.status, message: rec.body.error, retryAfter: Number(res.headers['retry-after']) || 0 }, { method, path });
        }
      }
    } else if (st.stream) {
      const headers = { cookie: cookie() };
      if (st.stream.setup) headers['x-vos-setup'] = st.stream.setup;
      const out = e.openStream({ path: '/api/v1/events' + (st.stream.passive ? '?passive=1' : ''), headers });
      rec.kind = 'stream';
      if (!out) rec.status = 0;
      else if (out.answer) {
        keep(out.answer.headers['set-cookie']);
        rec.status = out.answer.status;
        rec.body = parseBody(out.answer);
      } else {
        rec.status = 200;
        rec.replay = out.stream.replay.map((ev) => [ev.topic, JSON.parse(ev.data)]);
        out.stream.close();
      }
    } else if (st.event) {
      rec.kind = 'event';
      rec.ok = e.publish(st.event[0], st.event[1]);
    } else if (st.step || st.script) {
      rec.kind = st.step ? 'step' : 'script';
      const list = st.step ? [st.step] : fixtures.scripts[st.script].steps;
      for (const s of list) {
        if (!e.scriptStep(s, e.s.preset)) break;
        if (s.down === 0) e.settle();
      }
      if (list.some((s) => s.reset)) {
        e.settle();
        e.advance(0);
        seen = [];
      }
    } else if (st.wait) {
      rec.kind = 'wait';
      const t0 = clock.now();
      const timeout = st.wait.timeout ?? 30000;
      const done = () => {
        if (st.wait.until === 'down') return e.s.down;
        if (st.wait.until === 'up') return !e.s.down;
        return seen.some(([topic, data]) => topic === st.wait.topic && matches(st.wait.data ?? {}, data));
      };
      let ok = done();
      while (!ok) {
        const next = e.nextTask();
        if (next > t0 + timeout) {
          e.advance(t0 + timeout - clock.now());
          break;
        }
        e.advance(Math.max(0, next - clock.now()));
        ok = done();
      }
      rec.matched = ok;
    } else if (st.sleep !== undefined) {
      rec.kind = 'sleep';
      e.advance(st.sleep);
    } else if (st.down !== undefined) {
      rec.kind = 'down';
      e.setDown(st.down);
      if (st.down === 0) e.settle();
    } else if (st.wake) {
      rec.kind = 'wake';
      e.setDown(0);
      e.settle();
    } else if (st.preset) {
      rec.kind = 'preset';
      e.loadPreset(st.preset);
      e.settle();
      e.advance(0);
      seen = [];
    } else {
      throw new Error(`${v.name}: a step with nothing to do: ${JSON.stringify(st)}`);
    }
    let events = seen.filter(([topic, data]) => !(topic === SENTINEL && data && data.sentinel));
    if (st.topics) events = events.filter(([topic]) => st.topics.includes(topic));
    if (v.ignore_topics) events = events.filter(([topic]) => !v.ignore_topics.includes(topic));
    if (st.collapse) events = collapse(events);
    rec.events = events;
    steps.push(rec);
  }
  return { start, steps };
}

// collapse keeps the last of each run of update.progress events in one
// phase (the ticks in between depend on the Go side's timing).
function collapse(events) {
  const out = [];
  for (const ev of events) {
    const prev = out[out.length - 1];
    if (prev && ev[0] === 'update.progress' && prev[0] === 'update.progress' && prev[1].phase === ev[1].phase) out[out.length - 1] = ev;
    else out.push(ev);
  }
  return out;
}

let input;
if (process.argv[2]) input = { vectors: process.argv.slice(2).flatMap((p) => read(p).vectors) };
else input = JSON.parse(readFileSync(0, 'utf8'));
const out = {};
for (const v of input.vectors) {
  if (!fixtures.presets[v.preset]) throw new Error(`${v.name}: no preset ${v.preset}`);
  out[v.name] = play(v);
}
process.stdout.write(JSON.stringify(out, null, process.argv[2] ? 1 : 0) + '\n');
