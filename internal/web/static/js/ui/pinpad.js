// ui/pinpad.js: the PIN pad takeover (ARCH §6.7, spec-cc-screens §2.9),
// driven by the replayed pairing.state: it closes when Moonlight stops
// waiting. REDLINE: each digit heats the field one step; a wrong PIN is cold.

import { api, errorText } from '../core/api.js';
import { byId, cloneTpl, part, parts, reducedMotion } from '../core/dom.js';
import { pairPrompt } from '../copy.js';
import { cleanDeviceName } from '../validate.js';
import { dismiss, notify } from './notices.js';

let waiting = [];
let chosen = '';
let invoker = null;
let submitting = false;
let succeeded = false;
let noticeOn = true;
const KEYPAD = '(pointer: coarse) and (min-height: 480px)';

const pad = () => byId('pinpad');
const input = () => byId('pin');
const thermal = () => import('./thermal.js');
const field = () => pad().querySelector('.heat-field');

// initPinpad wires the takeover. notice: raise the Enter PIN notice on this
// page (Home and Devices have their own pairing surface).
export function initPinpad({ notice = true } = {}) {
  noticeOn = notice;
  const dlg = pad();
  if (dlg.dataset.wired) return;
  dlg.dataset.wired = '1';
  const pin = input();
  byId('pin-close').addEventListener('click', () => closePinpad());
  dlg.addEventListener('close', () => {
    if (invoker && invoker.isConnected) invoker.focus({ preventScroll: true });
    invoker = null;
    // Not now: the prompt stays one tap away while Moonlight waits.
    if (!succeeded && waiting.length) raise();
  });
  pin.addEventListener('input', () => {
    pin.value = pin.value.replace(/\D/g, '').slice(0, 4);
    paint();
  });
  byId('keypad').addEventListener('click', (e) => {
    const k = e.target.closest('[data-key]');
    if (!k || submitting) return;
    const key = k.dataset.key;
    if (key === 'back') pin.value = pin.value.slice(0, -1);
    else if (key === 'clear') pin.value = '';
    else if (pin.value.length < 4) pin.value += key;
    if (!reducedMotion()) navigator.vibrate?.(10);
    paint();
    // The cells are hidden from screen readers: say how far the PIN is.
    if (pin.value.length < 4) byId('pin-status').textContent = `${pin.value.length} of 4 digits`;
  });
  // A hardware keyboard types into the PIN wherever focus is in the takeover.
  dlg.addEventListener('keydown', (e) => {
    const typing = e.target.closest('textarea, select, input:not([type="radio"])');
    if (e.metaKey || e.ctrlKey || e.altKey || typing) return;
    if (/^[0-9]$/.test(e.key) && pin.value.length < 4) {
      pin.value += e.key;
      paint();
      e.preventDefault();
    } else if (e.key === 'Backspace') {
      pin.value = pin.value.slice(0, -1);
      paint();
      e.preventDefault();
    }
  });
  byId('pin-box').addEventListener('animationend', () => delete byId('pin-box').dataset.shake);
  const mq = matchMedia(KEYPAD);
  const keypad = () => {
    byId('keypad').hidden = !mq.matches;
    pin.inputMode = mq.matches ? 'none' : 'numeric';
  };
  mq.addEventListener?.('change', keypad);
  keypad();
}

function paint() {
  const v = input().value;
  parts(byId('pin-box'), 'cell').forEach((c, i) => {
    c.textContent = v[i] || '';
    c.dataset.on = String(i < v.length);
    c.dataset.cur = String(i === v.length);
  });
  pad().dataset.digits = String(v.length);
  if (v.length > 0) {
    byId('pin-error').textContent = '';
    input().removeAttribute('aria-invalid');
    if (pad().dataset.phase === 'error') pad().dataset.phase = 'idle';
  }
  // Each digit heats the field a step (painted ahead); a wrong PIN is cold.
  thermal().then((t) => {
    t.heat(field(), String(v.length), { map: pad().dataset.phase === 'error' ? 'cold' : 'heat' });
    if (v.length < 4 && pad().open) t.prime(field(), String(v.length + 1));
  });
  if (v.length === 4) setTimeout(() => input().value.length === 4 && submit(), 150);
}

function device() {
  return waiting.find((p) => p.id === chosen) || waiting[0] || null;
}

// renderTitle names the device only once it is the one: with several
// waiting and none chosen, it names none (the picker asks).
function renderTitle() {
  const d = device();
  const open = waiting.length > 1 && !chosen;
  byId('pin-title').textContent = open ? pairPrompt(waiting) : `${d?.name || 'A device'} wants to pair`;
  byId('pin-lead-name').textContent = open ? "the device you're pairing" : d?.name || 'it';
}

