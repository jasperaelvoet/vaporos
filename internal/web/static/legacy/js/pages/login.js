// Sign in: one password, then back to where the visitor was going.
import { boot, api, byId, onSubmit, showError, session, safeNext, ApiError } from '../lib.js';

const next = () => safeNext(new URLSearchParams(location.search).get('next'));

async function main() {
  const me = await boot('login', { auth: false });
  if (me.authenticated) {
    location.replace(next());
    return;
  }
  const pw = byId('login-password');
  onSubmit(byId('login-form'), async () => {
    showError('login-error', '');
    try {
      const r = await api('POST', '/auth/login', { password: pw.value }, { quiet401: true });
      session.csrf = r.csrf || '';
      location.replace(next());
    } catch (e) {
      if (!(e instanceof ApiError)) throw e;
      let msg = e.message;
      if (e.status === 401) msg = "That password isn't right.";
      else if (e.status === 429) msg = 'Too many wrong passwords. Wait a few minutes, then try again.';
      showError('login-error', msg);
      pw.setAttribute('aria-invalid', 'true');
      pw.select();
    }
  });
  pw.addEventListener('input', () => pw.removeAttribute('aria-invalid'));
}

main();
