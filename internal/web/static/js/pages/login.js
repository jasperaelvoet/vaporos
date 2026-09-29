// pages/login.js: a working stub from the shell (C0b) until the entry pages'
// owner (C5) replaces it: sign in, then go back where the visitor was.

import { api, errorText, url } from '../core/api.js';
import { boot } from '../core/boot.js';
import { byId } from '../core/dom.js';
import { safeNext } from '../validate.js';

const params = new URLSearchParams(location.search);
const next = () => url(safeNext(params.get('next') || '/'));

boot('login', { auth: false }).then((me) => {
  if (me.authenticated) {
    location.replace(next());
    return;
  }
  if (params.get('ended') === '1') byId('login-lead').textContent = 'You were signed out. Sign in again.';
  const form = byId('login-form');
  const pw = byId('login-password');
  pw.focus();
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = form.querySelector('button[type="submit"]');
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    byId('login-error').textContent = '';
    try {
      await api('POST', '/auth/login', { password: pw.value }, { quiet401: true });
      location.replace(next());
    } catch (err) {
      byId('login-error').textContent = await errorText(err);
      pw.setAttribute('aria-invalid', 'true');
      pw.focus();
      btn.disabled = false;
      btn.removeAttribute('aria-busy');
    }
  });
});
