// pages/devices.js: Devices (spec-cc-screens §4), the pair card. The shell's
// GET /status and replayed pairing.state say who waits for a PIN; the pad
// is the shell's takeover (ui/pinpad.js). It opens from the TV's /pair
// link (the shell), from Enter PIN, and by itself when Moonlight starts
// waiting while the page is open or comes back into view. The paired list,
// Playing now and the addresses are devices-list.js, fetched alongside.

import { api, errorText } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, cloneTpl, part } from '../core/dom.js';
import { onLink } from '../core/live.js';
import { region } from '../ui/region.js';
import { notify, onStatus, refresh, shell } from '../ui/shell.js';

const listing = import('./devices-list.js');
const pinpad = () => import('../ui/pinpad.js');
const thermal = () => import('../ui/thermal.js');

let snap = {};
let have = false; // a whole GET /status arrived
let waitingIds = null;
let list = null;
let reg;

const pairings = (s) => (s.sunshine && Array.isArray(s.sunshine.pairings) ? s.sunshine.pairings : []);
const prompt = (l) => (l.length > 1 ? `${l.length} devices want to pair` : `${(l[0] && l[0].name) || 'A device'} wants to pair`);

shell('devices').then(async () => {
  reg = region(byId('pair'));
  byId('dev-enter-pin').addEventListener('click', (e) => openPad(e.currentTarget));
  byId('dev-have-pin').addEventListener('click', havePin);
  byId('dev-have-pin').hidden = false;
  byId('dev-sun-restart').addEventListener('click', restartStreaming);
  // After a pairing the pad's Enter PIN is gone: focus stays on this card.
  byId('pinpad').addEventListener('close', () => setTimeout(() => {
    if (!document.activeElement || document.activeElement === document.body) byId('pair').focus();
  }));
  byId('dev-pair-retry').addEventListener('click', () => {
    reg.loading();
    refresh(false).then(() => have || api('GET', '/status').catch(failed));
  });
  onLink((l) => {
    byId('dev-listen').dataset.link = l.state;
    byId('dev-listen-text').textContent = l.state === 'live' ? 'Listening for Moonlight…' : 'Not listening: reconnecting…';
  });
  // The shell's GET /status is in flight: share it to learn if it fails.
  api('GET', '/status', undefined, { share: true }).catch(failed);
  list = await listing;
  list.init({
    changed: render,
    paired: (name) => {
      byId('dev-pair-done').textContent = `${name} is paired. Pick Steam in Moonlight to play.`;
    },
  });
  onStatus(update);
});

function update(s) {
  snap = s;
  have = have || !!(s.system || s.update || s.display);
  if (!have) return;
  const ids = pairings(s).map((p) => p.id);
  if (waitingIds) {
    // One that left may have just paired: the list asks again.
    if (waitingIds.some((id) => !ids.includes(id))) list.reload(true);
    if (ids.some((id) => !waitingIds.includes(id))) arrived();
  }
  waitingIds = ids;
  list.update(s);
  render();
}

async function failed(err) {
  if (have) return;
  byId('pair').dataset.pair = 'error';
  lead(await errorText(err));
  actions({ retry: true });
  reg.ready();
}

// Moonlight started waiting while the page is open: show the pad, unless
// the viewer is in another dialog or field.
function arrived() {
  const el = document.activeElement;
  if (document.querySelector('dialog[open]') || (el && el.matches('input:not([type="radio"]), textarea, select'))) {
    announce(prompt(pairings(snap)));
    return;
  }
  openPad(byId('dev-enter-pin'));
}

function openPad(from) {
  const waiting = pairings(snap);
  pinpad().then((m) => {
    m.initPinpad({ notice: false });
    m.updatePairings(waiting);
    if (!byId('pinpad').open) m.openPinpad({ from });
  });
}

// I have a PIN: ask VaporOS who waits now, in case this page missed it.
async function havePin(e) {
  const btn = e.currentTarget;
  if (btn.getAttribute('aria-busy') === 'true') return;
  btn.setAttribute('aria-busy', 'true');
  await refresh(false);
  btn.removeAttribute('aria-busy');
  if (pairings(snap).length) openPad(byId('dev-enter-pin'));
  else {
    // Not a live region: the PIN pad already says "paired"; this line is
    // spoken only when it answers a press.
    const words = "Moonlight isn't waiting yet. Start pairing in Moonlight, then this page asks for the PIN.";
    byId('dev-pair-done').textContent = words;
    announce(words);
  }
}

function render() {
  const known = list && list.clients();
  if (!have || !known) return; // wait for the list too, so the steps fold before they show
  const box = byId('pair');
  const sun = snap.sunshine;
  const waiting = pairings(snap);
  let mode = 'steps';
  if (sun && sun.running === false) mode = 'stopped';
  else if (waiting.length) mode = 'waiting';
  else if (known.err && known.err.status === 503) mode = 'starting';
  if (box.dataset.pair !== mode) byId('dev-pair-done').textContent = '';
  box.dataset.pair = mode;
  if (mode === 'waiting') box.dataset.attention = 'pair';
  else delete box.dataset.attention;
  // Painted once a device waits; the card's wrapper fades it.
  if (mode === 'waiting') thermal().then((t) => t.heat(box.querySelector('.heat-field'), 'waiting'));
  fold(known.list);
  byId('dev-pair-title').textContent = mode === 'waiting' ? prompt(waiting) : 'Pair a device';
  lead({
    waiting: `Enter the 4-digit PIN Moonlight shows.${waiting.length === 1 && waiting[0].address ? ` From ${waiting[0].address}.` : ''}`,
    stopped: "Streaming is stopped, so devices can't pair or play.",
    starting: 'Streaming is starting. Pairing works in a few seconds.',
  }[mode] || '');
  whoWaits(mode === 'waiting' && waiting.length > 1 ? waiting : []);
  actions({ pin: mode === 'waiting', restart: mode === 'stopped' });
  reg.ready();
}

// Steps 1 and 2 fold once a device is paired: step 3 is what is left.
let folded = false;
function fold(paired) {
  if (folded || !paired) return;
  folded = true;
  for (const id of ['dev-step-get', 'dev-step-add']) byId(id).open = paired.length === 0;
}

function lead(text) {
  const p = byId('dev-pair-lead');
  p.textContent = text;
  p.hidden = !text;
}

function actions({ pin = false, restart = false, retry = false }) {
  byId('dev-enter-pin').hidden = !pin;
  byId('dev-sun-restart').hidden = !restart;
  byId('dev-pair-retry').hidden = !retry;
  byId('dev-pair-actions').hidden = !(pin || restart || retry);
}

let whoKey = '';
function whoWaits(waiting) {
  const ul = byId('dev-waiting');
  ul.hidden = !waiting.length;
  const key = JSON.stringify(waiting);
  if (key === whoKey) return;
  whoKey = key;
  ul.replaceChildren(...waiting.map((p) => {
    const li = cloneTpl('tpl-dev-waiting');
    part(li, 'name').textContent = p.name || 'A device';
    part(li, 'addr').textContent = p.address || '';
    return li;
  }));
}

async function restartStreaming(e) {
  const btn = e.currentTarget;
  if (btn.getAttribute('aria-busy') === 'true') return;
  btn.setAttribute('aria-busy', 'true');
  try {
    await api('POST', '/sunshine/restart', {});
    notify('Streaming is restarting.', { kind: 'info' });
    setTimeout(() => {
      refresh(true);
      list.reload(true);
    }, 1500);
  } catch (err) {
    notify(await errorText(err), { kind: 'error' });
  } finally {
    btn.removeAttribute('aria-busy');
  }
}
