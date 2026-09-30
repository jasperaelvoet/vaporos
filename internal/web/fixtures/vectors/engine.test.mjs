// What the vectors cannot compare, because only the JS engine has it: the
// transport the demo installs as globalThis.vosTransport (fetch, the event
// stream and the cookie jar) and save/restore across the demo's page loads.
// TestDemoEngine (fake_engines_test.go) runs it with node --test.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createEngine, transport, virtualClock } from '../../demo/engine.js';

const here = dirname(fileURLToPath(import.meta.url));
const dir = (d) =>
  Object.fromEntries(
    readdirSync(join(here, '..', d))
      .filter((f) => f.endsWith('.json'))
      .map((f) => [basename(f, '.json'), JSON.parse(readFileSync(join(here, '..', d, f), 'utf8'))]),
  );
const fixtures = { base: dir('base'), presets: dir('presets'), scripts: dir('scripts') };
const tick = (ms = 0) => new Promise((r) => setTimeout(r, ms));

test('fetch answers /api/v1 from the engine, with the jar and the CSRF check', async () => {
  const e = createEngine(fixtures, { preset: 'idle', clock: virtualClock() });
  const T = transport(e);
  let me = await (await T.fetch('/api/v1/auth/me')).json();
  assert.equal(me.authenticated, false);
  const bad = await T.fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"password":"nope"}' });
  assert.equal(bad.status, 401);
  assert.deepEqual(await bad.json(), { error: 'wrong password' });
  const ok = await T.fetch('/api/v1/auth/login', { method: 'POST', body: JSON.stringify({ password: 'vaporvapor' }) });
  const { csrf } = await ok.json();
  assert.ok(e.s.client.cookies.vos_session, 'the jar keeps the session cookie');
  assert.equal(ok.headers.get('set-cookie'), null, 'no Set-Cookie reaches the page');
  assert.ok(Date.parse(ok.headers.get('date')), 'a Date header for the clock skew');
  me = await (await T.fetch('http://vapor.local/api/v1/auth/me', { headers: { Accept: 'application/json' } })).json();
  assert.equal(me.authenticated, true);
  assert.equal((await T.fetch('/api/v1/power', { method: 'PUT', body: '{"idle_minutes":30}' })).status, 403);
  const put = await T.fetch('/api/v1/power', { method: 'PUT', headers: { 'X-VOS-CSRF': csrf }, body: '{"idle_minutes":30}' });
  assert.equal((await put.json()).idle_minutes, 30);
  const logs = await T.fetch('/api/v1/sunshine/logs');
  assert.match(logs.headers.get('content-type'), /^text\/plain/);
  assert.match(await logs.text(), /Sunshine version/);
  await T.fetch('/api/v1/auth/logout', { method: 'POST', headers: { 'X-VOS-CSRF': csrf } });
  assert.equal(e.s.client.cookies.vos_session, undefined, 'signing out clears the cookie');
});

test('a box that is down drops fetches, and the latency option delays answers', async () => {
  const e = createEngine(fixtures, { preset: 'sunshine-unreachable', clock: virtualClock() });
  e.signIn();
  const T = transport(e);
  assert.equal(e.latency('GET', '/api/v1/sunshine'), 3000, "the preset's latency");
  assert.equal(e.latency('GET', '/api/v1/system'), 0);
  e.setSlow(true);
  assert.equal(e.latency('GET', '/api/v1/storage'), 900);
  assert.equal(e.latency('GET', '/api/v1/system'), 80);
  assert.equal(e.latency('GET', '/api/v1/ping'), 0, 'the core answers at once');
  e.setDown(-1);
  await assert.rejects(T.fetch('/api/v1/ping'), TypeError);
  e.setDown(0);
  assert.equal((await T.fetch('/api/v1/ping')).status, 200);
});

