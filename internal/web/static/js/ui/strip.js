// ui/strip.js: the now-streaming strip and its sheet (spec-cc-screens §2.8).
// Not a live region: the announcer says when a stream starts and ends.

import { api, errorText, serverNow } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, optionalById } from '../core/dom.js';
import { stripModel } from '../state.js';
import { confirmDialog } from './dialog.js';
import { notify } from './notices.js';
import { closeSheet, openSheet } from './sheet.js';

let current = { show: false };
let known = false;
let heroInView = false;
let clock = 0;

let wired = false;

function initStrip() {
  wired = true;
  const sheet = byId('stream-sheet');
  byId('strip-open').addEventListener('click', (e) => openSheet(sheet, { invoker: e.currentTarget }));
  byId('ss-end').addEventListener('click', async () => {
    if (!(await confirmDialog({ id: 'endstream' }))) return;
    try {
      await api('POST', '/sunshine/end-stream', {});
      closeSheet(sheet);
      notify('The stream ended.', { kind: 'ok' });
    } catch (err) {
      notify(await errorText(err), { kind: 'error' });
    }
  });
  byId('ss-restart').addEventListener('click', async () => {
    if (!(await confirmDialog({ id: 'sunrestart' }))) return;
    try {
      await api('POST', '/sunshine/restart', {});
      closeSheet(sheet);
      notify('Streaming is restarting.', { kind: 'info' });
    } catch (err) {
      notify(await errorText(err), { kind: 'error' });
    }
  });
  const hero = optionalById('hero');
  if (hero && 'IntersectionObserver' in globalThis) {
    heroInView = true;
    new IntersectionObserver(([e]) => {
      heroInView = e.isIntersecting;
      place();
    }).observe(hero);
  }
}

function place() {
  byId('strip').hidden = !current.show || heroInView;
}

// renderStrip shows the snapshot's stream, or hides the strip.
export function renderStrip(snap) {
  if (!wired) initStrip();
  const m = stripModel(snap, serverNow());
  const was = current.show;
  current = m;
  place();
  clearInterval(clock);
  if (known && was !== m.show) announce(m.show ? `Streaming started on ${m.client}` : 'Stream ended');
  known = true;
  if (!m.show) {
    if (byId('stream-sheet').open) closeSheet(byId('stream-sheet'));
    return;
  }
  byId('strip-client').textContent = m.client;
  byId('strip-mode').textContent = m.line;
  byId('strip-name').textContent = m.name;
  byId('ss-client').textContent = m.client;
  byId('ss-by').textContent = m.client;
  byId('ss-mode').textContent = m.modeText || 'Unknown';
  byId('ss-hdr').textContent = m.hdr ? 'On' : 'Off';
  byId('ss-app-row').hidden = !m.app;
  byId('ss-app').textContent = m.app;
  const tickSince = () => {
    const t = stripModel(snap, serverNow()).sinceText;
    byId('strip-since').textContent = t;
    byId('ss-since').textContent = t;
    byId('ss-since-row').hidden = !t;
  };
  tickSince();
  if (m.since) clock = setInterval(tickSince, 30000);
}
