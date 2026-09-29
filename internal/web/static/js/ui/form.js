// ui/form.js: one way to handle a form (spec-cc-screens R4, R5, ARCH §6.10).
// Forms are novalidate: on submit the pure rules run, errors appear under
// their fields (aria-invalid, aria-describedby) and the first invalid field
// takes focus; a server error lands on the field messages.js maps it to, or
// on the form's own role=alert line. Save is enabled only while the form is
// dirty, and a live refresh never overwrites a dirty form (B9). Unsaved
// text survives a sign-in or a tab switch in sessionStorage (never
// passwords, CRIT 35).

import { errorText } from '../core/api.js';
import { getJSON, remove, setJSON } from '../core/store.js';
import { announce } from '../core/announce.js';

const DRAFTS = 'vos-drafts';

// fieldError shows msg under a field (its <p id="<field id>-error">), or
// clears it.
export function fieldError(input, msg) {
  const p = document.getElementById(`${input.id}-error`);
  if (msg) input.setAttribute('aria-invalid', 'true');
  else input.removeAttribute('aria-invalid');
  if (!p) return;
  p.textContent = msg || '';
  p.hidden = !msg;
}

// isDirty reports whether a live refresh must leave the form alone.
export const isDirty = (form) => form.dataset.dirty === 'true';

// bindForm wires form. validate(form) returns {fieldId: message}; submit
// (form) does the work and may throw an ApiError; fieldFor(err) names the
// field a server error belongs to. draft: keep unsaved text for this tab.
export function bindForm(form, { validate = () => ({}), submit, fieldFor = () => '', draft = false, saved = 'Saved' } = {}) {
  form.noValidate = true;
  const key = `${document.documentElement.dataset.page}#${form.id}`;
  const submitBtn = form.querySelector('button[type="submit"]');
  const formError = form.querySelector('.form-error');
  const setDirty = (on) => {
    form.dataset.dirty = String(on);
    if (submitBtn && form.dataset.saveWhenDirty !== 'false') submitBtn.disabled = !on;
  };
  form.addEventListener('input', (e) => {
    setDirty(true);
    if (e.target.id) fieldError(e.target, '');
    if (draft) saveDraft(form, key);
  });
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (formError) formError.textContent = '';
    const errs = validate(form) || {};
    let first = null;
    for (const el of form.elements) {
      if (!el.id) continue;
      fieldError(el, errs[el.id] || '');
      if (errs[el.id] && !first) first = el;
    }
    if (first) {
      first.focus();
      return;
    }
    submitBtn?.setAttribute('aria-busy', 'true');
    if (submitBtn) submitBtn.disabled = true;
    try {
      await submit(form);
      setDirty(false);
      remove('session', `${DRAFTS}:${key}`);
      if (saved) announce(saved);
    } catch (err) {
      const id = fieldFor(err);
      const el = id && document.getElementById(id);
      if (el) {
        fieldError(el, await errorText(err));
        el.focus();
      } else if (formError) {
        formError.textContent = await errorText(err);
      }
      if (submitBtn) submitBtn.disabled = false;
    } finally {
      submitBtn?.removeAttribute('aria-busy');
    }
  });
  if (draft) restoreDraft(form, key, setDirty);
  return { setDirty, isDirty: () => isDirty(form) };
}

function saveDraft(form, key) {
  const values = {};
  for (const el of form.elements) {
    if (!el.name || el.type === 'password' || el.type === 'hidden' || el.type === 'submit') continue;
    values[el.name] = el.type === 'checkbox' || el.type === 'radio' ? el.checked : el.value;
  }
  setJSON('session', `${DRAFTS}:${key}`, values);
}

function restoreDraft(form, key, setDirty) {
  const values = getJSON('session', `${DRAFTS}:${key}`);
  if (!values) return;
  for (const el of form.elements) {
    if (!(el.name in values) || el.type === 'password') continue;
    if (el.type === 'checkbox' || el.type === 'radio') el.checked = !!values[el.name];
    else el.value = values[el.name];
  }
  setDirty(true);
}

// busy runs fn while btn is marked busy and cannot be pressed twice; an
// error becomes a notice unless onError handles it.
export async function busy(btn, fn, onError) {
  if (btn?.getAttribute('aria-busy') === 'true') return undefined;
  btn?.setAttribute('aria-busy', 'true');
  if (btn) btn.disabled = true;
  try {
    return await fn();
  } catch (err) {
    if (onError) onError(err);
    else throw err;
    return undefined;
  } finally {
    btn?.removeAttribute('aria-busy');
    if (btn) btn.disabled = false;
  }
}