test('the event stream opens with the replay, goes live, and reconnects after a restart', async () => {
  const e = createEngine(fixtures, { preset: 'streaming', clock: virtualClock() });
  e.signIn();
  e.settle();
  const T = transport(e);
  const es = new T.EventSource('/api/v1/events?passive=1');
  const seen = [];
  const states = [];
  for (const topic of ['session.begin', 'session.end', 'pairing.state', 'display.changed']) {
    es.addEventListener(topic, (ev) => seen.push([topic, JSON.parse(ev.data)]));
  }
  es.onopen = () => states.push('open');
  es.onerror = () => states.push(`error ${es.readyState}`);
  assert.equal(es.readyState, 0);
  await tick();
  assert.equal(es.readyState, 1);
  assert.deepEqual(
    seen.map(([t]) => t),
    ['pairing.state', 'session.begin'],
    'the replay, sorted by topic',
  );
  e.publish('session.end', {});
  assert.equal(seen.length, 2, 'live events arrive as a task, like the network');
  await tick();
  assert.deepEqual(seen[2], ['session.end', {}]);
  es.retry = 10; // the server said 3000 ms; the test does not wait that long
  e.setDown(-1);
  assert.deepEqual(states, ['open', 'error 0'], 'the stream drops and the browser keeps trying');
  await tick(25);
  assert.equal(es.readyState, 0, 'still trying while the box is off');
  e.setDown(0);
  await tick(30);
  assert.equal(es.readyState, 1);
  assert.equal(states.at(-1), 'open');
  es.close();
  assert.equal(es.readyState, 2);
});

test('a refused stream fails for good', async () => {
  const e = createEngine(fixtures, { preset: 'idle', clock: virtualClock() });
  const T = transport(e);
  const es = new T.EventSource('/api/v1/events');
  let errors = 0;
  es.onerror = () => errors++;
  await tick();
  assert.equal(es.readyState, 2);
  assert.equal(errors, 1);
});

test('save and restore carry a stage across page loads', () => {
  const clock = virtualClock(Date.UTC(2026, 8, 30, 12));
  const e = createEngine(fixtures, { preset: 'update-available', clock });
  const { csrf } = e.signIn();
  const cookie = `vos_session=${e.s.client.cookies.vos_session}`;
  const res = e.handle({ method: 'POST', path: '/api/v1/update/stage', headers: { cookie, 'x-vos-csrf': csrf }, body: '{}' });
  assert.equal(res.status, 200);
  e.advance(4000);
  const saved = JSON.parse(JSON.stringify(e.save()));
  const before = JSON.parse(e.handle({ method: 'GET', path: '/api/v1/update', headers: { cookie } }).body);
  assert.equal(before.progress.phase, 'write');
  const next = createEngine(fixtures, { clock, restore: saved });
  const again = JSON.parse(next.handle({ method: 'GET', path: '/api/v1/update', headers: { cookie } }).body);
  assert.deepEqual(again.progress, before.progress, 'the stage stands where it stood');
  next.advance(20000);
  const done = JSON.parse(next.handle({ method: 'GET', path: '/api/v1/update', headers: { cookie } }).body);
  assert.equal(done.busy, false);
  assert.equal(done.staged.version, '20260929.143000');
});

test('a real clock catches up on tasks that fell due while the page was away', async () => {
  let now = Date.now();
  const clock = { now: () => now, virtual: false };
  const e = createEngine(fixtures, { preset: 'idle', clock });
  const { csrf } = e.signIn();
  const cookie = `vos_session=${e.s.client.cookies.vos_session}`;
  e.handle({ method: 'POST', path: '/api/v1/system/reboot', headers: { cookie, 'x-vos-csrf': csrf } });
  const saved = e.save();
  e.stop();
  now += 30000; // the next page loads half a minute later
  const next = createEngine(fixtures, { clock, restore: saved });
  const ping = next.handle({ method: 'GET', path: '/api/v1/ping', headers: {} });
  assert.equal(ping.status, 200, 'down a second after the request, up 8 s later');
  const sys = JSON.parse(next.handle({ method: 'GET', path: '/api/v1/system', headers: { cookie } }).body);
  assert.ok(sys.uptime_s >= 20 && sys.uptime_s <= 22, `uptime ${sys.uptime_s} s counts from the boot`);
  next.stop();
});

test('scripts run on the clock and a preset resets them', () => {
  const e = createEngine(fixtures, { preset: 'idle', clock: virtualClock() });
  const topics = [];
  e.watch((m) => topics.push(m.type === 'event' ? m.topic : m.type));
  e.runScript('stream');
  e.advance(0);
  assert.deepEqual(topics, ['power.idle', 'session.begin']);
  e.advance(300);
  assert.equal(topics.at(-1), 'display.changed');
  e.runScript('power-off');
  e.advance(999);
  assert.equal(e.s.down, false);
  e.advance(1);
  assert.equal(e.s.down, true);
  e.runScript('wake');
  e.advance(0);
  assert.equal(e.s.down, false);
  e.runScript('stream-end');
  e.loadPreset('idle');
  e.advance(1000);
  assert.equal(e.state().docs.sunshine.streaming, false);
});
