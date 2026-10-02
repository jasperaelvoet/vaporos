// pages/extensions-ask.js: System › Extensions' own dialog, in the shared
// confirm's look and manners (ui/dialog.js: a danger one focuses Cancel).
// Beyond a confirm it lists what an extension can do, takes the VaporOS
// password and the choice to delete an extension's data, and stays open
// while the request runs, so a wrong password is said under its field.
// When the box asks for a password the card did not announce, the field
// appears with why, and the next press sends it.

import { errorText } from '../core/api.js';
import { byId, h } from '../core/dom.js';
import { wantsPassword } from '../ext.js';
import { fieldError, markBusy } from '../ui/form.js';

let pending = null;

// ask shows c: {title, body, can: [sentences], note, purge, password,
// passwordHint, confirm, tone, run}. It resolves true once
// run({password, purge}) succeeded, false when the viewer cancels; an
// error run throws keeps it open and says why.
export function ask(c) {
  const dlg = byId('ext-dialog');
  if (pending) pending(false);
  const invoker = document.activeElement;
  const ok = byId('ext-dialog-ok');
  const cancel = byId('ext-dialog-cancel');
  const pw = byId('ext-dialog-pw');
  const purge = byId('ext-dialog-purge');
  const err = byId('ext-dialog-error');
  byId('ext-dialog-title').textContent = c.title;
  show('ext-dialog-body', c.body);
  show('ext-dialog-note', c.note);
  const can = c.can || [];
  byId('ext-dialog-can').replaceChildren(...can.map((t) => h('li', { class: 'ext-fact', text: t })));
  byId('ext-dialog-can-box').hidden = !can.length;
  purge.checked = false;
  byId('ext-dialog-purge-row').hidden = !c.purge;
  pw.value = '';
  fieldError(pw, '');
  let needPw = !!c.password;
  byId('ext-dialog-pw-field').hidden = !needPw;
  byId('ext-dialog-pw-hint').textContent = c.passwordHint || 'The one you sign in with.';
  err.textContent = '';
  ok.textContent = c.confirm || 'Continue';
  markBusy(ok, false);
  const danger = c.tone === 'danger';
  dlg.dataset.tone = danger ? 'danger' : 'normal';
  dlg.setAttribute('role', danger ? 'alertdialog' : 'dialog');

  return new Promise((resolve) => {
    const form = byId('ext-dialog-form');
    const working = () => ok.getAttribute('aria-busy') === 'true';
    const done = (v) => {
      pending = null;
      form.removeEventListener('submit', onSubmit);
      cancel.removeEventListener('click', onCancel);
      dlg.removeEventListener('cancel', onEscape);
      dlg.removeEventListener('click', onBackdrop);
      if (dlg.open) dlg.close();
      if (invoker && invoker.isConnected) invoker.focus({ preventScroll: true });
      resolve(v);
    };
    const onCancel = () => {
      if (!working()) done(false);
    };
    const onEscape = (e) => {
      e.preventDefault();
      onCancel();
    };
    // A tap on the backdrop lands on the dialog element itself.
    const onBackdrop = (e) => {
      if (e.target === dlg) onCancel();
    };
    const onSubmit = async (e) => {
      e.preventDefault();
      if (working()) return;
      err.textContent = '';
      fieldError(pw, '');
      if (needPw && !pw.value) {
        fieldError(pw, 'Enter the VaporOS password.');
        pw.focus();
        return;
      }
      markBusy(ok, true);
      try {
        await c.run({ password: needPw ? pw.value : '', purge: !!c.purge && purge.checked });
        markBusy(ok, false);
        done(true);
      } catch (e2) {
        markBusy(ok, false);
        const text = await errorText(e2);
        if (!needPw && wantsPassword(e2)) {
          needPw = true;
          byId('ext-dialog-pw-field').hidden = false;
          fieldError(pw, text);
          pw.focus();
        } else if (needPw && [403, 429, 503].includes(e2 && e2.status)) {
          fieldError(pw, text);
          pw.select();
          pw.focus();
        } else {
          err.textContent = text;
        }
      }
    };
    // A newer question replaces this one in the open dialog: it answers no.
    pending = done;
    form.addEventListener('submit', onSubmit);
    cancel.addEventListener('click', onCancel);
    dlg.addEventListener('cancel', onEscape);
    dlg.addEventListener('click', onBackdrop);
    if (!dlg.open) dlg.showModal();
    (needPw ? pw : danger ? cancel : ok).focus();
  });
}

function show(id, text) {
  const el = byId(id);
  el.textContent = text || '';
  el.hidden = !text;
}
