// pages/setup.js: a working stub from the shell (C0b) until the entry pages'
// owner (C5) replaces it: the first-run admin password on an installed
// system that has none yet.

import { api, errorText, url } from '../core/api.js';
import { initSetupCode } from '../core/setup-code.js';
import { boot } from '../core/boot.js';
import { byId, optionalById } from '../core/dom.js';
import { passwordError } from '../validate.js';

boot('setup', { auth: false, events: false }).then((me) => {
  if (!me.needs_setup) {
    location.replace(url(me.authenticated ? '/' : '/login'));
    return;
  }
  initSetupCode(optionalById('setup-code'));
  const form = byId('first-run');
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const err = byId('setup-error');
    const pw = byId('setup-password').value;
    const why = passwordError(pw, byId('setup-password2').value);
    err.textContent = why;
    if (why) return;
    try {
      await api('POST', '/auth/setup', { password: pw }, { quiet401: true });
      location.replace(url('/'));
    } catch (e2) {
      err.textContent = await errorText(e2);
    }
  });
});
