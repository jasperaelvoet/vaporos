// pages/devices-size.js: Adjust's sheet on Devices: what the device playing
// now is and its interface size (PUT /display/screens/{id}). Each change
// shows at once and is put back when VaporOS refuses it (R4). Changes go
// out one after another, each with its whole value, so quick taps on +
// arrive in order and the last answer is what stays.

import { api, errorText } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, setVar } from '../core/dom.js';
import { modeLabel } from '../fmt.js';
import { closeSheet, openSheet } from '../ui/sheet.js';
import { notify } from '../ui/shell.js';
import { SIZE_MAX, SIZE_MIN, autoWords, canStep, edit, isAuto, isDefault, percent, stepSize, steamNote } from './devices-scale.js';
import { display, screen, showLocal } from './devices-list.js';

// HOLD: how long after the last answer the sheet's screen outranks
// /status, whose refresh follows display.changed by about a second.
const HOLD = 2500;
const WAIT = 60000;

const sheet = () => byId('size-sheet');
let shown = null; // the screen as the sheet shows it
let sure = null; // the screen as VaporOS last answered
let pending = 0;
let queue = Promise.resolve();

export function openSize(invoker) {
  wire();
  const sc = screen();
  if (!sc || !sc.savable) return;
  shown = sure = sc;
  render();
  openSheet(sheet(), { invoker });
}

// refreshSize follows the page: a new answer when nothing of the sheet's
// is on its way, and the sheet closes once the device stops playing.
export function refreshSize() {
  if (!sheet().open || !shown) return;
  const sc = screen();
  if (!sc || sc.id !== shown.id) {
    closeSheet(sheet());
    return;
  }
  if (pending) return;
  shown = sure = sc;
  render();
}

function wire() {
  const dlg = sheet();
  if (dlg.dataset.sized) return;
  dlg.dataset.sized = '1';
  dlg.addEventListener('change', (e) => {
    const r = e.target;
    if (r.name === 'size-kind' && r.checked) change({ kind: r.value });
    else if (r.id === 'size-steam-auto') change({ steam_auto: r.checked });
  });
  byId('size-down').addEventListener('click', () => step(-1));
  byId('size-up').addEventListener('click', () => step(1));
  byId('size-reset').addEventListener('click', () => {
    if (isDefault(shown)) return;
    change({ kind: 'auto', size: 1 });
    announce('Automatic, 100%');
  });
  // Adjust leaves with Playing now when the stream ends: focus goes to the list.
  dlg.addEventListener('close', () => setTimeout(() => {
    const el = document.activeElement;
    if (!el || el === document.body) byId('dev-paired-title').focus();
  }));
}

function step(dir) {
  if (shown.steam_auto || !canStep(shown, dir)) return;
  change({ size: stepSize(shown, dir) });
}

function change(body) {
  const id = shown.id;
  shown = edit(shown, body);
  pending++;
  render();
  showLocal(shown, WAIT);
  queue = queue.then(async () => {
    try {
      const sc = await api('PUT', `/display/screens/${encodeURIComponent(id)}`, body);
      if (sc && sc.id === id) sure = sc;
    } catch (err) {
      // Back to what VaporOS has; a change still queued may follow.
      shown = sure;
      notify(await errorText(err), { kind: 'error' });
    }
    if (--pending) return;
    shown = sure;
    showLocal(sure, HOLD);
    render();
  });
}

const key = (id, on) => {
  const b = byId(id);
  if (on) b.removeAttribute('aria-disabled');
  else b.setAttribute('aria-disabled', 'true');
};

function render() {
  const sc = shown;
  const own = !!sc.steam_auto;
  byId('size-for').textContent = [sc.name || 'This device', sc.mode ? modeLabel(sc.mode) : ''].filter(Boolean).join(' · ');
  const note = steamNote(display().steam_ui);
  const line = byId('size-steam');
  line.textContent = note.text;
  line.dataset.tone = note.error ? 'error' : '';
  line.hidden = !note.text;
  const pick = isAuto(sc) ? 'auto' : sc.kind;
  for (const r of sheet().querySelectorAll('input[name="size-kind"]')) r.checked = r.value === pick;
  byId('size-auto').textContent = `Automatic: ${autoWords(sc)}.`;
  byId('size-kinds').disabled = own;
  byId('size-steps').dataset.off = String(own);
  const pct = byId('size-pct');
  if (pct.textContent !== `${percent(sc.size)}%`) pct.textContent = `${percent(sc.size)}%`;
  // The ramp under it is where the size sits between 40% and 250%.
  setVar(byId('size-ramp'), '--at', ((Number(sc.size) || 1) - SIZE_MIN) / (SIZE_MAX - SIZE_MIN));
  key('size-down', !own && canStep(sc, -1));
  key('size-up', !own && canStep(sc, 1));
  byId('size-steam-auto').checked = own;
  key('size-reset', !isDefault(sc));
}
