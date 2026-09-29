// ui/dialog.js: the confirm dialog (ARCH §6.9). A danger confirm focuses
// Cancel, so Enter cancels (D4); a normal one focuses its action.

import { byId } from '../core/dom.js';
import { confirmCopy } from '../confirms.js';

let pending = null;

// confirmDialog asks and resolves true for yes. Pass {id, vars} for a row
// of the confirm table, or {title, body, confirm, tone, cancel}.
export function confirmDialog(opts) {
  const c = opts.id ? { ...confirmCopy(opts.id, opts.vars), ...pick(opts) } : { cancel: 'Cancel', tone: 'normal', ...opts };
  return ask(c, null);
}

// typedConfirm also needs a word typed before the action is enabled (the
// installer's erase).
export function typedConfirm(opts) {
  const c = opts.id ? { ...confirmCopy(opts.id, opts.vars), ...pick(opts) } : { cancel: 'Cancel', tone: 'danger', ...opts };
  return ask(c, opts.word || 'ERASE');
}

function pick(o) {
  const out = {};
  for (const k of ['title', 'body', 'confirm', 'tone', 'cancel']) if (o[k]) out[k] = o[k];
  return out;
}

function ask(c, word) {
  const dlg = byId('confirm');
  if (pending) pending(false);
  const invoker = document.activeElement;
  byId('confirm-title').textContent = c.title;
  byId('confirm-body').textContent = c.body || '';
  const ok = byId('confirm-ok');
  const cancel = byId('confirm-cancel');
  ok.textContent = c.confirm || 'Continue';
  cancel.textContent = c.cancel || 'Cancel';
  const danger = c.tone === 'danger';
  dlg.dataset.tone = danger ? 'danger' : 'normal';
  dlg.setAttribute('role', danger ? 'alertdialog' : 'dialog');
  const typed = byId('confirm-typed');
  const input = byId('confirm-word');
  typed.hidden = !word;
  input.value = '';
  ok.disabled = !!word;
  const onInput = () => {
    ok.disabled = input.value.trim().toUpperCase() !== word;
  };
  if (word) {
    byId('confirm-word-label').textContent = `Type ${word} to confirm`;
    input.addEventListener('input', onInput);
  }
  return new Promise((resolve) => {
    const onClose = () => done(dlg.returnValue === 'ok');
    const done = (v) => {
      pending = null;
      dlg.removeEventListener('close', onClose);
      input.removeEventListener('input', onInput);
      if (invoker && invoker.isConnected) invoker.focus({ preventScroll: true });
      resolve(v);
    };
    // A newer question replaces this one in the open dialog: it answers no.
    pending = (v) => done(v);
    dlg.returnValue = '';
    dlg.addEventListener('close', onClose);
    dlg.onclick = (e) => {
      if (e.target === dlg) dlg.close('cancel');
    };
    if (!dlg.open) dlg.showModal();
    (word ? input : danger ? cancel : ok).focus();
  });
}
