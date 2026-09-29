// ui/sheet.js: sheets on native <dialog> (ARCH §6.5): dragged down to close
// on phones, centred from the rail breakpoint up; unsaved forms ask first.

import { reducedMotion, setVar } from '../core/dom.js';
import { confirmDialog } from './dialog.js';

const open = new Map(); // dialog → {resolve, invoker}

export function openSheet(dlg, { invoker = document.activeElement, initialFocus = null } = {}) {
  if (dlg.open) return open.get(dlg)?.promise ?? Promise.resolve('');
  wire(dlg);
  let resolve;
  const promise = new Promise((r) => {
    resolve = r;
  });
  open.set(dlg, { resolve, invoker, promise });
  dlg.returnValue = '';
  setVar(dlg, '--drag', '0px');
  dlg.showModal();
  const first = initialFocus || dlg.querySelector('form input:not([type="hidden"]), form select, form textarea') || dlg.querySelector('h2[tabindex]');
  first?.focus({ preventScroll: true });
  return promise;
}

export function closeSheet(dlg, value = '') {
  if (dlg.open) dlg.close(value);
}

// onClose runs fn(returnValue) whenever the sheet closes.
export function onClose(dlg, fn) {
  dlg.addEventListener('close', () => fn(dlg.returnValue));
}

const dirty = (dlg) => !!dlg.querySelector('form[data-dirty="true"]');

async function guardedClose(dlg) {
  if (dirty(dlg)) {
    const ok = await confirmDialog({ id: 'discard' });
    if (!ok) return;
  }
  closeSheet(dlg);
}

function wire(dlg) {
  if (dlg.dataset.wired) return;
  dlg.dataset.wired = '1';
  dlg.addEventListener('close', () => {
    const o = open.get(dlg);
    open.delete(dlg);
    if (!o) return;
    if (o.invoker && o.invoker.isConnected) o.invoker.focus({ preventScroll: true });
    o.resolve(dlg.returnValue);
  });
  dlg.addEventListener('cancel', (e) => {
    if (dirty(dlg)) {
      e.preventDefault();
      guardedClose(dlg);
    }
  });
  for (const b of dlg.querySelectorAll('[data-close]')) b.addEventListener('click', () => guardedClose(dlg));
  // A tap on the backdrop lands on the dialog element itself.
  dlg.addEventListener('click', (e) => {
    if (e.target === dlg) guardedClose(dlg);
  });
  const grip = [dlg.querySelector('[data-part="handle"]'), dlg.querySelector('.sheet-head')].filter(Boolean);
  for (const g of grip) drag(dlg, g);
}

// drag follows a pointer on the handle or header. Downward moves track the
// finger; upward ones rubber-band (0.55). Letting go past a quarter of the
// height, or faster than 0.5 px/ms, closes the sheet.
function drag(dlg, grip) {
  let startY = 0;
  let lastY = 0;
  let lastT = 0;
  let v = 0;
  let id = null;
  grip.addEventListener('pointerdown', (e) => {
    if (e.button !== 0 || e.target.closest('button, a, input')) return;
    if (matchMedia('(min-width: 64rem)').matches) return; // a centred dialog does not drag
    id = e.pointerId;
    startY = lastY = e.clientY;
    lastT = e.timeStamp;
    v = 0;
    grip.setPointerCapture(id);
    dlg.dataset.dragging = 'true';
  });
  grip.addEventListener('pointermove', (e) => {
    if (e.pointerId !== id) return;
    const dy = e.clientY - startY;
    v = (e.clientY - lastY) / Math.max(1, e.timeStamp - lastT);
    lastY = e.clientY;
    lastT = e.timeStamp;
    setVar(dlg, '--drag', `${dy > 0 ? dy : dy * 0.55 * 0.2}px`);
  });
  const end = (e) => {
    if (e.pointerId !== id) return;
    id = null;
    delete dlg.dataset.dragging;
    const dy = lastY - startY;
    const h = dlg.getBoundingClientRect().height;
    if (dy > h * 0.25 || (v > 0.5 && dy > 16)) {
      if (!reducedMotion()) setVar(dlg, '--drag', `${h}px`);
      setTimeout(() => guardedClose(dlg).then(() => setVar(dlg, '--drag', '0px')), reducedMotion() ? 0 : 180);
    } else {
      setVar(dlg, '--drag', '0px');
    }
  };
  grip.addEventListener('pointerup', end);
  grip.addEventListener('pointercancel', end);
}
