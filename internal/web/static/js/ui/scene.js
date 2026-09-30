// ui/scene.js: the reconnection scene (ARCH §6.12, spec-cc-screens §2.5,
// NEW-1) and powerAction. It polls the public /ping (nobody's activity) with
// the event stream closed. REDLINE: the field cools while the box is down.

import { api, errorText, ping, session } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, h, sleep } from '../core/dom.js';
import { pause, resume } from '../core/live.js';
import { loadSnapshot, setString } from '../core/store.js';
import { fill } from '../copy.js';
import { elapsed } from '../fmt.js';
import { powerPlan } from '../state.js';
import { confirmDialog } from './dialog.js';
import { notify } from './notices.js';
import { fillWake } from './wake.js';

// The reconnection scene's phases (spec-cc-screens §2.5, NEW-1).
const SCENE = {
  restarting: { title: 'Restarting', text: 'VaporOS is restarting. This page reconnects by itself.' },
  updating: { title: 'Updating', text: 'VaporOS is restarting into version <v>.' },
  down: { text: 'Waiting for VaporOS to start…' },
  back: { title: 'Back', text: 'Back online.' },
  timeout: { title: "VaporOS hasn't come back yet", text: 'Check the PC, then reload this page.' },
  off: { title: 'Off', text: "Wake it from Moonlight, or press its power button. This page reconnects when it's back." },
  asleep: { title: 'Asleep', text: 'VaporOS stopped answering. It may have powered off after nobody played. This page reconnects by itself.' },
  unreachable: { title: "Can't reach VaporOS", text: "Check that the PC is on and that this device is on the same network. If it's asleep, wake it from Moonlight." },
};

const scene = () => byId('scene');
const thermal = () => import('./thermal.js');
let tick = 0;
let since = 0;
let dismissible = false;
const host = () => byId('host-name').textContent.trim() || 'VaporOS';

export const sceneOpen = () => scene().open;

function line(text, kind = '') {
  const log = byId('scene-ping');
  const li = h('li', { class: 'scene-ping-line', text });
  if (kind) li.dataset.kind = kind;
  log.append(li);
  while (log.children.length > 3) log.firstElementChild.remove();
}

// openScene shows the scene in phase. vars fill the copy (<v>).
// dismissible: the viewer may close it (an outage nobody asked for); a
// restart or power off they started keeps it up (Esc does nothing). from:
// when the box went away (the link's since), so the clock counts the outage,
// not how long the scene has been open.
export function openScene(phase, { vars = {}, closable = false, from = 0 } = {}) {
  const s = scene();
  dismissible = closable;
  byId('scene-ping').replaceChildren();
  byId('scene-actions').hidden = !closable;
  byId('scene-reload').hidden = true;
  byId('scene-close').hidden = !closable;
  since = from && from <= Date.now() ? from : Date.now();
  clearInterval(tick);
  byId('scene-elapsed').textContent = elapsed((Date.now() - since) / 1000);
  tick = setInterval(() => {
    byId('scene-elapsed').textContent = elapsed((Date.now() - since) / 1000);
  }, 1000);
  if (!s.dataset.wired) {
    s.dataset.wired = '1';
    s.addEventListener('cancel', (e) => {
      if (!dismissible) e.preventDefault();
    });
    s.addEventListener('close', () => clearInterval(tick));
    byId('scene-reload').addEventListener('click', () => location.reload());
    byId('scene-close').addEventListener('click', () => s.close());
  }
  // Start warm, then cool: the field's transition shows the box going down.
  s.dataset.phase = 'back';
  setPhase(phase, vars);
  if (!s.open) s.showModal();
  byId('scene-title').focus({ preventScroll: true });
  requestAnimationFrame(() => requestAnimationFrame(() => {
    s.dataset.phase = phase;
  }));
}

// setPhase changes the words and the art. The title only changes when the
// phase has one of its own.
export function setPhase(phase, vars = {}) {
  const s = scene();
  const c = SCENE[phase] || {};
  s.dataset.phase = phase;
  s.dataset.state = phase === 'off' || phase === 'asleep' ? 'asleep' : '';
  thermal().then((t) => t.heat(s.querySelector('.heat-field'), s.dataset.state || phase));
  if (c.title) byId('scene-title').textContent = fill(c.title, vars);
  if (c.text) byId('scene-text').textContent = fill(c.text, vars);
  const wake = byId('scene-wake');
  const last = byId('scene-last');
  const snap = loadSnapshot();
  const showWake = phase === 'off' || phase === 'asleep' || phase === 'unreachable';
  wake.hidden = !showWake;
  if (showWake) fillWake(wake, snap?.wol, { up: false });
  last.hidden = !(showWake && snap?.at);
  if (!last.hidden) {
    const sys = snap.status?.system || {};
    last.textContent = `Last seen ${new Date(snap.at).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' })}${sys.version ? ` · VaporOS ${sys.version}` : ''}`;
  }
  if (phase === 'timeout') {
    byId('scene-actions').hidden = false;
    byId('scene-reload').hidden = false;
    byId('scene-reload').focus();
  }
  if (phase === 'back') line(`${host()} answered`, 'ok');
}