// renderWho redraws the picker only when the list changes, so choosing a
// device keeps focus on its radio.
let pickedFor = '';
function renderWho() {
  renderTitle();
  const pick = byId('pin-pick');
  pick.hidden = waiting.length < 2;
  const key = waiting.map((p) => p.id).join(' ');
  if (waiting.length < 2 || key === pickedFor) return;
  pickedFor = key;
  byId('pin-pick-list').replaceChildren(...waiting.map((p) => {
    const el = cloneTpl('tpl-pin-device');
    const r = part(el, 'radio');
    r.value = p.id;
    r.checked = p.id === chosen;
    r.addEventListener('change', () => {
      chosen = p.id;
      byId('pin-device-name').value = p.name || '';
      renderTitle();
      // The PIN may already be complete: choosing the device pairs it.
      if (input().value.length === 4) submit();
    });
    part(el, 'name').textContent = p.name || p.address || 'A device';
    return el;
  }));
}

async function submit() {
  if (submitting) return;
  const d = device();
  if (waiting.length > 1 && !chosen) {
    byId('pin-error').textContent = 'Choose the device that shows this PIN.';
    byId('pin-pick-list').querySelector('input')?.focus();
    return;
  }
  submitting = true;
  pad().dataset.phase = 'submitting';
  byId('pin-status').textContent = 'Pairing…';
  const body = { pin: input().value, name: cleanDeviceName(byId('pin-device-name').value || d?.name || '') };
  if (d?.id && waiting.length > 1) body.pairing_id = d.id;
  try {
    await api('POST', '/sunshine/pair', body);
    succeeded = true;
    pad().dataset.phase = 'success';
    const name = body.name || d?.name || 'The device';
    // Said once, by the takeover's status line; the notice only shows it.
    byId('pin-status').textContent = `${name} is paired. Pick Steam in Moonlight to play.`;
    setTimeout(() => closePinpad(), 900);
    notify(`${name} is paired.`, { kind: 'ok', quiet: true });
  } catch (err) {
    pad().dataset.phase = 'error';
    byId('pin-status').textContent = '';
    byId('pin-error').textContent = await errorText(err, { name: d?.name || 'The device' });
    input().value = '';
    paint();
    input().setAttribute('aria-invalid', 'true');
    if (!reducedMotion()) byId('pin-box').dataset.shake = 'true';
    input().focus({ preventScroll: true });
  } finally {
    submitting = false;
  }
  // Moonlight stopped waiting while the PIN was on its way and it failed.
  if (!succeeded && !waiting.length) stoppedWaiting();
}

function stoppedWaiting() {
  closePinpad();
  notify('Moonlight stopped waiting. Start pairing again in Moonlight.', { kind: 'info' });
}

// openPinpad shows the takeover for the waiting devices (id preselects one).
export function openPinpad({ id = '', from = document.activeElement } = {}) {
  if (!waiting.length) return;
  const dlg = pad();
  invoker = from;
  succeeded = false;
  chosen = id || (waiting.length === 1 ? waiting[0].id : '');
  input().value = '';
  byId('pin-error').textContent = '';
  byId('pin-status').textContent = '';
  input().removeAttribute('aria-invalid');
  byId('pin-device-name').value = device()?.name || '';
  dlg.dataset.phase = 'idle';
  paint();
  pickedFor = '';
  renderWho();
  if (!dlg.open) dlg.showModal();
  // With a mouse the PIN takes focus; on touch the heading does, so the
  // system keyboard does not cover the keypad.
  (matchMedia('(pointer: fine)').matches ? input() : byId('pin-title')).focus({ preventScroll: true });
}

export function closePinpad() {
  if (pad().open) pad().close();
}

// updatePairings takes the waiting list (pairing.state, /status or GET
// /sunshine). An empty list ends every prompt.
export function updatePairings(list) {
  const before = waiting.length;
  waiting = Array.isArray(list) ? list : [];
  if (chosen && !waiting.some((p) => p.id === chosen)) chosen = '';
  if (pad().open) {
    // A pairing that succeeds ends Moonlight's wait too: while the PIN is on
    // its way, an empty list is the answer arriving, not Moonlight giving up.
    if (!waiting.length && !succeeded && !submitting) {
      stoppedWaiting();
    } else if (waiting.length) {
      renderWho();
    }
  }
  if (!waiting.length) dismiss('pair');
  else if (waiting.length !== before && !pad().open) raise();
  // The takeover opens on a field painted ahead.
  if (waiting.length && !pad().open) thermal().then((t) => t.prime(field(), '0'));
  return waiting;
}

function raise() {
  if (noticeOn) notify(pairPrompt(waiting), { kind: 'info', id: 'pair', sticky: true, action: { label: 'Enter PIN', onClick: () => openPinpad() } });
}

export const pairingsWaiting = () => waiting;
