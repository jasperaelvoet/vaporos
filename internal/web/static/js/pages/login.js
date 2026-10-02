// pages/login.js: sign in (spec-cc-screens §13). One password, then back to
// where the visitor was going (?next=, kept on this origin by safeNext, or
// an extension's page on this host by webNext). A bare page: no event
// stream.

import { ApiError, api, errorText, url } from '../core/api.js';
import { boot } from '../core/boot.js';
import { byId } from '../core/dom.js';
import { safeNext, webNext } from '../validate.js';
import { bindReveal, heat, retrySeconds, wait } from './entry-kit.js';

const params = new URLSearchParams(location.search);
const next = () => webNext(params.get('next'), location) || url(safeNext(params.get('next') || '/'));

// The lead line follows why the visitor is here, set before /auth/me
// answers so the default never shows first: signed out by a 401 (N4), or
// sent here by the installer's hand-off (§15.8).
const lead = params.get('installed') === '1'
  ? 'VaporOS is installed. Sign in with the password you just chose.'
  : params.get('ended') === '1' ? 'You were signed out. Sign in again.' : '';
if (lead) byId('login-lead').textContent = lead;
bindReveal();

boot('login', { auth: false }).then((me) => {
  if (me.authenticated) {
    location.replace(next());
    return;
  }
  const form = byId('login-form');
  const pw = byId('login-password');
  const btn = byId('login-submit');
  const error = byId('login-error');
  const vf = byId('login-vf');
  heat(vf, 'standby');
  pw.addEventListener('input', () => {
    pw.removeAttribute('aria-invalid');
    heat(vf, 'standby');
  });
  const fail = (text, { wrong = false } = {}) => {
    error.textContent = text;
    if (wrong) pw.setAttribute('aria-invalid', 'true');
    heat(vf, 'cold');
    pw.focus();
    pw.select();
  };
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (btn.disabled) return;
    if (!pw.value) {
      fail('Enter the admin password.', { wrong: true });
      return;
    }
    error.textContent = '';
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    try {
      await api('POST', '/auth/login', { password: pw.value }, { quiet401: true });
      location.replace(next());
      return;
    } catch (err) {
      btn.removeAttribute('aria-busy');
      const status = err instanceof ApiError ? err.status : 0;
      if (status === 409) {
        // No admin password yet: this PC still needs its first-run setup.
        location.replace(url('/setup'));
        return;
      }
      if (status === 429) {
        const n = retrySeconds(err);
        fail(err.retryAfter ? `Too many wrong passwords. Try again in ${n} s.` : 'Too many wrong passwords. Wait a few minutes and try again.');
        wait(btn, n, 'Sign in');
        return;
      }
      btn.disabled = false;
      fail(await errorText(err), { wrong: status === 401 });
    }
  });
});