export function closeScene() {
  clearInterval(tick);
  if (scene().open) scene().close();
}

// waitForRestart resolves with /ping's answer once VaporOS went away and
// came back, or null after timeout. A fast restart can slip between two
// polls, so after 20 s an uptime younger than the wait also counts.
export async function waitForRestart({ onDown, timeout = 8 * 60e3, every = 2000 } = {}) {
  const start = Date.now();
  let down = false;
  while (Date.now() - start < timeout) {
    await sleep(every);
    const p = await ping();
    if (!p) {
      if (!down) {
        onDown?.();
        line(`${host()} going down`);
      } else {
        line(`ping ${host()} · no answer`);
      }
      down = true;
      continue;
    }
    if (down) return p;
    if (Date.now() - start > 20000) {
      try {
        const s = await api('GET', '/system', undefined, { quiet401: true, passive: true });
        if (Number(s.uptime_s) * 1000 < Date.now() - start) return p;
      } catch {
        /* signed out by the restart: keep waiting */
      }
    }
  }
  return null;
}

// waitForBack polls until /ping answers again (VaporOS was off).
export async function waitForBack({ timeout = 24 * 3600e3 } = {}) {
  const start = Date.now();
  let every = 2000;
  while (Date.now() - start < timeout) {
    await sleep(every);
    const p = await ping();
    if (p) return p;
    line(`ping ${host()} · no answer`);
    if (Date.now() - start > 10 * 60e3) every = 5000;
  }
  return null;
}

const PATHS = { reboot: '/system/reboot', poweroff: '/system/poweroff', activate: '/update/activate' };

// suppress is true while this tab's own restart or power off is under way,
// so the box's "Restarting…" message does not show as a notice too.
export const suppress = { messages: false };

// powerAction asks (unless the caller already did), calls the API and
// covers the page until VaporOS is back, or tells the viewer it is off.
// snap is the page's snapshot, for the right words (the hold rule).
export async function powerAction(kind, { confirmed = false, snap = {}, version = '' } = {}) {
  if (!confirmed) {
    const c = kind === 'activate' ? { id: 'activate', vars: { v: version } } : powerPlan(kind, snap).confirm;
    if (!(await confirmDialog(c))) return false;
  }
  if (kind === 'activate' && version) setString('session', 'vos-expect-version', version);
  suppress.messages = true;
  try {
    await api('POST', PATHS[kind], {});
  } catch (err) {
    suppress.messages = false;
    notify(await errorText(err, { v: version }), { kind: 'error' });
    return false;
  }
  pause();
  if (kind === 'poweroff') {
    openScene('off');
    announce('VaporOS is off.');
    const back = await waitForBack();
    if (back) location.reload();
    return true;
  }
  openScene(kind === 'activate' ? 'updating' : 'restarting', { vars: { v: version } });
  announce(kind === 'activate' ? 'Updating. This page reconnects by itself.' : 'Restarting. This page reconnects by itself.');
  const back = await waitForRestart({ onDown: () => setPhase('down') });
  if (back) {
    setPhase('back');
    announce('Back online.');
    await sleep(600);
    location.reload();
  } else {
    setPhase('timeout');
    announce("VaporOS hasn't come back yet.", { assertive: true });
  }
  return true;
}

// asleep shows the scene for an outage nobody asked for (DECISIONS NEW-1):
// the box stopped answering, most likely an idle power-off. It closes by
// itself once VaporOS answers again; the page then refreshes live.
export async function asleep({ off = false, from = 0 } = {}) {
  if (sceneOpen()) return;
  openScene(off ? 'off' : 'asleep', { closable: !off, from });
  pause();
  const back = await waitForBack();
  if (!back) return;
  if (sceneOpen()) {
    setPhase('back');
    announce('Back online.');
    await sleep(900);
    closeScene();
  }
  session.booting = false;
  resume();
}
