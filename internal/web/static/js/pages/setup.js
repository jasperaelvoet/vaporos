// pages/setup.js: first-run setup (spec-cc-screens §14): the admin password
// on an installed system that has none yet, guarded by the setup code the
// screen connected to the PC shows. A bare page: no event stream.

import { ApiError, api, errorText, url, useSetupCode } from '../core/api.js';
import { initSetupCode } from '../core/setup-code.js';
import { boot } from '../core/boot.js';
import { byId, optionalById } from '../core/dom.js';
import { normalizeCode, passwordError } from '../validate.js';
import { bindReveal, fieldError, heat, retrySeconds, wait } from './entry-kit.js';

bindReveal();

boot('setup', { auth: false, events: false }).then((me) => {
  if (!me.needs_setup) {
    location.replace(url(me.authenticated ? '/' : '/login'));
    return;
  }
  const code = optionalById('setup-code');
  initSetupCode(code);
  const form = byId('first-run');
  const pw = byId('setup-password');
  const pw2 = byId('setup-password2');
  const btn = byId('setup-submit');
  const error = byId('setup-error');
  const vf = byId('setup-vf');
  form.addEventListener('input', (e) => {
    if (e.target.id) fieldError(e.target, '');
    heat(vf, 'standby');
  });
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (btn.disabled) return;
    error.textContent = '';
    // The rules of the server (T6), under the field they are about.
    const errs = [];
    if (code) {
      code.value = normalizeCode(code.value);
      errs.push([code, code.value ? '' : 'Enter the setup code from the screen.']);
    }
    const why = passwordError(pw.value, pw2.value);
    const mismatch = !!why && pw.value && passwordError(pw.value) === '';
    errs.push([pw, mismatch ? '' : why], [pw2, mismatch ? why : '']);
    for (const [el, msg] of errs) fieldError(el, msg);
    const first = errs.find(([, msg]) => msg);
    if (first) {
      first[0].focus();
      return;
    }
    if (code) useSetupCode(code.value);
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    try {
      await api('POST', '/auth/setup', { password: pw.value }, { quiet401: true });
      useSetupCode('');
      location.replace(url('/?welcome=1'));
      return;
    } catch (err) {
      btn.removeAttribute('aria-busy');
      const status = err instanceof ApiError ? err.status : 0;
      if (status === 409) {
        // The admin password exists already (set from another tab).
        location.replace(url('/login'));
        return;
      }
      heat(vf, 'cold');
      if (status === 429) {
        const n = retrySeconds(err);
        error.textContent = err.retryAfter ? `Too many wrong codes. Try again in ${n} s.` : 'Too many wrong codes. Wait a few minutes and try again.';
        wait(btn, n, 'Save and continue');
        return;
      }
      btn.disabled = false;
      const text = await errorText(err);
      const field = status === 403 ? code : status === 400 ? pw : null;
      if (field) {
        fieldError(field, text);
        field.focus();
        if (field === code) code.select();
      } else {
        error.textContent = text;
      }
    }
  });
});
